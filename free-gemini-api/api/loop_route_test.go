package api

import (
	"strings"
	"testing"
)

// TestLoopRouteFailedRepeatWarnsEarlier covers the LoopRoute rule: a repeated
// call whose previous occurrence failed reaches the warning one occurrence
// sooner than a plain repeat, while a successful repeat keeps the historical
// threshold.
func TestLoopRouteFailedRepeatWarnsEarlier(t *testing.T) {
	args := `{"path":"a.go"}`

	healthy := NewDoomLoopTracker()
	healthy.RecordToolCallWithID("call_1", "read", args, false)
	healthy.RecordToolCallWithID("call_2", "read", args, false)
	if action, _ := healthy.CheckIntervention(); action != ActionNone {
		t.Fatalf("healthy repeat at 2: action = %q, want %q", action, ActionNone)
	}

	failing := NewDoomLoopTracker()
	failing.RecordToolCallWithID("call_1", "read", args, false)
	failing.RecordToolCallWithID("call_2", "read", args, false)
	failing.RecordToolOutcome("call_1", true)
	failing.RecordToolOutcome("call_2", true)
	action, detail := failing.CheckIntervention()
	if action != ActionWarnRepeat {
		t.Fatalf("failed repeat at 2: action = %q, want %q", action, ActionWarnRepeat)
	}
	if !strings.HasPrefix(detail, "repeat:") || !strings.Contains(detail, "failed") {
		t.Fatalf("failed repeat detail = %q, want a repeat: prefix mentioning the failure", detail)
	}
}

// TestLoopRouteHaltsOnConsecutiveFailures covers the halt threshold: four
// identical failures stop the turn, three do not.
func TestLoopRouteHaltsOnConsecutiveFailures(t *testing.T) {
	tracker := NewDoomLoopTracker()
	args := `{"path":"a.go"}`
	for call := 0; call < 3; call++ {
		id := "call_" + string(rune('a'+call))
		tracker.RecordToolCallWithID(id, "read", args, false)
		tracker.RecordToolOutcome(id, true)
		if action, _ := tracker.CheckIntervention(); action == ActionHalt {
			t.Fatalf("call %d: halted before the failed-repeat threshold", call+1)
		}
	}
	tracker.RecordToolCallWithID("call_d", "read", args, false)
	tracker.RecordToolOutcome("call_d", true)
	action, detail := tracker.CheckIntervention()
	if action != ActionHalt {
		t.Fatalf("4 consecutive failures: action = %q, want %q", action, ActionHalt)
	}
	if !strings.Contains(detail, "failed 4 consecutive times") {
		t.Fatalf("detail = %q, want the consecutive-failure count", detail)
	}
}

// TestLoopRouteIgnoresUnknownCallID proves an outcome for a call that is not in
// the bounded history cannot fabricate a failure streak.
func TestLoopRouteIgnoresUnknownCallID(t *testing.T) {
	tracker := NewDoomLoopTracker()
	args := `{"path":"a.go"}`
	for call := 0; call < 3; call++ {
		tracker.RecordToolCall("read", args, false)
	}
	tracker.RecordToolOutcome("unknown_call", true)
	if action, _ := tracker.CheckIntervention(); action == ActionWarnRepeat {
		// Three plain repeats legitimately warn; the point is that the streak
		// stays at 0 so no halt is possible.
		if _, detail := tracker.CheckIntervention(); strings.Contains(detail, "failed") {
			t.Fatalf("unknown outcome fabricated a failure: %q", detail)
		}
	}
}

// TestLoopRouteResetClearsOutcomes proves a new user turn clears the failure
// evidence along with the rest of the tracker state.
func TestLoopRouteResetClearsOutcomes(t *testing.T) {
	tracker := NewDoomLoopTracker()
	args := `{"path":"a.go"}`
	for call := 0; call < 4; call++ {
		id := "call_" + string(rune('a'+call))
		tracker.RecordToolCallWithID(id, "read", args, false)
		tracker.RecordToolOutcome(id, true)
	}
	if action, _ := tracker.CheckIntervention(); action != ActionHalt {
		t.Fatalf("setup: action = %q, want halt", action)
	}

	tracker.Reset()
	tracker.RecordToolCallWithID("call_x", "read", args, false)
	if action, _ := tracker.CheckIntervention(); action != ActionNone {
		t.Fatalf("after Reset: action = %q, want %q", action, ActionNone)
	}
}

func TestCollectToolOutcomesBounded(t *testing.T) {
	var messages []OpenAIChatMessage
	for i := 0; i < maxToolOutcomesPerTurn+10; i++ {
		messages = append(messages, OpenAIChatMessage{
			Role:       "tool",
			ToolCallID: "call_" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Content:    contentRawString("error: file not found"),
		})
	}
	outcomes := collectToolOutcomes(messages, 5000)
	if len(outcomes) != maxToolOutcomesPerTurn {
		t.Fatalf("outcomes = %d, want the %d bound", len(outcomes), maxToolOutcomesPerTurn)
	}
	for _, outcome := range outcomes {
		if !outcome.IsError {
			t.Fatalf("outcome %q was not classified as an error", outcome.ToolCallID)
		}
		if outcome.ReceivedAtMs != 5000 {
			t.Fatalf("received_at_ms = %d, want 5000", outcome.ReceivedAtMs)
		}
	}
}

func TestDiagnosticTargetHintSanitizes(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"absolute path", `{"path":"/home/alice/secret/project/internal/api/handler.go"}`, "api/handler.go"},
		{"windows path", `{"file_path":"C:\\Users\\alice\\repo\\main.go"}`, "repo/main.go"},
		{"single segment", `{"path":"main.go"}`, "main.go"},
		{"no path key", `{"pattern":"secret-token"}`, ""},
		{"invalid json", `{`, ""},
		{"empty", `{}`, ""},
	}
	for _, tc := range cases {
		if got := diagnosticTargetHint(tc.args); got != tc.want {
			t.Errorf("%s: hint = %q, want %q", tc.name, got, tc.want)
		}
	}
}
