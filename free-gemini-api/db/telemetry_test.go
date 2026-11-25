package db

import "testing"

// baseAgentRecord returns a minimal record that satisfies every CHECK.
func baseAgentRecord(sessionID string) AgentTurnRecord {
	firstThought := int64(1100)
	thoughtEnded := int64(1450)
	firstAnswer := int64(1600)
	attemptStarted := int64(1000)
	return AgentTurnRecord{
		SessionID:            sessionID,
		UserID:               "user-fingerprint",
		ConversationID:       "conv-1",
		SystemScopeHash:      "scope-hash",
		RequestedModel:       "gemini-4-neon",
		EffectiveModel:       "gemini-4-neon",
		SendModel:            "gemini-4-neon",
		ProductMode:          0,
		ThinkingTier:         "none",
		WorkerID:             "worker-1",
		AccountTier:          "free",
		ToolsDeclared:        0,
		UpstreamConvID:       "conv-1",
		ReplayReason:         "new",
		FlatPromptBytes:      120,
		SentPromptBytes:      120,
		RequestStartedMs:     1000,
		AttemptStartedMs:     &attemptStarted,
		FirstThoughtMs:       &firstThought,
		ThoughtEndedMs:       &thoughtEnded,
		FirstAnswerMs:        &firstAnswer,
		ClientFirstThoughtMs: &firstThought,
		CompletedMs:          2000,
		InputTokens:          30,
		ThoughtTokens:        100,
		AnswerTokens:         50,
		TokenEstimator:       "heuristic",
		AttemptCount:         1,
		TurnClass:            "ok",
		FinishReason:         "stop",
		DoomLoopAction:       "none",
		DoomLoopSignal:       "",
		ThoughtUnclosed:      false,
		ThoughtSHA256:        "deadbeef",
		VisibleLen:           42,
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	var enabled int
	if err := DB.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatalf("pragma foreign_keys: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1: the DSN pragma is not applied to pooled connections", enabled)
	}
}

// TestAgentTurnCascade proves the foreign key is enforced by the database, not
// merely declared: deleting the session must remove its turns with no explicit
// child delete.
func TestAgentTurnCascade(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const session = "cascade-session"
	for i := 0; i < 3; i++ {
		if err := LogAgentTurn(baseAgentRecord(session)); err != nil {
			t.Fatalf("LogAgentTurn %d: %v", i, err)
		}
	}

	var turns int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM agent_turns WHERE session_id = ?`, session).Scan(&turns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turns != 3 {
		t.Fatalf("turns = %d, want 3", turns)
	}

	if _, err := DB.Exec(`DELETE FROM agent_sessions WHERE session_id = ?`, session); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	var remaining int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM agent_turns WHERE session_id = ?`, session).Scan(&remaining); err != nil {
		t.Fatalf("count after cascade: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("turns after deleting the session = %d, want 0 (ON DELETE CASCADE ineffective)", remaining)
	}
}

// TestAgentTurnAcceptsProducedLabels inserts every value the Go code can produce
// for each bounded column. A CHECK that rejects one of them would silently drop
// real telemetry.
func TestAgentTurnAcceptsProducedLabels(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*AgentTurnRecord)
	}{
		{"class tool_call", func(r *AgentTurnRecord) { r.TurnClass = "tool_call" }},
		{"class ok", func(r *AgentTurnRecord) { r.TurnClass = "ok" }},
		{"class greeting", func(r *AgentTurnRecord) { r.TurnClass = "greeting" }},
		{"class incomplete", func(r *AgentTurnRecord) { r.TurnClass = "incomplete" }},
		{"class refusal", func(r *AgentTurnRecord) { r.TurnClass = "refusal" }},
		{"class pseudo_report", func(r *AgentTurnRecord) { r.TurnClass = "pseudo_report" }},
		{"class unbacked_completion", func(r *AgentTurnRecord) { r.TurnClass = "unbacked_completion" }},
		{"class untagged_mutation", func(r *AgentTurnRecord) { r.TurnClass = "untagged_mutation" }},
		{"class schema_invalid", func(r *AgentTurnRecord) { r.TurnClass = "schema_invalid" }},
		{"class none", func(r *AgentTurnRecord) { r.TurnClass = "none" }},

		{"replay none", func(r *AgentTurnRecord) { r.ReplayReason = "none" }},
		{"replay delta", func(r *AgentTurnRecord) { r.ReplayReason = "delta"; r.IsDelta = true }},
		{"replay new", func(r *AgentTurnRecord) { r.ReplayReason = "new" }},
		{"replay forced", func(r *AgentTurnRecord) { r.ReplayReason = "forced" }},
		{"replay worker_mismatch", func(r *AgentTurnRecord) { r.ReplayReason = "worker_mismatch" }},
		{"replay conv_mismatch", func(r *AgentTurnRecord) { r.ReplayReason = "conv_mismatch" }},
		{"replay identical", func(r *AgentTurnRecord) { r.ReplayReason = "identical" }},
		{"replay prefix_mismatch", func(r *AgentTurnRecord) { r.ReplayReason = "prefix_mismatch" }},

		{"doom none", func(r *AgentTurnRecord) { r.DoomLoopAction = "none"; r.DoomLoopSignal = "" }},
		{"doom warn_repeat", func(r *AgentTurnRecord) { r.DoomLoopAction = "warn_repeat"; r.DoomLoopSignal = "repeat" }},
		{"doom warn_passive_read", func(r *AgentTurnRecord) { r.DoomLoopAction = "warn_passive_read"; r.DoomLoopSignal = "passive_read" }},
		{"doom halt cycle", func(r *AgentTurnRecord) { r.DoomLoopAction = "halt"; r.DoomLoopSignal = "cycle" }},

		{"finish stop", func(r *AgentTurnRecord) { r.FinishReason = "stop" }},
		{"finish tool_calls", func(r *AgentTurnRecord) { r.FinishReason = "tool_calls" }},
		{"finish error", func(r *AgentTurnRecord) { r.FinishReason = "error" }},

		{"empty tier normalized", func(r *AgentTurnRecord) { r.AccountTier = ""; r.ThinkingTier = ""; r.TokenEstimator = "" }},
		{"zero attempts", func(r *AgentTurnRecord) { r.AttemptCount = 0 }},
		{"halt before attempt", func(r *AgentTurnRecord) {
			r.AttemptCount = 0
			r.AttemptStartedMs = nil
			r.FirstThoughtMs = nil
			r.ThoughtEndedMs = nil
			r.FirstAnswerMs = nil
			r.ClientFirstThoughtMs = nil
			r.TurnClass = "none"
			r.FinishReason = "error"
		}},
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := baseAgentRecord("labels-session-" + tc.name)
			tc.mutate(&rec)
			if err := LogAgentTurn(rec); err != nil {
				t.Fatalf("case %d (%s): %v", index, tc.name, err)
			}
		})
	}

	// The empty optional labels must have been normalized, never stored empty.
	var tier, estimator string
	if err := DB.QueryRow(
		`SELECT s.account_tier, t.token_estimator
		 FROM agent_turns t JOIN agent_sessions s ON s.session_id = t.session_id
		 WHERE t.session_id = ?`, "labels-session-empty tier normalized",
	).Scan(&tier, &estimator); err != nil {
		t.Fatalf("read normalized labels: %v", err)
	}
	if tier != "free" || estimator != "heuristic" {
		t.Fatalf("normalized labels = (%q, %q), want (free, heuristic)", tier, estimator)
	}
}

func TestAgentTurnGeneratedColumns(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	rec := baseAgentRecord("generated-session")
	if err := LogAgentTurn(rec); err != nil {
		t.Fatalf("LogAgentTurn: %v", err)
	}

	var ttftThought, ttftAnswer, thoughtDuration, totalOutput, totalDuration int
	var share, ratio float64
	err := DB.QueryRow(`
		SELECT ttft_thought_ms, ttft_answer_ms, thought_duration_ms,
		       total_output_tokens, total_duration_ms, thinking_share, thought_to_answer_ratio
		FROM agent_turns WHERE session_id = ?`, rec.SessionID,
	).Scan(&ttftThought, &ttftAnswer, &thoughtDuration, &totalOutput, &totalDuration, &share, &ratio)
	if err != nil {
		t.Fatalf("read generated columns: %v", err)
	}

	if ttftThought != 100 || ttftAnswer != 600 || thoughtDuration != 350 || totalDuration != 1000 {
		t.Fatalf("durations = (%d, %d, %d, %d), want (100, 600, 350, 1000)",
			ttftThought, ttftAnswer, thoughtDuration, totalDuration)
	}
	if totalOutput != 150 {
		t.Fatalf("total_output_tokens = %d, want 150", totalOutput)
	}
	if share < 0.66 || share > 0.67 {
		t.Fatalf("thinking_share = %f, want ~0.6667", share)
	}
	if ratio != 2.0 {
		t.Fatalf("thought_to_answer_ratio = %f, want 2.0", ratio)
	}

	// A turn with no completion tokens must not divide by zero.
	empty := baseAgentRecord("generated-empty")
	empty.ThoughtTokens = 0
	empty.AnswerTokens = 0
	if err := LogAgentTurn(empty); err != nil {
		t.Fatalf("empty LogAgentTurn: %v", err)
	}
	if err := DB.QueryRow(
		`SELECT thinking_share, thought_to_answer_ratio FROM agent_turns WHERE session_id = ?`, empty.SessionID,
	).Scan(&share, &ratio); err != nil {
		t.Fatalf("read empty generated columns: %v", err)
	}
	if share != 0 || ratio != 0 {
		t.Fatalf("empty ratios = (%f, %f), want (0, 0)", share, ratio)
	}
}

func TestSessionTelemetrySummaryView(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const session = "view-session"
	first := baseAgentRecord(session)
	first.ReplayReason = "new"
	if err := LogAgentTurn(first); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	second := baseAgentRecord(session)
	second.ReplayReason = "delta"
	second.IsDelta = true
	second.DoomLoopAction = "warn_repeat"
	second.DoomLoopSignal = "repeat"
	second.StreamStalled = true
	if err := LogAgentTurn(second); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	var totalTurns, fullReplays, doomEvents, stalls int
	var sumThought, sumAnswer int
	var avgTTFTThought float64
	err := DB.QueryRow(`
		SELECT total_turns, full_replays, doom_loop_events, stream_stalls,
		       sum_thought_tokens, sum_answer_tokens, avg_ttft_thought_ms
		FROM v_session_telemetry_summary WHERE session_id = ?`, session,
	).Scan(&totalTurns, &fullReplays, &doomEvents, &stalls, &sumThought, &sumAnswer, &avgTTFTThought)
	if err != nil {
		t.Fatalf("read view: %v", err)
	}
	if totalTurns != 2 || fullReplays != 1 {
		t.Fatalf("turns/replays = (%d, %d), want (2, 1)", totalTurns, fullReplays)
	}
	if doomEvents != 1 || stalls != 1 {
		t.Fatalf("doom/stalls = (%d, %d), want (1, 1)", doomEvents, stalls)
	}
	if sumThought != 200 || sumAnswer != 100 {
		t.Fatalf("token sums = (%d, %d), want (200, 100)", sumThought, sumAnswer)
	}
	if avgTTFTThought != 100 {
		t.Fatalf("avg_ttft_thought_ms = %f, want 100", avgTTFTThought)
	}
}

// TestAgentTurnTemporalClamp covers a caller-supplied non-monotone timeline: the
// record must be stored corrected rather than rejected by a CHECK.
func TestAgentTurnTemporalClamp(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	rec := baseAgentRecord("clamp-session")
	rec.RequestStartedMs = 5000
	rec.CompletedMs = 4000

	if err := LogAgentTurn(rec); err != nil {
		t.Fatalf("LogAgentTurn with inverted timeline: %v", err)
	}

	var completed int64
	if err := DB.QueryRow(`SELECT completed_ms FROM agent_turns WHERE session_id = ?`, rec.SessionID).Scan(&completed); err != nil {
		t.Fatalf("read completed_ms: %v", err)
	}
	if completed != 5000 {
		t.Fatalf("completed_ms = %d, want 5000 after clamping", completed)
	}
}

// TestDeleteConversationRemovesAgentTelemetry covers a session that crossed an
// upstream reset: deleting c_1 must remove its turns while c_2 survives.
func TestDeleteConversationRemovesAgentTelemetry(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const session = "gdpr-multireset-session"

	first := baseAgentRecord(session)
	first.ConversationID = "c_1"
	first.UpstreamConvID = "c_1"
	if err := LogAgentTurn(first); err != nil {
		t.Fatalf("turn c_1: %v", err)
	}
	second := baseAgentRecord(session)
	second.ConversationID = "c_2"
	second.UpstreamConvID = "c_2"
	if err := LogAgentTurn(second); err != nil {
		t.Fatalf("turn c_2: %v", err)
	}

	deleted, err := DeleteConversation("c_1")
	if err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 agent turn", deleted)
	}

	var c1, c2 int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM agent_turns WHERE upstream_conv_id = 'c_1'`).Scan(&c1)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM agent_turns WHERE upstream_conv_id = 'c_2'`).Scan(&c2)
	if c1 != 0 {
		t.Fatalf("c_1 turns = %d, want 0", c1)
	}
	if c2 != 1 {
		t.Fatalf("c_2 turns = %d, want 1 (the session is still alive)", c2)
	}

	// Deleting the session's current conversation removes the session row, and
	// the cascade removes its remaining turns.
	if _, err := DeleteConversation("c_2"); err != nil {
		t.Fatalf("DeleteConversation c_2: %v", err)
	}
	var sessions, turns int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE session_id = ?`, session).Scan(&sessions)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM agent_turns WHERE session_id = ?`, session).Scan(&turns)
	if sessions != 0 || turns != 0 {
		t.Fatalf("after deleting the session conversation: sessions=%d turns=%d, want 0/0", sessions, turns)
	}
}

// TestPurgeExpiredAgentSessions covers retention on updated_at with the cascade.
func TestPurgeExpiredAgentSessions(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const session = "retention-session"
	if err := LogAgentTurn(baseAgentRecord(session)); err != nil {
		t.Fatalf("LogAgentTurn: %v", err)
	}
	if _, err := DB.Exec(`UPDATE agent_sessions SET updated_at = datetime('now', '-45 days') WHERE session_id = ?`, session); err != nil {
		t.Fatalf("age session: %v", err)
	}

	if _, err := PurgeExpiredData(30); err != nil {
		t.Fatalf("PurgeExpiredData: %v", err)
	}

	var sessions, turns int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE session_id = ?`, session).Scan(&sessions)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM agent_turns WHERE session_id = ?`, session).Scan(&turns)
	if sessions != 0 || turns != 0 {
		t.Fatalf("expired session: sessions=%d turns=%d, want 0/0", sessions, turns)
	}
}
