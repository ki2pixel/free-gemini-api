package db

import (
	"database/sql"
	"testing"
)

func TestEnsureColumnIdempotent(t *testing.T) {
	raw, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)

	if _, err := raw.Exec(`CREATE TABLE legacy (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// SQLite resolves a view's columns at query time, so a view created before
	// the migration must become queryable once ensureColumn has run. This is
	// exactly what an upgrade from the Step 3 schema does.
	if _, err := raw.Exec(`CREATE VIEW legacy_view AS SELECT trial_id FROM legacy`); err != nil {
		t.Fatalf("create view: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := ensureColumn(raw, "legacy", "trial_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
			t.Fatalf("ensureColumn run %d: %v", i, err)
		}
	}
	var viewRows int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM legacy_view`).Scan(&viewRows); err != nil {
		t.Fatalf("query migrated view: %v", err)
	}
	if viewRows != 0 {
		t.Fatalf("legacy_view rows = %d, want 0", viewRows)
	}

	rows, err := raw.Query(`PRAGMA table_info(legacy)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "trial_id" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("trial_id columns = %d, want exactly 1 after two migrations", count)
	}
}

func TestResolveSessionForTrialPrefersToolDeclaringSession(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const trial = "resolve-trial-unique"

	auxiliary := baseAgentRecord("resolve-aux")
	auxiliary.TrialID = trial
	auxiliary.ToolsDeclared = 0
	if err := LogAgentTurn(auxiliary); err != nil {
		t.Fatalf("aux turn: %v", err)
	}

	agentic := baseAgentRecord("resolve-agentic")
	agentic.TrialID = trial
	agentic.ToolsDeclared = 3
	if err := LogAgentTurn(agentic); err != nil {
		t.Fatalf("agentic turn 1: %v", err)
	}
	if err := LogAgentTurn(agentic); err != nil {
		t.Fatalf("agentic turn 2: %v", err)
	}

	corr, err := ResolveSessionForTrial(trial)
	if err != nil {
		t.Fatalf("ResolveSessionForTrial: %v", err)
	}
	if corr.SessionID != "resolve-agentic" {
		t.Fatalf("resolved session = %q, want resolve-agentic (tools declared outranks turn count)", corr.SessionID)
	}
	if corr.WorkerID != agentic.WorkerID {
		t.Fatalf("worker_id = %q, want %q", corr.WorkerID, agentic.WorkerID)
	}
}

func TestBenchmarkTrialViewsAndCostOfPass(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const suite, task = "cop-suite", "cop-task"

	pass := baseAgentRecord("cop-session-pass")
	pass.TrialID = "cop-trial-pass"
	pass.InputTokens = 100
	pass.ThoughtTokens = 10
	pass.AnswerTokens = 5
	if err := LogAgentTurn(pass); err != nil {
		t.Fatalf("pass turn: %v", err)
	}

	fail := baseAgentRecord("cop-session-fail")
	fail.TrialID = "cop-trial-fail"
	fail.InputTokens = 50
	fail.ThoughtTokens = 20
	fail.AnswerTokens = 10
	if err := LogAgentTurn(fail); err != nil {
		t.Fatalf("fail turn: %v", err)
	}

	base := BenchmarkTrialRecord{
		SuiteID:        suite,
		TaskID:         task,
		Paradigm:       "chat",
		RequestedModel: "gemini-4-neon",
		TrialIndex:     1,
		OracleMode:     "transcript",
	}
	passTrial := base
	passTrial.TrialID = "cop-trial-pass"
	passTrial.OraclePassed = true
	if _, err := UpsertBenchmarkTrial(passTrial); err != nil {
		t.Fatalf("upsert pass: %v", err)
	}
	failTrial := base
	failTrial.TrialID = "cop-trial-fail"
	failTrial.PrimaryFailureCode = "E_INCOMPLETE_PROSE"
	if _, err := UpsertBenchmarkTrial(failTrial); err != nil {
		t.Fatalf("upsert fail: %v", err)
	}

	trials, err := ListBenchmarkTrials(suite, task, 10)
	if err != nil {
		t.Fatalf("ListBenchmarkTrials: %v", err)
	}
	if len(trials) != 2 {
		t.Fatalf("trials = %d, want 2", len(trials))
	}
	byID := map[string]BenchmarkTrialTelemetry{}
	for _, item := range trials {
		byID[item.TrialID] = item
	}
	if got := byID["cop-trial-pass"]; got.InputTokens != 100 || got.TotalOutputTokens != 15 || got.ObservedTurns != 1 {
		t.Fatalf("pass telemetry = %+v, want input=100 output=15 turns=1", got)
	}
	if got := byID["cop-trial-fail"].PrimaryFailureCode; got != "E_INCOMPLETE_PROSE" {
		t.Fatalf("failure code = %q, want E_INCOMPLETE_PROSE", got)
	}

	cost, err := GetBenchmarkCostOfPass(suite)
	if err != nil {
		t.Fatalf("GetBenchmarkCostOfPass: %v", err)
	}
	if len(cost) != 1 {
		t.Fatalf("cost rows = %d, want 1", len(cost))
	}
	row := cost[0]
	if row.Trials != 2 || row.Passes != 1 || row.PassRate != 0.5 {
		t.Fatalf("trials/passes/rate = %d/%d/%f, want 2/1/0.5", row.Trials, row.Passes, row.PassRate)
	}
	if row.CostOfPassTotalTokens == nil || *row.CostOfPassTotalTokens != 195 {
		t.Fatalf("cost_of_pass_total_tokens = %v, want 195", row.CostOfPassTotalTokens)
	}
	if row.CostOfPassOutputTokens == nil || *row.CostOfPassOutputTokens != 45 {
		t.Fatalf("cost_of_pass_output_tokens = %v, want 45", row.CostOfPassOutputTokens)
	}
}

func TestBenchmarkCostOfPassIsNullWithoutPasses(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const suite = "cop-zero-suite"
	for i := 0; i < 2; i++ {
		if _, err := UpsertBenchmarkTrial(BenchmarkTrialRecord{
			TrialID:        "cop-zero-" + string(rune('a'+i)),
			SuiteID:        suite,
			TaskID:         "task",
			Paradigm:       "chat",
			RequestedModel: "m",
			TrialIndex:     i + 1,
			OracleMode:     "transcript",
		}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	cost, err := GetBenchmarkCostOfPass(suite)
	if err != nil {
		t.Fatalf("GetBenchmarkCostOfPass: %v", err)
	}
	if len(cost) != 1 {
		t.Fatalf("cost rows = %d, want 1", len(cost))
	}
	if cost[0].CostOfPassTotalTokens != nil {
		t.Fatalf("cost_of_pass = %v, want NULL when no trial passed", *cost[0].CostOfPassTotalTokens)
	}
}

// TestBenchmarkTrialSurvivesSessionDeletion covers the ON DELETE SET NULL
// contract: the pseudonymous session disappears, the aggregate verdict stays,
// and the trial is still purged on its own age.
func TestBenchmarkTrialSurvivesSessionDeletion(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const trial, conv = "survive-trial", "survive-conv"

	turn := baseAgentRecord("survive-session")
	turn.TrialID = trial
	turn.ConversationID = conv
	turn.UpstreamConvID = conv
	if err := LogAgentTurn(turn); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if _, err := UpsertBenchmarkTrial(BenchmarkTrialRecord{
		TrialID:        trial,
		SuiteID:        "survive-suite",
		TaskID:         "survive-task",
		Paradigm:       "chat",
		RequestedModel: "m",
		TrialIndex:     1,
		OraclePassed:   true,
		OracleMode:     "transcript",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var sessionID sql.NullString
	if err := DB.QueryRow(`SELECT session_id FROM benchmark_trials WHERE trial_id = ?`, trial).Scan(&sessionID); err != nil {
		t.Fatalf("read session_id: %v", err)
	}
	if !sessionID.Valid || sessionID.String == "" {
		t.Fatal("session_id was not correlated from the trial turns")
	}

	if _, err := DeleteConversation(conv); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if err := DB.QueryRow(`SELECT session_id FROM benchmark_trials WHERE trial_id = ?`, trial).Scan(&sessionID); err != nil {
		t.Fatalf("re-read session_id: %v", err)
	}
	if sessionID.Valid {
		t.Fatalf("session_id = %q after deletion, want NULL", sessionID.String)
	}

	var rows int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM benchmark_trials WHERE trial_id = ?`, trial).Scan(&rows); err != nil {
		t.Fatalf("count trial: %v", err)
	}
	if rows != 1 {
		t.Fatalf("trial rows = %d after session deletion, want 1", rows)
	}

	if _, err := DB.Exec(`UPDATE benchmark_trials SET created_at = datetime('now', '-45 days') WHERE trial_id = ?`, trial); err != nil {
		t.Fatalf("age trial: %v", err)
	}
	if _, err := PurgeExpiredData(30); err != nil {
		t.Fatalf("PurgeExpiredData: %v", err)
	}
	if err := DB.QueryRow(`SELECT COUNT(*) FROM benchmark_trials WHERE trial_id = ?`, trial).Scan(&rows); err != nil {
		t.Fatalf("count after purge: %v", err)
	}
	if rows != 0 {
		t.Fatalf("trial rows = %d after retention, want 0", rows)
	}
}

func TestBenchmarkTrialRejectsUnknownFailureCode(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	_, err := UpsertBenchmarkTrial(BenchmarkTrialRecord{
		TrialID:            "bad-code-trial",
		SuiteID:            "s",
		TaskID:             "t",
		Paradigm:           "chat",
		RequestedModel:     "m",
		TrialIndex:         1,
		PrimaryFailureCode: "E_BOGUS",
	})
	if err == nil {
		t.Fatal("an unknown primary_failure_code must be rejected by the CHECK constraint")
	}
}

func TestFailureCodeFromCorrelation(t *testing.T) {
	cases := []struct {
		name string
		corr SessionCorrelation
		want string
	}{
		{"stall wins", SessionCorrelation{StreamStalled: true, TurnClass: "refusal"}, "E_STALL_OR_TIMEOUT"},
		{"transport", SessionCorrelation{TransportRetries: 2}, "E_STALL_OR_TIMEOUT"},
		{"refusal", SessionCorrelation{TurnClass: "refusal"}, "E_UPSTREAM_REFUSAL"},
		{"schema", SessionCorrelation{TurnClass: "schema_invalid"}, "E_SCHEMA_INVALID"},
		{"untagged", SessionCorrelation{TurnClass: "untagged_mutation"}, "E_UNTAGGED_MUTATION"},
		{"unbacked", SessionCorrelation{TurnClass: "unbacked_completion"}, "E_UNBACKED_COMPLETION"},
		{"greeting", SessionCorrelation{TurnClass: "greeting"}, "E_GREETING_LOOP"},
		{"unclosed", SessionCorrelation{ThoughtUnclosed: true}, "E_UNCLOSED_THOUGHT"},
		{"doom halt", SessionCorrelation{DoomLoopAction: "halt", TurnClass: "refusal"}, "E_DOOM_LOOP_HALT"},
		{"clean", SessionCorrelation{TurnClass: "ok"}, "none"},
	}
	for _, tc := range cases {
		if got := failureCodeFromCorrelation(tc.corr); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestBenchmarkFailureCodeRefinement covers the precedence between the shell
// oracle and the proxy evidence: a coarse symptom code is replaced by the
// specific turn signal, while a transcript-proven code is preserved.
func TestBenchmarkFailureCodeRefinement(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	upsert := func(trialID, code string, passed bool) BenchmarkTrialRecord {
		rec, err := UpsertBenchmarkTrial(BenchmarkTrialRecord{
			TrialID:            trialID,
			SuiteID:            "refine-suite",
			TaskID:             "refine-task",
			Paradigm:           "chat",
			RequestedModel:     "m",
			TrialIndex:         1,
			OraclePassed:       passed,
			PrimaryFailureCode: code,
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", trialID, err)
		}
		return rec
	}

	stalled := baseAgentRecord("refine-session-stall")
	stalled.TrialID = "refine-stall"
	stalled.StreamStalled = true
	if err := LogAgentTurn(stalled); err != nil {
		t.Fatalf("stall turn: %v", err)
	}
	if got := upsert("refine-stall", "E_ERROR_FINISH", false).PrimaryFailureCode; got != "E_STALL_OR_TIMEOUT" {
		t.Fatalf("coarse E_ERROR_FINISH refined to %q, want E_STALL_OR_TIMEOUT", got)
	}

	refusal := baseAgentRecord("refine-session-refusal")
	refusal.TrialID = "refine-refusal"
	refusal.TurnClass = "refusal"
	if err := LogAgentTurn(refusal); err != nil {
		t.Fatalf("refusal turn: %v", err)
	}
	if got := upsert("refine-refusal", "E_ORACLE_ASSERTION_FAIL", false).PrimaryFailureCode; got != "E_UPSTREAM_REFUSAL" {
		t.Fatalf("coarse E_ORACLE_ASSERTION_FAIL refined to %q, want E_UPSTREAM_REFUSAL", got)
	}

	// A transcript-proven failure must not be overwritten by a less precise
	// proxy signal.
	if got := upsert("refine-refusal", "E_FALSE_QUOTA", false).PrimaryFailureCode; got != "E_FALSE_QUOTA" {
		t.Fatalf("specific E_FALSE_QUOTA became %q", got)
	}

	halted := baseAgentRecord("refine-session-halt")
	halted.TrialID = "refine-halt"
	halted.DoomLoopAction = "halt"
	if err := LogAgentTurn(halted); err != nil {
		t.Fatalf("halt turn: %v", err)
	}
	if got := upsert("refine-halt", "E_ORACLE_ASSERTION_FAIL", false).PrimaryFailureCode; got != "E_DOOM_LOOP_HALT" {
		t.Fatalf("halt refined to %q, want E_DOOM_LOOP_HALT", got)
	}

	// A passing trial keeps its 'none'.
	if got := upsert("refine-stall", "none", true).PrimaryFailureCode; got != "none" {
		t.Fatalf("passing trial code = %q, want none", got)
	}
}
