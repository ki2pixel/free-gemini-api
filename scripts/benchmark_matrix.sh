#!/usr/bin/env bash
# Benchmark matrix for the v3.5.7 in-flight regressions (Wave P1).
#
# The script replays the v3.5.7 documentation plan through OpenCode for each
# model, K times, in an isolated git worktree. Each run is analysed for:
#   - emitted tool calls
#   - greetings / conversational resets
#   - hallucinated tool quotas ("0 remaining", "maximum number of tool", ...)
#   - unbacked S7 completion claims
#   - short unpunctuated prose (interrupted upstream stream)
#   - terminal finish/outcome
#
# It prints a per-run table plus the unbiased pass^k estimators of Yao et al.:
# pass^1 (c/n), pass^k (C(c,k)/C(n,k)) and pass@k (1 - C(n-c,k)/C(n,k)).
#
# Prerequisites:
#   - OpenCode is configured with a provider pointing at the proxy. The default
#     provider prefix is "free-gemini/"; override it with BENCHMARK_MODEL_PREFIX.
#   - The proxy is running and authenticated for that provider.
#
# Usage:
#   ./scripts/benchmark_matrix.sh
#   BENCHMARK_RUNS=3 BENCHMARK_MODELS="gemini-4-neon gemini-4-hydrogen" ./scripts/benchmark_matrix.sh
#   BENCHMARK_RUNS=5 BENCHMARK_K=3 ./scripts/benchmark_matrix.sh
#
# Environment:
#   BENCHMARK_RUNS             Runs per model (default: 3).
#   BENCHMARK_K                k for pass^k / pass@k (default: BENCHMARK_RUNS).
#                              Must satisfy 1 <= BENCHMARK_K <= BENCHMARK_RUNS.
#   BENCHMARK_MODELS           Space/comma separated model IDs (default: the
#                              four v3.5.7 models).
#   BENCHMARK_MODEL_PREFIX     OpenCode provider prefix (default: free-gemini/).
#                              Set to an empty string to use bare model IDs.
#   BENCHMARK_ALLOW_TASK_MODE_FOR_TOOLS
#                              auto|true|false (default: auto). The proxy
#                              requalifies a tool-bearing -task request to Chat
#                              mode [0] unless the caller opts in. "auto" sends
#                              X-Allow-Task-Mode-For-Tools: true for every model
#                              whose canonical name ends in -task, so the Task
#                              [40] arm actually reaches AppMode 40. "true"
#                              forces the header on every model, "false" never
#                              sends it (reproduces the degraded Chat-vs-Chat
#                              baseline).
#   BENCHMARK_STANDALONE       auto|true|false (default: auto). The header is
#                              injected through OPENCODE_CONFIG_CONTENT, which
#                              only reaches the server that owns the config. A
#                              private server is therefore required: "auto" adds
#                              --standalone exactly when an overlay is injected.
#   BENCHMARK_PROVIDER_ID      Provider key used when the model reference has no
#                              "provider/" prefix (default: free-gemini).
#   BENCHMARK_MIN_PASS_K       Minimum pass^k for a model to be reported PASS and
#                              for BENCHMARK_STRICT=1 to exit 0 (default: 1.0,
#                              i.e. the historical "all runs must pass").
#   BENCHMARK_PLAN_FILE        Explicit plan file. Default: extract the first
#                              user message from the v3.5.7 debug transcript.
#   BENCHMARK_PLAN_SOURCE      JSON transcript used to extract the plan.
#   BENCHMARK_OUT_DIR          Output directory (default: debug/benchmark_matrix_<ts>).
#   BENCHMARK_WORKTREE_BASE    Parent directory for isolated worktrees.
#   OPENCODE_BIN               OpenCode executable (default: opencode).
#   BENCHMARK_STRICT           "1" exits non-zero when a model fails pass^k.
#   BENCHMARK_KEEP_WORKTREES   "1" keeps the per-run worktrees for inspection.
#
#   BENCHMARK_SUITE_ID         Suite name recorded in benchmark_trials (default:
#                              legacy-v357, or the --suite directory name).
#   BENCHMARK_TASKS_DIR        Directory of <task>/prompt.md tasks. Empty keeps
#                              the historical single-plan mode.
#   BENCHMARK_TASK_FILTER      Extended regular expression (grep -E) matched
#                              against each task_id and directory name under
#                              BENCHMARK_TASKS_DIR. Empty runs every task.
#                              Requires BENCHMARK_TASKS_DIR; a filter that
#                              matches no task is a hard error.
#   BENCHMARK_VERIFY_CMD       Deterministic oracle command run inside the
#                              worktree when a task has no verify.sh.
#   BENCHMARK_ORACLE_MODE      auto|hybrid|verify_only|transcript_only
#                              (default: auto = hybrid when an oracle exists).
#   BENCHMARK_PAIRING          sequential|abba (default: sequential). abba runs
#                              Chat [0] and Task [40] on the same task, in
#                              alternating order.
#   BENCHMARK_PAIR_COOLDOWN_SEC  Seconds between the two arms of a pair.
#   BENCHMARK_EMIT_TRIAL_ID    true|false (default: true). Sends
#                              X-Benchmark-Trial-ID so turns can be correlated
#                              with benchmark_trials. Because the header travels
#                              through OPENCODE_CONFIG_CONTENT, "auto" standalone
#                              then applies to every run: the overlay only
#                              reaches the server that owns the config.
#   BENCHMARK_RECORD_TRIALS_URL  Verdict endpoint (default:
#                              <proxy>/v1/benchmark/trials, "none" disables).
#   BENCHMARK_PROXY_URL        Proxy base used for /reset (default 127.0.0.1:8100).
#   API_TOKEN                  Bearer token for the verdict endpoint and /reset.
#
# The script snapshots the current working tree (tracked diff + untracked,
# non-ignored files) and creates a fresh worktree from that snapshot for every
# run. The user's main working tree is never reset or cleaned.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RUNS="${BENCHMARK_RUNS:-3}"
K="${BENCHMARK_K:-}"
MODELS="${BENCHMARK_MODELS:-gemini-4-neon gemini-4-hydrogen gemini-4-hydrogen-task gemini-4-argon}"
MODEL_PREFIX="${BENCHMARK_MODEL_PREFIX-free-gemini/}"
PROVIDER_ID_FALLBACK="${BENCHMARK_PROVIDER_ID:-free-gemini}"
ALLOW_TASK_MODE="${BENCHMARK_ALLOW_TASK_MODE_FOR_TOOLS:-auto}"
STANDALONE_MODE="${BENCHMARK_STANDALONE:-auto}"
MIN_PASS_K="${BENCHMARK_MIN_PASS_K:-1.0}"
OPENCODE_BIN="${OPENCODE_BIN:-opencode}"
STRICT="${BENCHMARK_STRICT:-0}"
KEEP_WORKTREES="${BENCHMARK_KEEP_WORKTREES:-0}"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT_DIR="${BENCHMARK_OUT_DIR:-$ROOT_DIR/debug/benchmark_matrix_$STAMP}"
WORKTREE_BASE="${BENCHMARK_WORKTREE_BASE:-$ROOT_DIR/.benchmark-worktrees}"
PLAN_SOURCE="${BENCHMARK_PLAN_SOURCE:-$ROOT_DIR/debug/Mise-a-jour-et-Reorganisation-des-Documentations-v3.5.7/gemini-4-hydrogen_Documentation-plan-v3.5.7.json}"
SUITE_ID="${BENCHMARK_SUITE_ID:-}"
TASKS_DIR="${BENCHMARK_TASKS_DIR:-}"
TASK_FILTER="${BENCHMARK_TASK_FILTER:-}"
VERIFY_CMD="${BENCHMARK_VERIFY_CMD:-}"
ORACLE_MODE="${BENCHMARK_ORACLE_MODE:-auto}"
PAIRING="${BENCHMARK_PAIRING:-sequential}"
PAIR_COOLDOWN="${BENCHMARK_PAIR_COOLDOWN_SEC:-10}"
EMIT_TRIAL_ID="${BENCHMARK_EMIT_TRIAL_ID:-true}"
PROXY_URL="${BENCHMARK_PROXY_URL:-http://127.0.0.1:8100}"
RECORD_TRIALS_URL="${BENCHMARK_RECORD_TRIALS_URL:-${PROXY_URL%/}/v1/benchmark/trials}"
AB_CHAT_MODEL="${BENCHMARK_AB_CHAT_MODEL:-}"
AB_TASK_MODEL="${BENCHMARK_AB_TASK_MODEL:-}"

if ! [[ "$RUNS" =~ ^[1-9][0-9]*$ ]]; then
  echo "Erreur: BENCHMARK_RUNS doit être un entier positif (reçu: $RUNS)" >&2
  exit 1
fi

# BENCHMARK_K defaults to BENCHMARK_RUNS, the historical pass^RUNS behaviour.
if [ -z "$K" ]; then
  K="$RUNS"
fi
if ! [[ "$K" =~ ^[1-9][0-9]*$ ]]; then
  echo "Erreur: BENCHMARK_K doit être un entier positif (reçu: $K)" >&2
  exit 1
fi
if [ "$K" -gt "$RUNS" ]; then
  echo "Erreur: BENCHMARK_K ($K) ne peut pas dépasser BENCHMARK_RUNS ($RUNS)" >&2
  exit 1
fi

for enum_pair in \
  "BENCHMARK_ALLOW_TASK_MODE_FOR_TOOLS=$ALLOW_TASK_MODE" \
  "BENCHMARK_STANDALONE=$STANDALONE_MODE"; do
  enum_name="${enum_pair%%=*}"
  enum_value="${enum_pair#*=}"
  case "$enum_value" in
    auto|true|false) ;;
    *)
      echo "Erreur: ${enum_name} doit valoir auto, true ou false (reçu: $enum_value)" >&2
      exit 1
      ;;
  esac
done

if ! [[ "$MIN_PASS_K" =~ ^([0-9]+([.][0-9]+)?|[.][0-9]+)$ ]]; then
  echo "Erreur: BENCHMARK_MIN_PASS_K doit être un nombre dans [0,1] (reçu: $MIN_PASS_K)" >&2
  exit 1
fi

for enum_pair in \
  "BENCHMARK_ORACLE_MODE=$ORACLE_MODE:auto|hybrid|verify_only|transcript_only" \
  "BENCHMARK_PAIRING=$PAIRING:sequential|abba" \
  "BENCHMARK_EMIT_TRIAL_ID=$EMIT_TRIAL_ID:true|false"; do
  enum_name="${enum_pair%%=*}"
  enum_rest="${enum_pair#*=}"
  enum_value="${enum_rest%%:*}"
  enum_allowed="${enum_rest#*:}"
  case "|$enum_allowed|" in
    *"|$enum_value|"*) ;;
    *)
      echo "Erreur: ${enum_name} doit valoir ${enum_allowed//|/, } (reçu: $enum_value)" >&2
      exit 1
      ;;
  esac
done
if ! [[ "$PAIR_COOLDOWN" =~ ^(0|[1-9][0-9]*)$ ]]; then
  echo "Erreur: BENCHMARK_PAIR_COOLDOWN_SEC doit être un entier positif (reçu: $PAIR_COOLDOWN)" >&2
  exit 1
fi
if ! awk -v v="$MIN_PASS_K" 'BEGIN { exit (v >= 0 && v <= 1) ? 0 : 1 }'; then
  echo "Erreur: BENCHMARK_MIN_PASS_K doit être dans [0,1] (reçu: $MIN_PASS_K)" >&2
  exit 1
fi

if [ "$STRICT" != "0" ] && [ "$STRICT" != "1" ]; then
  echo "Erreur: BENCHMARK_STRICT doit valoir 0 ou 1 (reçu: $STRICT)" >&2
  exit 1
fi

# BENCHMARK_TASK_FILTER selects a subset of the task suite. grep -E is the
# reference matcher, so an invalid expression must fail before any run starts
# instead of silently selecting nothing.
if [ -n "$TASK_FILTER" ]; then
  if [ -z "$TASKS_DIR" ]; then
    echo "Erreur: BENCHMARK_TASK_FILTER exige BENCHMARK_TASKS_DIR." >&2
    exit 1
  fi
  filter_probe_rc=0
  printf '' | grep -Eq -- "$TASK_FILTER" 2>/dev/null || filter_probe_rc=$?
  if [ "$filter_probe_rc" -gt 1 ]; then
    echo "Erreur: BENCHMARK_TASK_FILTER n'est pas une expression régulière valide: $TASK_FILTER" >&2
    exit 1
  fi
fi
if ! command -v "$OPENCODE_BIN" >/dev/null 2>&1; then
  echo "Erreur: '$OPENCODE_BIN' est introuvable. Installez OpenCode ou définissez OPENCODE_BIN." >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "Erreur: python3 est requis pour analyser les transcripts." >&2
  exit 1
fi

read -r -a MODEL_LIST <<<"${MODELS//,/ }"
if [ "${#MODEL_LIST[@]}" -eq 0 ]; then
  echo "Erreur: BENCHMARK_MODELS est vide." >&2
  exit 1
fi

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/gemini-benchmark.XXXXXX")"
WORKTREES=()
cleanup() {
  if [ "$KEEP_WORKTREES" != "1" ]; then
    for dir in "${WORKTREES[@]:-}"; do
      if [ -n "$dir" ]; then
        # The agent may create root-owned runtime directories inside its
        # worktree (for example free-gemini-api/*/data). Neither git nor rm -rf
        # can remove those as the invoking user: the benchmark itself already
        # succeeded, so cleanup must never turn it into a non-zero exit.
        git -C "$ROOT_DIR" worktree remove --force "$dir" >/dev/null 2>&1 \
          || rm -rf "$dir" 2>/dev/null \
          || true
      fi
    done
    git -C "$ROOT_DIR" worktree prune >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP_DIR" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$OUT_DIR" "$WORKTREE_BASE"
git -C "$ROOT_DIR" worktree prune >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# Task-mode opt-in
# ---------------------------------------------------------------------------
# canonical_model_name strips the "provider/" prefix and the "#variant" suffix
# so a model reference can be matched on its model ID alone. opencode accepts
# "provider/model#variant" (see --model in `opencode run --help`).
canonical_model_name() {
  local ref="$1"
  ref="${ref%%#*}"
  if [[ "$ref" == */* ]]; then
    ref="${ref##*/}"
  fi
  printf '%s' "$ref"
}

# should_opt_in_task decides whether this model needs the
# X-Allow-Task-Mode-For-Tools header. The proxy requalifies every tool-bearing
# -task request to Chat mode [0] unless the caller opts in, so without this the
# Task [40] arm silently measures the Chat model.
should_opt_in_task() {
  local ref="$1" canonical
  case "$ALLOW_TASK_MODE" in
    true) return 0 ;;
    false) return 1 ;;
  esac
  canonical="$(canonical_model_name "$ref")"
  [[ "$canonical" == *-task ]]
}

# build_run_overlay prints the OPENCODE_CONFIG_CONTENT document that carries the
# per-run headers for one provider, merging into any overlay already exported by
# the caller. OpenCode decodes config with onExcessProperty "ignore"
# (core/src/config.ts), so a misplaced key would be dropped silently: the
# headers MUST live under providers.<id>.headers.
build_run_overlay() {
  local provider_id="$1" base="$2" trial_id="$3" opt_in="$4"
  EXISTING_OVERLAY="$base" TRIAL_ID="$trial_id" OPT_IN="$opt_in" python3 - "$provider_id" <<'PY'
import json, os, sys

provider_id = sys.argv[1]
raw = os.environ.get("EXISTING_OVERLAY") or "{}"
try:
    cfg = json.loads(raw)
except json.JSONDecodeError:
    cfg = {}
if not isinstance(cfg, dict):
    cfg = {}

providers = cfg.setdefault("providers", {})
if not isinstance(providers, dict):
    providers = {}
    cfg["providers"] = providers
provider = providers.setdefault(provider_id, {})
if not isinstance(provider, dict):
    provider = {}
    providers[provider_id] = provider
headers = provider.setdefault("headers", {})
if not isinstance(headers, dict):
    headers = {}
    provider["headers"] = headers

trial_id = os.environ.get("TRIAL_ID") or ""
if trial_id:
    headers["X-Benchmark-Trial-ID"] = trial_id
if os.environ.get("OPT_IN") == "true":
    headers["X-Allow-Task-Mode-For-Tools"] = "true"
print(json.dumps(cfg))
PY
}

# provider_id_for_model resolves the provider key the overlay must target.
provider_id_for_model() {
  local ref="$1"
  if [[ "$ref" == */* ]]; then
    printf '%s' "${ref%%/*}"
    return
  fi
  printf '%s' "$PROVIDER_ID_FALLBACK"
}

# ---------------------------------------------------------------------------
# Benchmark plan
# ---------------------------------------------------------------------------
PLAN_FILE="${BENCHMARK_PLAN_FILE:-}"
if [ -z "$PLAN_FILE" ] && [ -f "$PLAN_SOURCE" ] && command -v jq >/dev/null 2>&1; then
  PLAN_FILE="$TMP_DIR/benchmark_plan.md"
  jq -r '[.messages[] | select(.type == "user") | .text] | first // empty' "$PLAN_SOURCE" >"$PLAN_FILE"
fi
if [ -z "$PLAN_FILE" ] || [ ! -s "$PLAN_FILE" ]; then
  PLAN_FILE="$TMP_DIR/benchmark_plan.md"
  cat >"$PLAN_FILE" <<'PLAN'
# Plan d'action : alignement documentaire (benchmark v3.5.7)

Tu es un agent de codage. Utilise les outils disponibles pour réaliser ce plan
dans le dépôt courant, sans te limiter à une synthèse.

1. Lis `README.md`, `AGENTS.md` et les fichiers de `docs/` utiles.
2. Crée `docs/benchmark/README.md` avec un résumé des contrôles de fiabilité.
3. Mets à jour `CHANGELOG.md` avec une entrée « Benchmark matrix ».
4. Vérifie le résultat avec un outil de lecture ou une commande shell.
5. Continue jusqu'à ce que les modifications et vérifications soient faites.
PLAN
fi
PLAN_TEXT="$(cat "$PLAN_FILE")"
if [ -z "$PLAN_TEXT" ]; then
  echo "Erreur: le plan de benchmark est vide." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Task suite
# ---------------------------------------------------------------------------
# Each task directory carries prompt.md, optionally meta.env (TASK_ID, STRATUM),
# setup.sh (runs inside the fresh worktree before the agent) and verify.sh (the
# deterministic oracle). Without BENCHMARK_TASKS_DIR the historical mono-plan
# mode is preserved unchanged.
TASK_IDS=()
TASK_STRATA=()
TASK_PROMPTS=()
TASK_DIRS=()
if [ -n "$TASKS_DIR" ]; then
  for task_dir in "$TASKS_DIR"/*/; do
    [ -f "${task_dir}prompt.md" ] || continue
    task_id="$(basename "$task_dir")"
    stratum=""
    if [ -f "${task_dir}meta.env" ]; then
      declared="$(sed -n 's/^[[:space:]]*TASK_ID[[:space:]]*=[[:space:]]*//p' "${task_dir}meta.env" | tail -n 1)"
      [ -n "$declared" ] && task_id="$declared"
      stratum="$(sed -n 's/^[[:space:]]*STRATUM[[:space:]]*=[[:space:]]*//p' "${task_dir}meta.env" | tail -n 1)"
    fi
    case "$stratum" in S1|S2|S3) ;; *) stratum="" ;; esac
    if [ -n "$TASK_FILTER" ]; then
      # Match the declared TASK_ID or the directory name, independently, so an
      # anchored expression such as ^s2_long_horizon_chain$ selects one task.
      task_dir_name="$(basename "$task_dir")"
      if ! printf '%s\n' "$task_id" | grep -Eq -- "$TASK_FILTER" &&
         ! printf '%s\n' "$task_dir_name" | grep -Eq -- "$TASK_FILTER"; then
        continue
      fi
    fi
    # Store an absolute path: run_one uses it for setup.sh/verify.sh, and
    # prefixing a user-supplied absolute directory with ROOT_DIR would double it.
    abs_task_dir="${task_dir%/}"
    case "$abs_task_dir" in
      /*) ;;
      *) abs_task_dir="$ROOT_DIR/$abs_task_dir" ;;
    esac
    TASK_IDS+=("$task_id")
    TASK_STRATA+=("$stratum")
    TASK_PROMPTS+=("$(cat "${task_dir}prompt.md")")
    TASK_DIRS+=("$abs_task_dir")
  done
  if [ "${#TASK_IDS[@]}" -eq 0 ]; then
    if [ -n "$TASK_FILTER" ]; then
      echo "Erreur: BENCHMARK_TASK_FILTER='$TASK_FILTER' ne correspond à aucune tâche sous $TASKS_DIR" >&2
    else
      echo "Erreur: aucune tâche avec prompt.md sous $TASKS_DIR" >&2
    fi
    exit 1
  fi
  [ -z "$SUITE_ID" ] && SUITE_ID="$(basename "$TASKS_DIR")"
else
  TASK_IDS=("legacy")
  TASK_STRATA=("")
  TASK_PROMPTS=("$PLAN_TEXT")
  TASK_DIRS=("")
fi
[ -z "$SUITE_ID" ] && SUITE_ID="legacy-v357"

# ---------------------------------------------------------------------------
# Working-tree snapshot
# ---------------------------------------------------------------------------
SNAPSHOT_COMMIT="$(git -C "$ROOT_DIR" stash create "benchmark matrix $STAMP" 2>/dev/null || true)"
if [ -z "$SNAPSHOT_COMMIT" ]; then
  SNAPSHOT_COMMIT="$(git -C "$ROOT_DIR" rev-parse HEAD)"
fi

UNTRACKED=()
while IFS= read -r -d '' rel; do
  UNTRACKED+=("$rel")
done < <(git -C "$ROOT_DIR" ls-files --others --exclude-standard -z)

copy_untracked() {
  local dest="$1" rel
  for rel in "${UNTRACKED[@]:-}"; do
    [ -n "$rel" ] || continue
    mkdir -p "$dest/$(dirname "$rel")"
    cp -a "$ROOT_DIR/$rel" "$dest/$rel"
  done
}

prepare_worktree() {
  local dir="$1"
  rm -rf "$dir"
  git -C "$ROOT_DIR" worktree add --detach --force "$dir" "$SNAPSHOT_COMMIT" >/dev/null
  copy_untracked "$dir"
  WORKTREES+=("$dir")
}

# ---------------------------------------------------------------------------
# Transcript analyser
# ---------------------------------------------------------------------------
ANALYZER="$TMP_DIR/analyze.py"
cat >"$ANALYZER" <<'PY'
import json
import os
import re
import sys

path, model, run = sys.argv[1], sys.argv[2], sys.argv[3]

try:
    raw = open(path, encoding="utf-8", errors="replace").read()
except OSError:
    # Twenty-three tab-separated fields, matching the results.tsv header. The
    # read error already reached stderr through the shell redirection.
    print("\t".join([
        model, str(run), "0", "0", "0", "0", "0", "harness_error",
        os.environ.get("BM_TRIAL_ID", ""),
        os.environ.get("BM_SUITE_ID", ""),
        os.environ.get("BM_TASK_ID", ""),
        os.environ.get("BM_STRATUM", ""),
        os.environ.get("BM_PARADIGM", "chat"),
        os.environ.get("BM_PAIR_ID", ""),
        os.environ.get("BM_ARM_ORDER", "single"),
        "transcript", "0", "0", os.environ.get("BM_DIFF_FILES", "0"),
        "0", os.environ.get("BM_WALL_MS", "0"), "none", "FAIL",
    ]))
    sys.exit(0)

objects = []
try:
    objects = [json.loads(raw)]
except Exception:
    for line in raw.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            objects.append(json.loads(line))
        except Exception:
            continue

texts = []
finishes = []
outcomes = []
tool_calls = 0
tool_names = []

def walk(node):
    global tool_calls
    if isinstance(node, dict):
        for key in ("finish", "finish_reason", "finishReason"):
            value = node.get(key)
            if isinstance(value, str):
                finishes.append(value)
        value = node.get("outcome")
        if isinstance(value, str):
            outcomes.append(value)

        for key in ("tool_calls", "toolCalls"):
            value = node.get(key)
            if isinstance(value, list):
                tool_calls += len(value)

        kind = node.get("type")
        if isinstance(kind, str) and kind.lower() in {"tool", "tool_use", "tool-call", "tool_call"}:
            tool_calls += 1
            for key in ("tool", "toolName", "name"):
                value = node.get(key)
                if isinstance(value, str):
                    tool_names.append(value)

        for key in ("text", "content", "reasoning", "thinking"):
            value = node.get(key)
            if isinstance(value, str) and value.strip():
                texts.append((key, value))

        for value in node.values():
            walk(value)
    elif isinstance(node, list):
        for value in node:
            walk(value)

for obj in objects:
    walk(obj)

final = ""
for kind, text in texts:
    if kind in ("text", "content") and text.strip():
        final = text.strip()
if not final:
    for _, text in texts:
        if text.strip():
            final = text.strip()

corpus = "\n".join(text for _, text in texts)
lower_final = final.lower()
lower_corpus = corpus.lower()

greeting_patterns = [
    r"^(hello|hi|hey|bonjour|salut|good morning|good afternoon|good evening|greetings)\b",
    r"how can i help", r"how may i help", r"how can i assist",
    r"what can i do for you", r"comment puis-je vous aider",
    r"que puis-je faire pour vous", r"en quoi puis-je vous aider",
]
greeting = (
    bool(final)
    and len(final) <= 200
    and not any(ch in final for ch in "`/\\{}")
    and any(re.search(pattern, lower_final) for pattern in greeting_patterns)
)

fake_quota = any(
    phrase in lower_corpus
    for phrase in ("0 remaining", "maximum number of tool", "search calls")
)

s7_markers = [
    "ont été entièrement réalisé", "ont été entièrement réalisées",
    "ont été entièrement exécuté", "ont été entièrement exécutées",
    "entièrement réalisé", "entièrement réalisée",
    "résumé des livrables", "livrables réalisés",
    "dans le working tree", "conformément au plan d'action validé",
    "the task is complete", "all files have been updated",
    "the plan has been fully executed", "everything is in place",
]
s7 = any(marker in lower_final for marker in s7_markers)

incomplete = (
    bool(final)
    and 10 <= len(final) <= 80
    and final[-1] not in ".!?…。！？"
    and not any(ch in final for ch in "\n`/.:")
    and not lower_final.startswith("http")
)

finish = finishes[-1] if finishes else (outcomes[-1] if outcomes else "unknown")
finish_lower = finish.lower()
finish_ok = finish_lower not in {"error", "failed", "failure", "cancelled", "canceled"}

transcript_clean = (
    bool(tool_calls > 0)
    and finish_ok
    and not greeting
    and not fake_quota
    and not s7
    and not incomplete
)

# The deterministic oracle, when a task provides one, outranks the transcript:
# verify_only trusts it alone, hybrid requires both, transcript_only ignores it.
verify_present = os.environ.get("BM_VERIFY_PRESENT") == "1"
try:
    oracle_exit = int(os.environ.get("BM_ORACLE_EXIT", "0") or 0)
except ValueError:
    oracle_exit = 1
oracle_mode = os.environ.get("BM_ORACLE_MODE", "transcript")
if verify_present and oracle_mode in ("hybrid", "verify_sh"):
    oracle_passed = oracle_exit == 0
else:
    oracle_passed = transcript_clean

if oracle_mode == "verify_sh" and verify_present:
    passed = oracle_passed
elif oracle_mode == "hybrid" and verify_present:
    passed = oracle_passed and transcript_clean
else:
    passed = transcript_clean

# Hierarchical failure classification. Only failures the transcript can prove
# are emitted here; the proxy refines the rest from the correlated turn.
if passed:
    failure_code = "none"
elif greeting:
    failure_code = "E_GREETING_LOOP"
elif fake_quota:
    failure_code = "E_FALSE_QUOTA"
elif s7:
    failure_code = "E_UNBACKED_COMPLETION"
elif incomplete:
    failure_code = "E_INCOMPLETE_PROSE"
elif not finish_ok:
    failure_code = "E_ERROR_FINISH"
elif verify_present and oracle_mode != "transcript" and not oracle_passed:
    failure_code = "E_ORACLE_ASSERTION_FAIL"
elif tool_calls == 0:
    failure_code = "E_NO_TOOL_CALL"
else:
    failure_code = "none"

print("\t".join([
    model,
    str(run),
    str(tool_calls),
    "1" if greeting else "0",
    "1" if fake_quota else "0",
    "1" if s7 else "0",
    "1" if incomplete else "0",
    finish or "unknown",
    os.environ.get("BM_TRIAL_ID", ""),
    os.environ.get("BM_SUITE_ID", ""),
    os.environ.get("BM_TASK_ID", ""),
    os.environ.get("BM_STRATUM", ""),
    os.environ.get("BM_PARADIGM", "chat"),
    os.environ.get("BM_PAIR_ID", ""),
    os.environ.get("BM_ARM_ORDER", "single"),
    oracle_mode,
    "1" if oracle_passed else "0",
    str(oracle_exit),
    os.environ.get("BM_DIFF_FILES", "0"),
    "1" if transcript_clean else "0",
    os.environ.get("BM_WALL_MS", "0"),
    failure_code,
    "PASS" if passed else "FAIL",
]))
PY

# ---------------------------------------------------------------------------
# Runs
# ---------------------------------------------------------------------------
RESULTS="$OUT_DIR/results.tsv"
printf 'model\trun\ttool_calls\tgreeting\tfake_quota\ts7\tincomplete\tfinish\ttrial_id\tsuite_id\ttask_id\tstratum\tparadigm\tpair_id\tarm_order\toracle_mode\toracle_passed\toracle_exit_code\tworktree_diff_files\ttranscript_clean\twall_time_ms\tprimary_failure_code\tresult\n' >"$RESULTS"

# Preserve any overlay the caller exported: build_run_overlay merges into it, so
# it must survive being unset between runs.
CALLER_OVERLAY="${OPENCODE_CONFIG_CONTENT:-}"

restore_caller_overlay() {
  if [ -n "$CALLER_OVERLAY" ]; then
    export OPENCODE_CONFIG_CONTENT="$CALLER_OVERLAY"
  else
    unset OPENCODE_CONFIG_CONTENT
  fi
}

slug() { printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_'; }
SUITE_ID="$(slug "$SUITE_ID")"
[ -n "$SUITE_ID" ] || SUITE_ID="legacy-v357"

reset_proxy() {
  [ -n "${API_TOKEN:-}" ] || return 0
  [ -n "$PROXY_URL" ] && [ "$PROXY_URL" != "none" ] || return 0
  curl -sS -m 5 -X POST "${PROXY_URL%/}/reset" \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${API_TOKEN}" \
    -d '{}' >/dev/null 2>&1 || true
}

# record_trial posts the oracle verdict so the proxy can join it with the turn
# telemetry carrying the same trial id. A network failure is a warning, never a
# missing TSV row.
record_trial() {
  local row="$1" trial_id="$2"
  [ -n "$trial_id" ] || return 0
  [ -n "$RECORD_TRIALS_URL" ] || return 0
  [ "$RECORD_TRIALS_URL" = "none" ] && return 0
  if ! command -v curl >/dev/null 2>&1; then
    echo "    (avertissement: curl absent, verdict non enregistré)" >&2
    return 0
  fi

  local payload
  payload="$(RECORD_ROW="$row" python3 - <<'PY'
import json, os

keys = ["model", "run", "tool_calls", "greeting", "fake_quota", "s7", "incomplete", "finish",
        "trial_id", "suite_id", "task_id", "stratum", "paradigm", "pair_id", "arm_order",
        "oracle_mode", "oracle_passed", "oracle_exit_code", "worktree_diff_files",
        "transcript_clean", "wall_time_ms", "primary_failure_code", "result"]
record = dict(zip(keys, os.environ.get("RECORD_ROW", "").split("\t")))
payload = {
    "trial_id": record.get("trial_id", ""),
    "suite_id": record.get("suite_id", ""),
    "task_id": record.get("task_id", ""),
    "stratum": record.get("stratum", ""),
    "requested_model": record.get("model", ""),
    "paradigm": record.get("paradigm", "chat"),
    "thinking_tier": "",
    "trial_index": int(record.get("run", "1") or 1),
    "pair_id": record.get("pair_id", ""),
    "arm_order": record.get("arm_order", "single"),
    "oracle_passed": record.get("oracle_passed") == "1",
    "oracle_mode": record.get("oracle_mode", "transcript"),
    "oracle_exit_code": int(record.get("oracle_exit_code", "0") or 0),
    "worktree_diff_files": int(record.get("worktree_diff_files", "0") or 0),
    "transcript_clean": record.get("transcript_clean") == "1",
    "wall_time_ms": int(record.get("wall_time_ms", "0") or 0),
    "primary_failure_code": record.get("primary_failure_code", "none"),
}
print(json.dumps(payload))
PY
)"

  local http_code
  http_code="$(curl -sS -m 5 -o /dev/null -w '%{http_code}' -X POST "$RECORD_TRIALS_URL" \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${API_TOKEN:-}" \
    --data-binary "$payload" 2>/dev/null || printf '000')"
  if [ "$http_code" != "200" ]; then
    echo "    (avertissement: verdict non enregistré, HTTP $http_code)" >&2
  fi
}

# run_one executes one (task, model, run, arm) trial in its own worktree.
run_one() {
  local task_index="$1" model="$2" run="$3" pair_id="$4" arm_order="$5"
  local task_id="${TASK_IDS[$task_index]}"
  local stratum="${TASK_STRATA[$task_index]}"
  local task_prompt="${TASK_PROMPTS[$task_index]}"
  local task_dir="${TASK_DIRS[$task_index]}"

  local safe_model safe_task arm_suffix stem dir out log
  safe_model="$(slug "$model")"
  safe_task="$(slug "$task_id")"
  arm_suffix=""
  [ "$arm_order" != "single" ] && arm_suffix="-${arm_order}"
  stem="${safe_model}-${safe_task}-run${run}${arm_suffix}"
  dir="$WORKTREE_BASE/$stem"
  out="$OUT_DIR/$stem.json"
  log="$OUT_DIR/$stem.log"

  local model_ref="$model"
  if [ -n "$MODEL_PREFIX" ]; then
    model_ref="${MODEL_PREFIX}${model}"
  fi

  local task_mode_opt_in=false paradigm="chat"
  if should_opt_in_task "$model_ref"; then
    task_mode_opt_in=true
    paradigm="task"
  fi

  local trial_id=""
  if [ "$EMIT_TRIAL_ID" = "true" ]; then
    trial_id="${SUITE_ID}:${safe_task}:${paradigm}:${safe_model}:${run}"
  fi

  local overlay=""
  if [ "$task_mode_opt_in" = "true" ] || [ -n "$trial_id" ]; then
    overlay="$(build_run_overlay "$(provider_id_for_model "$model_ref")" "$CALLER_OVERLAY" "$trial_id" "$task_mode_opt_in")"
  fi

  # The overlay only reaches the server that owns the config, so a private
  # server is required whenever one is injected.
  local use_standalone=false
  case "$STANDALONE_MODE" in
    true) use_standalone=true ;;
    auto) [ -n "$overlay" ] && use_standalone=true ;;
  esac

  local run_args=()
  if [ "$use_standalone" = "true" ]; then
    run_args+=(--standalone)
  fi
  run_args+=(run --format json --auto --model "$model_ref" --title "bench-${stem}" "$task_prompt")

  echo "=== [${model} ${run}/${RUNS}] task=${task_id} arm=${arm_order} (task_mode_opt_in=${task_mode_opt_in}, standalone=${use_standalone}) ==="
  prepare_worktree "$dir"

  if [ -n "$task_dir" ] && [ -f "$task_dir/setup.sh" ]; then
    if ! (cd "$dir" && bash "$task_dir/setup.sh") >"$OUT_DIR/$stem.setup.log" 2>&1; then
      echo "    (setup.sh a échoué, voir $stem.setup.log)" >&2
    fi
  fi

  # Freeze the post-setup state in a baseline commit. Untracked fixture
  # directories are invisible to a status diff (porcelain collapses them to a
  # single "?? dir/" entry, unchanged by an edit inside), so an agent editing
  # such a fixture would otherwise report zero changed files.
  git -C "$dir" add -A >/dev/null 2>&1 || true
  git -C "$dir" -c user.email=benchmark@local -c user.name=benchmark \
    commit --no-verify -qm "benchmark baseline" >/dev/null 2>&1 || true

  # Scope the overlay to this single invocation: leaving it exported would
  # leak the headers into the next arm.
  if [ -n "$overlay" ]; then
    export OPENCODE_CONFIG_CONTENT="$overlay"
  else
    restore_caller_overlay
  fi

  local started_ms ended_ms wall_ms run_rc=0
  started_ms="$(date +%s%3N)"
  (cd "$dir" && "$OPENCODE_BIN" "${run_args[@]}") >"$out" 2>"$log" || run_rc=$?
  ended_ms="$(date +%s%3N)"
  wall_ms=$((ended_ms - started_ms))
  restore_caller_overlay

  local diff_files
  diff_files="$(git -C "$dir" status --porcelain | grep -c . || true)"
  [ -n "$diff_files" ] || diff_files=0

  local verify_present=0 oracle_exit=0 oracle_mode_effective="transcript"
  local verify_script=""
  if [ -n "$task_dir" ] && [ -f "$task_dir/verify.sh" ]; then
    verify_script="$task_dir/verify.sh"
  fi
  if [ -n "$verify_script" ] || [ -n "$VERIFY_CMD" ]; then
    verify_present=1
    case "$ORACLE_MODE" in
      transcript_only) oracle_mode_effective="transcript" ;;
      verify_only) oracle_mode_effective="verify_sh" ;;
      *) oracle_mode_effective="hybrid" ;;
    esac
    if [ -n "$verify_script" ]; then
      (cd "$dir" && bash "$verify_script") >"$OUT_DIR/$stem.verify.log" 2>&1 || oracle_exit=$?
    else
      (cd "$dir" && bash -c "$VERIFY_CMD") >"$OUT_DIR/$stem.verify.log" 2>&1 || oracle_exit=$?
    fi
  fi

  # A crashed harness still reports the oracle it actually ran: hardcoding
  # transcript/0/0 hid both a passing verify.sh and a failing one.
  local harness_oracle_passed=0 harness_failure="none"
  if [ "$verify_present" = "1" ] && [ "$oracle_mode_effective" != "transcript" ]; then
    if [ "$oracle_exit" -eq 0 ]; then
      harness_oracle_passed=1
    else
      harness_failure="E_ORACLE_ASSERTION_FAIL"
    fi
  fi

  local row
  if [ "$run_rc" -ne 0 ]; then
    row="$(printf '%s\t%d\t0\t0\t0\t0\t0\tharness_error\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t0\t%d\t%s\tFAIL' \
      "$model" "$run" "$trial_id" "$SUITE_ID" "$task_id" "$stratum" "$paradigm" "$pair_id" "$arm_order" \
      "$oracle_mode_effective" "$harness_oracle_passed" "$oracle_exit" "$diff_files" "$wall_ms" "$harness_failure")"
    echo "    (voir $log)" >&2
  else
    row="$(BM_TRIAL_ID="$trial_id" BM_SUITE_ID="$SUITE_ID" BM_TASK_ID="$task_id" BM_STRATUM="$stratum" \
      BM_PARADIGM="$paradigm" BM_PAIR_ID="$pair_id" BM_ARM_ORDER="$arm_order" \
      BM_VERIFY_PRESENT="$verify_present" BM_ORACLE_EXIT="$oracle_exit" BM_ORACLE_MODE="$oracle_mode_effective" \
      BM_DIFF_FILES="$diff_files" BM_WALL_MS="$wall_ms" \
      python3 "$ANALYZER" "$out" "$model" "$run")"
  fi
  printf '%s\n' "$row" | tee -a "$RESULTS"
  record_trial "$row" "$trial_id"
}

if [ "$PAIRING" = "abba" ]; then
  PAIR_CHAT=()
  PAIR_TASK=()
  if [ -n "$AB_CHAT_MODEL" ] && [ -n "$AB_TASK_MODEL" ]; then
    PAIR_CHAT+=("$AB_CHAT_MODEL")
    PAIR_TASK+=("$AB_TASK_MODEL")
  else
    for model in "${MODEL_LIST[@]}"; do
      [[ "$(canonical_model_name "$model")" == *-task ]] && continue
      for candidate in "${MODEL_LIST[@]}"; do
        if [ "$candidate" = "${model}-task" ]; then
          PAIR_CHAT+=("$model")
          PAIR_TASK+=("$candidate")
          break
        fi
      done
    done
  fi
  if [ "${#PAIR_CHAT[@]}" -eq 0 ]; then
    echo "Erreur: BENCHMARK_PAIRING=abba exige des paires (modèle, modèle-task), BENCHMARK_AB_CHAT_MODEL ou BENCHMARK_AB_TASK_MODEL." >&2
    exit 1
  fi

  UNPAIRED=()
  for model in "${MODEL_LIST[@]}"; do
    paired=false
    for pair_model in "${PAIR_CHAT[@]}" "${PAIR_TASK[@]}"; do
      [ "$model" = "$pair_model" ] && paired=true
    done
    [ "$paired" = false ] && UNPAIRED+=("$model")
  done

  for task_index in "${!TASK_IDS[@]}"; do
    for ((run = 1; run <= RUNS; run++)); do
      for pair_index in "${!PAIR_CHAT[@]}"; do
        chat_model="${PAIR_CHAT[$pair_index]}"
        task_model="${PAIR_TASK[$pair_index]}"
        pair_id="${SUITE_ID}:$(slug "${TASK_IDS[$task_index]}"):pair:$(slug "$(canonical_model_name "$chat_model")"):${run}"
        if (( run % 2 == 1 )); then
          run_one "$task_index" "$chat_model" "$run" "$pair_id" "AB_first"
          reset_proxy
          sleep "$PAIR_COOLDOWN"
          run_one "$task_index" "$task_model" "$run" "$pair_id" "AB_second"
        else
          run_one "$task_index" "$task_model" "$run" "$pair_id" "BA_first"
          reset_proxy
          sleep "$PAIR_COOLDOWN"
          run_one "$task_index" "$chat_model" "$run" "$pair_id" "BA_second"
        fi
      done
      for model in "${UNPAIRED[@]:-}"; do
        [ -n "$model" ] || continue
        run_one "$task_index" "$model" "$run" "" "single"
      done
    done
  done
else
  for task_index in "${!TASK_IDS[@]}"; do
    for model in "${MODEL_LIST[@]}"; do
      for ((run = 1; run <= RUNS; run++)); do
        run_one "$task_index" "$model" "$run" "" "single"
      done
    done
  done
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
# pass^k and pass@k are the unbiased combinatorial estimators of Yao et al.,
# replacing the naive (c/n)^k which is badly biased when n is small.
#   pass^1 = c/n
#   pass^k = C(c,k) / C(n,k)            (0 when c < k)
#   pass@k = 1 - C(n-c,k) / C(n,k)      (1 when n - c < k)
SUMMARY="$OUT_DIR/summary.txt"
failed_models=0
{
  echo "Benchmark matrix v3.5.7 — n=${RUNS}, k=${K}"
  echo "Plan: $PLAN_FILE"
  echo
  printf '%-28s %8s %8s %8s %8s %8s %8s\n' \
    "model" "passes" "runs" "pass^1" "pass^k" "pass@k" "verdict"
} >"$SUMMARY"

SUMMARY_ROWS="$(M="${MODEL_LIST[*]}" python3 - "$RESULTS" "$K" "$MIN_PASS_K" <<'PY'
import math, os, sys

results_path, k_raw, min_raw = sys.argv[1], sys.argv[2], sys.argv[3]
k = int(k_raw)
min_pass_k = float(min_raw)
models = os.environ["M"].split()
EPSILON = 1e-9

counts = {model: [0, 0] for model in models}
with open(results_path, encoding="utf-8") as handle:
    header = handle.readline().rstrip("\n").split("\t")
    for line in handle:
        fields = line.rstrip("\n").split("\t")
        # A malformed row must never be silently counted as a pass.
        if len(fields) != len(header):
            continue
        model, result = fields[0], fields[-1]
        if model not in counts:
            continue
        counts[model][1] += 1
        if result == "PASS":
            counts[model][0] += 1

failed = 0
rows = []
for model in models:
    passes, runs = counts[model]
    pass1 = passes / runs if runs else 0.0
    if runs >= k and passes >= k:
        passk = math.comb(passes, k) / math.comb(runs, k)
    else:
        passk = 0.0
    if runs >= k and (runs - passes) >= k:
        passat = 1.0 - math.comb(runs - passes, k) / math.comb(runs, k)
    else:
        passat = 1.0

    verdict = "PASS" if passk + EPSILON >= min_pass_k else "FAIL"
    if verdict == "FAIL":
        failed += 1
    rows.append((model, passes, runs, pass1, passk, passat, verdict))

print(failed)
for model, passes, runs, pass1, passk, passat, verdict in rows:
    print(f"{model}\t{passes}\t{runs}\t{pass1:.3f}\t{passk:.3f}\t{passat:.3f}\t{verdict}")
PY
)"

# The first line carries the FAIL count, the rest are the table rows.
failed_models="${SUMMARY_ROWS%%$'\n'*}"
{
  while IFS= read -r row; do
    [ -n "$row" ] || continue
    IFS=$'\t' read -r model passes runs pass1 passk passat verdict <<<"$row"
    printf '%-28s %8s %8s %8s %8s %8s %8s\n' \
      "$model" "$passes" "$runs" "$pass1" "$passk" "$passat" "$verdict"
  done < <(printf '%s\n' "${SUMMARY_ROWS#*$'\n'}")
  echo
  echo "Détail: $RESULTS"
  echo "Transcripts: $OUT_DIR"
  if [ "$failed_models" -gt 0 ]; then
    echo "Modèles sous pass^${K} >= ${MIN_PASS_K}: $failed_models/${#MODEL_LIST[@]}"
  fi
} >>"$SUMMARY"

if [ -n "$TASKS_DIR" ]; then
  {
    echo
    echo "Par tâche (pass^${K}):"
    printf '%-20s %-28s %8s %8s %8s\n' "task" "model" "passes" "runs" "pass^k"
  } >>"$SUMMARY"
  python3 - "$RESULTS" "$K" <<'PY' >>"$SUMMARY"
import math, sys

results_path, k = sys.argv[1], int(sys.argv[2])
counts = {}
with open(results_path, encoding="utf-8") as handle:
    header = handle.readline().rstrip("\n").split("\t")
    try:
        task_col = header.index("task_id")
    except ValueError:
        sys.exit(0)
    for line in handle:
        fields = line.rstrip("\n").split("\t")
        if len(fields) != len(header):
            continue
        task, model, result = fields[task_col], fields[0], fields[-1]
        key = (task, model)
        counts.setdefault(key, [0, 0])
        counts[key][1] += 1
        if result == "PASS":
            counts[key][0] += 1

for (task, model), (passes, runs) in sorted(counts.items()):
    if runs >= k and passes >= k:
        value = math.comb(passes, k) / math.comb(runs, k)
    else:
        value = 0.0
    print(f"{task:<20} {model:<28} {passes:>8} {runs:>8} {value:>8.3f}")
PY
fi

cat "$SUMMARY"

if [ "$STRICT" = "1" ] && [ "$failed_models" -gt 0 ]; then
  exit 1
fi
exit 0
