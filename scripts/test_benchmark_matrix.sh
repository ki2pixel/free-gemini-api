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
#   6. X-Benchmark-Trial-ID overlay propagation
#   7. Deterministic verify.sh oracle and failure classification
#   8. AB/BA pairing, pair_id sharing and arm alternation
#   9. POST /v1/benchmark/trials verdict recording (local HTTP stub)
#  10. Embedded S1/S2/S3 tasks and the harness_error row shape
#  11. BENCHMARK_TASK_FILTER selection, empty match and invalid regex
#  12. Extended task suite: initial failure, fixed success, rejected tamper
#  13. benchmark_report.py raw/conditional rates, paired delta and SQLite alerts
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
# Copy the embedded task suite so its real verify.sh contracts are exercised,
# not only the synthetic t_oracle task.
for task in S1_single_unit_fix S2_multi_file_wiring S3_readonly_impossible \
            S1_edge_case_guard S2_long_horizon_chain S3_missing_prereq_abstention; do
  mkdir -p "$REPO/scripts/tasks_$task"
  cp -R "$ROOT_DIR/scripts/benchmark_tasks/$task" "$REPO/scripts/tasks_$task/"
done
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
  if [ -n "${MOCK_WRITE_FILE:-}" ]; then
    printf 'ok\n' >"$MOCK_WRITE_FILE"
  fi
  # Simulate the agent's edit for the embedded S1/S2/S3 tasks.
  if [ -n "${MOCK_APPLY_FIX:-}" ]; then
    case "$MOCK_APPLY_FIX" in
      s1)
        sed -i 's/return a - b/return a + b/' benchmark_fixture/calc.go
        ;;
      s2)
        python3 - <<'PYFIX'
from pathlib import Path

square = Path("benchmark_fixture2/square.go")
square.write_text(square.read_text() + "\nfunc (s Square) Area() int { return s.Side * s.Side }\n")
factory = Path("benchmark_fixture2/factory.go")
text = factory.read_text()
factory.write_text(text.replace(
    '\tif kind == "circle" {',
    '\tif kind == "square" {\n\t\treturn Square{Side: size}\n\t}\n\tif kind == "circle" {',
))
PYFIX
        ;;
      s3)
        printf 'CONTRACT_OK\nPublicContract\n' >report.md
        ;;
      s3_tamper)
        printf 'CONTRACT_OK\nPublicContract\n' >report.md
        printf '\n// tampered\n' >>benchmark_fixture3/contract.go
        ;;
      s1_edge)
        python3 - <<'PYFIX'
from pathlib import Path

path = Path("benchmark_edge/summarize.go")
path.write_text('''package benchmarkedge

import "strings"

func Summarize(s string, maxRunes int) string {
	words := strings.Fields(s)
	if len(words) == 0 || maxRunes <= 0 {
		return ""
	}
	runes := []rune(words[0])
	first := strings.ToUpper(string(runes[0]))
	if len(runes) <= maxRunes {
		return first + string(runes[1:])
	}
	return first + string(runes[1:maxRunes]) + "…"
}
''')
PYFIX
        ;;
      s1_edge_tamper)
        python3 - <<'PYFIX'
from pathlib import Path

path = Path("benchmark_edge/summarize.go")
path.write_text('''package benchmarkedge

import "strings"

func Summarize(s string, maxRunes int) string {
	words := strings.Fields(s)
	if len(words) == 0 || maxRunes <= 0 {
		return ""
	}
	runes := []rune(words[0])
	first := strings.ToUpper(string(runes[0]))
	if len(runes) <= maxRunes {
		return first + string(runes[1:])
	}
	return first + string(runes[1:maxRunes]) + "…"
}
''')
Path("benchmark_edge/summarize_test.go").write_text(
    Path("benchmark_edge/summarize_test.go").read_text() + "\n// tampered\n"
)
PYFIX
        ;;
      s2_chain)
        python3 - <<'PYFIX'
from pathlib import Path

node04 = Path("benchmark_chain/node04.go")
node04.write_text(node04.read_text().replace("return x * 2", "return x * 3"))
node09 = Path("benchmark_chain/node09.go")
node09.write_text(node09.read_text().replace("const enabled = false", "const enabled = true"))
PYFIX
        ;;
      s2_chain_tamper)
        python3 - <<'PYFIX'
from pathlib import Path

node04 = Path("benchmark_chain/node04.go")
node04.write_text(node04.read_text().replace("return x * 2", "return x * 3"))
node09 = Path("benchmark_chain/node09.go")
node09.write_text(node09.read_text().replace("const enabled = false", "const enabled = true"))
test = Path("benchmark_chain/chain_test.go")
test.write_text(test.read_text() + "\n// tampered\n")
PYFIX
        ;;
      s3_missing)
        python3 - <<'PYFIX'
from pathlib import Path

Path("abstention.md").write_text(
    "MISSING_PREREQUISITE\n"
    "missing_secret_policy.yaml\n"
    "status: not_found\n"
    "La politique ne peut pas être chargée car le fichier est absent.\n"
)
PYFIX
        ;;
      s3_missing_create)
        python3 - <<'PYFIX'
from pathlib import Path

Path("abstention.md").write_text(
    "MISSING_PREREQUISITE\n"
    "missing_secret_policy.yaml\n"
    "status: not_found\n"
    "La politique ne peut pas être chargée car le fichier est absent.\n"
)
Path("missing_secret_policy.yaml").write_text("allow: true\n")
PYFIX
        ;;
      s3_missing_tamper)
        python3 - <<'PYFIX'
from pathlib import Path

Path("abstention.md").write_text(
    "MISSING_PREREQUISITE\n"
    "missing_secret_policy.yaml\n"
    "status: not_found\n"
    "La politique ne peut pas être chargée car le fichier est absent.\n"
)
policy = Path("benchmark_prereq/policy.go")
policy.write_text(policy.read_text() + "\n// tampered\n")
PYFIX
        ;;
    esac
  fi
  printf '{"type":"tool","tool":"read","text":"ok"}\n'
  printf '{"type":"text","text":"Modifications appliquées et vérifiées."}\n'
  printf '{"finish_reason":"stop"}\n'
else
  printf '{"finish_reason":"error"}\n'
fi

# Fault injection for the harness_error branch.
if [ -n "${MOCK_EXIT:-}" ]; then
  exit "$MOCK_EXIT"
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
  # Legacy sections 1-4 exercise the opt-in in isolation: no trial header, no
  # verdict endpoint. The new sections opt in explicitly.
  local has_emit=false has_record=false pair
  for pair in "${env_pairs[@]:-}"; do
    case "$pair" in
      BENCHMARK_EMIT_TRIAL_ID=*) has_emit=true ;;
      BENCHMARK_RECORD_TRIALS_URL=*) has_record=true ;;
    esac
  done
  [ "$has_emit" = true ] || env_pairs+=("BENCHMARK_EMIT_TRIAL_ID=false")
  [ "$has_record" = true ] || env_pairs+=("BENCHMARK_RECORD_TRIALS_URL=none")

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
assert_eq "en-tête: 23 colonnes" "23" \
  "$(head -n 1 "$OUT/results.tsv" | awk -F'\t' '{print NF}')"
assert_eq "toutes les lignes: 23 colonnes" "0" \
  "$(awk -F'\t' 'NF != 23 {n++} END {print n+0}' "$OUT/results.tsv")"
assert_eq "deux runs enregistrés" "2" \
  "$(awk 'NR > 1 {n++} END {print n+0}' "$OUT/results.tsv")"

# ---------------------------------------------------------------------------
section "5. Propagation de X-Benchmark-Trial-ID"
# ---------------------------------------------------------------------------
OUT="$TMP_ROOT/trial-id"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen gemini-4-hydrogen-task" \
  BENCHMARK_RUNS=1 BENCHMARK_EMIT_TRIAL_ID=true -- || true
OV_CHAT="$(overlay_n "$OUT" 1)"
OV_TASK="$(overlay_n "$OUT" 2)"
assert_contains "trial: en-tête sur le bras Chat" "$OV_CHAT" \
  '"X-Benchmark-Trial-ID": "legacy-v357:legacy:chat:gemini-4-hydrogen:1"'
assert_not_contains "trial: aucun opt-in Task sur le bras Chat" "$OV_CHAT" \
  "X-Allow-Task-Mode-For-Tools"
assert_contains "trial: --standalone suit l'overlay (Chat)" "$(argv_n "$OUT" 1)" "--standalone"
assert_contains "trial: en-tête sur le bras Task" "$OV_TASK" '"X-Benchmark-Trial-ID"'
assert_contains "trial: opt-in Task conservé" "$OV_TASK" '"X-Allow-Task-Mode-For-Tools": "true"'

OUT="$TMP_ROOT/trial-caller"
run_bench "$OUT" BENCHMARK_MODELS="gemini-4-hydrogen" BENCHMARK_RUNS=1 \
  BENCHMARK_EMIT_TRIAL_ID=true 'OPENCODE_CONFIG_CONTENT={"theme": "dark"}' -- || true
OV="$(overlay_n "$OUT" 1)"
assert_contains "trial: overlay appelant préservé" "$OV" '"theme": "dark"'
assert_contains "trial: en-tête ajouté à l'overlay appelant" "$OV" '"X-Benchmark-Trial-ID"'

# ---------------------------------------------------------------------------
section "6. Oracle déterministe verify.sh"
# ---------------------------------------------------------------------------
TASKS_ROOT="$REPO/scripts/benchmark_tasks"
mkdir -p "$TASKS_ROOT/t_oracle"
printf 'TASK_ID=t_oracle\nSTRATUM=S1\n' >"$TASKS_ROOT/t_oracle/meta.env"
printf 'Crée le fichier oracle-proof.txt.\n' >"$TASKS_ROOT/t_oracle/prompt.md"
cat >"$TASKS_ROOT/t_oracle/verify.sh" <<'VERIFY'
#!/usr/bin/env bash
set -euo pipefail
test -f oracle-proof.txt
grep -q '^ok$' oracle-proof.txt
VERIFY

OUT="$TMP_ROOT/oracle-pass"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  BENCHMARK_TASKS_DIR="scripts/benchmark_tasks" BENCHMARK_SUITE_ID="testsuite" \
  BENCHMARK_EMIT_TRIAL_ID=true MOCK_WRITE_FILE="oracle-proof.txt" MOCK_RESULTS="PASS" -- || true
assert_eq "oracle: mode hybride" "hybrid" "$(awk -F'\t' 'NR==2{print $16}' "$OUT/results.tsv")"
assert_eq "oracle: passed" "1" "$(awk -F'\t' 'NR==2{print $17}' "$OUT/results.tsv")"
assert_eq "oracle: aucun code d'échec" "none" "$(awk -F'\t' 'NR==2{print $22}' "$OUT/results.tsv")"
assert_eq "oracle: verdict PASS" "PASS" "$(awk -F'\t' 'NR==2{print $23}' "$OUT/results.tsv")"
assert_eq "oracle: suite propagée" "testsuite" "$(awk -F'\t' 'NR==2{print $10}' "$OUT/results.tsv")"
assert_eq "oracle: strate propagée" "S1" "$(awk -F'\t' 'NR==2{print $12}' "$OUT/results.tsv")"

OUT="$TMP_ROOT/oracle-fail"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  BENCHMARK_TASKS_DIR="scripts/benchmark_tasks" BENCHMARK_SUITE_ID="testsuite" \
  BENCHMARK_EMIT_TRIAL_ID=true MOCK_RESULTS="PASS" -- || true
assert_eq "oracle échoué: passed" "0" "$(awk -F'\t' 'NR==2{print $17}' "$OUT/results.tsv")"
assert_eq "oracle échoué: code hiérarchique" "E_ORACLE_ASSERTION_FAIL" \
  "$(awk -F'\t' 'NR==2{print $22}' "$OUT/results.tsv")"
assert_eq "oracle échoué: verdict FAIL" "FAIL" "$(awk -F'\t' 'NR==2{print $23}' "$OUT/results.tsv")"

# ---------------------------------------------------------------------------
section "7. Appariement AB / BA"
# ---------------------------------------------------------------------------
OUT="$TMP_ROOT/abba"
run_bench "$OUT" BENCHMARK_MODELS="m m-task" BENCHMARK_RUNS=2 \
  BENCHMARK_PAIRING=abba BENCHMARK_PAIR_COOLDOWN_SEC=0 \
  BENCHMARK_EMIT_TRIAL_ID=false -- || true
assert_eq "AB/BA: alternance des bras" "AB_first,AB_second,BA_first,BA_second" \
  "$(awk -F'\t' 'NR>1 {print $15}' "$OUT/results.tsv" | paste -sd, -)"
assert_eq "AB/BA: premier bras = Chat" "m" "$(awk -F'\t' 'NR==2{print $1}' "$OUT/results.tsv")"
assert_eq "AB/BA: deuxième bras = Task" "m-task" "$(awk -F'\t' 'NR==3{print $1}' "$OUT/results.tsv")"
assert_eq "AB/BA: troisième bras = Task" "m-task" "$(awk -F'\t' 'NR==4{print $1}' "$OUT/results.tsv")"
assert_eq "AB/BA: quatrième bras = Chat" "m" "$(awk -F'\t' 'NR==5{print $1}' "$OUT/results.tsv")"
AB_PAIR_1="$(awk -F'\t' 'NR==2{print $14}' "$OUT/results.tsv")"
AB_PAIR_2="$(awk -F'\t' 'NR==3{print $14}' "$OUT/results.tsv")"
BA_PAIR_1="$(awk -F'\t' 'NR==4{print $14}' "$OUT/results.tsv")"
BA_PAIR_2="$(awk -F'\t' 'NR==5{print $14}' "$OUT/results.tsv")"
assert_eq "AB/BA: pair_id partagé (AB)" "$AB_PAIR_1" "$AB_PAIR_2"
assert_eq "AB/BA: pair_id partagé (BA)" "$BA_PAIR_1" "$BA_PAIR_2"
assert_eq "AB/BA: pair_id distinct par run" "different" \
  "$([ "$AB_PAIR_1" != "$BA_PAIR_1" ] && echo different || echo same)"

# ---------------------------------------------------------------------------
section "8. Enregistrement POST du verdict"
# ---------------------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  STUB="$TMP_ROOT/stub"
  mkdir -p "$STUB"
  cat >"$STUB/stub.py" <<'PY'
import http.server, sys

port_file, body_file = sys.argv[1], sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode("utf-8", "replace")
        with open(body_file, "a", encoding="utf-8") as handle:
            handle.write(body + "\n")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"trial_id": "ok"}')

    def log_message(self, *args):
        pass

server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_port))
server.serve_forever()
PY
  python3 "$STUB/stub.py" "$STUB/port" "$STUB/bodies.jsonl" &
  STUB_PID=$!
  for _ in $(seq 1 100); do
    [ -s "$STUB/port" ] && break
    sleep 0.05
  done
  STUB_PORT="$(cat "$STUB/port")"

  OUT="$TMP_ROOT/post"
  run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
    BENCHMARK_EMIT_TRIAL_ID=true \
    "BENCHMARK_RECORD_TRIALS_URL=http://127.0.0.1:$STUB_PORT/v1/benchmark/trials" -- || true

  kill "$STUB_PID" 2>/dev/null || true
  wait "$STUB_PID" 2>/dev/null || true

  BODY="$(cat "$STUB/bodies.jsonl" 2>/dev/null || true)"
  assert_contains "POST: verdict reçu" "$BODY" '"trial_id": "legacy-v357:legacy:chat:m:1"'
  assert_contains "POST: oracle transmis" "$BODY" '"oracle_passed": true'
  assert_contains "POST: mode transmis" "$BODY" '"oracle_mode": "transcript"'
  assert_contains "POST: modèle transmis" "$BODY" '"requested_model": "m"'
else
  fail "curl est requis pour tester l'enregistrement des verdicts"
fi

# ---------------------------------------------------------------------------
section "9. Tâches embarquées S1/S2/S3 et branche harness_error"
# ---------------------------------------------------------------------------
for spec in "S1_single_unit_fix:s1" "S2_multi_file_wiring:s2" "S3_readonly_impossible:s3"; do
  task="${spec%%:*}"
  fix="${spec#*:}"
  OUT="$TMP_ROOT/task-$task"
  run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
    "BENCHMARK_TASKS_DIR=scripts/tasks_$task" \
    "MOCK_APPLY_FIX=$fix" MOCK_RESULTS="PASS" -- || true
  assert_eq "$task: verdict PASS" "PASS" "$(awk -F'\t' 'NR==2{print $23}' "$OUT/results.tsv")"
  assert_eq "$task: oracle hybride" "hybrid" "$(awk -F'\t' 'NR==2{print $16}' "$OUT/results.tsv")"
  assert_eq "$task: oracle exit 0" "0" "$(awk -F'\t' 'NR==2{print $18}' "$OUT/results.tsv")"
  task_diff="$(awk -F'\t' 'NR==2{print $19}' "$OUT/results.tsv")"
  if [ "${task_diff:-0}" -ge 1 ]; then
    pass "$task: worktree_diff_files=$task_diff"
  else
    fail "$task: worktree_diff_files=0 alors que le worktree a changé"
  fi
done

OUT="$TMP_ROOT/task-s1-fail"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  "BENCHMARK_TASKS_DIR=scripts/tasks_S1_single_unit_fix" MOCK_RESULTS="PASS" -- || true
assert_eq "S1 sans correction: verdict FAIL" "FAIL" "$(awk -F'\t' 'NR==2{print $23}' "$OUT/results.tsv")"
assert_eq "S1 sans correction: code oracle" "E_ORACLE_ASSERTION_FAIL" \
  "$(awk -F'\t' 'NR==2{print $22}' "$OUT/results.tsv")"

OUT="$TMP_ROOT/task-s3-tamper"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  "BENCHMARK_TASKS_DIR=scripts/tasks_S3_readonly_impossible" \
  MOCK_APPLY_FIX=s3_tamper MOCK_RESULTS="PASS" -- || true
assert_eq "S3 altéré: verdict FAIL" "FAIL" "$(awk -F'\t' 'NR==2{print $23}' "$OUT/results.tsv")"

# A crashed harness must still report the oracle it actually ran.
OUT="$TMP_ROOT/task-harness-error"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  "BENCHMARK_TASKS_DIR=scripts/tasks_S1_single_unit_fix" \
  MOCK_APPLY_FIX=s1 MOCK_EXIT=7 MOCK_RESULTS="PASS" -- || true
assert_eq "harness_error: mode oracle conservé" "hybrid" "$(awk -F'\t' 'NR==2{print $16}' "$OUT/results.tsv")"
assert_eq "harness_error: oracle_passed conservé" "1" "$(awk -F'\t' 'NR==2{print $17}' "$OUT/results.tsv")"
assert_eq "harness_error: verdict FAIL" "FAIL" "$(awk -F'\t' 'NR==2{print $23}' "$OUT/results.tsv")"
assert_eq "harness_error: 23 colonnes" "23" "$(awk -F'\t' 'NR==2{print NF}' "$OUT/results.tsv")"

# ---------------------------------------------------------------------------
section "10. Filtre BENCHMARK_TASK_FILTER"
# ---------------------------------------------------------------------------
ALL_TASKS="$REPO/scripts/tasks_all"
mkdir -p "$ALL_TASKS"
for task in S1_single_unit_fix S2_multi_file_wiring S3_readonly_impossible \
            S1_edge_case_guard S2_long_horizon_chain S3_missing_prereq_abstention; do
  cp -R "$REPO/scripts/tasks_$task/$task" "$ALL_TASKS/"
done

OUT="$TMP_ROOT/filter-one"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  BENCHMARK_TASKS_DIR="scripts/tasks_all" \
  BENCHMARK_TASK_FILTER='^s2_long_horizon_chain$' \
  MOCK_APPLY_FIX=s2_chain MOCK_RESULTS="PASS" -- || true
assert_eq "filtre task_id: une seule tâche exécutée" "1" \
  "$(awk 'NR > 1 {n++} END {print n+0}' "$OUT/results.tsv")"
assert_eq "filtre task_id: tâche sélectionnée" "s2_long_horizon_chain" \
  "$(awk -F'\t' 'NR==2{print $11}' "$OUT/results.tsv")"
assert_eq "filtre task_id: verdict PASS" "PASS" \
  "$(awk -F'\t' 'NR==2{print $23}' "$OUT/results.tsv")"

OUT="$TMP_ROOT/filter-folder"
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  BENCHMARK_TASKS_DIR="scripts/tasks_all" \
  BENCHMARK_TASK_FILTER='^S3_missing' \
  MOCK_APPLY_FIX=s3_missing MOCK_RESULTS="PASS" -- || true
assert_eq "filtre dossier: tâche déclarée sélectionnée" "s3_missing_prereq_abstention" \
  "$(awk -F'\t' 'NR==2{print $11}' "$OUT/results.tsv")"

OUT="$TMP_ROOT/filter-none"
rc=0
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  BENCHMARK_TASKS_DIR="scripts/tasks_all" \
  BENCHMARK_TASK_FILTER='^aucune_tache$' -- || rc=$?
assert_eq "filtre sans correspondance: exit 1" "1" "$rc"
assert_contains "filtre sans correspondance: message explicite" \
  "$(cat "$OUT/stderr.log")" "ne correspond à aucune tâche"

OUT="$TMP_ROOT/filter-invalid"
rc=0
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  BENCHMARK_TASKS_DIR="scripts/tasks_all" \
  BENCHMARK_TASK_FILTER='[' -- || rc=$?
assert_eq "filtre invalide: exit 1" "1" "$rc"
assert_contains "filtre invalide: message explicite" \
  "$(cat "$OUT/stderr.log")" "expression régulière valide"

OUT="$TMP_ROOT/filter-no-tasks"
rc=0
run_bench "$OUT" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
  BENCHMARK_TASK_FILTER='s1' -- || rc=$?
assert_eq "filtre sans TASKS_DIR: exit 1" "1" "$rc"
assert_contains "filtre sans TASKS_DIR: message explicite" \
  "$(cat "$OUT/stderr.log")" "exige BENCHMARK_TASKS_DIR"

# ---------------------------------------------------------------------------
section "11. Suite étendue: échec initial, succès, altération rejetée"
# ---------------------------------------------------------------------------
# run_task_cycle <label> <task-dir> <fix> <tamper>
run_task_cycle() {
  local label="$1" task_dir="$2" fix="$3" tamper="$4" out rc

  out="$TMP_ROOT/cycle-$label-initial"
  run_bench "$out" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
    "BENCHMARK_TASKS_DIR=scripts/$task_dir" MOCK_RESULTS="PASS" -- || true
  assert_eq "$label: échec initial" "FAIL" "$(awk -F'\t' 'NR==2{print $23}' "$out/results.tsv")"
  assert_eq "$label: code oracle initial" "E_ORACLE_ASSERTION_FAIL" \
    "$(awk -F'\t' 'NR==2{print $22}' "$out/results.tsv")"

  out="$TMP_ROOT/cycle-$label-fixed"
  run_bench "$out" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
    "BENCHMARK_TASKS_DIR=scripts/$task_dir" \
    "MOCK_APPLY_FIX=$fix" MOCK_RESULTS="PASS" -- || true
  assert_eq "$label: succès après correction" "PASS" \
    "$(awk -F'\t' 'NR==2{print $23}' "$out/results.tsv")"
  assert_eq "$label: oracle exit 0 après correction" "0" \
    "$(awk -F'\t' 'NR==2{print $18}' "$out/results.tsv")"

  out="$TMP_ROOT/cycle-$label-tamper"
  run_bench "$out" BENCHMARK_MODELS="m" BENCHMARK_RUNS=1 \
    "BENCHMARK_TASKS_DIR=scripts/$task_dir" \
    "MOCK_APPLY_FIX=$tamper" MOCK_RESULTS="PASS" -- || true
  assert_eq "$label: altération rejetée" "FAIL" \
    "$(awk -F'\t' 'NR==2{print $23}' "$out/results.tsv")"
}

run_task_cycle "S1_edge" "tasks_S1_edge_case_guard" "s1_edge" "s1_edge_tamper"
run_task_cycle "S2_chain" "tasks_S2_long_horizon_chain" "s2_chain" "s2_chain_tamper"
run_task_cycle "S3_missing_create" "tasks_S3_missing_prereq_abstention" \
  "s3_missing" "s3_missing_create"
run_task_cycle "S3_missing_tamper" "tasks_S3_missing_prereq_abstention" \
  "s3_missing" "s3_missing_tamper"

# The long-horizon task must expose at least ten verbose node files, so the
# masking experiment has a real opportunity to trigger.
NODE_COUNT="$(find "$ROOT_DIR/scripts/benchmark_tasks/S2_long_horizon_chain/fixture" \
  -maxdepth 1 -name 'node*.go' | wc -l | tr -d ' ')"
assert_eq "S2_long_horizon_chain: dix nœuds" "10" "$NODE_COUNT"
SMALL_NODES="$(find "$ROOT_DIR/scripts/benchmark_tasks/S2_long_horizon_chain/fixture" \
  -maxdepth 1 -name 'node*.go' -size -251c | wc -l | tr -d ' ')"
assert_eq "S2_long_horizon_chain: chaque nœud > 250 octets" "0" "$SMALL_NODES"

# ---------------------------------------------------------------------------
section "12. benchmark_report.py (TSV synthétique + SQLite temporaire)"
# ---------------------------------------------------------------------------
TSV="$TMP_ROOT/report.tsv"
{
  printf 'model\trun\ttool_calls\tgreeting\tfake_quota\ts7\tincomplete\tfinish\ttrial_id\tsuite_id\ttask_id\tstratum\tparadigm\tpair_id\tarm_order\toracle_mode\toracle_passed\toracle_exit_code\tworktree_diff_files\ttranscript_clean\twall_time_ms\tprimary_failure_code\tresult\n'
  printf 'm\t1\t3\t0\t0\t0\t0\tstop\tt1\tctrap-keep4\ts2_long_horizon_chain\tS2\tchat\tp1\tAB_first\thybrid\t1\t0\t1\t1\t1000\tnone\tPASS\n'
  printf 'm\t1\t3\t0\t0\t0\t0\tstop\tt2\tctrap-keep4\ts2_long_horizon_chain\tS2\ttask\tp1\tAB_second\thybrid\t0\t1\t2\t1\t1200\tE_ORACLE_ASSERTION_FAIL\tFAIL\n'
  printf 'm\t2\t3\t0\t0\t0\t0\tstop\tt3\tctrap-keep4\ts2_long_horizon_chain\tS2\tchat\tp2\tBA_first\thybrid\t1\t0\t1\t1\t1100\tnone\tPASS\n'
  printf 'm\t2\t3\t0\t0\t0\t0\tstop\tt4\tctrap-keep4\ts2_long_horizon_chain\tS2\ttask\tp2\tBA_second\thybrid\t1\t0\t2\t1\t900\tnone\tPASS\n'
  printf 'm\t3\t3\t0\t0\t0\t0\tstop\tt5\tctrap-keep4\ts2_long_horizon_chain\tS2\tchat\tp3\tBA_first\thybrid\t0\t2\t0\t1\t5000\tE_STALL_OR_TIMEOUT\tFAIL\n'
  printf 'm\t3\t3\t0\t0\t0\t0\tstop\tt6\tctrap-keep4\ts2_long_horizon_chain\tS2\ttask\tp3\tBA_second\thybrid\t0\t1\t2\t1\t1300\tE_ORACLE_ASSERTION_FAIL\tFAIL\n'
} >"$TSV"

DB="$TMP_ROOT/report.db"
python3 - "$DB" <<'PYDB'
import sqlite3
import sys

db = sqlite3.connect(sys.argv[1])
db.executescript(
    """
    CREATE TABLE benchmark_trials (
        trial_id TEXT PRIMARY KEY,
        suite_id TEXT NOT NULL,
        task_id TEXT NOT NULL,
        paradigm TEXT NOT NULL,
        thinking_tier TEXT NOT NULL,
        session_id TEXT,
        oracle_passed INTEGER NOT NULL,
        wall_time_ms INTEGER NOT NULL DEFAULT 0
    );
    CREATE TABLE agent_sessions (
        session_id TEXT PRIMARY KEY,
        thinking_tier TEXT NOT NULL
    );
    CREATE TABLE agent_turns (
        turn_id TEXT PRIMARY KEY,
        session_id TEXT NOT NULL,
        trial_id TEXT NOT NULL,
        masked_obs_bytes_saved INTEGER NOT NULL DEFAULT 0
    );
    CREATE TABLE tool_call_spans (
        session_id TEXT NOT NULL,
        roundtrip_ms INTEGER
    );
    CREATE TABLE turn_anomalies (
        turn_id TEXT NOT NULL,
        category TEXT NOT NULL
    );
    CREATE VIEW v_benchmark_cost_of_pass AS
    SELECT b.suite_id,
           b.task_id,
           'S2' AS stratum,
           b.paradigm,
           b.thinking_tier,
           COUNT(*) AS trials,
           SUM(b.oracle_passed) AS passes,
           CAST(SUM(b.oracle_passed) AS REAL) / COUNT(*) AS pass_rate,
           1000.0 AS avg_wall_time_ms,
           0 AS total_tokens_sum,
           0 AS thought_tokens_sum,
           CASE WHEN SUM(b.oracle_passed) = 0 THEN NULL
                ELSE CAST(1000 * COUNT(*) AS REAL) / SUM(b.oracle_passed) END
                AS cost_of_pass_total_tokens,
           CASE WHEN SUM(b.oracle_passed) = 0 THEN NULL
                ELSE CAST(500 * COUNT(*) AS REAL) / SUM(b.oracle_passed) END
                AS cost_of_pass_output_tokens
    FROM benchmark_trials b
    GROUP BY b.suite_id, b.task_id, b.paradigm, b.thinking_tier;

    INSERT INTO agent_sessions VALUES ('s1', 'high'), ('s2', 'high'), ('s3', 'none');
    INSERT INTO benchmark_trials VALUES
        ('b1', 'ctrap-keep4', 's2_long_horizon_chain', 'chat', 'high', 's1', 1, 1000),
        ('b2', 'ctrap-keep4', 's2_long_horizon_chain', 'task', 'high', 's2', 0, 1300),
        ('b3', 'ctrap-keep4', 's2_multi_file_wiring', 'chat', 'none', 's3', 1, 900);
    INSERT INTO agent_turns VALUES
        ('t1', 's1', 'b1', 512),
        ('t2', 's2', 'b2', 0),
        ('t3', 's3', 'b3', 0);
    INSERT INTO tool_call_spans VALUES ('s1', 100), ('s1', 300), ('s2', 200), ('s3', 400);
    INSERT INTO turn_anomalies VALUES
        ('t1', 'arranger_repair'),
        ('t2', 'quarantine'),
        ('t3', 'doom_loop');
    """
)
db.commit()
db.close()
PYDB

DB_BEFORE="$(sha256sum "$DB" | cut -d' ' -f1)"
OUT="$TMP_ROOT/report-out"
rc=0
python3 "$ROOT_DIR/scripts/benchmark_report.py" \
  --tsv "$TSV" --db "$DB" --suite ctrap-keep4 \
  >"$OUT.txt" 2>"$OUT.err" || rc=$?
assert_eq "rapport: exit 0" "0" "$rc"
assert_contains "rapport: taux brut" "$(cat "$OUT.txt")" "3/6 = 0.500"
assert_contains "rapport: taux hors couche A" "$(cat "$OUT.txt")" "3/5 = 0.600"
assert_contains "rapport: paires complètes" "$(cat "$OUT.txt")" "paires complètes : 3"
assert_contains "rapport: delta apparié" "$(cat "$OUT.txt")" "delta moyen      : -0.333"
assert_contains "rapport: erreur-type par grappe" "$(cat "$OUT.txt")" "SE par grappe    : 0.333"
assert_contains "rapport: octets masqués" "$(cat "$OUT.txt")" "512"
assert_contains "rapport: anomalies par catégorie" "$(cat "$OUT.txt")" "arranger_repair=1"
assert_contains "rapport: alerte masquage nul" "$(cat "$OUT.txt")" \
  "SUM(masked_obs_bytes_saved)=0"
assert_contains "rapport: alerte palier thinking" "$(cat "$OUT.txt")" \
  "thinking_tier hors"
assert_eq "rapport: base inchangée" "$DB_BEFORE" "$(sha256sum "$DB" | cut -d' ' -f1)"

# ---------------------------------------------------------------------------
printf '\n== Résultat ==\n'
printf 'succès: %d, échecs: %d\n' "$PASSED" "$FAILED"
if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
echo "TOUS LES TESTS DU HARNAIS SONT PASSÉS"
