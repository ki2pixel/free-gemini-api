#!/usr/bin/env bash
# Offline regression suite for scripts/benchmark_matrix.sh.
#
# It never contacts the proxy or Google: OPENCODE_BIN points at a fake binary
# that records its argv and the inherited OPENCODE_CONFIG_CONTENT, then emits a
# minimal transcript. The harness is copied into a throwaway git repository so
# ROOT_DIR, the snapshot and every worktree stay outside this checkout.
#
# Coverage:
#   1. Task-mode opt-in routing (auto / true / false, provider prefix, #variant)
#   2. Unbiased pass^k / pass@k estimators (Yao et al.) and their edge cases
#   3. Input validation (K > RUNS, invalid enums, out-of-range MIN_PASS_K)
#   4. BENCHMARK_MIN_PASS_K thresholding and BENCHMARK_STRICT exit codes
#   5. results.tsv column integrity
#
# Usage: ./scripts/test_benchmark_matrix.sh

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BENCH_SRC="$ROOT_DIR/scripts/benchmark_matrix.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/benchmark-selftest.XXXXXX")"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASSED=0
FAILED=0

pass() { PASSED=$((PASSED + 1)); printf '  ok   %s\n' "$1"; }
fail() { FAILED=$((FAILED + 1)); printf '  FAIL %s\n' "$1" >&2; }
section() { printf '\n== %s ==\n' "$1"; }

assert_eq() {
  if [ "$2" = "$3" ]; then
    pass "$1"
  else
    fail "$1 (attendu: '$2', obtenu: '$3')"
  fi
}

assert_contains() {
  case "$2" in
    *"$3"*) pass "$1" ;;
    *) fail "$1 (motif '$3' absent de '$2')" ;;
  esac
}

assert_not_contains() {
  case "$2" in
    *"$3"*) fail "$1 (motif '$3' présent dans '$2')" ;;
    *) pass "$1" ;;
  esac
}

# ---------------------------------------------------------------------------
# Isolated repository + harness copy
# ---------------------------------------------------------------------------
# The harness derives ROOT_DIR from its own location and runs `git stash create`
# plus `git worktree add` against it. Copying it into the throwaway repo keeps
# every git side effect away from the real checkout.
REPO="$TMP_ROOT/repo"
mkdir -p "$REPO/scripts"
cp "$BENCH_SRC" "$REPO/scripts/benchmark_matrix.sh"
chmod +x "$REPO/scripts/benchmark_matrix.sh"
git init -q "$REPO"
git -C "$REPO" config user.email selftest@example.com
git -C "$REPO" config user.name selftest
printf 'seed\n' >"$REPO/README.md"
git -C "$REPO" add README.md
git -C "$REPO" commit -qm seed

PLAN_FILE="$TMP_ROOT/plan.md"
printf 'Plan de test hors-ligne.\n' >"$PLAN_FILE"

# ---------------------------------------------------------------------------
# Fake OpenCode
# ---------------------------------------------------------------------------
FAKE_BIN="$TMP_ROOT/opencode"
cat >"$FAKE_BIN" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail

# One record per invocation: the overlay it inherited, then its argv.
printf 'overlay\t%s\n' "${OPENCODE_CONFIG_CONTENT:-}" >>"$MOCK_LOG"
printf 'argv\t%s\n' "$*" >>"$MOCK_LOG"

# Pick this invocation's scripted verdict from MOCK_RESULTS (comma separated).
index=$(grep -c '^overlay' "$MOCK_LOG")
index=$((index - 1))
verdict="PASS"
if [ -n "${MOCK_RESULTS:-}" ]; then
  IFS=',' read -r -a seq <<<"$MOCK_RESULTS"
  if [ "$index" -lt "${#seq[@]}" ]; then
    verdict="${seq[$index]}"
  else
    verdict="${seq[$((${#seq[@]} - 1))]}"
  fi
fi

if [ "$verdict" = "PASS" ]; then
  printf '{"type":"tool","tool":"read","text":"ok"}\n'
  printf '{"type":"text","text":"Modifications appliquées et vérifiées."}\n'
  printf '{"finish_reason":"stop"}\n'
else
  printf '{"finish_reason":"error"}\n'
fi
MOCK
chmod +x "$FAKE_BIN"

# run_bench <out_dir> <env assignment>... -- <args>...
run_bench() {
  local out_dir="$1"
  shift
  local env_pairs=()
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
    env_pairs+=("$1")
    shift
  done
  [ "$#" -gt 0 ] && shift

  mkdir -p "$out_dir"
  local rc=0
  ( cd "$REPO" && env "${env_pairs[@]}" \
      BENCHMARK_OUT_DIR="$out_dir" \
      BENCHMARK_WORKTREE_BASE="$out_dir/worktrees" \
      BENCHMARK_PLAN_FILE="$PLAN_FILE" \
      OPENCODE_BIN="$FAKE_BIN" \
      MOCK_LOG="$out_dir/mock.log" \
      ./scripts/benchmark_matrix.sh "$@" ) \
    >"$out_dir/stdout.log" 2>"$out_dir/stderr.log" || rc=$?
  return "$rc"
}

# overlay_n <out_dir> <n> — the overlay of the n-th invocation (1-based)
overlay_n() { sed -n 's/^overlay\t//p' "$1/mock.log" | sed -n "$2p"; }
argv_n() { sed -n 's/^argv\t//p' "$1/mock.log" | sed -n "$2p"; }

# summary_field <out_dir> <column-name> <field-number>
summary_row() {
  awk -F'[[:space:]]+' -v want="$2" '$1 == want { print $0 }' "$1/summary.txt"
}

# ---------------------------------------------------------------------------
section "1. Injection Task [40] vs Chat [0]"
# ---------------------------------------------------------------------------
OUT="$TMP_ROOT/t1"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen gemini-4-hydrogen-task" \
  BENCHMARK_RUNS=1 -- || true

ARGV_H="$(argv_n "$OUT" 1)"
ARGV_T="$(argv_n "$OUT" 2)"
OV_H="$(overlay_n "$OUT" 1)"
OV_T="$(overlay_n "$OUT" 2)"

assert_not_contains "Chat: aucun --standalone" "$ARGV_H" "--standalone"
assert_eq "Chat: aucun overlay injecté" "" "$OV_H"
assert_contains "Task: --standalone présent" "$ARGV_T" "--standalone"
assert_contains "Task: header sous providers.free-gemini.headers" "$OV_T" \
  '"free-gemini": {"headers": {"X-Allow-Task-Mode-For-Tools": "true"}}'
assert_contains "Task: référence préfixée" "$ARGV_T" "free-gemini/gemini-4-hydrogen-task"

OUT="$TMP_ROOT/t2"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen-task#high" \
  BENCHMARK_RUNS=1 -- || true
assert_contains "#variant: opt-in appliqué" "$(overlay_n "$OUT" 1)" \
  "X-Allow-Task-Mode-For-Tools"
assert_contains "#variant: référence intacte" "$(argv_n "$OUT" 1)" "#high"

OUT="$TMP_ROOT/t3"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen-task" \
  BENCHMARK_RUNS=1 BENCHMARK_ALLOW_TASK_MODE_FOR_TOOLS=false -- || true
assert_eq "forced=false: aucun overlay" "" "$(overlay_n "$OUT" 1)"
assert_not_contains "forced=false: aucun --standalone" "$(argv_n "$OUT" 1)" "--standalone"

OUT="$TMP_ROOT/t4"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen" \
  BENCHMARK_RUNS=1 BENCHMARK_ALLOW_TASK_MODE_FOR_TOOLS=true -- || true
assert_contains "forced=true: overlay sur un modèle Chat" "$(overlay_n "$OUT" 1)" \
  "X-Allow-Task-Mode-For-Tools"

OUT="$TMP_ROOT/t5"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen-task" \
  BENCHMARK_RUNS=1 BENCHMARK_MODEL_PREFIX="" \
  BENCHMARK_PROVIDER_ID="my-provider" -- || true
assert_contains "provider de repli utilisé" "$(overlay_n "$OUT" 1)" '"my-provider"'
assert_not_contains "provider de repli: pas de préfixe dans le modèle" \
  "$(argv_n "$OUT" 1)" "free-gemini/"

OUT="$TMP_ROOT/t6"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen-task" BENCHMARK_RUNS=1 \
  OPENCODE_CONFIG_CONTENT='{"theme":"dark","providers":{"free-gemini":{"headers":{"X-Keep":"1"}}}}' \
  -- || true
OV_M="$(overlay_n "$OUT" 1)"
assert_contains "overlay appelant: clé racine préservée" "$OV_M" '"theme": "dark"'
assert_contains "overlay appelant: header existant préservé" "$OV_M" '"X-Keep": "1"'
assert_contains "overlay appelant: opt-in ajouté" "$OV_M" "X-Allow-Task-Mode-For-Tools"

# BENCHMARK_STANDALONE=true must isolate a Chat model without injecting a header.
OUT="$TMP_ROOT/t7"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen" BENCHMARK_RUNS=1 \
  BENCHMARK_STANDALONE=true -- || true
assert_contains "standalone=true: --standalone sur Chat" "$(argv_n "$OUT" 1)" "--standalone"
assert_eq "standalone=true: aucun header injecté" "" "$(overlay_n "$OUT" 1)"

# ---------------------------------------------------------------------------
section "2. Estimateurs combinatoires (Yao et al.)"
# ---------------------------------------------------------------------------
# check_estimators <label> <results> <k> <min> <exp_passes> <p1> <pk> <pat> <verdict>
check_estimators() {
  local label="$1" results="$2" k="$3" min="$4"
  local want_passes="$5" want_p1="$6" want_pk="$7" want_pat="$8" want_verdict="$9"
  local out="$TMP_ROOT/est-$label"
  run_bench "$out" BENCHMARK_MODELS="m" BENCHMARK_RUNS=5 BENCHMARK_K="$k" \
    BENCHMARK_MIN_PASS_K="$min" MOCK_RESULTS="$results" -- || true

  local row
  row="$(summary_row "$out" "m")"
  # shellcheck disable=SC2086
  set -- $row
  assert_eq "$label: passes" "$want_passes" "$2"
  assert_eq "$label: runs" "5" "$3"
  assert_eq "$label: pass^1" "$want_p1" "$4"
  assert_eq "$label: pass^k" "$want_pk" "$5"
  assert_eq "$label: pass@k" "$want_pat" "$6"
  assert_eq "$label: verdict" "$want_verdict" "$7"
}

# c=3, n=5, k=3 → 0.600 / 0.100 / 1.000. The naive (c/n)^k would give 0.216.
check_estimators "c3" "PASS,PASS,PASS,FAIL,FAIL" 3 1.0 3 0.600 0.100 1.000 FAIL
# c=5 → every estimator saturates.
check_estimators "c5" "PASS,PASS,PASS,PASS,PASS" 3 1.0 5 1.000 1.000 1.000 PASS
# c=0 → pass^k = 0 and pass@k = 0 because n - c >= k.
check_estimators "c0" "FAIL,FAIL,FAIL,FAIL,FAIL" 3 1.0 0 0.000 0.000 0.000 FAIL
# k=1 → pass^k == pass@k == pass^1.
check_estimators "k1" "PASS,PASS,PASS,FAIL,FAIL" 1 1.0 3 0.600 0.600 0.600 FAIL
# c=4, n=5, k=3 → 0.800 / 0.400 / 1.000.
check_estimators "c4" "PASS,PASS,PASS,PASS,FAIL" 3 1.0 4 0.800 0.400 1.000 FAIL
# Threshold: pass^3 = 0.100 clears 0.05 but not 0.5.
check_estimators "min-low" "PASS,PASS,PASS,FAIL,FAIL" 3 0.05 3 0.600 0.100 1.000 PASS
check_estimators "min-high" "PASS,PASS,PASS,FAIL,FAIL" 3 0.5 3 0.600 0.100 1.000 FAIL

# BENCHMARK_K defaults to BENCHMARK_RUNS.
OUT="$TMP_ROOT/est-default-k"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=3 \
  MOCK_RESULTS="PASS,PASS,PASS" -- || true
assert_contains "K par défaut = RUNS (en-tête)" "$(cat "$OUT/summary.txt")" "n=3, k=3"

# BENCHMARK_STRICT=1 exit codes.
strict_case() {
  local label="$1" want_rc="$2" min="$3" results="$4"
  local out="$TMP_ROOT/strict-$label" rc=0
  run_bench "$out" BENCHMARK_MODELS="m" BENCHMARK_RUNS=5 BENCHMARK_K=3 \
    BENCHMARK_MIN_PASS_K="$min" BENCHMARK_STRICT=1 MOCK_RESULTS="$results" -- || rc=$?
  assert_eq "$label" "$want_rc" "$rc"
}

strict_case "STRICT=1 sous le seuil: exit 1" 1 1.0 "PASS,PASS,PASS,FAIL,FAIL"
strict_case "STRICT=1 au-dessus du seuil: exit 0" 0 0.1 "PASS,PASS,PASS,FAIL,FAIL"
strict_case "STRICT=1 tout vert: exit 0" 0 1.0 "PASS,PASS,PASS,PASS,PASS"

# BENCHMARK_STRICT=0 must never fail on a red model.
OUT="$TMP_ROOT/nonstrict"
rc=0
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=2 BENCHMARK_STRICT=0 \
  MOCK_RESULTS="FAIL,FAIL" -- || rc=$?
assert_eq "STRICT=0: exit 0 malgré un échec" "0" "$rc"

# ---------------------------------------------------------------------------
section "3. Validation des entrées"
# ---------------------------------------------------------------------------
expect_reject() {
  local label="$1"
  shift
  local rc=0
  run_bench "$TMP_ROOT/reject-$label" "$@" -- || rc=$?
  if [ "$rc" -ne 0 ]; then
    pass "$label rejeté (exit $rc)"
  else
    fail "$label aurait dû être rejeté"
  fi
}

expect_reject "K>RUNS" BENCHMARK_RUNS=2 BENCHMARK_K=3 BENCHMARK_MODELS="m" \
  MOCK_RESULTS="PASS,PASS"
expect_reject "K=0" BENCHMARK_RUNS=2 BENCHMARK_K=0 BENCHMARK_MODELS="m" \
  MOCK_RESULTS="PASS,PASS"
expect_reject "K non numérique" BENCHMARK_RUNS=2 BENCHMARK_K=abc BENCHMARK_MODELS="m" \
  MOCK_RESULTS="PASS,PASS"
expect_reject "ALLOW_TASK_MODE invalide" BENCHMARK_RUNS=1 BENCHMARK_MODELS="m" \
  BENCHMARK_ALLOW_TASK_MODE_FOR_TOOLS=yes MOCK_RESULTS="PASS"
expect_reject "STANDALONE invalide" BENCHMARK_RUNS=1 BENCHMARK_MODELS="m" \
  BENCHMARK_STANDALONE=maybe MOCK_RESULTS="PASS"
expect_reject "MIN_PASS_K hors bornes" BENCHMARK_RUNS=1 BENCHMARK_MODELS="m" \
  BENCHMARK_MIN_PASS_K=1.5 MOCK_RESULTS="PASS"

# ---------------------------------------------------------------------------
section "4. Intégrité du TSV"
# ---------------------------------------------------------------------------
OUT="$TMP_ROOT/tsv"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=2 \
  MOCK_RESULTS="PASS,FAIL" -- || true
assert_eq "en-tête: 9 colonnes" "9" \
  "$(head -n 1 "$OUT/results.tsv" | awk -F'\t' '{print NF}')"
assert_eq "toutes les lignes: 9 colonnes" "0" \
  "$(awk -F'\t' 'NF != 9 {n++} END {print n+0}' "$OUT/results.tsv")"
assert_eq "deux runs enregistrés" "2" \
  "$(awk 'NR > 1 {n++} END {print n+0}' "$OUT/results.tsv")"

# ---------------------------------------------------------------------------
printf '\n== Résultat ==\n'
printf 'succès: %d, échecs: %d\n' "$PASSED" "$FAILED"
if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
echo "TOUS LES TESTS DU HARNAIS SONT PASSÉS"
