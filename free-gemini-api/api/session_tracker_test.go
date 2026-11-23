package api

import (
	"fmt"
	"testing"

	"goapi/gemini"
)

func cleanupSessionTrackerTest(t *testing.T, sessionKey string) {
	t.Helper()
	t.Cleanup(func() {
		sessionTurnMu.Lock()
		delete(sessionTurnState, sessionKey)
		sessionTurnMu.Unlock()
	})
}

func TestSessionTrackerPersistsAcrossCommit(t *testing.T) {
	sessionKey := "tracker-persist-" + t.Name()
	cleanupSessionTrackerTest(t, sessionKey)

	tracker := getOrCreateSessionTracker(sessionKey)
	if tracker == nil {
		t.Fatal("getOrCreateSessionTracker returned nil")
	}
	for i := 0; i < doomLoopRepeatWarnThreshold; i++ {
		tracker.RecordToolCall("read", `{"path":"a.go"}`, false)
	}

	commitSessionTurn(sessionKey, "flattened prompt", "conv-1", "resp-1", "choice-1", "worker-1")
	afterCommit := getOrCreateSessionTracker(sessionKey)
	if afterCommit != tracker {
		t.Fatal("commitSessionTurn replaced the session's doom-loop tracker")
	}
	action, _ := afterCommit.CheckIntervention()
	if action != ActionWarnRepeat {
		t.Fatalf("tracker state was lost across commit: action = %q, want %q", action, ActionWarnRepeat)
	}
}

func TestRecordSessionToolCalls(t *testing.T) {
	sessionKey := "tracker-record-" + t.Name()
	cleanupSessionTrackerTest(t, sessionKey)

	calls := []gemini.ToolCall{
		{Function: gemini.ToolCallFunction{Name: "read", Arguments: `{"path":"a.go"}`}},
		{Function: gemini.ToolCallFunction{Name: "read", Arguments: `{"path":"b.go"}`}},
		{Function: gemini.ToolCallFunction{Name: "write", Arguments: `{"path":"c.go","content":"done"}`}},
		{Function: gemini.ToolCallFunction{Name: "read", Arguments: `{"path":"d.go"}`}},
	}
	recordSessionToolCalls(sessionKey, calls)

	tracker := getOrCreateSessionTracker(sessionKey)
	action, _ := tracker.CheckIntervention()
	if action != ActionNone {
		t.Fatalf("mutation did not reset the passive-read streak: action = %q", action)
	}

	for i := 0; i < doomLoopPassiveReadThreshold; i++ {
		recordSessionToolCalls(sessionKey, []gemini.ToolCall{{
			Function: gemini.ToolCallFunction{
				Name:      "read",
				Arguments: fmt.Sprintf(`{"path":"after-%d.go"}`, i),
			},
		}})
	}
	action, detail := tracker.CheckIntervention()
	if action != ActionWarnPassiveRead {
		t.Fatalf("action = %q, want %q (detail %q)", action, ActionWarnPassiveRead, detail)
	}
}
