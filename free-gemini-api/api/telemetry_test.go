package api

import (
	"strings"
	"sync"
	"testing"

	"goapi/gemini"
)

// TestStreamPhaseTrackerMilestones pins the TTFT and duration derivations to an
// injected clock, so the test is deterministic and never sleeps.
func TestStreamPhaseTrackerMilestones(t *testing.T) {
	now := int64(1000)
	tracker := newStreamPhaseTracker(func() int64 { return now }, 1000)

	tracker.markAttemptStart() // attempt starts at 1000

	now = 1100
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true})
	now = 1150
	tracker.markClientThought()

	// Re-observing a phase must never move its milestone.
	now = 1200
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true})
	now = 1300
	tracker.markClientThought()

	now = 1450
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true, ThoughtClosed: true})
	now = 1600
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true, ThoughtClosed: true, AnswerStarted: true})

	tel := TurnTelemetry{}
	tracker.apply(&tel)

	if tel.RequestStartedMs != 1000 {
		t.Fatalf("requestStartedMs = %d, want 1000", tel.RequestStartedMs)
	}
	if tel.AttemptStartedMs == nil || *tel.AttemptStartedMs != 1000 {
		t.Fatalf("attemptStartedMs = %v, want 1000", tel.AttemptStartedMs)
	}
	if tel.FirstThoughtMs == nil || *tel.FirstThoughtMs != 1100 {
		t.Fatalf("firstThoughtMs = %v, want 1100", tel.FirstThoughtMs)
	}
	if tel.ThoughtEndedMs == nil || *tel.ThoughtEndedMs != 1450 {
		t.Fatalf("thoughtEndedMs = %v, want 1450 (re-observation must not move it)", tel.ThoughtEndedMs)
	}
	if tel.FirstAnswerMs == nil || *tel.FirstAnswerMs != 1600 {
		t.Fatalf("firstAnswerMs = %v, want 1600", tel.FirstAnswerMs)
	}
	if tel.ClientFirstThoughtMs == nil || *tel.ClientFirstThoughtMs != 1150 {
		t.Fatalf("clientFirstThoughtMs = %v, want the first marked value 1150", tel.ClientFirstThoughtMs)
	}

	if got := tel.TTFTThoughtMs(); got == nil || *got != 100 {
		t.Fatalf("TTFTThoughtMs = %v, want 100", got)
	}
	if got := tel.TTFTAnswerMs(); got == nil || *got != 600 {
		t.Fatalf("TTFTAnswerMs = %v, want 600", got)
	}
	if got := tel.ThoughtDurationMs(); got == nil || *got != 350 {
		t.Fatalf("ThoughtDurationMs = %v, want 350", got)
	}
	if got := tel.AnswerDurationMs(); got != nil {
		t.Fatalf("AnswerDurationMs = %v, want nil before the turn completes", got)
	}
}

// TestStreamPhaseTrackerRetryResetsPhase verifies the retry arbitration: a
// rejected generation must not inflate the retained generation's reasoning
// span, while the client-facing first thought is preserved because the client
// did receive those chunks.
func TestStreamPhaseTrackerRetryResetsPhase(t *testing.T) {
	now := int64(1000)
	tracker := newStreamPhaseTracker(func() int64 { return now }, 1000)

	tracker.markAttemptStart()
	now = 1100
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true})
	now = 1200
	tracker.markClientThought()

	// Attempt 0 is rejected late, after a stall.
	now = 2000
	tracker.markAttemptStart()

	tel := TurnTelemetry{}
	tracker.apply(&tel)
	if tel.FirstThoughtMs != nil || tel.ThoughtEndedMs != nil || tel.FirstAnswerMs != nil {
		t.Fatalf("per-attempt milestones survived the retry: %+v", tel)
	}
	if tel.AttemptStartedMs == nil || *tel.AttemptStartedMs != 2000 {
		t.Fatalf("attemptStartedMs = %v, want 2000", tel.AttemptStartedMs)
	}
	if tel.ClientFirstThoughtMs == nil || *tel.ClientFirstThoughtMs != 1200 {
		t.Fatalf("clientFirstThoughtMs = %v, want 1200 (must survive a retry)", tel.ClientFirstThoughtMs)
	}

	now = 2100
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true, ThoughtClosed: true})
	tracker.apply(&tel)
	if tel.FirstThoughtMs == nil || *tel.FirstThoughtMs != 2100 {
		t.Fatalf("firstThoughtMs = %v, want 2100 from the retained attempt", tel.FirstThoughtMs)
	}
	if got := tel.TTFTThoughtMs(); got == nil || *got != 1100 {
		t.Fatalf("TTFTThoughtMs = %v, want 1100 (from request start, not attempt start)", got)
	}
}

// TestFinalizeTurnTelemetryDisjointTokens covers the accounting defect: a turn
// with no prose but a tool call still produced a completion.
func TestFinalizeTurnTelemetryDisjointTokens(t *testing.T) {
	arranged := &gemini.ArrangedOutput{
		CleanText:        "",
		ReasoningContent: "step by step reasoning about the task",
		ThoughtUnclosed:  true,
		ToolCalls: []gemini.ToolCall{
			{Function: gemini.ToolCallFunction{Name: "read", Arguments: `{"path":"/tmp/file"}`}},
		},
	}

	tel := TurnTelemetry{}
	finalizeTurnTelemetry(&tel, "", arranged)

	if tel.ThoughtTokens <= 0 {
		t.Fatalf("thoughtTokens = %d, want > 0", tel.ThoughtTokens)
	}
	if tel.AnswerTokens <= 0 {
		t.Fatalf("answerTokens = %d, want > 0 for a call-only turn", tel.AnswerTokens)
	}
	if tel.ThoughtSHA256 == "" {
		t.Fatal("thought fingerprint missing while thought tokens exist")
	}
	if !tel.ThoughtUnclosed {
		t.Fatal("thought_unclosed not propagated from the arranged output")
	}
	if tel.TokenEstimator != "heuristic" {
		t.Fatalf("tokenEstimator = %q, want heuristic", tel.TokenEstimator)
	}
}

func TestFinalizeTurnTelemetryFingerprintOnlyWithThoughts(t *testing.T) {
	tel := TurnTelemetry{}
	finalizeTurnTelemetry(&tel, "", &gemini.ArrangedOutput{CleanText: "answer only"})
	if tel.ThoughtTokens != 0 || tel.ThoughtSHA256 != "" {
		t.Fatalf("thoughts=%d sha=%q, want both empty", tel.ThoughtTokens, tel.ThoughtSHA256)
	}
	if tel.AnswerTokens <= 0 {
		t.Fatalf("answerTokens = %d, want > 0", tel.AnswerTokens)
	}
}

// TestFinalizeTurnTelemetryFromRaw covers the blocked-before-arranger branches:
// the raw buffer still yields a disjoint breakdown.
func TestFinalizeTurnTelemetryFromRaw(t *testing.T) {
	tel := TurnTelemetry{}
	finalizeTurnTelemetry(&tel, "<thought>private reasoning</thought>visible answer", nil)

	if tel.ThoughtTokens <= 0 || tel.AnswerTokens <= 0 {
		t.Fatalf("thoughts=%d answer=%d, want both > 0", tel.ThoughtTokens, tel.AnswerTokens)
	}
	if tel.ThoughtSHA256 == "" {
		t.Fatal("thought fingerprint missing")
	}
	if tel.ThoughtUnclosed {
		t.Fatal("a closed thought was flagged unclosed")
	}
}

func TestFinalizeTurnTelemetryUnclosedFromRaw(t *testing.T) {
	tel := TurnTelemetry{}
	finalizeTurnTelemetry(&tel, "<thought>truncated", nil)
	if !tel.ThoughtUnclosed {
		t.Fatal("truncated raw reasoning not flagged")
	}
}

// TestTurnTelemetrySinkUnregister proves the sink can be removed, so a test
// sink can never leak into another test.
func TestTurnTelemetrySinkUnregister(t *testing.T) {
	var mu sync.Mutex
	count := 0
	unregister := RegisterTurnTelemetrySink(func(TurnTelemetry) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	emitTestTurn(t)
	mu.Lock()
	afterFirst := count
	mu.Unlock()
	if afterFirst != 1 {
		t.Fatalf("sink calls = %d, want 1", afterFirst)
	}

	unregister()
	emitTestTurn(t)
	mu.Lock()
	afterUnregister := count
	mu.Unlock()
	if afterUnregister != afterFirst {
		t.Fatalf("sink calls after unregister = %d, want %d", afterUnregister, afterFirst)
	}
}

// TestTurnTelemetryEmitterEmitsOnce is the regression guard for the double
// emission risk: several error branches call the summary before returning, and a
// second record would double-count the Prometheus counters.
func TestTurnTelemetryEmitterEmitsOnce(t *testing.T) {
	var mu sync.Mutex
	records := 0
	unregister := RegisterTurnTelemetrySink(func(TurnTelemetry) {
		mu.Lock()
		records++
		mu.Unlock()
	})
	defer unregister()

	before := counterValue(&answerTokenCounters, "none")

	emitter := &turnTelemetryEmitter{tel: TurnTelemetry{
		SessionKey:       "session-under-test",
		SendModel:        "gemini-4-neon",
		RequestStartedMs: 1,
	}}
	arranged := &gemini.ArrangedOutput{CleanText: "visible answer"}

	emitter.emit("", arranged, "ok", "stop", len(arranged.CleanText), "")
	emitter.emit("", arranged, "ok", "stop", len(arranged.CleanText), "")

	mu.Lock()
	got := records
	mu.Unlock()
	if got != 1 {
		t.Fatalf("emitted %d records, want exactly 1", got)
	}

	after := counterValue(&answerTokenCounters, "none")
	if after <= before {
		t.Fatalf("answer token counter for the emitted tier did not increase (before=%d after=%d)", before, after)
	}
}

func emitTestTurn(t *testing.T) {
	t.Helper()
	emitter := &turnTelemetryEmitter{tel: TurnTelemetry{
		SendModel:        "gemini-4-neon",
		RequestStartedMs: 1,
	}}
	emitter.emit("", &gemini.ArrangedOutput{CleanText: "x"}, "ok", "stop", 1, "")
}

// TestTurnTelemetryEmitterRecordsInputAndWorker covers the fields that were
// declared but never populated: the input token estimate of the payload actually
// sent upstream, and the worker identity that served the turn.
func TestTurnTelemetryEmitterRecordsInputAndWorker(t *testing.T) {
	emitter := &turnTelemetryEmitter{tel: TurnTelemetry{
		SendModel:        "gemini-4-neon",
		RequestStartedMs: 1,
	}}
	prompt := "User: inspect the repository and report the failing test."

	emitter.setSentPrompt(prompt)
	applyWorkerTelemetry(&emitter.tel, &AccountWorker{ID: "worker-7", Tier: "subscription"})

	if emitter.tel.SentPromptBytes != len(prompt) {
		t.Fatalf("sentPromptBytes = %d, want %d", emitter.tel.SentPromptBytes, len(prompt))
	}
	if emitter.tel.InputTokens <= 0 {
		t.Fatalf("inputTokens = %d, want > 0 for a non-empty payload", emitter.tel.InputTokens)
	}
	if emitter.tel.WorkerID != "worker-7" {
		t.Fatalf("workerID = %q, want worker-7", emitter.tel.WorkerID)
	}
	if emitter.tel.AccountTier != "subscription" {
		t.Fatalf("accountTier = %q, want subscription", emitter.tel.AccountTier)
	}

	// An empty prompt is a real state (blocked before an attempt): it must not
	// fabricate tokens.
	emitter.setSentPrompt("")
	if emitter.tel.InputTokens != 0 || emitter.tel.SentPromptBytes != 0 {
		t.Fatalf("empty prompt produced input=%d bytes=%d", emitter.tel.InputTokens, emitter.tel.SentPromptBytes)
	}

	// A nil worker must leave the fields untouched instead of panicking.
	tel := TurnTelemetry{}
	applyWorkerTelemetry(&tel, nil)
	if tel.WorkerID != "" || tel.AccountTier != "" {
		t.Fatalf("nil worker populated identity: %+v", tel)
	}
}

// TestStreamPhaseTrackerDatesLastThoughtClose covers a turn that interleaves
// reasoning and visible text: the thought end must follow the last closed block,
// not freeze on the first one.
func TestStreamPhaseTrackerDatesLastThoughtClose(t *testing.T) {
	now := int64(0)
	tracker := newStreamPhaseTracker(func() int64 { return now }, 0)
	tracker.markAttemptStart()

	now = 100
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true, ThoughtClosed: true, ClosedBlocks: 1})

	// The same count observed again must not move the milestone.
	now = 200
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true, ThoughtClosed: true, ClosedBlocks: 1})

	now = 300
	tracker.observeProgress(gemini.StreamThoughtState{ThoughtSeen: true, ThoughtClosed: true, ClosedBlocks: 2})

	tel := TurnTelemetry{}
	tracker.apply(&tel)
	if tel.ThoughtEndedMs == nil || *tel.ThoughtEndedMs != 300 {
		t.Fatalf("thoughtEndedMs = %v, want 300 from the second close", tel.ThoughtEndedMs)
	}
	if tel.FirstThoughtMs == nil || *tel.FirstThoughtMs != 100 {
		t.Fatalf("firstThoughtMs = %v, want 100 (unchanged by later blocks)", tel.FirstThoughtMs)
	}
}

// TestStreamingTurnTelemetryScenario replays a realistic chunk sequence through
// the same two producers the streaming handler drives — InspectStreamProgress and
// the phase tracker — and checks the resulting record end to end.
func TestStreamingTurnTelemetryScenario(t *testing.T) {
	now := int64(1000)
	tracker := newStreamPhaseTracker(func() int64 { return now }, 1000)
	tracker.markAttemptStart()

	// The upstream splits the opening tag across chunks, which is the case a
	// naive content check would misread as an answer.
	chunks := []string{
		"<thou",
		"ght>",
		"Let me inspect the repository.",
		"</thought>",
		"Voici le résultat.",
	}

	now = 1050
	buffer := ""
	for _, chunk := range chunks {
		buffer += chunk
		now += 50
		tracker.observeProgress(gemini.InspectStreamProgress(buffer))
	}

	tel := TurnTelemetry{
		RequestStartedMs: 1000,
		SendModel:        "gemini-4-neon",
		ReplayReason:     "delta",
		IsDelta:          true,
	}
	tracker.apply(&tel)
	finalizeTurnTelemetry(&tel, buffer, gemini.ArrangeGeminiOutputWithTools(buffer, nil))

	if tel.FirstThoughtMs == nil || *tel.FirstThoughtMs != 1150 {
		t.Fatalf("firstThoughtMs = %v, want 1150 (after the full opening tag)", tel.FirstThoughtMs)
	}
	if tel.ThoughtEndedMs == nil || *tel.ThoughtEndedMs != 1250 {
		t.Fatalf("thoughtEndedMs = %v, want 1250", tel.ThoughtEndedMs)
	}
	if tel.FirstAnswerMs == nil || *tel.FirstAnswerMs != 1300 {
		t.Fatalf("firstAnswerMs = %v, want 1300", tel.FirstAnswerMs)
	}
	if got := tel.TTFTThoughtMs(); got == nil || *got != 150 {
		t.Fatalf("TTFTThoughtMs = %v, want 150", got)
	}
	if got := tel.TTFTAnswerMs(); got == nil || *got != 300 {
		t.Fatalf("TTFTAnswerMs = %v, want 300", got)
	}
	if got := tel.ThoughtDurationMs(); got == nil || *got != 100 {
		t.Fatalf("ThoughtDurationMs = %v, want 100", got)
	}
	if tel.ThoughtTokens <= 0 || tel.AnswerTokens <= 0 {
		t.Fatalf("thoughts=%d answer=%d, want both > 0", tel.ThoughtTokens, tel.AnswerTokens)
	}
	if tel.ThoughtUnclosed {
		t.Fatal("a properly closed thought was flagged unclosed")
	}
	if tel.ThoughtSHA256 == "" {
		t.Fatal("reasoning fingerprint missing")
	}
}

// TestRecordOutputTokenBreakdownKeepsCountersDisjoint verifies the two families
// are fed independently and rendered on /metrics.
func TestRecordOutputTokenBreakdownKeepsCountersDisjoint(t *testing.T) {
	const tier = "test-breakdown"
	beforeThought := counterValue(&thoughtTokenCounters, tier)
	beforeAnswer := counterValue(&answerTokenCounters, tier)

	RecordOutputTokenBreakdown(tier, 7, 11)

	if got := counterValue(&thoughtTokenCounters, tier) - beforeThought; got != 7 {
		t.Fatalf("thought delta = %d, want 7", got)
	}
	if got := counterValue(&answerTokenCounters, tier) - beforeAnswer; got != 11 {
		t.Fatalf("answer delta = %d, want 11", got)
	}

	// Zero values must not create a series that never had tokens.
	RecordOutputTokenBreakdown("test-breakdown-zero", 0, 0)
	if got := counterValue(&thoughtTokenCounters, "test-breakdown-zero"); got != 0 {
		t.Fatalf("zero thought breakdown created a value: %d", got)
	}

	rendered := string(renderMetricsWithStats(WorkerPoolStats{}))
	for _, family := range []string{"gemini_thought_tokens_total", "gemini_answer_tokens_total"} {
		if !strings.Contains(rendered, family) {
			t.Fatalf("%s is missing from the /metrics exposition", family)
		}
	}
	for _, tierLabel := range []string{`tier="none"`, `tier="low"`, `tier="medium"`, `tier="high"`} {
		if !strings.Contains(rendered, tierLabel) {
			t.Fatalf("known label %s is missing from the thought/answer exposition", tierLabel)
		}
	}
}
