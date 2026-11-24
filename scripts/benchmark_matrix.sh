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
# It prints a per-run table and a pass^k summary (all K runs must pass).
#
# Prerequisites:
#   - OpenCode is configured with a provider pointing at the proxy. The default
#     provider prefix is "free-gemini/"; override it with BENCHMARK_MODEL_PREFIX.
#   - The proxy is running and authenticated for that provider.
#
# Usage:
#   ./scripts/benchmark_matrix.sh
#   BENCHMARK_RUNS=3 BENCHMARK_MODELS="gemini-4-neon gemini-4-hydrogen" ./scripts/benchmark_matrix.sh
#
# Environment:
#   BENCHMARK_RUNS             Runs per model (default: 3).
#   BENCHMARK_MODELS           Space/comma separated model IDs (default: the
#                              four v3.5.7 models).
#   BENCHMARK_MODEL_PREFIX     OpenCode provider prefix (default: free-gemini/).
#                              Set to an empty string to use bare model IDs.
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
MODELS="${BENCHMARK_MODELS:-gemini-4-neon gemini-4-hydrogen gemini-4-hydrogen-task gemini-4-argon}"
MODEL_PREFIX="${BENCHMARK_MODEL_PREFIX-free-gemini/}"
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
except OSError as exc:
    print(f"{model}\t{run}\t0\t0\t0\t0\t0\tharness_error\tFAIL\t{exc}")
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

    echo "=== [${model} ${run}/${RUNS}] ${model_ref} ==="
    prepare_worktree "$dir"

    if (cd "$dir" && "$OPENCODE_BIN" run --format json --auto \
      --model "$model_ref" --title "bench-${model}-run${run}" "$PLAN_TEXT") \
      >"$out" 2>"$log"; then
      python3 "$ANALYZER" "$out" "$model" "$run" | tee -a "$RESULTS"
    else
      printf '%s\t%d\t0\t0\t0\t0\t0\tharness_error\tFAIL\n' "$model" "$run" | tee -a "$RESULTS"
      echo "    (voir $log)" >&2
    fi
  done
done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
SUMMARY="$OUT_DIR/summary.txt"
{
  echo "Benchmark matrix v3.5.7 — pass^${RUNS}"
  echo "Plan: $PLAN_FILE"
  echo
  printf '%-28s %8s %8s %8s\n' "model" "passes" "runs" "pass^k"
  failed_models=0
  for model in "${MODEL_LIST[@]}"; do
    passes="$(awk -F'\t' -v model="$model" '$1 == model && $9 == "PASS" { n++ } END { print n+0 }' "$RESULTS")"
    total="$(awk -F'\t' -v model="$model" '$1 == model { n++ } END { print n+0 }' "$RESULTS")"
    if [ "$total" -eq "$RUNS" ] && [ "$passes" -eq "$RUNS" ]; then
      verdict="PASS"
    else
      verdict="FAIL"
      failed_models=$((failed_models + 1))
    fi
    printf '%-28s %8d %8d %8s\n' "$model" "$passes" "$total" "$verdict"
  done
  echo
  echo "Détail: $RESULTS"
  echo "Transcripts: $OUT_DIR"
  if [ "$failed_models" -gt 0 ]; then
    echo "Modèles sans pass^${RUNS}: $failed_models/${#MODEL_LIST[@]}"
  fi
} | tee "$SUMMARY"

if [ "$STRICT" = "1" ] && grep -q $'\tFAIL$' "$RESULTS"; then
  exit 1
fi
exit 0
