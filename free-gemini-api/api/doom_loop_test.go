package api

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestDoomLoopIdenticalRepeatWarnAndHalt(t *testing.T) {
	tracker := NewDoomLoopTracker()
	args := `{"path":"internal/api/chat_handler.go"}`

	for call := 1; call <= 5; call++ {
		tracker.RecordToolCall("read", args, false)
		action, detail := tracker.CheckIntervention()

		switch {
		case call < doomLoopRepeatWarnThreshold:
			if action != ActionNone {
				t.Fatalf("call %d: action = %q, want %q", call, action, ActionNone)
			}
		case call < doomLoopRepeatHaltThreshold:
			if action != ActionWarnRepeat {
				t.Fatalf("call %d: action = %q, want %q", call, action, ActionWarnRepeat)
			}
			if !strings.HasPrefix(detail, "repeat:") || !strings.Contains(detail, "read") {
				t.Fatalf("call %d: detail = %q, want a repeat detail naming read", call, detail)
			}
		default:
			if action != ActionHalt {
				t.Fatalf("call %d: action = %q, want %q", call, action, ActionHalt)
			}
			if !strings.Contains(detail, "repeated 5 times") {
				t.Fatalf("call %d: detail = %q, want repeat count", call, detail)
			}
		}
	}
}

func TestDoomLoopCanonicalFingerprintIgnoresArgumentOrder(t *testing.T) {
	left := canonicalCallFingerprint("write", `{"path":"a.txt","content":"hello"}`)
	right := canonicalCallFingerprint("write", `{"content":"hello","path":"a.txt"}`)
	if left != right {
		t.Fatalf("argument order changed the fingerprint: %q != %q", left, right)
	}
	if left == canonicalCallFingerprint("write", `{"path":"a.txt","content":"other"}`) {
		t.Fatal("different arguments must produce different fingerprints")
	}
	if left == canonicalCallFingerprint("edit", `{"path":"a.txt","content":"hello"}`) {
		t.Fatal("different tool names must produce different fingerprints")
	}
}

func TestDoomLoopCycleDetectionAB(t *testing.T) {
	tracker := NewDoomLoopTracker()
	record := func(name, args string) {
		tracker.RecordToolCall(name, args, false)
	}

	record("read", `{"path":"a.go"}`)
	record("grep", `{"pattern":"TODO"}`)
	record("read", `{"path":"a.go"}`)
	record("grep", `{"pattern":"TODO"}`)

	action, detail := tracker.CheckIntervention()
	if action != ActionWarnRepeat {
		t.Fatalf("A/B/A/B action = %q, want %q", action, ActionWarnRepeat)
	}
	if !strings.HasPrefix(detail, "cycle:") {
		t.Fatalf("A/B/A/B detail = %q, want cycle prefix", detail)
	}

	// A third full repetition must trip the circuit breaker even though no
	// single call is repeated five times consecutively.
	record("read", `{"path":"a.go"}`)
	record("grep", `{"pattern":"TODO"}`)

	action, detail = tracker.CheckIntervention()
	if action != ActionHalt {
		t.Fatalf("A/B x3 action = %q, want %q (detail %q)", action, ActionHalt, detail)
	}
	if !strings.HasPrefix(detail, "cycle:") {
		t.Fatalf("A/B x3 detail = %q, want cycle prefix", detail)
	}
}

func TestDoomLoopCycleDetectionABC(t *testing.T) {
	tracker := NewDoomLoopTracker()
	for i := 0; i < 2; i++ {
		tracker.RecordToolCall("read", `{"path":"a.go"}`, false)
		tracker.RecordToolCall("grep", `{"pattern":"TODO"}`, false)
		tracker.RecordToolCall("list", `{"path":"."}`, false)
	}

	action, detail := tracker.CheckIntervention()
	if action != ActionWarnRepeat {
		t.Fatalf("A/B/C x2 action = %q, want %q (detail %q)", action, ActionWarnRepeat, detail)
	}
	if !strings.HasPrefix(detail, "cycle:") {
		t.Fatalf("A/B/C x2 detail = %q, want cycle prefix", detail)
	}

	for i := 0; i < 1; i++ {
		tracker.RecordToolCall("read", `{"path":"a.go"}`, false)
		tracker.RecordToolCall("grep", `{"pattern":"TODO"}`, false)
		tracker.RecordToolCall("list", `{"path":"."}`, false)
	}

	action, detail = tracker.CheckIntervention()
	if action != ActionHalt {
		t.Fatalf("A/B/C x3 action = %q, want %q (detail %q)", action, ActionHalt, detail)
	}
}

func TestDoomLoopPassiveReads(t *testing.T) {
	tracker := NewDoomLoopTracker()

	for i := 0; i < doomLoopPassiveReadThreshold; i++ {
		tracker.RecordToolCall("read", fmt.Sprintf(`{"path":"file%d.go"}`, i), false)
		action, _ := tracker.CheckIntervention()
		if action != ActionNone {
			t.Fatalf("read %d: action = %q, want %q", i+1, action, ActionNone)
		}
	}

	tracker.RecordToolCall("grep", `{"path":"file4.go","pattern":"TODO"}`, false)
	action, detail := tracker.CheckIntervention()
	if action != ActionWarnPassiveRead {
		t.Fatalf("action = %q, want %q", action, ActionWarnPassiveRead)
	}
	for _, want := range []string{"passive_read:", "file0.go", "file4.go"} {
		if !strings.Contains(detail, want) {
			t.Errorf("passive-read detail %q does not contain %q", detail, want)
		}
	}
}

func TestDoomLoopMutationResetsPassiveReads(t *testing.T) {
	tracker := NewDoomLoopTracker()

	for i := 0; i < doomLoopPassiveReadThreshold; i++ {
		tracker.RecordToolCall("read", fmt.Sprintf(`{"path":"before%d.go"}`, i), false)
	}
	tracker.RecordToolCall("write", `{"path":"out.go","content":"done"}`, true)

	action, _ := tracker.CheckIntervention()
	if action != ActionNone {
		t.Fatalf("after mutation: action = %q, want %q", action, ActionNone)
	}

	for i := 0; i < doomLoopPassiveReadThreshold; i++ {
		tracker.RecordToolCall("read", fmt.Sprintf(`{"path":"after%d.go"}`, i), false)
	}
	action, _ = tracker.CheckIntervention()
	if action != ActionNone {
		t.Fatalf("four reads after mutation: action = %q, want %q", action, ActionNone)
	}

	tracker.RecordToolCall("read", `{"path":"after4.go"}`, false)
	action, detail := tracker.CheckIntervention()
	if action != ActionWarnPassiveRead {
		t.Fatalf("fifth read after mutation: action = %q, want %q", action, ActionWarnPassiveRead)
	}
	if strings.Contains(detail, "before") {
		t.Fatalf("mutation must clear the previous read set, got %q", detail)
	}
}

func TestDoomLoopNonReadInspectionDoesNotBreakStreak(t *testing.T) {
	tracker := NewDoomLoopTracker()
	for i := 0; i < 3; i++ {
		tracker.RecordToolCall("read", fmt.Sprintf(`{"path":"f%d.go"}`, i), false)
	}
	// A non-mutating, non-read tool is still inspection: it must not reset the
	// consecutive-read counter.
	tracker.RecordToolCall("webfetch", `{"url":"https://example.com"}`, false)
	tracker.RecordToolCall("read", `{"path":"f3.go"}`, false)
	tracker.RecordToolCall("read", `{"path":"f4.go"}`, false)

	action, _ := tracker.CheckIntervention()
	if action != ActionWarnPassiveRead {
		t.Fatalf("action = %q, want %q", action, ActionWarnPassiveRead)
	}
}

func TestDoomLoopReset(t *testing.T) {
	tracker := NewDoomLoopTracker()
	for i := 0; i < 5; i++ {
		tracker.RecordToolCall("read", `{"path":"same.go"}`, false)
	}
	if action, _ := tracker.CheckIntervention(); action != ActionHalt {
		t.Fatalf("action before reset = %q, want %q", action, ActionHalt)
	}

	tracker.Reset()
	if action, _ := tracker.CheckIntervention(); action != ActionNone {
		t.Fatalf("action after reset = %q, want %q", action, ActionNone)
	}

	tracker.RecordToolCall("read", `{"path":"same.go"}`, false)
	tracker.RecordToolCall("read", `{"path":"same.go"}`, false)
	if action, _ := tracker.CheckIntervention(); action != ActionNone {
		t.Fatalf("action after two fresh calls = %q, want %q", action, ActionNone)
	}
}

func TestDoomLoopConcurrentAccess(t *testing.T) {
	tracker := NewDoomLoopTracker()
	const goroutines = 32
	const iterations = 100

	var wg sync.WaitGroup
	for worker := 0; worker < goroutines; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				tracker.RecordToolCall(
					"read",
					fmt.Sprintf(`{"path":"file-%d.go"}`, worker),
					false,
				)
				_, _ = tracker.CheckIntervention()
			}
		}(worker)
	}
	wg.Wait()

	tracker.mu.Lock()
	historyLen := len(tracker.history)
	tracker.mu.Unlock()
	if historyLen > doomLoopHistoryLimit {
		t.Fatalf("history length = %d, want <= %d", historyLen, doomLoopHistoryLimit)
	}
}

func TestFormatDoomLoopDirective(t *testing.T) {
	passive := formatDoomLoopDirective(ActionWarnPassiveRead, "passive_read:a.go, b.go")
	for _, want := range []string{
		"Tu as déjà inspecté",
		"a.go, b.go",
		"Interdiction de relire",
		"write ou edit",
	} {
		if !strings.Contains(passive, want) {
			t.Errorf("passive directive %q does not contain %q", passive, want)
		}
	}

	repeat := formatDoomLoopDirective(ActionWarnRepeat, "repeat:write x3")
	for _, want := range []string{
		"Action répétée",
		"write x3",
		"n'émets plus le même appel",
	} {
		if !strings.Contains(repeat, want) {
			t.Errorf("repeat directive %q does not contain %q", repeat, want)
		}
	}

	if got := formatDoomLoopDirective(ActionNone, ""); got != "" {
		t.Fatalf("nominal directive = %q, want empty", got)
	}
}

func TestDoomLoopSignalLabels(t *testing.T) {
	cases := []struct {
		action string
		detail string
		want   string
	}{
		{ActionWarnRepeat, "repeat:read x3", "repeat"},
		{ActionWarnRepeat, "cycle:read -> grep x2", "cycle"},
		{ActionWarnPassiveRead, "passive_read:a.go", "passive_read"},
		{ActionWarnPassiveRead, "", "passive_read"},
	}
	for _, tc := range cases {
		if got := doomLoopSignal(tc.action, tc.detail); got != tc.want {
			t.Errorf("doomLoopSignal(%q, %q) = %q, want %q", tc.action, tc.detail, got, tc.want)
		}
	}
}

func TestDoomLoopHumanDetail(t *testing.T) {
	cases := map[string]string{
		"repeat:identical tool call 'read' repeated 5 times": "identical tool call 'read' repeated 5 times",
		"cycle:rotating cycle read -> grep repeated 3 times": "rotating cycle read -> grep repeated 3 times",
		"passive_read:a.go, b.go":                            "a.go, b.go",
		"plain detail":                                       "plain detail",
	}
	for input, want := range cases {
		if got := doomLoopHumanDetail(input); got != want {
			t.Errorf("doomLoopHumanDetail(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRequestStartsNewUserTurn(t *testing.T) {
	messages := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("task")},
		{Role: "assistant", Content: contentRawString("working")},
		{Role: "tool", Content: contentRawString("result")},
		{Role: "user", Content: contentRawString("new instruction")},
		{Role: "system", Content: contentRawString("late system notice")},
	}
	if !requestStartsNewUserTurn(messages) {
		t.Fatal("last non-system message is a user message")
	}
	messages[len(messages)-2] = OpenAIChatMessage{Role: "tool", Content: contentRawString("result")}
	if requestStartsNewUserTurn(messages) {
		t.Fatal("last non-system message is a tool result")
	}
}

func TestRequestStartsFreshTask(t *testing.T) {
	fresh := []OpenAIChatMessage{
		{Role: "system", Content: contentRawString("system")},
		{Role: "user", Content: contentRawString("do the task")},
	}
	if !requestStartsFreshTask(fresh) {
		t.Fatal("system+user transcript must be classified as a fresh task")
	}

	continuing := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("do the task")},
		{Role: "assistant", Content: contentRawString("working")},
		{Role: "tool", Content: contentRawString("result")},
	}
	if requestStartsFreshTask(continuing) {
		t.Fatal("assistant/tool transcript must not be classified as a fresh task")
	}
}
