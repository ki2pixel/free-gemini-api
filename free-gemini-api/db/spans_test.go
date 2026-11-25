package db

import (
	"database/sql"
	"testing"
)

func baseSpanRecord(spanID, callID, name string) ToolCallSpanRecord {
	return ToolCallSpanRecord{
		SpanID:            spanID,
		CallIndex:         0,
		ToolCallID:        callID,
		ToolName:          name,
		ExtractionChannel: "tagged_xml",
		Disposition:       "emitted",
		ArgsFingerprint:   "fingerprint-" + callID,
		ArgsBytes:         42,
		TargetHint:        "internal/api/handler.go",
		EmittedAtMs:       1000,
	}
}

// TestToolSpanRoundtrip covers the open span of turn N being closed by the tool
// result observed on turn N+1, including the generated roundtrip column.
func TestToolSpanRoundtrip(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const session = "span-session"

	first := baseAgentRecord(session)
	spanA := baseSpanRecord("span-a", "call_1", "read")
	spanB := baseSpanRecord("span-b", "call_2", "grep")
	spanB.CallIndex = 1
	first.Spans = []ToolCallSpanRecord{spanA, spanB}
	if err := LogAgentTurn(first); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	var open int
	if err := DB.QueryRow(
		`SELECT COUNT(*) FROM tool_call_spans WHERE session_id = ? AND result_received = 0`, session,
	).Scan(&open); err != nil {
		t.Fatalf("count open: %v", err)
	}
	if open != 2 {
		t.Fatalf("open spans = %d, want 2", open)
	}

	second := baseAgentRecord(session)
	second.RequestStartedMs = 3000
	second.CompletedMs = 4000
	second.ToolOutcomes = []ToolCallOutcomeRecord{
		{ToolCallID: "call_1", IsError: false, ResultBytes: 120, ReceivedAtMs: 3500},
		{ToolCallID: "call_2", IsError: true, ResultBytes: 60, ReceivedAtMs: 900},
	}
	if err := LogAgentTurn(second); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	var received, isError, resultBytes int
	var receivedMs sql.NullInt64
	var roundtrip sql.NullInt64
	if err := DB.QueryRow(
		`SELECT result_received, result_is_error, result_bytes, result_received_ms, roundtrip_ms
		 FROM tool_call_spans WHERE span_id = 'span-a'`,
	).Scan(&received, &isError, &resultBytes, &receivedMs, &roundtrip); err != nil {
		t.Fatalf("read span-a: %v", err)
	}
	if received != 1 || isError != 0 || resultBytes != 120 {
		t.Fatalf("span-a = received:%d error:%d bytes:%d, want 1/0/120", received, isError, resultBytes)
	}
	if !roundtrip.Valid || roundtrip.Int64 != 2500 {
		t.Fatalf("roundtrip_ms = %v, want 2500", roundtrip)
	}

	// An observation clock behind the emission is clamped, never stored negative.
	if err := DB.QueryRow(
		`SELECT result_received_ms, roundtrip_ms FROM tool_call_spans WHERE span_id = 'span-b'`,
	).Scan(&receivedMs, &roundtrip); err != nil {
		t.Fatalf("read span-b: %v", err)
	}
	if !receivedMs.Valid || receivedMs.Int64 != 1000 {
		t.Fatalf("clamped result_received_ms = %v, want 1000", receivedMs)
	}
	if !roundtrip.Valid || roundtrip.Int64 != 0 {
		t.Fatalf("clamped roundtrip_ms = %v, want 0", roundtrip)
	}

	// A second outcome for the same call must not rewrite a closed span.
	third := baseAgentRecord(session)
	third.ToolOutcomes = []ToolCallOutcomeRecord{
		{ToolCallID: "call_1", IsError: true, ResultBytes: 999, ReceivedAtMs: 9000},
	}
	if err := LogAgentTurn(third); err != nil {
		t.Fatalf("turn 3: %v", err)
	}
	if err := DB.QueryRow(
		`SELECT result_is_error, result_bytes FROM tool_call_spans WHERE span_id = 'span-a'`,
	).Scan(&isError, &resultBytes); err != nil {
		t.Fatalf("re-read span-a: %v", err)
	}
	if isError != 0 || resultBytes != 120 {
		t.Fatalf("span-a was rewritten to error:%d bytes:%d", isError, resultBytes)
	}
}

func TestTurnAnomaliesAndCascade(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const session = "anomaly-session"

	rec := baseAgentRecord(session)
	rec.Spans = []ToolCallSpanRecord{baseSpanRecord("span-anomaly", "call_x", "write")}
	rec.Anomalies = []TurnAnomalyRecord{
		{AttemptIndex: 1, Category: "arranger_repair", Code: "control_chars", Severity: "info"},
		{AttemptIndex: 2, Category: "arranger_rejection", Code: "schema_invalid", Severity: "warn"},
		{AttemptIndex: 2, Category: "doom_loop", Code: "repeat", Severity: "halt"},
	}
	if err := LogAgentTurn(rec); err != nil {
		t.Fatalf("LogAgentTurn: %v", err)
	}

	anomalies, err := ListTurnAnomalies(session, 10)
	if err != nil {
		t.Fatalf("ListTurnAnomalies: %v", err)
	}
	if len(anomalies) != 3 {
		t.Fatalf("anomalies = %d, want 3", len(anomalies))
	}

	summary, err := GetSessionTelemetrySummary(session)
	if err != nil {
		t.Fatalf("GetSessionTelemetrySummary: %v", err)
	}
	if summary.TotalAnomalies != 3 || summary.ArrangerRepairs != 1 || summary.ToolsEmitted != 1 {
		t.Fatalf("summary = anomalies:%d repairs:%d tools:%d, want 3/1/1",
			summary.TotalAnomalies, summary.ArrangerRepairs, summary.ToolsEmitted)
	}

	// An unknown code must be rejected by the CHECK, not silently stored.
	bad := baseAgentRecord("anomaly-bad")
	bad.Anomalies = []TurnAnomalyRecord{
		{AttemptIndex: 1, Category: "arranger_repair", Code: "not_a_code", Severity: "info"},
	}
	if err := LogAgentTurn(bad); err == nil {
		t.Fatal("an unknown anomaly code must be rejected")
	}

	if _, err := DB.Exec(`DELETE FROM agent_sessions WHERE session_id = ?`, session); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	var spans, remaining int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM tool_call_spans WHERE session_id = ?`, session).Scan(&spans)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM turn_anomalies WHERE session_id = ?`, session).Scan(&remaining)
	if spans != 0 || remaining != 0 {
		t.Fatalf("after cascade: spans=%d anomalies=%d, want 0/0", spans, remaining)
	}
}

// TestListToolSpansAndSessionSummaries covers the read path used by the
// telemetry route: full span rows, closed spans and the session list.
func TestListToolSpansAndSessionSummaries(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const session = "list-spans-session"

	rec := baseAgentRecord(session)
	open := baseSpanRecord("list-span-open", "call_open", "read")
	rejected := baseSpanRecord("list-span-rejected", "call_rejected", "write")
	rejected.CallIndex = 1
	rejected.IsMutating = true
	rejected.Disposition = "rejected_schema_invalid"
	rejected.ValidationError = "missing required property 'path'"
	rec.Spans = []ToolCallSpanRecord{open, rejected}
	rec.Anomalies = []TurnAnomalyRecord{
		{AttemptIndex: 1, Category: "quarantine", Code: "untagged_mutation", Severity: "warn"},
	}
	if err := LogAgentTurn(rec); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	closed := baseAgentRecord(session)
	closed.ToolOutcomes = []ToolCallOutcomeRecord{
		{ToolCallID: "call_open", IsError: false, ResultBytes: 88, ReceivedAtMs: 1500},
	}
	if err := LogAgentTurn(closed); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	spans, err := ListToolSpans(session, 10)
	if err != nil {
		t.Fatalf("ListToolSpans: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	byID := map[string]ToolCallSpanRecord{}
	for _, span := range spans {
		byID[span.SpanID] = span
	}
	closedSpan := byID["list-span-open"]
	if !closedSpan.ResultReceived || closedSpan.ResultIsError || closedSpan.ResultBytes != 88 {
		t.Fatalf("closed span = %+v, want received/ok/88 bytes", closedSpan)
	}
	if closedSpan.ResultReceivedMs == nil || *closedSpan.ResultReceivedMs != 1500 {
		t.Fatalf("result_received_ms = %v, want 1500", closedSpan.ResultReceivedMs)
	}
	if closedSpan.RoundtripMs == nil || *closedSpan.RoundtripMs != 500 {
		t.Fatalf("roundtrip_ms = %v, want 500", closedSpan.RoundtripMs)
	}
	rejectedSpan := byID["list-span-rejected"]
	if rejectedSpan.Disposition != "rejected_schema_invalid" || rejectedSpan.ValidationError == "" {
		t.Fatalf("rejected span = %+v, want its disposition and structural error", rejectedSpan)
	}
	if rejectedSpan.ResultReceivedMs != nil || rejectedSpan.RoundtripMs != nil {
		t.Fatalf("unclosed span reported a roundtrip: %+v", rejectedSpan)
	}
	if rejectedSpan.TargetHint != "internal/api/handler.go" {
		t.Fatalf("target_hint = %q", rejectedSpan.TargetHint)
	}

	summaries, err := ListSessionTelemetrySummaries(50)
	if err != nil {
		t.Fatalf("ListSessionTelemetrySummaries: %v", err)
	}
	found := false
	for _, summary := range summaries {
		if summary.SessionID == session {
			found = true
			if summary.TotalTurns != 2 {
				t.Fatalf("total_turns = %d, want 2", summary.TotalTurns)
			}
			if summary.ToolsEmitted != 1 || summary.ToolsBlockedOrThrottled != 1 {
				t.Fatalf("tools emitted/blocked = %d/%d, want 1/1",
					summary.ToolsEmitted, summary.ToolsBlockedOrThrottled)
			}
		}
	}
	if !found {
		t.Fatalf("session %q missing from the summary list", session)
	}
}

func TestSessionTelemetrySummaryUnknownSession(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	summary, err := GetSessionTelemetrySummary("does-not-exist")
	if err != nil {
		t.Fatalf("GetSessionTelemetrySummary: %v", err)
	}
	if summary.SessionID != "" {
		t.Fatalf("unknown session returned %q", summary.SessionID)
	}
	if _, err := ListToolSpans("does-not-exist", 0); err != nil {
		t.Fatalf("ListToolSpans empty: %v", err)
	}
	if _, err := ListTurnAnomalies("does-not-exist", 5000); err != nil {
		t.Fatalf("ListTurnAnomalies empty: %v", err)
	}
}
func TestSessionSummaryViewExposesToolColumns(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const session = "view-tools-session"

	rec := baseAgentRecord(session)
	okSpan := baseSpanRecord("view-span-ok", "call_ok", "read")
	badSpan := baseSpanRecord("view-span-rejected", "call_bad", "write")
	badSpan.CallIndex = 1
	badSpan.Disposition = "rejected_schema_invalid"
	badSpan.ValidationError = "missing required property 'path'"
	rec.Spans = []ToolCallSpanRecord{okSpan, badSpan}
	rec.Anomalies = []TurnAnomalyRecord{
		{AttemptIndex: 1, Category: "arranger_repair", Code: "brace_envelope", Severity: "info"},
	}
	if err := LogAgentTurn(rec); err != nil {
		t.Fatalf("LogAgentTurn: %v", err)
	}

	summary, err := GetSessionTelemetrySummary(session)
	if err != nil {
		t.Fatalf("GetSessionTelemetrySummary: %v", err)
	}
	if summary.TotalTurns != 1 {
		t.Fatalf("total_turns = %d, want 1 (pre-existing column preserved)", summary.TotalTurns)
	}
	if summary.SumThoughtTokens != rec.ThoughtTokens {
		t.Fatalf("sum_thought_tokens = %d, want %d", summary.SumThoughtTokens, rec.ThoughtTokens)
	}
	if summary.ToolsEmitted != 1 || summary.ToolsBlockedOrThrottled != 1 {
		t.Fatalf("tools emitted/blocked = %d/%d, want 1/1",
			summary.ToolsEmitted, summary.ToolsBlockedOrThrottled)
	}
	if summary.ArrangerRepairs != 1 {
		t.Fatalf("arranger_repairs = %d, want 1", summary.ArrangerRepairs)
	}
}
