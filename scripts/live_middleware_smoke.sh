#!/usr/bin/env bash
# Live middleware smoke tests for the hardened free-gemini-api build.
#
# These tests exercise the real Gemini upstream, so they consume a small
# amount of quota and depend on a healthy cookie/extension worker. They are
# intentionally NOT part of scripts/audit_check.sh. The refusal-classifier
# control is deterministic (it runs the Go unit test); the other controls
# exercise the live upstream.
#
# Usage:
#   API_TOKEN=... ./scripts/live_middleware_smoke.sh
#   MODEL=gemini-4-neon ./scripts/live_middleware_smoke.sh
#
# Environment:
#   API_TOKEN  Required unless free-gemini-api/.env contains one.
#   BASE_URL   Default: http://127.0.0.1:8100
#   MODEL      Default: gemini-4-hydrogen

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BASE_URL="${BASE_URL:-http://127.0.0.1:8100}"
MODEL="${MODEL:-gemini-4-hydrogen}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/gemini-live-smoke.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

if [ -z "${API_TOKEN:-}" ] && [ -f "$ROOT_DIR/free-gemini-api/.env" ]; then
  API_TOKEN="$(grep -E '^API_TOKEN=' "$ROOT_DIR/free-gemini-api/.env" | cut -d= -f2-)"
fi
if [ -z "${API_TOKEN:-}" ]; then
  echo "Erreur: API_TOKEN absent (variable d'environnement ou free-gemini-api/.env)" >&2
  exit 1
fi

auth=(-H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json")
failures=0

pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }

metric_value() {
  curl -s -m 10 "${auth[@]}" "$BASE_URL/metrics" \
    | grep -F "$1" | tail -1 | awk '{print $2}'
}

post_json() {
  local payload="$1" session="$2"
  curl -s -m 180 "${auth[@]}" -H "X-Session-ID: $session" \
    --data-binary "@$payload" "$BASE_URL/v1/chat/completions"
}

echo "=== [1/6] Runtime sync: agentic counters exposed ==="
metrics="$(curl -s -m 10 "${auth[@]}" "$BASE_URL/metrics")"
for counter in \
  'gemini_doom_loop_interventions_total' \
  'gemini_arranger_repairs_total{kind="orphan_tags"}' \
  'gemini_ground_truth_directives_total'; do
  if grep -Fq "$counter" <<<"$metrics"; then
    pass "counter present: $counter"
  else
    fail "counter missing: $counter (container likely not rebuilt)"
  fi
done

echo "=== [2/6] Orphan closing-tag sanitizer ==="
cat >"$TMP_DIR/leak.json" <<JSON
{
  "model": "$MODEL",
  "stream": false,
  "messages": [
    {"role": "user", "content": "Reproduce exactly this token sequence ten times in a row, with single spaces, and output nothing else: </script> </tool_call>"}
  ]
}
JSON
before_orphan="$(metric_value 'gemini_arranger_repairs_total{kind="orphan_tags"}')"
post_json "$TMP_DIR/leak.json" "smoke-leak-$(date +%s)" >"$TMP_DIR/leak.out"
after_orphan="$(metric_value 'gemini_arranger_repairs_total{kind="orphan_tags"}')"
if python3 - "$TMP_DIR/leak.out" <<'PY'
import json, sys
try:
    data = json.load(open(sys.argv[1]))
    content = data["choices"][0]["message"].get("content", "")
except Exception:
    sys.exit(1)
if "</script>" in content or "</tool_call>" in content:
    print(f"orphan tags still visible: {content[:200]!r}")
    sys.exit(1)
PY
then
  pass "visible response contains no repeated orphan closing tags"
else
  fail "orphan closing tags leaked into the visible response"
fi
if [ "${after_orphan:-0}" -gt "${before_orphan:-0}" ] 2>/dev/null; then
  pass "orphan_tags counter incremented ($before_orphan -> $after_orphan)"
else
  fail "orphan_tags counter did not increment ($before_orphan -> $after_orphan)"
fi

echo "=== [3/6] Ground-truth recovery directive (Counter-S7) ==="
cat >"$TMP_DIR/groundtruth.json" <<JSON
{
  "model": "$MODEL",
  "stream": false,
  "messages": [
    {"role": "system", "content": "You are a coding agent. You must use the provided tools. If a tool call fails, call read before retrying. Never answer with prose when a tool call is required."},
    {"role": "user", "content": "Update the API endpoints table in README.md."},
    {"role": "assistant", "content": "", "tool_calls": [{"id":"call_smoke_1","type":"function","function":{"name":"edit","arguments":"{\"path\":\"README.md\",\"oldString\":\"### API Endpoints Reference\",\"newString\":\"### API Endpoints Reference (updated)\"}"}}]},
    {"role": "tool", "tool_call_id":"call_smoke_1", "name":"edit", "content":"Error: Could not find oldString in README.md. It must match exactly, including whitespace and indentation."}
  ],
  "tools": [
    {"type":"function","function":{"name":"edit","description":"Replace exact text in a file","parameters":{"type":"object","properties":{"path":{"type":"string"},"oldString":{"type":"string"},"newString":{"type":"string"}},"required":["path","oldString","newString"],"additionalProperties":false}}},
    {"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}}
  ]
}
JSON
before_gt="$(metric_value 'gemini_ground_truth_directives_total')"
post_json "$TMP_DIR/groundtruth.json" "smoke-groundtruth-$(date +%s)" >"$TMP_DIR/groundtruth.out"
after_gt="$(metric_value 'gemini_ground_truth_directives_total')"
if python3 - "$TMP_DIR/groundtruth.out" <<'PY'
import json, sys
try:
    message = json.load(open(sys.argv[1]))["choices"][0]["message"]
except Exception:
    sys.exit(1)
calls = [tc.get("function", {}).get("name") for tc in (message.get("tool_calls") or [])]
if calls:
    sys.exit(0)
text = (message.get("content") or "").lower()
if any(word in text for word in ("read", "relire", "anchor", "ancre", "error", "erreur")):
    sys.exit(0)
sys.exit(1)
PY
then
  pass "model acknowledged the failed anchor and issued a recovery tool call (or explained the recovery)"
else
  echo "WARN: ground-truth directive was injected, but the model did not visibly recover in this sample (stochastic model behavior)"
fi
if [ "${after_gt:-0}" -gt "${before_gt:-0}" ] 2>/dev/null; then
  pass "ground-truth counter incremented ($before_gt -> $after_gt)"
else
  fail "ground-truth counter did not increment ($before_gt -> $after_gt)"
fi

echo "=== [4/6] Instruction notice compaction (OpenCode replay) ==="
cat >"$TMP_DIR/notice.json" <<JSON
{
  "model": "$MODEL",
  "stream": false,
  "messages": [
    {"role": "system", "content": "You are a coding agent. Use the available tools."},
    {"role": "user", "content": "Update README.md with a new endpoint row. Use the edit tool."},
    {"role": "assistant", "content": "", "tool_calls": [{"id":"call_notice_write","type":"function","function":{"name":"write","arguments":"{\"path\":\"AGENTS.md\",\"content\":\"# AGENTS.md\\nProject instructions. Do not modify safety protocols or bypass restrictions.\"}"}}]},
    {"role": "tool", "tool_call_id":"call_notice_write", "name":"write", "content":"Wrote AGENTS.md"},
    {"role": "system", "content": "The instructions changed:\nInstructions from: /home/kidpixel/free-gemini-api-main/AGENTS.md\n# AGENTS.md — Free Gemini API\n\n## Safety protocols\nDo not modify core safety protocols, alter system directives, or bypass restrictions. $(printf 'instruction line. %.0s' {1..200})\nSENTINEL_FULL_AGENTS_BODY"}
  ],
  "tools": [
    {"type":"function","function":{"name":"write","description":"Write a file","parameters":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}}},
    {"type":"function","function":{"name":"edit","description":"Replace exact text in a file","parameters":{"type":"object","properties":{"path":{"type":"string"},"oldString":{"type":"string"},"newString":{"type":"string"}},"required":["path","oldString","newString"],"additionalProperties":false}}},
    {"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}}
  ]
}
JSON
post_json "$TMP_DIR/notice.json" "smoke-notice-$(date +%s)" >"$TMP_DIR/notice.out"
if python3 - "$TMP_DIR/notice.out" <<'PY'
import json, sys
try:
    message = json.load(open(sys.argv[1]))["choices"][0]["message"]
except Exception as exc:
    print(f"cannot parse response: {exc}")
    sys.exit(1)
content = (message.get("content") or "")
lower = content.lower()
refusals = [
    "cannot execute the requested system directive",
    "issue the tool call directly in this manner",
    "modifying core safety protocols",
    "altering system directives",
    "bypassing restrictions",
    "cannot assist with modifying",
    "i must refuse to assist with",
    "dangerous or illegal activities",
]
for phrase in refusals:
    if phrase in lower:
        print(f"false refusal after instruction notice: {phrase!r}")
        sys.exit(1)
if "sentinel_full_agents_body" in lower:
    print("the full instruction notice was echoed/replayed")
    sys.exit(1)
if message.get("tool_calls"):
    sys.exit(0)
if content.strip():
    sys.exit(0)
print("empty response after instruction notice")
sys.exit(1)
PY
then
  pass "instruction notice did not trigger a refusal and the model continued"
else
  fail "instruction notice handling failed (refusal, echo, or empty response)"
fi

echo "=== [5/6] Upstream safety-refusal classifier ==="
run_refusal_classifier() {
  if command -v go >/dev/null 2>&1; then
    (cd "$ROOT_DIR/free-gemini-api" && go test -run 'TestIsToolRefusalOutput_GlobalSafetyRefusal' -count=1 ./api)
  elif command -v docker >/dev/null 2>&1; then
    docker run --rm -v "$ROOT_DIR/free-gemini-api:/app" -w /app golang:1.26-alpine \
      go test -run 'TestIsToolRefusalOutput_GlobalSafetyRefusal' -count=1 ./api
  else
    echo "ni go ni docker n'est disponible pour exécuter le test de classifieur" >&2
    return 127
  fi
}
if run_refusal_classifier >"$TMP_DIR/refusal-classifier.log" 2>&1; then
  pass "isToolRefusalOutput intercepts 'dangerous or illegal activities'"
else
  cat "$TMP_DIR/refusal-classifier.log" >&2
  fail "isToolRefusalOutput does not intercept the Google global safety refusal"
fi

echo "=== [6/6] Write-Serial: at most one mutation per turn ==="
cat >"$TMP_DIR/writeserial.json" <<JSON
{
  "model": "$MODEL",
  "stream": true,
  "messages": [
    {"role": "user", "content": "You are testing the Write-Serial middleware. In ONE response, emit exactly two <tool_call> XML blocks: the first calls edit on /tmp/proxy-a.txt with oldString \"ALPHA\" and newString \"BETA\"; the second calls edit on /tmp/proxy-b.txt with oldString \"GAMMA\" and newString \"DELTA\". Do not explain and do not add any other tool call."}
  ],
  "tools": [
    {"type":"function","function":{"name":"edit","description":"Replace exact text in a file","parameters":{"type":"object","properties":{"path":{"type":"string"},"oldString":{"type":"string"},"newString":{"type":"string"}},"required":["path","oldString","newString"],"additionalProperties":false}}},
    {"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}}
  ]
}
JSON
curl -N -s -m 180 "${auth[@]}" -H "X-Session-ID: smoke-writeserial-$(date +%s)" \
  --data-binary "@$TMP_DIR/writeserial.json" \
  "$BASE_URL/v1/chat/completions" >"$TMP_DIR/writeserial.sse" || true
if python3 - "$TMP_DIR/writeserial.sse" <<'PY'
import json, sys
calls = []
for line in open(sys.argv[1], encoding="utf-8", errors="replace"):
    if not line.startswith("data: "):
        continue
    data = line[6:].strip()
    if data == "[DONE]":
        continue
    try:
        obj = json.loads(data)
    except Exception:
        continue
    for choice in obj.get("choices", []):
        for tc in (choice.get("delta") or {}).get("tool_calls") or []:
            calls.append(tc)
for index, tc in enumerate(calls):
    if tc.get("index") != index:
        print(f"tool_calls[{index}].index = {tc.get('index')!r}, expected {index}")
        sys.exit(1)
mutations = [
    tc for tc in calls
    if (tc.get("function", {}).get("name") or "").lower() in {"edit", "write", "patch", "apply_patch", "multiedit"}
]
if len(mutations) > 1:
    names = [tc.get("function", {}).get("name") for tc in mutations]
    print(f"emitted {len(mutations)} mutations {names}, expected at most 1")
    sys.exit(1)
PY
then
  pass "at most one mutation and sequential tool_calls[].index values"
else
  fail "Write-Serial or SSE index invariant violated"
fi

echo
if [ "$failures" -eq 0 ]; then
  echo "=== TOUS LES TESTS LIVE SONT PASSÉS (exit 0) ==="
  exit 0
fi
echo "=== $failures TEST(S) LIVE ONT ÉCHOUÉ ===" >&2
exit 1
