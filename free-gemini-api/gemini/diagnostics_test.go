package gemini

import (
	"strings"
	"testing"
)

// TestToolDiagnosticsChannelsAndDispositions proves every channel and
// disposition has a producer, and that the diagnostics never alter the
// functional output.
func TestToolDiagnosticsChannelsAndDispositions(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name": "read",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []any{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name": "write",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []any{"path"},
				},
			},
		},
	}

	cases := []struct {
		name        string
		raw         string
		wantChannel string
		wantDispo   string
	}{
		{
			name:        "tagged xml",
			raw:         `<tool_call>{"name":"read","arguments":{"path":"a.go"}}</tool_call>`,
			wantChannel: "tagged_xml",
			wantDispo:   "emitted",
		},
		{
			name:        "tagged schema invalid",
			raw:         `<tool_call>{"name":"read","arguments":{}}</tool_call>`,
			wantChannel: "tagged_xml",
			wantDispo:   "rejected_schema_invalid",
		},
		{
			name:        "tagged undeclared",
			raw:         `<tool_call>{"name":"delete_everything","arguments":{"path":"a.go"}}</tool_call>`,
			wantChannel: "tagged_xml",
			wantDispo:   "rejected_undeclared_tool",
		},
		{
			name:        "untagged readonly promoted",
			raw:         `{"name":"read","arguments":{"path":"a.go"}}`,
			wantChannel: "untagged_readonly_promoted",
			wantDispo:   "emitted",
		},
		{
			name:        "untagged mutation quarantined",
			raw:         `{"name":"write","arguments":{"path":"a.go"}}`,
			wantChannel: "untagged_quarantined",
			wantDispo:   "quarantined_untagged_mutation",
		},
		{
			name:        "untagged undeclared",
			raw:         `{"name":"rm_rf","arguments":{"path":"/"}}`,
			wantChannel: "untagged_quarantined",
			wantDispo:   "rejected_undeclared_tool",
		},
	}

	for _, tc := range cases {
		arranged := ArrangeGeminiOutputWithTools(tc.raw, tools)
		if len(arranged.ToolDiagnostics) == 0 {
			t.Errorf("%s: no diagnostic recorded", tc.name)
			continue
		}
		found := false
		for _, diagnostic := range arranged.ToolDiagnostics {
			if diagnostic.ExtractionChannel == tc.wantChannel && diagnostic.Disposition == tc.wantDispo {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: diagnostics = %+v, want channel %q disposition %q",
				tc.name, arranged.ToolDiagnostics, tc.wantChannel, tc.wantDispo)
		}
	}
}

// TestToolDiagnosticsDoNotChangeOutput is the invariance guard: the presence of
// diagnostics must not alter what the arranger returns.
func TestToolDiagnosticsDoNotChangeOutput(t *testing.T) {
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "read",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []any{"path"},
			},
		},
	}}

	raw := "Je vérifie.\n<tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"a.go\"}}</tool_call>"
	arranged := ArrangeGeminiOutputWithTools(raw, tools)

	if len(arranged.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(arranged.ToolCalls))
	}
	if arranged.FinishReason != "tool_calls" || !arranged.IsToolCall {
		t.Fatalf("finish/isToolCall = %q/%t, want tool_calls/true", arranged.FinishReason, arranged.IsToolCall)
	}
	if strings.Contains(arranged.CleanText, "tool_call") {
		t.Fatalf("clean text leaked the call: %q", arranged.CleanText)
	}
	if arranged.UntaggedMutation {
		t.Fatal("a single valid tagged call must not be flagged as an untagged mutation")
	}
}

func TestStructuralValidationDetailNeverLeaksValues(t *testing.T) {
	cases := []struct {
		detail string
		want   string
	}{
		{"missing required property 'path'", "missing required property 'path'"},
		{"type mismatch for 'path': got number, want string", "type mismatch for 'path': got number, want string"},
		{"invalid value for 'mode': value must be one of \"a\", \"b\"", "invalid value for 'mode'"},
		{"invalid JSON arguments: unexpected end of JSON input", "invalid JSON arguments"},
		{"tool 'rm_rf' is not declared in the request", "undeclared tool"},
		{"", ""},
		{`some entirely new message containing a secret value s3cr3t`, "schema validation failed"},
	}
	for _, tc := range cases {
		if got := StructuralValidationDetail(tc.detail); got != tc.want {
			t.Errorf("StructuralValidationDetail(%q) = %q, want %q", tc.detail, got, tc.want)
		}
	}
	if got := StructuralValidationDetail(`unexpected text with s3cr3t value`); strings.Contains(got, "s3cr3t") {
		t.Fatalf("detail leaked a value: %q", got)
	}
}

func TestThrottleMutatingToolCallsWithDropped(t *testing.T) {
	calls := []ToolCall{
		{ID: "a", Function: ToolCallFunction{Name: "read", Arguments: "{}"}},
		{ID: "b", Function: ToolCallFunction{Name: "write", Arguments: "{}"}},
		{ID: "c", Function: ToolCallFunction{Name: "read", Arguments: "{}"}},
	}
	kept, dropped := ThrottleMutatingToolCallsWithDropped(calls)
	if len(kept) != 2 || len(dropped) != 1 {
		t.Fatalf("kept/dropped = %d/%d, want 2/1", len(kept), len(dropped))
	}
	if dropped[0].ID != "c" {
		t.Fatalf("dropped call = %q, want c", dropped[0].ID)
	}
	if legacy := ThrottleMutatingToolCalls(calls); len(legacy) != len(kept) {
		t.Fatalf("wrapper kept %d calls, want %d", len(legacy), len(kept))
	}
}
