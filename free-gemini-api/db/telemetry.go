package db

import (
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

	_, err = tx.Exec(`
		INSERT INTO agent_turns (
			turn_id, session_id, turn_index, upstream_conv_id, send_model,
			is_delta, replay_reason, flat_prompt_bytes, sent_prompt_bytes, delta_bytes,
			masked_obs_bytes_saved, request_started_ms, attempt_started_ms,
			first_thought_ms, thought_ended_ms, first_answer_ms, client_first_thought_ms,
			completed_ms, input_tokens, thought_tokens, answer_tokens, token_estimator,
			attempt_count, empty_gen_retries, refusal_retries, transport_retries,
			stream_stalled, upstream_suspected_stop, ground_truth_injected,
			turn_class, finish_reason, doom_loop_action, doom_loop_signal,
			thought_unclosed, thought_sha256, visible_len
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		uuid.NewString(), rec.SessionID, turnIndex, rec.UpstreamConvID, rec.SendModel,
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

	return tx.Commit()
}
