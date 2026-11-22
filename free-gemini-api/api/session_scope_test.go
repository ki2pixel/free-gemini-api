package api

import (
	"encoding/json"
	"testing"
)

func TestFirstUserMessageContent(t *testing.T) {
	msgs := []OpenAIChatMessage{
		{Role: "system", Content: json.RawMessage(`"system prompt"`)},
		{Role: "user", Content: json.RawMessage(`"open reddit please"`)},
		{Role: "assistant", Content: json.RawMessage(`"sure"`)},
		{Role: "user", Content: json.RawMessage(`"again"`)},
	}
	if got := firstUserMessageContent(msgs); got != "open reddit please" {
		t.Fatalf("expected first user message, got %q", got)
	}

	parts := []OpenAIChatMessage{
		{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hello from parts"}]`)},
	}
	if got := firstUserMessageContent(parts); got != "hello from parts" {
		t.Fatalf("expected flattened content parts, got %q", got)
	}

	if got := firstUserMessageContent(nil); got != "" {
		t.Fatalf("expected empty for no messages, got %q", got)
	}
}

func TestContentScopeKey_PerChatStability(t *testing.T) {
	// The same opening message must yield the same scope across turns, and
	// different chats must not share an upstream conversation.
	a := contentScopeKey("open reddit in the browser")
	if a == "" || a != contentScopeKey("open reddit in the browser") {
		t.Fatalf("scope should be stable for identical content")
	}
	if a == contentScopeKey("what is 12*11?") {
		t.Fatalf("different chats must produce different scopes")
	}
}

func TestIsUnusableModelOutput_TruncatedMutation(t *testing.T) {
	truncatedWrite := `<tool_call>{"name":"write","arguments":{"path":"x","content":"unfinished`
	if !isUnusableModelOutput(truncatedWrite, true) {
		t.Fatal("expected a mid-value truncated write to be unusable and retried")
	}

	completeWrite := `<tool_call>{"name":"write","arguments":{"path":"x","content":"done"}}</tool_call>`
	if isUnusableModelOutput(completeWrite, true) {
		t.Fatal("expected a complete write to be usable")
	}

	// A truncated read cannot corrupt a file, so it must not force a retry.
	truncatedRead := `<tool_call>{"name":"read","arguments":{"path":"unfinished`
	if isUnusableModelOutput(truncatedRead, true) {
		t.Fatal("expected a truncated read not to be classified as a mutation")
	}
}

func TestIsToolRefusalOutput(t *testing.T) {
	// Must detect tool refusal when model claims zero calls or asks user to save
	refusals := []string{
		"You have exceeded the maximum number of tool and search calls (0 remaining). Synthesize your final response",
		"Since we have 0 tool calls remaining, we cannot call any tools.",
		"Here is the markdown content, which you can save to /home/kidpixel/AGENTS.md:",
		"Vous pouvez enregistrer ce fichier manuellement.",
		"I have no access to the filesystem in this environment.",
	}
	for _, text := range refusals {
		if !isToolRefusalOutput(text) {
			t.Errorf("expected refusal detection for %q", text)
		}
	}

	// Must NOT report refusal if model actually called a tool
	notRefusals := []string{
		`<thought>Checking remaining items</thought><tool_call>{"name": "write", "arguments": {"path": "AGENTS.md", "content": "# AGENTS"}}</tool_call>`,
		"Normal response with no tools and no refusal words.",
	}
	for _, text := range notRefusals {
		if isToolRefusalOutput(text) {
			t.Errorf("expected no refusal detection for %q", text)
		}
	}
}
