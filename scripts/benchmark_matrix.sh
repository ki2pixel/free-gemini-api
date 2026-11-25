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
if ! awk -v v="$MIN_PASS_K" 'BEGIN { exit (v >= 0 && v <= 1) ? 0 : 1 }'; then
  echo "Erreur: BENCHMARK_MIN_PASS_K doit être dans [0,1] (reçu: $MIN_PASS_K)" >&2
  exit 1
fi

if [ "$STRICT" != "0" ] && [ "$STRICT" != "1" ]; then
  echo "Erreur: BENCHMARK_STRICT doit valoir 0 ou 1 (reçu: $STRICT)" >&2
  exit 1
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
        git -C "$ROOT_DIR" worktree remove --force "$dir" >/dev/null 2>&1 || rm -rf "$dir"
      fi
    done
  fi
  rm -rf "$TMP_DIR"
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

# build_task_mode_overlay prints the OPENCODE_CONFIG_CONTENT document that
# carries the opt-in header for one provider, merging into any overlay already
# exported by the caller. OpenCode decodes config with onExcessProperty
# "ignore" (core/src/config.ts), so a misplaced key would be dropped silently:
# the header MUST live under providers.<id>.headers.
build_task_mode_overlay() {
  local provider_id="$1" base="$2"
  EXISTING_OVERLAY="$base" python3 - "$provider_id" <<'PY'
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
import re
import sys

path, model, run = sys.argv[1], sys.argv[2], sys.argv[3]

try:
    raw = open(path, encoding="utf-8", errors="replace").read()
except OSError:
    # Nine tab-separated fields, matching the results.tsv header. The read
    # error already reached stderr through the shell redirection.
    print(f"{model}\t{run}\t0\t0\t0\t0\t0\tharness_error\tFAIL")
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
passed = (
    tool_calls > 0
    and finish_ok
    and not greeting
    and not fake_quota
    and not s7
    and not incomplete
)

print("\t".join([
    model,
    str(run),
    str(tool_calls),
    "1" if greeting else "0",
    "1" if fake_quota else "0",
    "1" if s7 else "0",
    "1" if incomplete else "0",
    finish or "unknown",
    "PASS" if passed else "FAIL",
]))
PY

# ---------------------------------------------------------------------------
# Runs
# ---------------------------------------------------------------------------
RESULTS="$OUT_DIR/results.tsv"
printf 'model\trun\ttool_calls\tgreeting\tfake_quota\ts7\tincomplete\tfinish\tresult\n' >"$RESULTS"

# Preserve any overlay the caller exported: build_task_mode_overlay merges into
# it, so it must survive being unset between runs.
CALLER_OVERLAY="${OPENCODE_CONFIG_CONTENT:-}"

restore_caller_overlay() {
  if [ -n "$CALLER_OVERLAY" ]; then
    export OPENCODE_CONFIG_CONTENT="$CALLER_OVERLAY"
  else
    unset OPENCODE_CONFIG_CONTENT
  fi
}

for model in "${MODEL_LIST[@]}"; do
  safe_model="$(printf '%s' "$model" | tr -c 'A-Za-z0-9._-' '_')"
  for ((run = 1; run <= RUNS; run++)); do
    dir="$WORKTREE_BASE/${safe_model}-run${run}"
    out="$OUT_DIR/${safe_model}-run${run}.json"
    log="$OUT_DIR/${safe_model}-run${run}.log"
    model_ref="$model"
    if [ -n "$MODEL_PREFIX" ]; then
      model_ref="${MODEL_PREFIX}${model}"
    fi

    # Decide the Task [40] opt-in for this run. Without the header the proxy
    # requalifies a tool-bearing -task request to Chat [0], so the Task arm
    # would silently measure the Chat model (chat_service.go:191).
    overlay=""
    task_mode_opt_in=false
    if should_opt_in_task "$model_ref"; then
      task_mode_opt_in=true
      overlay="$(build_task_mode_overlay "$(provider_id_for_model "$model_ref")" "$CALLER_OVERLAY")"
    fi

    # The overlay only reaches the server that owns the config, so a private
    # server is required whenever one is injected.
    use_standalone=false
    case "$STANDALONE_MODE" in
      true) use_standalone=true ;;
      auto)
        if [ -n "$overlay" ]; then
          use_standalone=true
        fi
        ;;
    esac

    run_args=()
    if [ "$use_standalone" = "true" ]; then
      run_args+=(--standalone)
    fi
    run_args+=(run --format json --auto --model "$model_ref"
      --title "bench-${model}-run${run}" "$PLAN_TEXT")

    echo "=== [${model} ${run}/${RUNS}] ${model_ref} (task_mode_opt_in=${task_mode_opt_in}, standalone=${use_standalone}) ==="
    prepare_worktree "$dir"

    # Scope the overlay to this single invocation: leaving it exported would
    # leak the Task opt-in into the Chat arm of the next iteration.
    if [ -n "$overlay" ]; then
      export OPENCODE_CONFIG_CONTENT="$overlay"
    else
      restore_caller_overlay
    fi

    if (cd "$dir" && "$OPENCODE_BIN" "${run_args[@]}") >"$out" 2>"$log"; then
      python3 "$ANALYZER" "$out" "$model" "$run" | tee -a "$RESULTS"
    else
      printf '%s\t%d\t0\t0\t0\t0\t0\tharness_error\tFAIL\n' "$model" "$run" | tee -a "$RESULTS"
      echo "    (voir $log)" >&2
    fi
    restore_caller_overlay
  done
done

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

cat "$SUMMARY"

if [ "$STRICT" = "1" ] && [ "$failed_models" -gt 0 ]; then
  exit 1
fi
exit 0
