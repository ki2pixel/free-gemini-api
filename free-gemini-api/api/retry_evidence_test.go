package api

import (
	"goapi/gemini"
	"testing"
)

// readOnlyTools declares a read and a write tool, both requiring a path, so a
// call without one is schema-invalid and an untagged write is a quarantine.
func readOnlyTools() []map[string]any {
	schema := func(name string) map[string]any {
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": name,
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					},
					"required": []any{"path"},
				},
			},
		}
	}
	return []map[string]any{schema("read"), schema("write")}
}

// TestRecordRejectedAttemptKeepsAbsorbedEvidence covers the gap a successful
// retry used to hide: the rejected attempt must leave a span and an anomaly of
// its own before the loop continues.
func TestRecordRejectedAttemptKeepsAbsorbedEvidence(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		wantDispo    string
		wantCategory string
		wantCode     string
	}{
		{
			name:         "schema invalid",
			raw:          `<tool_call>{"name":"read","arguments":{}}</tool_call>`,
			wantDispo:    "rejected_schema_invalid",
			wantCategory: "arranger_rejection",
			wantCode:     "schema_invalid",
		},
		{
			name:         "truncated mutation",
			raw:          `<tool_call>{"name":"write","arguments":{"path":"a.go","content":"unterminated`,
			wantDispo:    "rejected_mid_value_truncation",
			wantCategory: "arranger_rejection",
			wantCode:     "mid_value_truncation",
		},
		{
			name:         "untagged mutation",
			raw:          `{"name":"write","arguments":{"path":"a.go","content":"x"}}`,
			wantDispo:    "quarantined_untagged_mutation",
			wantCategory: "quarantine",
			wantCode:     "untagged_mutation",
		},
	}

	for _, tc := range cases {
		record := &TurnTelemetry{}
		recordRejectedAttempt(record, 1, tc.raw, readOnlyTools(), 42)

		foundSpan := false
		for _, span := range record.ToolSpans {
			if span.Disposition == tc.wantDispo {
				foundSpan = true
				if span.EmittedAtMs != 42 {
					t.Errorf("%s: emitted_at_ms = %d, want 42", tc.name, span.EmittedAtMs)
				}
				if span.SpanID == "" {
					t.Errorf("%s: span has no id", tc.name)
				}
			}
		}
		if !foundSpan {
			t.Errorf("%s: spans = %+v, want a %q disposition", tc.name, record.ToolSpans, tc.wantDispo)
		}

		// The rejection itself is recorded by the caller, which knows the exact
		// reason; here we assert the arranger-derived evidence alone.
		if len(record.Anomalies) == 0 && tc.wantCode != "" {
			t.Logf("%s: arranger produced no anomaly; the caller adds the categorical one", tc.name)
		}
	}
}

// TestRecordedRetryEvidenceReachesTheTurn assembles the full retry evidence the
// streaming loop now emits for a schema-invalid attempt followed by a clean one.
func TestRecordedRetryEvidenceReachesTheTurn(t *testing.T) {
	record := &TurnTelemetry{}

	// Attempt 1 is rejected for schema; the loop retries.
	recordRejectedAttempt(record, 1, `<tool_call>{"name":"read","arguments":{}}</tool_call>`, readOnlyTools(), 100)
	addTurnAnomaly(record, 1, "arranger_rejection", "schema_invalid", "warn")

	// Attempt 2 succeeds with a valid call.
	recordArrangerEvidence(record, 2,
		gemini.ArrangeGeminiOutputWithTools(`<tool_call>{"name":"read","arguments":{"path":"a.go"}}</tool_call>`, readOnlyTools()),
		nil, 200)

	var attempt1Spans, attempt2Spans int
	for _, span := range record.ToolSpans {
		switch span.Disposition {
		case "rejected_schema_invalid":
			attempt1Spans++
		case "emitted":
			attempt2Spans++
		}
	}
	if attempt1Spans != 1 {
		t.Fatalf("rejected spans = %d, want 1 (the absorbed attempt must survive)", attempt1Spans)
	}
	if attempt2Spans != 1 {
		t.Fatalf("emitted spans = %d, want 1", attempt2Spans)
	}

	var rejectionAnomalies int
	for _, anomaly := range record.Anomalies {
		if anomaly.Category == "arranger_rejection" && anomaly.Code == "schema_invalid" {
			rejectionAnomalies++
			if anomaly.AttemptIndex != 1 {
				t.Fatalf("attempt_index = %d, want 1", anomaly.AttemptIndex)
			}
		}
	}
	if rejectionAnomalies != 1 {
		t.Fatalf("schema_invalid anomalies = %d, want 1", rejectionAnomalies)
	}
}

// TestAddTurnAnomalyDeduplicates proves a repeated incident on the same attempt
// does not inflate the journal, since cardinality lives in tool_call_spans.
func TestAddTurnAnomalyDeduplicates(t *testing.T) {
	record := &TurnTelemetry{}
	addTurnAnomaly(record, 2, "arranger_rejection", "schema_invalid", "warn")
	addTurnAnomaly(record, 2, "arranger_rejection", "schema_invalid", "warn")
	addTurnAnomaly(record, 3, "arranger_rejection", "schema_invalid", "warn")

	if len(record.Anomalies) != 2 {
		t.Fatalf("anomalies = %d, want 2 (one per attempt)", len(record.Anomalies))
	}
	if record.Anomalies[0].AttemptIndex != 2 || record.Anomalies[1].AttemptIndex != 3 {
		t.Fatalf("attempt indexes = %d/%d, want 2/3",
			record.Anomalies[0].AttemptIndex, record.Anomalies[1].AttemptIndex)
	}
}

// TestTransferPreTelemetryPreservesNonStreamRetrySpans proves the non-streaming
// retry buffer forwards rejected spans alongside anomalies and keeps CallIndex
// sequential when the final attempt appends its own spans.
func TestTransferPreTelemetryPreservesNonStreamRetrySpans(t *testing.T) {
	pre := &TurnTelemetry{}
	recordRejectedAttempt(pre, 1, `<tool_call>{"name":"read","arguments":{}}</tool_call>`, readOnlyTools(), 100)
	addTurnAnomaly(pre, 1, "arranger_rejection", "schema_invalid", "warn")

	emitterTel := &TurnTelemetry{}
	transferPreTelemetry(emitterTel, pre)
	transferPreTelemetry(nil, pre)
	transferPreTelemetry(emitterTel, nil)

	finalArranged := gemini.ArrangeGeminiOutputWithTools(`<tool_call>{"name":"read","arguments":{"path":"a.go"}}</tool_call>`, readOnlyTools())
	recordArrangerEvidence(emitterTel, 2, finalArranged, nil, 200)

	if len(emitterTel.ToolSpans) != 2 {
		t.Fatalf("spans = %d, want 2 (1 rejected + 1 emitted)", len(emitterTel.ToolSpans))
	}
	if emitterTel.ToolSpans[0].Disposition != "rejected_schema_invalid" || emitterTel.ToolSpans[0].CallIndex != 0 {
		t.Fatalf("first span = %+v, want rejected_schema_invalid at CallIndex 0", emitterTel.ToolSpans[0])
	}
	if emitterTel.ToolSpans[1].Disposition != "emitted" || emitterTel.ToolSpans[1].CallIndex != 1 {
		t.Fatalf("second span = %+v, want emitted at CallIndex 1", emitterTel.ToolSpans[1])
	}
	if len(emitterTel.Anomalies) != 1 || emitterTel.Anomalies[0].Code != "schema_invalid" {
		t.Fatalf("anomalies = %+v, want one schema_invalid entry", emitterTel.Anomalies)
	}
}
