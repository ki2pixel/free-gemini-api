package db

import (
	"database/sql"

	"github.com/google/uuid"
)

// AgentTurnRecord is the content-free telemetry of one completed turn. It mirrors
// the API's TurnTelemetry; session identity is already hashed by the caller, so
// no prompt, reasoning body or tool argument can reach this layer.
type AgentTurnRecord struct {
	// Session identity.
	SessionID       string // sha256 hex of the API session key
	UserID          string // short fingerprint of the client session id
	ConversationID  string // last known upstream conversation id
	SystemScopeHash string

	// Model and routing.
	RequestedModel   string
	EffectiveModel   string
	SendModel        string
	ProductMode      int
	RoutedTaskToChat bool
	ThinkingTier     string
	WorkerID         string
	AccountTier      string
	ToolsDeclared    int

	// Context shape.
	UpstreamConvID   string
	TrialID          string
	IsDelta          bool
	ReplayReason     string
	FlatPromptBytes  int
	SentPromptBytes  int
	DeltaBytes       int
	MaskedBytesSaved int

	// Milestones in epoch milliseconds. Nil means the phase was never observed.
	RequestStartedMs     int64
	AttemptStartedMs     *int64
	FirstThoughtMs       *int64
	ThoughtEndedMs       *int64
	FirstAnswerMs        *int64
	ClientFirstThoughtMs *int64
	CompletedMs          int64

	// Disjoint token accounting.
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
	VisibleLen            int

	// Granular evidence. Spans and anomalies are inserted in the same
	// transaction as the turn; outcomes close spans emitted by earlier turns.
	Spans        []ToolCallSpanRecord
	ToolOutcomes []ToolCallOutcomeRecord
	Anomalies    []TurnAnomalyRecord
}

// ToolCallSpanRecord is one tool-call attempt. Arguments are never stored: only
// a digest, a two-segment hint and a structural validation error.
type ToolCallSpanRecord struct {
	SpanID            string `json:"span_id"`
	TurnID            string `json:"turn_id,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	CallIndex         int    `json:"call_index"`
	ToolCallID        string `json:"tool_call_id"`
	ToolName          string `json:"tool_name"`
	IsMutating        bool   `json:"is_mutating"`
	ExtractionChannel string `json:"extraction_channel"`
	Disposition       string `json:"disposition"`
	ArgsFingerprint   string `json:"args_fingerprint"`
	ArgsBytes         int    `json:"args_bytes"`
	TargetHint        string `json:"target_hint"`
	ValidationError   string `json:"validation_error"`
	ResultReceived    bool   `json:"result_received"`
	ResultIsError     bool   `json:"result_is_error"`
	ResultBytes       int    `json:"result_bytes"`
	EmittedAtMs       int64  `json:"emitted_at_ms"`
	ResultReceivedMs  *int64 `json:"result_received_ms,omitempty"`
	RoundtripMs       *int64 `json:"roundtrip_ms,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
}

// ToolCallOutcomeRecord is a role:"tool" result observed on the next turn.
type ToolCallOutcomeRecord struct {
	ToolCallID   string
	IsError      bool
	ResultBytes  int
	ReceivedAtMs int64
}

// TurnAnomalyRecord is one content-free incident. Detail is categorical only.
type TurnAnomalyRecord struct {
	AnomalyID    int64  `json:"anomaly_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	ToolSpanID   string `json:"tool_span_id,omitempty"`
	AttemptIndex int    `json:"attempt_index"`
	Category     string `json:"category"`
	Code         string `json:"code"`
	Severity     string `json:"severity"`
	Detail       string `json:"detail"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// nullInt64 converts an optional milestone to a driver value.
func nullInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// clampMilestone returns an independent copy of value raised to floor, so a
// caller's pointer is never mutated.
func clampMilestone(value *int64, floor int64) *int64 {
	if value == nil {
		return nil
	}
	clamped := *value
	if clamped < floor {
		clamped = floor
	}
	return &clamped
}

// normalizeAgentTurnRecord aligns optional labels and temporal invariants with
// the table's CHECK constraints. A caller that leaves a field empty or supplies a
// non-monotone timeline is corrected, never silently rejected: losing the turn
// would be worse than losing one millisecond of precision.
func normalizeAgentTurnRecord(rec *AgentTurnRecord) {
	if rec.AccountTier == "" {
		rec.AccountTier = "free"
	}
	if rec.ThinkingTier == "" {
		rec.ThinkingTier = "none"
	}
	if rec.ReplayReason == "" {
		rec.ReplayReason = "none"
	}
	if rec.TurnClass == "" {
		rec.TurnClass = "none"
	}
	if rec.FinishReason == "" {
		rec.FinishReason = "error"
	}
	if rec.DoomLoopAction == "" {
		rec.DoomLoopAction = "none"
	}
	if rec.TokenEstimator == "" {
		rec.TokenEstimator = "heuristic"
	}

	if rec.RequestStartedMs < 1 {
		rec.RequestStartedMs = 1
	}
	rec.AttemptStartedMs = clampMilestone(rec.AttemptStartedMs, rec.RequestStartedMs)
	rec.FirstThoughtMs = clampMilestone(rec.FirstThoughtMs, rec.RequestStartedMs)
	rec.ThoughtEndedMs = clampMilestone(rec.ThoughtEndedMs, rec.RequestStartedMs)
	rec.FirstAnswerMs = clampMilestone(rec.FirstAnswerMs, rec.RequestStartedMs)
	rec.ClientFirstThoughtMs = clampMilestone(rec.ClientFirstThoughtMs, rec.RequestStartedMs)

	// A thought cannot end before it started.
	if rec.ThoughtEndedMs != nil && rec.FirstThoughtMs != nil && *rec.ThoughtEndedMs < *rec.FirstThoughtMs {
		ended := *rec.FirstThoughtMs
		rec.ThoughtEndedMs = &ended
	}

	if rec.CompletedMs < rec.RequestStartedMs {
		rec.CompletedMs = rec.RequestStartedMs
	}
	if rec.FirstAnswerMs != nil && rec.CompletedMs < *rec.FirstAnswerMs {
		rec.CompletedMs = *rec.FirstAnswerMs
	}
}

// LogAgentTurn persists one turn and its session row in a single transaction.
//
// The session row is upserted first because the turn references it; the turn
// index is computed inside the same transaction so concurrent telemetry workers
// can never allocate the same (session_id, turn_index) pair.
func LogAgentTurn(rec AgentTurnRecord) error {
	normalizeAgentTurnRecord(&rec)

	dbMu.Lock()
	defer dbMu.Unlock()

	if DB == nil {
		return nil
	}

	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`
		INSERT INTO agent_sessions (
			session_id, conversation_id, user_id, system_scope_hash,
			requested_model, effective_model, product_mode, routed_task_to_chat,
			thinking_tier, worker_id, account_tier, tools_declared_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			conversation_id = CASE WHEN excluded.conversation_id != '' THEN excluded.conversation_id ELSE agent_sessions.conversation_id END,
			user_id = CASE WHEN excluded.user_id != '' THEN excluded.user_id ELSE agent_sessions.user_id END,
			system_scope_hash = CASE WHEN excluded.system_scope_hash != '' THEN excluded.system_scope_hash ELSE agent_sessions.system_scope_hash END,
			requested_model = excluded.requested_model,
			effective_model = excluded.effective_model,
			product_mode = excluded.product_mode,
			routed_task_to_chat = excluded.routed_task_to_chat,
			thinking_tier = excluded.thinking_tier,
			worker_id = CASE WHEN excluded.worker_id != '' THEN excluded.worker_id ELSE agent_sessions.worker_id END,
			account_tier = excluded.account_tier,
			tools_declared_count = excluded.tools_declared_count,
			updated_at = CURRENT_TIMESTAMP
	`,
		rec.SessionID, rec.ConversationID, rec.UserID, rec.SystemScopeHash,
		rec.RequestedModel, rec.EffectiveModel, rec.ProductMode, boolToInt(rec.RoutedTaskToChat),
		rec.ThinkingTier, rec.WorkerID, rec.AccountTier, rec.ToolsDeclared,
	)
	if err != nil {
		return err
	}

	var turnIndex int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(turn_index) + 1, 0) FROM agent_turns WHERE session_id = ?`,
		rec.SessionID,
	).Scan(&turnIndex); err != nil {
		return err
	}

	turnID := uuid.NewString()
	_, err = tx.Exec(`
		INSERT INTO agent_turns (
			turn_id, session_id, turn_index, upstream_conv_id, send_model, trial_id,
			is_delta, replay_reason, flat_prompt_bytes, sent_prompt_bytes, delta_bytes,
			masked_obs_bytes_saved, request_started_ms, attempt_started_ms,
			first_thought_ms, thought_ended_ms, first_answer_ms, client_first_thought_ms,
			completed_ms, input_tokens, thought_tokens, answer_tokens, token_estimator,
			attempt_count, empty_gen_retries, refusal_retries, transport_retries,
			stream_stalled, upstream_suspected_stop, ground_truth_injected,
			turn_class, finish_reason, doom_loop_action, doom_loop_signal,
			thought_unclosed, thought_sha256, visible_len
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		turnID, rec.SessionID, turnIndex, rec.UpstreamConvID, rec.SendModel, rec.TrialID,
		boolToInt(rec.IsDelta), rec.ReplayReason, rec.FlatPromptBytes, rec.SentPromptBytes, rec.DeltaBytes,
		rec.MaskedBytesSaved, rec.RequestStartedMs, nullInt64(rec.AttemptStartedMs),
		nullInt64(rec.FirstThoughtMs), nullInt64(rec.ThoughtEndedMs), nullInt64(rec.FirstAnswerMs), nullInt64(rec.ClientFirstThoughtMs),
		rec.CompletedMs, rec.InputTokens, rec.ThoughtTokens, rec.AnswerTokens, rec.TokenEstimator,
		rec.AttemptCount, rec.EmptyGenRetries, rec.RefusalRetries, rec.TransportRetries,
		boolToInt(rec.StreamStalled), boolToInt(rec.UpstreamSuspectedStop), boolToInt(rec.GroundTruthInjected),
		rec.TurnClass, rec.FinishReason, rec.DoomLoopAction, rec.DoomLoopSignal,
		boolToInt(rec.ThoughtUnclosed), rec.ThoughtSHA256, rec.VisibleLen,
	)
	if err != nil {
		return err
	}

	// Close spans emitted by earlier turns before inserting this turn's own
	// spans. The partial index on (session_id, tool_call_id) keeps each UPDATE
	// O(1); a span already closed never matches again.
	for _, outcome := range rec.ToolOutcomes {
		if outcome.ToolCallID == "" {
			continue
		}
		resultBytes := outcome.ResultBytes
		if resultBytes < 0 {
			resultBytes = 0
		}
		receivedAt := outcome.ReceivedAtMs
		if _, err := tx.Exec(`
			UPDATE tool_call_spans
			SET result_received = 1,
			    result_is_error = ?,
			    result_bytes = ?,
			    result_received_ms = CASE WHEN ? >= emitted_at_ms THEN ? ELSE emitted_at_ms END
			WHERE session_id = ? AND tool_call_id = ?
			  AND disposition = 'emitted' AND result_received = 0`,
			boolToInt(outcome.IsError), resultBytes, receivedAt, receivedAt,
			rec.SessionID, outcome.ToolCallID,
		); err != nil {
			return err
		}
	}

	for index, span := range rec.Spans {
		if span.SpanID == "" {
			span.SpanID = uuid.NewString()
		}
		if span.CallIndex < 0 {
			span.CallIndex = index
		}
		if span.ToolName == "" {
			span.ToolName = "unknown"
		}
		if span.ExtractionChannel == "" {
			span.ExtractionChannel = "tagged_xml"
		}
		if span.Disposition == "" {
			span.Disposition = "emitted"
		}
		if span.EmittedAtMs < 1 {
			span.EmittedAtMs = 1
		}
		if span.ArgsBytes < 0 {
			span.ArgsBytes = 0
		}
		if span.ResultBytes < 0 {
			span.ResultBytes = 0
		}
		receivedAt := span.ResultReceivedMs
		if receivedAt != nil && *receivedAt < span.EmittedAtMs {
			clamped := span.EmittedAtMs
			receivedAt = &clamped
		}
		if _, err := tx.Exec(`
			INSERT INTO tool_call_spans (
				span_id, turn_id, session_id, call_index, tool_call_id, tool_name,
				is_mutating, extraction_channel, disposition, args_fingerprint, args_bytes,
				target_hint, validation_error, result_received, result_is_error,
				result_bytes, emitted_at_ms, result_received_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			span.SpanID, turnID, rec.SessionID, span.CallIndex, span.ToolCallID, span.ToolName,
			boolToInt(span.IsMutating), span.ExtractionChannel, span.Disposition,
			span.ArgsFingerprint, span.ArgsBytes, span.TargetHint, span.ValidationError,
			boolToInt(span.ResultReceived || receivedAt != nil), boolToInt(span.ResultIsError),
			span.ResultBytes, span.EmittedAtMs, nullInt64(receivedAt),
		); err != nil {
			return err
		}
	}

	for _, anomaly := range rec.Anomalies {
		// An anomaly without a bounded category or code would be rejected by the
		// CHECK; skip it rather than lose the whole turn's telemetry.
		if anomaly.Category == "" || anomaly.Code == "" {
			continue
		}
		severity := anomaly.Severity
		if severity == "" {
			severity = "info"
		}
		attempt := anomaly.AttemptIndex
		if attempt < 0 {
			attempt = 0
		}
		detail := anomaly.Detail
		if len(detail) > 512 {
			detail = detail[:512]
		}
		var spanID any
		if anomaly.ToolSpanID != "" {
			spanID = anomaly.ToolSpanID
		}
		if _, err := tx.Exec(`
			INSERT INTO turn_anomalies (
				turn_id, session_id, tool_span_id, attempt_index, category, code, severity, detail
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			turnID, rec.SessionID, spanID, attempt, anomaly.Category, anomaly.Code, severity, detail,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// BenchmarkTrialRecord mirrors one benchmark_trials row. The verdict comes from
// the shell oracle; the proxy enriches it with the correlated telemetry.
type BenchmarkTrialRecord struct {
	TrialID            string `json:"trial_id"`
	SuiteID            string `json:"suite_id"`
	TaskID             string `json:"task_id"`
	Stratum            string `json:"stratum"`
	RequestedModel     string `json:"requested_model"`
	EffectiveModel     string `json:"effective_model"`
	Paradigm           string `json:"paradigm"`
	ThinkingTier       string `json:"thinking_tier"`
	TrialIndex         int    `json:"trial_index"`
	PairID             string `json:"pair_id"`
	ArmOrder           string `json:"arm_order"`
	SessionID          string `json:"session_id"`
	WorkerID           string `json:"worker_id"`
	AccountTier        string `json:"account_tier"`
	OraclePassed       bool   `json:"oracle_passed"`
	OracleMode         string `json:"oracle_mode"`
	OracleExitCode     int    `json:"oracle_exit_code"`
	WorktreeDiffFiles  int    `json:"worktree_diff_files"`
	TranscriptClean    bool   `json:"transcript_clean"`
	WallTimeMs         int64  `json:"wall_time_ms"`
	PrimaryFailureCode string `json:"primary_failure_code"`
	Notes              string `json:"notes"`
	CreatedAt          string `json:"created_at,omitempty"`
}

// SessionCorrelation is the session and turn evidence behind one trial.
type SessionCorrelation struct {
	SessionID        string
	EffectiveModel   string
	ThinkingTier     string
	WorkerID         string
	AccountTier      string
	TurnClass        string
	FinishReason     string
	DoomLoopAction   string
	StreamStalled    bool
	TransportRetries int
	ThoughtUnclosed  bool
}

// BenchmarkTrialTelemetry is one row of v_benchmark_trial_telemetry.
type BenchmarkTrialTelemetry struct {
	TrialID            string  `json:"trial_id"`
	SuiteID            string  `json:"suite_id"`
	TaskID             string  `json:"task_id"`
	Stratum            string  `json:"stratum"`
	RequestedModel     string  `json:"requested_model"`
	EffectiveModel     string  `json:"effective_model"`
	Paradigm           string  `json:"paradigm"`
	ThinkingTier       string  `json:"thinking_tier"`
	TrialIndex         int     `json:"trial_index"`
	PairID             string  `json:"pair_id"`
	ArmOrder           string  `json:"arm_order"`
	SessionID          string  `json:"session_id"`
	WorkerID           string  `json:"worker_id"`
	AccountTier        string  `json:"account_tier"`
	OraclePassed       bool    `json:"oracle_passed"`
	OracleMode         string  `json:"oracle_mode"`
	OracleExitCode     int     `json:"oracle_exit_code"`
	WorktreeDiffFiles  int     `json:"worktree_diff_files"`
	TranscriptClean    bool    `json:"transcript_clean"`
	WallTimeMs         int64   `json:"wall_time_ms"`
	PrimaryFailureCode string  `json:"primary_failure_code"`
	CreatedAt          string  `json:"created_at"`
	ObservedTurns      int     `json:"observed_turns"`
	FullReplays        int     `json:"full_replays"`
	InputTokens        int     `json:"input_tokens"`
	ThoughtTokens      int     `json:"thought_tokens"`
	AnswerTokens       int     `json:"answer_tokens"`
	TotalOutputTokens  int     `json:"total_output_tokens"`
	AvgTTFTThoughtMs   float64 `json:"avg_ttft_thought_ms"`
	AvgTTFTAnswerMs    float64 `json:"avg_ttft_answer_ms"`
	DoomLoopEvents     int     `json:"doom_loop_events"`
	StreamStalls       int     `json:"stream_stalls"`
	RefusalRetries     int     `json:"refusal_retries"`
}

// BenchmarkCostOfPass is one row of v_benchmark_cost_of_pass.
type BenchmarkCostOfPass struct {
	SuiteID                string   `json:"suite_id"`
	TaskID                 string   `json:"task_id"`
	Stratum                string   `json:"stratum"`
	Paradigm               string   `json:"paradigm"`
	ThinkingTier           string   `json:"thinking_tier"`
	Trials                 int      `json:"trials"`
	Passes                 int      `json:"passes"`
	PassRate               float64  `json:"pass_rate"`
	AvgWallTimeMs          float64  `json:"avg_wall_time_ms"`
	TotalTokensSum         int      `json:"total_tokens_sum"`
	ThoughtTokensSum       int      `json:"thought_tokens_sum"`
	CostOfPassTotalTokens  *float64 `json:"cost_of_pass_total_tokens"`
	CostOfPassOutputTokens *float64 `json:"cost_of_pass_output_tokens"`
}

// normalizeBenchmarkTrialRecord aligns optional labels with the table's CHECK
// constraints. Invalid paradigm / arm_order / oracle_mode values are left intact
// so the database rejects them: silently rewriting a dimension would corrupt the
// experiment.
func normalizeBenchmarkTrialRecord(rec *BenchmarkTrialRecord) {
	if rec.Stratum == "" {
		rec.Stratum = ""
	}
	if rec.ThinkingTier == "" {
		rec.ThinkingTier = "none"
	}
	if rec.ArmOrder == "" {
		rec.ArmOrder = "single"
	}
	if rec.AccountTier == "" {
		rec.AccountTier = "free"
	}
	if rec.OracleMode == "" {
		rec.OracleMode = "transcript"
	}
	if rec.PrimaryFailureCode == "" {
		rec.PrimaryFailureCode = "none"
	}
	if rec.Paradigm == "" {
		rec.Paradigm = "chat"
	}
	if rec.TrialIndex < 1 {
		rec.TrialIndex = 1
	}
	if rec.WorktreeDiffFiles < 0 {
		rec.WorktreeDiffFiles = 0
	}
	if rec.WallTimeMs < 0 {
		rec.WallTimeMs = 0
	}
}

// resolveSessionForTrial ranks the sessions that carried a trial id. An
// auxiliary title/compaction request shares the id but declares no tools, so
// tools_declared_count comes first, then turn count and recency. Without that
// ordering, a one-shot title request could be correlated as the agentic run.
func resolveSessionForTrial(trialID string) (SessionCorrelation, error) {
	var corr SessionCorrelation
	if DB == nil || trialID == "" {
		return corr, nil
	}

	err := DB.QueryRow(`
		SELECT s.session_id, s.effective_model, s.thinking_tier, s.worker_id, s.account_tier
		FROM agent_turns t
		JOIN agent_sessions s ON s.session_id = t.session_id
		WHERE t.trial_id = ?
		GROUP BY s.session_id
		ORDER BY
			CASE WHEN s.tools_declared_count > 0 THEN 0 ELSE 1 END,
			COUNT(t.turn_id) DESC,
			MAX(t.completed_ms) DESC
		LIMIT 1`, trialID,
	).Scan(&corr.SessionID, &corr.EffectiveModel, &corr.ThinkingTier, &corr.WorkerID, &corr.AccountTier)
	if err == sql.ErrNoRows {
		return SessionCorrelation{}, nil
	}
	if err != nil {
		return SessionCorrelation{}, err
	}

	var stalled, unclosed int
	err = DB.QueryRow(`
		SELECT turn_class, finish_reason, doom_loop_action,
		       stream_stalled, transport_retries, thought_unclosed
		FROM agent_turns
		WHERE trial_id = ? AND session_id = ?
		ORDER BY
			CASE turn_class
				WHEN 'refusal' THEN 0
				WHEN 'schema_invalid' THEN 1
				WHEN 'untagged_mutation' THEN 2
				WHEN 'unbacked_completion' THEN 3
				WHEN 'greeting' THEN 4
				WHEN 'incomplete' THEN 5
				ELSE 6
			END,
			completed_ms DESC
		LIMIT 1`, trialID, corr.SessionID,
	).Scan(&corr.TurnClass, &corr.FinishReason, &corr.DoomLoopAction, &stalled, &corr.TransportRetries, &unclosed)
	if err == sql.ErrNoRows {
		return corr, nil
	}
	if err != nil {
		return SessionCorrelation{}, err
	}
	corr.StreamStalled = stalled != 0
	corr.ThoughtUnclosed = unclosed != 0
	return corr, nil
}

// ResolveSessionForTrial exposes the correlation for callers outside an upsert.
func ResolveSessionForTrial(trialID string) (SessionCorrelation, error) {
	return resolveSessionForTrial(trialID)
}

// failureCodeFromCorrelation maps the most severe observed turn signal to the
// bounded failure vocabulary. It only refines a failure the oracle already
// reported: a passing trial keeps 'none'.
func failureCodeFromCorrelation(corr SessionCorrelation) string {
	switch {
	case corr.DoomLoopAction == "halt":
		return "E_DOOM_LOOP_HALT"
	case corr.StreamStalled || corr.TransportRetries > 0:
		return "E_STALL_OR_TIMEOUT"
	case corr.TurnClass == "refusal":
		return "E_UPSTREAM_REFUSAL"
	case corr.TurnClass == "schema_invalid":
		return "E_SCHEMA_INVALID"
	case corr.TurnClass == "untagged_mutation":
		return "E_UNTAGGED_MUTATION"
	case corr.TurnClass == "unbacked_completion":
		return "E_UNBACKED_COMPLETION"
	case corr.TurnClass == "greeting":
		return "E_GREETING_LOOP"
	case corr.ThoughtUnclosed:
		return "E_UNCLOSED_THOUGHT"
	}
	return "none"
}

// isCoarseFailureCode reports whether the shell oracle could only classify the
// symptom. The proxy holds the turn evidence and replaces these with a specific
// code; transcript-proven codes (greeting, false quota, unbacked completion,
// incomplete prose) are kept as-is.
func isCoarseFailureCode(code string) bool {
	switch code {
	case "none", "E_ERROR_FINISH", "E_NO_TOOL_CALL", "E_ORACLE_ASSERTION_FAIL":
		return true
	}
	return false
}

// UpsertBenchmarkTrial persists one oracle verdict and correlates it with the
// session that carried the same trial id. Re-posting the same trial updates the
// row rather than duplicating it.
func UpsertBenchmarkTrial(rec BenchmarkTrialRecord) (BenchmarkTrialRecord, error) {
	normalizeBenchmarkTrialRecord(&rec)
	if DB == nil {
		return rec, nil
	}

	dbMu.Lock()
	defer dbMu.Unlock()

	if rec.SessionID == "" && rec.TrialID != "" {
		corr, err := resolveSessionForTrial(rec.TrialID)
		if err != nil {
			return rec, err
		}
		if corr.SessionID != "" {
			rec.SessionID = corr.SessionID
			if rec.EffectiveModel == "" {
				rec.EffectiveModel = corr.EffectiveModel
			}
			if rec.ThinkingTier == "none" && corr.ThinkingTier != "" {
				rec.ThinkingTier = corr.ThinkingTier
			}
			if rec.WorkerID == "" {
				rec.WorkerID = corr.WorkerID
			}
			if corr.AccountTier != "" {
				rec.AccountTier = corr.AccountTier
			}
			if !rec.OraclePassed && isCoarseFailureCode(rec.PrimaryFailureCode) {
				if derived := failureCodeFromCorrelation(corr); derived != "none" {
					rec.PrimaryFailureCode = derived
				}
			}
		}
	}

	_, err := DB.Exec(`
		INSERT INTO benchmark_trials (
			trial_id, suite_id, task_id, stratum, requested_model, effective_model,
			paradigm, thinking_tier, trial_index, pair_id, arm_order,
			session_id, worker_id, account_tier,
			oracle_passed, oracle_mode, oracle_exit_code,
			worktree_diff_files, transcript_clean, wall_time_ms,
			primary_failure_code, notes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(trial_id) DO UPDATE SET
			suite_id = excluded.suite_id,
			task_id = excluded.task_id,
			stratum = excluded.stratum,
			requested_model = excluded.requested_model,
			effective_model = CASE WHEN excluded.effective_model != '' THEN excluded.effective_model ELSE benchmark_trials.effective_model END,
			paradigm = excluded.paradigm,
			thinking_tier = excluded.thinking_tier,
			trial_index = excluded.trial_index,
			pair_id = excluded.pair_id,
			arm_order = excluded.arm_order,
			session_id = COALESCE(excluded.session_id, benchmark_trials.session_id),
			worker_id = CASE WHEN excluded.worker_id != '' THEN excluded.worker_id ELSE benchmark_trials.worker_id END,
			account_tier = excluded.account_tier,
			oracle_passed = excluded.oracle_passed,
			oracle_mode = excluded.oracle_mode,
			oracle_exit_code = excluded.oracle_exit_code,
			worktree_diff_files = excluded.worktree_diff_files,
			transcript_clean = excluded.transcript_clean,
			wall_time_ms = excluded.wall_time_ms,
			primary_failure_code = excluded.primary_failure_code,
			notes = excluded.notes
	`,
		rec.TrialID, rec.SuiteID, rec.TaskID, rec.Stratum, rec.RequestedModel, rec.EffectiveModel,
		rec.Paradigm, rec.ThinkingTier, rec.TrialIndex, rec.PairID, rec.ArmOrder,
		nullableString(rec.SessionID), rec.WorkerID, rec.AccountTier,
		boolToInt(rec.OraclePassed), rec.OracleMode, rec.OracleExitCode,
		rec.WorktreeDiffFiles, boolToInt(rec.TranscriptClean), rec.WallTimeMs,
		rec.PrimaryFailureCode, rec.Notes,
	)
	if err != nil {
		return rec, err
	}

	_ = DB.QueryRow(`SELECT created_at FROM benchmark_trials WHERE trial_id = ?`, rec.TrialID).Scan(&rec.CreatedAt)
	return rec, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ListBenchmarkTrials reads the correlated trial view, newest first.
func ListBenchmarkTrials(suiteID, taskID string, limit int) ([]BenchmarkTrialTelemetry, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := DB.Query(`
		SELECT trial_id, suite_id, task_id, stratum, requested_model, effective_model,
		       paradigm, thinking_tier, trial_index, pair_id, arm_order,
		       COALESCE(session_id, ''), worker_id, account_tier,
		       oracle_passed, oracle_mode, oracle_exit_code,
		       worktree_diff_files, transcript_clean, wall_time_ms,
		       primary_failure_code, created_at,
		       observed_turns, full_replays, input_tokens, thought_tokens, answer_tokens,
		       total_output_tokens, COALESCE(avg_ttft_thought_ms, 0), COALESCE(avg_ttft_answer_ms, 0),
		       doom_loop_events, stream_stalls, refusal_retries
		FROM v_benchmark_trial_telemetry
		WHERE (? = '' OR suite_id = ?) AND (? = '' OR task_id = ?)
		ORDER BY created_at DESC, trial_id
		LIMIT ?`, suiteID, suiteID, taskID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BenchmarkTrialTelemetry
	for rows.Next() {
		var (
			item                     BenchmarkTrialTelemetry
			oraclePassed, transcript int
		)
		if err := rows.Scan(
			&item.TrialID, &item.SuiteID, &item.TaskID, &item.Stratum,
			&item.RequestedModel, &item.EffectiveModel, &item.Paradigm, &item.ThinkingTier,
			&item.TrialIndex, &item.PairID, &item.ArmOrder,
			&item.SessionID, &item.WorkerID, &item.AccountTier,
			&oraclePassed, &item.OracleMode, &item.OracleExitCode,
			&item.WorktreeDiffFiles, &transcript, &item.WallTimeMs,
			&item.PrimaryFailureCode, &item.CreatedAt,
			&item.ObservedTurns, &item.FullReplays, &item.InputTokens, &item.ThoughtTokens,
			&item.AnswerTokens, &item.TotalOutputTokens, &item.AvgTTFTThoughtMs, &item.AvgTTFTAnswerMs,
			&item.DoomLoopEvents, &item.StreamStalls, &item.RefusalRetries,
		); err != nil {
			return nil, err
		}
		item.OraclePassed = oraclePassed != 0
		item.TranscriptClean = transcript != 0
		result = append(result, item)
	}
	return result, rows.Err()
}

// GetBenchmarkCostOfPass reads the Cost-of-Pass aggregates for one suite.
func GetBenchmarkCostOfPass(suiteID string) ([]BenchmarkCostOfPass, error) {
	if DB == nil {
		return nil, nil
	}
	rows, err := DB.Query(`
		SELECT suite_id, task_id, stratum, paradigm, thinking_tier,
		       trials, passes, pass_rate, COALESCE(avg_wall_time_ms, 0),
		       total_tokens_sum, thought_tokens_sum,
		       cost_of_pass_total_tokens, cost_of_pass_output_tokens
		FROM v_benchmark_cost_of_pass
		WHERE (? = '' OR suite_id = ?)
		ORDER BY suite_id, task_id, paradigm, thinking_tier`, suiteID, suiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BenchmarkCostOfPass
	for rows.Next() {
		var (
			item                  BenchmarkCostOfPass
			costTotal, costOutput sql.NullFloat64
		)
		if err := rows.Scan(
			&item.SuiteID, &item.TaskID, &item.Stratum, &item.Paradigm, &item.ThinkingTier,
			&item.Trials, &item.Passes, &item.PassRate, &item.AvgWallTimeMs,
			&item.TotalTokensSum, &item.ThoughtTokensSum,
			&costTotal, &costOutput,
		); err != nil {
			return nil, err
		}
		if costTotal.Valid {
			item.CostOfPassTotalTokens = &costTotal.Float64
		}
		if costOutput.Valid {
			item.CostOfPassOutputTokens = &costOutput.Float64
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// SessionTelemetrySummary is one row of v_session_telemetry_summary.
type SessionTelemetrySummary struct {
	SessionID               string  `json:"session_id"`
	UserID                  string  `json:"user_id"`
	RequestedModel          string  `json:"requested_model"`
	EffectiveModel          string  `json:"effective_model"`
	ProductMode             int     `json:"product_mode"`
	RoutedTaskToChat        bool    `json:"routed_task_to_chat"`
	ThinkingTier            string  `json:"thinking_tier"`
	AccountTier             string  `json:"account_tier"`
	WorkerID                string  `json:"worker_id"`
	TotalTurns              int     `json:"total_turns"`
	FullReplays             int     `json:"full_replays"`
	SumThoughtTokens        int     `json:"sum_thought_tokens"`
	SumAnswerTokens         int     `json:"sum_answer_tokens"`
	SumOutputTokens         int     `json:"sum_output_tokens"`
	SessionThinkingShare    float64 `json:"session_thinking_share"`
	AvgTTFTThoughtMs        float64 `json:"avg_ttft_thought_ms"`
	AvgTTFTAnswerMs         float64 `json:"avg_ttft_answer_ms"`
	SumTotalDurationMs      int64   `json:"sum_total_duration_ms"`
	DoomLoopEvents          int     `json:"doom_loop_events"`
	StreamStalls            int     `json:"stream_stalls"`
	RefusalRetries          int     `json:"refusal_retries"`
	ToolsEmitted            int     `json:"tools_emitted"`
	ToolsBlockedOrThrottled int     `json:"tools_blocked_or_throttled"`
	ToolErrors              int     `json:"tool_errors"`
	ArrangerRepairs         int     `json:"arranger_repairs"`
	TotalAnomalies          int     `json:"total_anomalies"`
	LastTurnAt              string  `json:"last_turn_at"`
}

const sessionSummaryColumns = `
	session_id, user_id, requested_model, effective_model, product_mode,
	routed_task_to_chat, thinking_tier, account_tier, worker_id,
	total_turns, full_replays, sum_thought_tokens, sum_answer_tokens,
	sum_output_tokens, session_thinking_share, COALESCE(avg_ttft_thought_ms, 0),
	COALESCE(avg_ttft_answer_ms, 0), sum_total_duration_ms, doom_loop_events,
	stream_stalls, refusal_retries, tools_emitted, tools_blocked_or_throttled,
	tool_errors, arranger_repairs, total_anomalies, last_turn_at`

func scanSessionSummary(row interface{ Scan(...any) error }) (SessionTelemetrySummary, error) {
	var (
		item   SessionTelemetrySummary
		routed int
		share  float64
	)
	if err := row.Scan(
		&item.SessionID, &item.UserID, &item.RequestedModel, &item.EffectiveModel,
		&item.ProductMode, &routed, &item.ThinkingTier, &item.AccountTier, &item.WorkerID,
		&item.TotalTurns, &item.FullReplays, &item.SumThoughtTokens, &item.SumAnswerTokens,
		&item.SumOutputTokens, &share, &item.AvgTTFTThoughtMs, &item.AvgTTFTAnswerMs,
		&item.SumTotalDurationMs, &item.DoomLoopEvents, &item.StreamStalls,
		&item.RefusalRetries, &item.ToolsEmitted, &item.ToolsBlockedOrThrottled,
		&item.ToolErrors, &item.ArrangerRepairs, &item.TotalAnomalies, &item.LastTurnAt,
	); err != nil {
		return item, err
	}
	item.RoutedTaskToChat = routed != 0
	item.SessionThinkingShare = share
	return item, nil
}

// ListSessionTelemetrySummaries reads the most recently active sessions.
func ListSessionTelemetrySummaries(limit int) ([]SessionTelemetrySummary, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := DB.Query(`SELECT `+sessionSummaryColumns+`
		FROM v_session_telemetry_summary
		ORDER BY last_turn_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionTelemetrySummary
	for rows.Next() {
		item, err := scanSessionSummary(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GetSessionTelemetrySummary reads one session, or a zero value if unknown.
func GetSessionTelemetrySummary(sessionID string) (SessionTelemetrySummary, error) {
	var item SessionTelemetrySummary
	if DB == nil || sessionID == "" {
		return item, nil
	}
	item, err := scanSessionSummary(DB.QueryRow(
		`SELECT `+sessionSummaryColumns+` FROM v_session_telemetry_summary WHERE session_id = ?`,
		sessionID,
	))
	if err == sql.ErrNoRows {
		return SessionTelemetrySummary{}, nil
	}
	return item, err
}

// ListToolSpans reads the most recent spans of one session.
func ListToolSpans(sessionID string, limit int) ([]ToolCallSpanRecord, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := DB.Query(`
		SELECT span_id, turn_id, session_id, call_index, tool_call_id, tool_name,
		       is_mutating, extraction_channel, disposition, args_fingerprint, args_bytes,
		       target_hint, validation_error, result_received, result_is_error,
		       result_bytes, emitted_at_ms, result_received_ms, roundtrip_ms, created_at
		FROM tool_call_spans
		WHERE session_id = ?
		ORDER BY created_at DESC, call_index DESC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ToolCallSpanRecord
	for rows.Next() {
		var (
			item                  ToolCallSpanRecord
			mutating, received    int
			isError               int
			receivedMs, roundtrip sql.NullInt64
		)
		if err := rows.Scan(
			&item.SpanID, &item.TurnID, &item.SessionID, &item.CallIndex, &item.ToolCallID,
			&item.ToolName, &mutating, &item.ExtractionChannel, &item.Disposition,
			&item.ArgsFingerprint, &item.ArgsBytes, &item.TargetHint, &item.ValidationError,
			&received, &isError, &item.ResultBytes, &item.EmittedAtMs, &receivedMs, &roundtrip,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		item.IsMutating = mutating != 0
		item.ResultReceived = received != 0
		item.ResultIsError = isError != 0
		if receivedMs.Valid {
			item.ResultReceivedMs = &receivedMs.Int64
		}
		if roundtrip.Valid {
			item.RoundtripMs = &roundtrip.Int64
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ListTurnAnomalies reads the most recent incidents of one session.
func ListTurnAnomalies(sessionID string, limit int) ([]TurnAnomalyRecord, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := DB.Query(`
		SELECT anomaly_id, turn_id, session_id, COALESCE(tool_span_id, ''),
		       attempt_index, category, code, severity, detail, created_at
		FROM turn_anomalies
		WHERE session_id = ?
		ORDER BY created_at DESC, anomaly_id DESC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TurnAnomalyRecord
	for rows.Next() {
		var item TurnAnomalyRecord
		if err := rows.Scan(
			&item.AnomalyID, &item.TurnID, &item.SessionID, &item.ToolSpanID,
			&item.AttemptIndex, &item.Category, &item.Code, &item.Severity,
			&item.Detail, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
