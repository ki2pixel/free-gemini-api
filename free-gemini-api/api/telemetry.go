package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"goapi/db"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"goapi/gemini"
)

// TurnTelemetry is the content-free record of one completed turn.
//
// It carries metrics, lengths, timestamps and a reasoning fingerprint only:
// never a prompt body, a reasoning body or tool arguments. Every field is
// either numeric, a bounded label, or a non-reversible hash, so the record can
// be persisted and aggregated without a redaction pass.
type TurnTelemetry struct {
	// Identity and routing.
	SessionKey       string
	SessionID        string
	RequestedModel   string
	EffectiveModel   string
	SendModel        string
	ProductMode      int
	RoutedTaskToChat bool
	ThinkingTier     string
	WorkerID         string
	AccountTier      string
	UpstreamConvID   string
	ToolsDeclared    int

	// Context shape.
	IsDelta          bool
	ReplayReason     string
	SystemScopeHash  string
	BenchmarkTrialID string
	FlatPromptBytes  int
	SentPromptBytes  int
	DeltaBytes       int
	MaskedBytesSaved int

	// Milestones in epoch milliseconds. A nil milestone was never observed
	// (for example every intra-stream milestone of a non-streaming turn), which
	// becomes SQL NULL at the persistence step.
	RequestStartedMs     int64
	AttemptStartedMs     *int64
	FirstThoughtMs       *int64
	ThoughtEndedMs       *int64
	FirstAnswerMs        *int64
	ClientFirstThoughtMs *int64
	CompletedMs          int64

	// Disjoint token accounting. InputTokens counts the payload actually sent
	// upstream; ThoughtTokens and AnswerTokens never overlap, and AnswerTokens
	// includes the tool calls so a call-only turn is not reported as zero.
	InputTokens    int
	ThoughtTokens  int
	AnswerTokens   int
	TokenEstimator string

	// Reliability and classification.
	AttemptCount          int
	EmptyGenRetries       int
	RefusalRetries        int
	TransportRetries      int
	StreamStalled         bool
	UpstreamSuspectedStop bool
	GroundTruthInjected   bool
	TurnClass             string
	FinishReason          string
	DoomLoopAction        string
	DoomLoopSignal        string
	ThoughtUnclosed       bool
	ThoughtSHA256         string

	// VisibleLen is the assistant-visible byte count the turn summary logs.
	VisibleLen int

	// Granular evidence collected from every attempt of this turn.
	ToolSpans             []db.ToolCallSpanRecord
	ToolOutcomes          []db.ToolCallOutcomeRecord
	Anomalies             []db.TurnAnomalyRecord
	MaskedErrorsPreserved int
}

// benchmarkTrialIDRe bounds the trial identifier to a safe, non-personal
// alphabet. The shell bench builds "<suite>:<task>:<paradigm>:<model>:<run>".
var benchmarkTrialIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// sanitizeBenchmarkTrialID accepts a well-formed trial id and returns "" for
// anything else. A malformed header never rejects a legitimate chat request: it
// only loses the benchmark correlation.
func sanitizeBenchmarkTrialID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 128 || !benchmarkTrialIDRe.MatchString(raw) {
		return ""
	}
	return raw
}

// maxToolOutcomesPerTurn bounds the role:"tool" results attached to one turn.
// The transcript is replayed every request, but only the most recent batch can
// still have open spans; 32 covers any realistic parallel-call batch.
const maxToolOutcomesPerTurn = 32

// collectToolOutcomes reads the host-visible results of previously emitted
// calls. Content is measured, never stored: only the call id, the error flag,
// the byte count and the observation time reach the telemetry record.
func collectToolOutcomes(messages []OpenAIChatMessage, receivedAtMs int64) []db.ToolCallOutcomeRecord {
	var outcomes []db.ToolCallOutcomeRecord
	for index := len(messages) - 1; index >= 0 && len(outcomes) < maxToolOutcomesPerTurn; index-- {
		message := messages[index]
		if !strings.EqualFold(message.Role, "tool") || strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		content := FlattenContent(message.Content)
		outcomes = append(outcomes, db.ToolCallOutcomeRecord{
			ToolCallID:   message.ToolCallID,
			IsError:      toolResultIndicatesError(content),
			ResultBytes:  len(content),
			ReceivedAtMs: receivedAtMs,
		})
	}
	return outcomes
}

// addTurnAnomaly records one categorical incident on the turn. Identical
// (attempt, category, code) triples are merged: cardinality lives in
// tool_call_spans, the journal only needs to know the incident happened.
func addTurnAnomaly(t *TurnTelemetry, attempt int, category, code, severity string) {
	if t == nil {
		return
	}
	if attempt < 1 {
		attempt = 1
	}
	appendTurnAnomaly(t, db.TurnAnomalyRecord{
		AttemptIndex: attempt,
		Category:     category,
		Code:         code,
		Severity:     severity,
		Detail:       fmt.Sprintf("attempt=%d", attempt),
	})
}

func appendTurnAnomaly(t *TurnTelemetry, anomaly db.TurnAnomalyRecord) {
	for _, existing := range t.Anomalies {
		if existing.AttemptIndex == anomaly.AttemptIndex &&
			existing.Category == anomaly.Category &&
			existing.Code == anomaly.Code {
			return
		}
	}
	t.Anomalies = append(t.Anomalies, anomaly)
}

// transferPreTelemetry moves rejected spans and deduplicated anomalies captured
// before the emitter existed onto the turn record. Re-indexing CallIndex keeps
// (turn_id, call_index) unique when the final attempt later appends its own spans.
func transferPreTelemetry(dst, src *TurnTelemetry) {
	if dst == nil || src == nil {
		return
	}
	for _, span := range src.ToolSpans {
		span.CallIndex = len(dst.ToolSpans)
		dst.ToolSpans = append(dst.ToolSpans, span)
	}
	for _, anomaly := range src.Anomalies {
		appendTurnAnomaly(dst, anomaly)
	}
}

// recordRejectedAttempt captures the evidence of an attempt the retry loop
// discards. Without it, a retry that eventually succeeds would erase every
// trace of the failure it absorbed: no span for the rejected candidate and no
// anomaly for the reason the turn was replayed.
func recordRejectedAttempt(t *TurnTelemetry, attempt int, raw string, tools []map[string]any, emittedAtMs int64) {
	if t == nil {
		return
	}
	recordArrangerEvidence(t, attempt, gemini.ArrangeGeminiOutputWithTools(raw, tools), nil, emittedAtMs)
}

// diagnosticTargetHint keeps at most the last two path segments. Patterns and
// search terms are deliberately ignored: they can carry user content.
func diagnosticTargetHint(argsJSON string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	target := ""
	for _, key := range []string{
		"path", "file_path", "filePath", "filepath", "target_file", "directory", "dir",
	} {
		if value, ok := args[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				target = value
				break
			}
		}
	}
	if target == "" {
		return ""
	}
	target = strings.ReplaceAll(target, "\\", "/")
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(target, "/") {
		if segment == "" || segment == "." || segment == ".." {
			continue
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return ""
	}
	if len(segments) > 2 {
		segments = segments[len(segments)-2:]
	}
	hint := strings.Join(segments, "/")
	if runes := []rune(hint); len(runes) > 120 {
		hint = string(runes[len(runes)-120:])
	}
	return hint
}

// recordArrangerEvidence turns the arranger diagnostics into spans and
// anomalies. It never stores arguments: only the digest, the hint and the
// structural validation error reach the record.
func recordArrangerEvidence(t *TurnTelemetry, attempt int, arranged *gemini.ArrangedOutput, dropped []gemini.ToolCall, emittedAtMs int64) {
	if t == nil || arranged == nil {
		return
	}
	droppedIDs := make(map[string]struct{}, len(dropped))
	for _, call := range dropped {
		droppedIDs[call.ID] = struct{}{}
	}

	for _, diagnostic := range arranged.ToolDiagnostics {
		disposition := diagnostic.Disposition
		if disposition == "" {
			disposition = "emitted"
		}
		if disposition == "emitted" && diagnostic.ToolCallID != "" {
			if _, ok := droppedIDs[diagnostic.ToolCallID]; ok {
				disposition = "throttled_write_serial"
			}
		}
		t.ToolSpans = append(t.ToolSpans, db.ToolCallSpanRecord{
			SpanID:            uuid.NewString(),
			CallIndex:         len(t.ToolSpans),
			ToolCallID:        diagnostic.ToolCallID,
			ToolName:          diagnostic.ToolName,
			IsMutating:        diagnostic.IsMutating,
			ExtractionChannel: diagnostic.ExtractionChannel,
			Disposition:       disposition,
			ArgsFingerprint:   canonicalCallFingerprint(diagnostic.ToolName, diagnostic.Arguments),
			ArgsBytes:         len(diagnostic.Arguments),
			TargetHint:        diagnosticTargetHint(diagnostic.Arguments),
			ValidationError:   diagnostic.ValidationError,
			EmittedAtMs:       emittedAtMs,
		})
	}

	for _, repair := range arranged.Repairs {
		addTurnAnomaly(t, attempt, "arranger_repair", repair, "info")
	}
	quarantineSeen := false
	for _, diagnostic := range arranged.ToolDiagnostics {
		switch diagnostic.Disposition {
		case "rejected_schema_invalid":
			addTurnAnomaly(t, attempt, "arranger_rejection", "schema_invalid", "warn")
		case "rejected_undeclared_tool":
			addTurnAnomaly(t, attempt, "arranger_rejection", "undeclared_tool", "warn")
		case "rejected_mid_value_truncation":
			addTurnAnomaly(t, attempt, "arranger_rejection", "mid_value_truncation", "warn")
		case "quarantined_untagged_mutation":
			addTurnAnomaly(t, attempt, "quarantine", "untagged_mutation", "warn")
			quarantineSeen = true
		}
	}
	if arranged.UntaggedMutation && !quarantineSeen {
		addTurnAnomaly(t, attempt, "quarantine", "untagged_mutation", "warn")
	}
}

// doomLoopAnomalyCode maps an intervention detail to the bounded anomaly code.
func doomLoopAnomalyCode(detail string) string {
	switch {
	case strings.HasPrefix(detail, "cycle:"):
		return "cycle"
	case strings.HasPrefix(detail, "passive_read:"):
		return "passive_read"
	default:
		return "repeat"
	}
}

// int64Ptr returns an independent pointer so two milestones recorded in the same
// observation can never share one storage cell.
func int64Ptr(value int64) *int64 {
	return &value
}

// milestoneDelta returns end - start, or nil when either milestone is missing.
func milestoneDelta(start, end *int64) *int64 {
	if start == nil || end == nil {
		return nil
	}
	delta := *end - *start
	return &delta
}

// TTFTThoughtMs is the delay from request start to the first reasoning byte.
func (t TurnTelemetry) TTFTThoughtMs() *int64 {
	return milestoneDelta(&t.RequestStartedMs, t.FirstThoughtMs)
}

// TTFTAnswerMs is the delay from request start to the first visible answer byte.
// It is measured at the upstream front, never at the client write: with tools
// declared the content is buffered until EOF, so a client-write timestamp would
// collapse into the total turn duration.
func (t TurnTelemetry) TTFTAnswerMs() *int64 {
	return milestoneDelta(&t.RequestStartedMs, t.FirstAnswerMs)
}

// ThoughtDurationMs is the reasoning span. It is nil when the reasoning block
// never closed: reporting the turn's remaining time as reasoning would invent
// data the upstream never produced.
func (t TurnTelemetry) ThoughtDurationMs() *int64 {
	return milestoneDelta(t.FirstThoughtMs, t.ThoughtEndedMs)
}

// AnswerDurationMs is the span from the first visible answer byte to turn end.
// It is nil until the turn completes, so a negative span is never reported.
func (t TurnTelemetry) AnswerDurationMs() *int64 {
	if t.CompletedMs == 0 {
		return nil
	}
	return milestoneDelta(t.FirstAnswerMs, &t.CompletedMs)
}

// TotalDurationMs is the whole turn, request start to completion. It is zero
// until the turn completes.
func (t TurnTelemetry) TotalDurationMs() int64 {
	if t.CompletedMs == 0 {
		return 0
	}
	return t.CompletedMs - t.RequestStartedMs
}

// streamPhaseTracker timestamps the reasoning and answer phases of a streaming
// turn. The clock is injectable so tests are fully deterministic.
//
// Timestamps are reset per upstream attempt: a retry discards the rejected
// generation, so the milestones must describe the generation whose tokens are
// reported. Only requestStartedMs and clientFirstThoughtMs survive, because the
// client did receive the rejected attempt's reasoning chunks.
//
// No cross-phase ordering is enforced. The extractor emits visible content that
// precedes a thought block into the cleaned content, so an answer can start
// before a thought closes; forcing firstThought <= thoughtEnded <= firstAnswer
// would fabricate a timeline.
type streamPhaseTracker struct {
	mu sync.Mutex

	now func() int64

	requestStartedMs     int64
	attemptStartedMs     *int64
	firstThoughtMs       *int64
	thoughtEndedMs       *int64
	firstAnswerMs        *int64
	clientFirstThoughtMs *int64
	closedBlocks         int
}

func newStreamPhaseTracker(now func() int64, requestStartedMs int64) *streamPhaseTracker {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &streamPhaseTracker{now: now, requestStartedMs: requestStartedMs}
}

// markAttemptStart begins a new upstream generation, discarding the phase
// milestones of the previous one.
func (p *streamPhaseTracker) markAttemptStart() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	p.attemptStartedMs = int64Ptr(now)
	p.firstThoughtMs = nil
	p.thoughtEndedMs = nil
	p.firstAnswerMs = nil
	p.closedBlocks = 0
}

// observeProgress records each phase transition. TTFT milestones keep their
// first occurrence; the thought end advances to the last closed block, because a
// turn may interleave reasoning and visible text.
func (p *streamPhaseTracker) observeProgress(state gemini.StreamThoughtState) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	if state.ThoughtSeen && p.firstThoughtMs == nil {
		p.firstThoughtMs = int64Ptr(now)
	}
	// Callers that only know the boolean are normalized to a single close, so a
	// missing count can never leave the milestone unset.
	closedBlocks := state.ClosedBlocks
	if closedBlocks == 0 && state.ThoughtClosed {
		closedBlocks = 1
	}
	if closedBlocks > p.closedBlocks {
		p.closedBlocks = closedBlocks
		p.thoughtEndedMs = int64Ptr(now)
	}
	if state.AnswerStarted && p.firstAnswerMs == nil {
		p.firstAnswerMs = int64Ptr(now)
	}
}

// markClientThought records the first reasoning chunk actually written to the
// client, across every attempt.
func (p *streamPhaseTracker) markClientThought() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.clientFirstThoughtMs != nil {
		return
	}
	p.clientFirstThoughtMs = int64Ptr(p.now())
}

// apply copies the tracked milestones onto a telemetry record.
func (p *streamPhaseTracker) apply(t *TurnTelemetry) {
	p.mu.Lock()
	defer p.mu.Unlock()

	t.RequestStartedMs = p.requestStartedMs
	t.AttemptStartedMs = p.attemptStartedMs
	t.FirstThoughtMs = p.firstThoughtMs
	t.ThoughtEndedMs = p.thoughtEndedMs
	t.FirstAnswerMs = p.firstAnswerMs
	t.ClientFirstThoughtMs = p.clientFirstThoughtMs
}

var (
	telemetrySinkMu   sync.Mutex
	telemetrySinkID   uint64
	telemetrySinkFunc func(TurnTelemetry)
)

// RegisterTurnTelemetrySink installs the single downstream consumer of turn
// telemetry. It returns an unregister function, which tests must call so a sink
// never leaks into another test.
func RegisterTurnTelemetrySink(fn func(TurnTelemetry)) func() {
	telemetrySinkMu.Lock()
	telemetrySinkID++
	id := telemetrySinkID
	telemetrySinkFunc = fn
	telemetrySinkMu.Unlock()

	return func() {
		telemetrySinkMu.Lock()
		if telemetrySinkID == id {
			telemetrySinkFunc = nil
		}
		telemetrySinkMu.Unlock()
	}
}

// notifyTurnTelemetry delivers a record to the registered sink. The sink runs
// outside the lock so it can never deadlock by registering or unregistering.
func notifyTurnTelemetry(t TurnTelemetry) {
	telemetrySinkMu.Lock()
	fn := telemetrySinkFunc
	telemetrySinkMu.Unlock()

	if fn != nil {
		fn(t)
	}
}

// finalizeTurnTelemetry derives the disjoint token counts, the reasoning
// fingerprint and the truncation flag from the arranged output, or from the raw
// buffer when the turn was blocked before arranging.
//
// It deliberately uses the cheap heuristic estimator: the exact BPE encoder
// sits behind a two-slot global semaphore, and these fields are internal
// telemetry rather than the client-facing usage numbers.
func finalizeTurnTelemetry(t *TurnTelemetry, raw string, arranged *gemini.ArrangedOutput) {
	var cleaned, reasoning string
	var unclosed bool

	if arranged != nil {
		cleaned = arranged.CleanText
		reasoning = arranged.ReasoningContent
		unclosed = arranged.ThoughtUnclosed
	} else {
		state := gemini.InspectStreamProgress(raw)
		cleaned = state.CleanedContent
		reasoning = state.ReasoningContent
		unclosed = state.ThoughtUnclosed
	}

	t.ThoughtUnclosed = unclosed
	t.ThoughtTokens = EstimateTokens(reasoning)
	if t.ThoughtTokens > 0 {
		sum := sha256.Sum256([]byte(reasoning))
		t.ThoughtSHA256 = hex.EncodeToString(sum[:])
	}

	answer := EstimateTokens(cleaned)
	if arranged != nil {
		for _, call := range arranged.ToolCalls {
			// A call-only turn carries no prose, so the arguments are the only
			// completion the model actually produced.
			answer += EstimateTokens(call.Function.Name)
			answer += EstimateTokens(call.Function.Arguments)
		}
	}
	t.AnswerTokens = answer

	if t.TokenEstimator == "" {
		t.TokenEstimator = "heuristic"
	}
}

// setSentPrompt records the payload actually sent upstream. Only its size and a
// token estimate are kept: the prompt itself never enters the record.
func (e *turnTelemetryEmitter) setSentPrompt(prompt string) {
	e.tel.SentPromptBytes = len(prompt)
	e.tel.InputTokens = EstimateTokens(prompt)
}

// applyWorkerTelemetry records the pool worker that served the turn, so a
// session row can be joined to the account that produced it.
func applyWorkerTelemetry(tel *TurnTelemetry, worker *AccountWorker) {
	if worker == nil {
		return
	}
	tel.WorkerID = worker.ID
	tel.AccountTier = worker.Tier
}
