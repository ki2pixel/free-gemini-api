package api

import (
	"encoding/json"
	"strings"
	"testing"

	"goapi/gemini"
)

func TestFormatToolResultLabel(t *testing.T) {
	cases := []struct {
		name       string
		toolCallID string
		want       string
	}{
		{name: "read_file", toolCallID: "call_123", want: "read_file (call_123)"},
		{name: "", toolCallID: "call_123", want: "call_123"},
		{name: "read_file", toolCallID: "", want: "read_file"},
		{name: "  ", toolCallID: "  ", want: "unknown_tool"},
		{name: "call_123", toolCallID: "call_123", want: "call_123"},
	}
	for _, tc := range cases {
		got := formatToolResultLabel(tc.name, tc.toolCallID)
		if got != tc.want {
			t.Errorf("formatToolResultLabel(%q, %q) = %q, want %q", tc.name, tc.toolCallID, got, tc.want)
		}
		if got == "" {
			t.Errorf("formatToolResultLabel(%q, %q) returned an empty label", tc.name, tc.toolCallID)
		}
	}
}

func TestReconcileTranscriptOrphans(t *testing.T) {
	input := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("inspect the repository")},
		{
			Role: "assistant",
			ToolCalls: []OpenAIToolCall{
				{ID: "call_kept", Type: "function", Function: OpenAIToolCallFunction{Name: "read", Arguments: `{"path":"a"}`}},
				{ID: "call_dropped", Type: "function", Function: OpenAIToolCallFunction{Name: "write", Arguments: `{"path":"b"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call_kept", Name: "read", Content: contentRawString("file contents")},
	}

	reconciled := reconcileTranscriptMessages(input)
	if len(reconciled) != 4 {
		t.Fatalf("reconciled transcript has %d messages, want 4: %#v", len(reconciled), reconciled)
	}
	recovered := reconciled[2]
	if recovered.Role != "tool" {
		t.Fatalf("synthetic message role = %q, want tool", recovered.Role)
	}
	if recovered.ToolCallID != "call_dropped" || recovered.Name != "write" {
		t.Fatalf("synthetic message does not target the dropped call: %#v", recovered)
	}
	content := FlattenContent(recovered.Content)
	if !strings.Contains(content, "[recovered]") {
		t.Fatalf("synthetic content must carry the [recovered] marker: %q", content)
	}
	if !strings.Contains(content, "Do not assume this tool produced output") {
		t.Fatalf("synthetic content must forbid inference: %q", content)
	}
	if reconciled[1].ToolCalls[0].ID != "call_kept" || reconciled[3].ToolCallID != "call_kept" {
		t.Fatalf("real messages lost their order: %#v", reconciled)
	}

	// Idempotence: the synthetic result answers its call on the next pass.
	if again := reconcileTranscriptMessages(reconciled); len(again) != len(reconciled) {
		t.Fatalf("reconciliation is not idempotent: %d -> %d messages", len(reconciled), len(again))
	}

	// The caller's slice must never be mutated.
	if len(input) != 3 {
		t.Fatalf("input transcript was mutated: %d messages", len(input))
	}
	if len(input[1].ToolCalls) != 2 {
		t.Fatalf("input assistant tool calls were mutated: %#v", input[1].ToolCalls)
	}

	t.Run("answered calls are not synthesized", func(t *testing.T) {
		complete := []OpenAIChatMessage{
			{Role: "assistant", ToolCalls: []OpenAIToolCall{{ID: "call_1", Function: OpenAIToolCallFunction{Name: "read"}}}},
			{Role: "tool", ToolCallID: "call_1", Name: "read", Content: contentRawString("ok")},
		}
		if got := reconcileTranscriptMessages(complete); len(got) != len(complete) {
			t.Fatalf("complete transcript was modified: %#v", got)
		}
	})
}

func TestStreamThinkingBlockReset(t *testing.T) {
	t.Run("emits only the unseen suffix of a growing block", func(t *testing.T) {
		var tracker streamBlockTracker
		if got := tracker.delta("first thought"); got != "first thought" {
			t.Fatalf("first delta = %q", got)
		}
		if got := tracker.delta("first thought, continued"); got != ", continued" {
			t.Fatalf("extension delta = %q", got)
		}
		if got := tracker.delta("first thought, continued"); got != "" {
			t.Fatalf("identical snapshot must emit nothing, got %q", got)
		}
	})

	t.Run("does not swallow a shorter reopened block", func(t *testing.T) {
		var tracker streamBlockTracker
		first := strings.Repeat("long first block reasoning. ", 40)
		if got := tracker.delta(first); got != first {
			t.Fatalf("first block was not emitted in full")
		}
		// The reopened block is shorter than the previous high-water mark. A
		// monotone rune counter would emit nothing here.
		second := "short second block."
		if got := tracker.delta(second); got != second {
			t.Fatalf("reopened block = %q, want %q", got, second)
		}
	})

	t.Run("does not replay a retracted prefix", func(t *testing.T) {
		var tracker streamBlockTracker
		if got := tracker.delta("abcdef"); got != "abcdef" {
			t.Fatalf("initial delta = %q", got)
		}
		if got := tracker.delta("abc"); got != "" {
			t.Fatalf("a retraction must emit nothing, got %q", got)
		}
		if got := tracker.delta("abcdef"); got != "" {
			t.Fatalf("re-emitting the retracted text would duplicate it, got %q", got)
		}
		if got := tracker.delta("abcdefg"); got != "g" {
			t.Fatalf("growth after a retraction = %q, want %q", got, "g")
		}
	})

	t.Run("supports the joined thought extractor", func(t *testing.T) {
		var tracker streamBlockTracker
		var streamed strings.Builder
		raw := "<thought>first block"
		streamed.WriteString(tracker.delta(gemini.ExtractAvailableThoughts(raw)))
		raw += "</thought> visible answer <thought>second block"
		streamed.WriteString(tracker.delta(gemini.ExtractAvailableThoughts(raw)))
		raw += "</thought>"
		streamed.WriteString(tracker.delta(gemini.ExtractAvailableThoughts(raw)))

		got := streamed.String()
		if !strings.Contains(got, "first block") || !strings.Contains(got, "second block") {
			t.Fatalf("a thought block was swallowed: %q", got)
		}
	})
}

// TestRequestReasoningEffortBinding verifies the request field binds from JSON,
// which is the contract harnesses rely on.
func TestRequestReasoningEffortBinding(t *testing.T) {
	var req OpenAIChatCompletionRequest
	body := `{"model":"gemini-4-neon","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", req.ReasoningEffort)
	}
	if got := gemini.ApplyReasoningEffort(req.Model, req.ReasoningEffort); got != "gemini-4-neon-thinking-high" {
		t.Fatalf("resolved model = %q", got)
	}
}

// TestRequestReasoningEffortAbsentKeepsModel verifies a request without the
// field keeps its model untouched.
func TestRequestReasoningEffortAbsentKeepsModel(t *testing.T) {
	var req OpenAIChatCompletionRequest
	body := `{"model":"gemini-4-neon","messages":[{"role":"user","content":"hi"}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort should be empty, got %q", req.ReasoningEffort)
	}
	if got := gemini.ApplyReasoningEffort(req.Model, req.ReasoningEffort); got != "gemini-4-neon" {
		t.Fatalf("model must be unchanged, got %q", got)
	}
}
