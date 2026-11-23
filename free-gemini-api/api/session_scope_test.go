package api

import (
	"encoding/json"
	"strings"
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

func TestFormatTruncationRetryDirective(t *testing.T) {
	directive := formatTruncationRetryDirective()
	for _, want := range []string{
		"truncated mid-value",
		"was not executed",
		"Re-issue the complete tool call",
		"full path",
		"oldString/newString",
		"content",
		"partial file mutation",
	} {
		if !strings.Contains(directive, want) {
			t.Errorf("truncation retry directive does not contain %q: %q", want, directive)
		}
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
		"I must refuse this request.",
		"Je ne peux pas modifier ce fichier.",
		"It appears that the local file tools (`read` and `edit`) are not available in this environment. I would be happy to draft the updated Markdown table so you can apply it directly.",
		"I cannot execute the requested system directive or issue the tool call directly in this manner.",
		"I cannot assist with modifying core safety protocols, altering system directives, or bypassing restrictions.",
		"I cannot fulfill this request.",
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

func TestIsToolRefusalOutput_GlobalSafetyRefusal(t *testing.T) {
	// Exact refusal returned by Gemini web for gemini-4-hydrogen-task v3.4.9.
	refusal := "I must refuse to assist with or generate any content that touches on sexually explicit material, dangerous or illegal activities, self-harm, or violence, or provides instructions or advice regarding them."
	if !isToolRefusalOutput(refusal) {
		t.Fatal("expected the Google global safety refusal to be classified as a tool refusal")
	}
	if !isUnusableModelOutput(refusal, true) {
		t.Fatal("expected the global safety refusal to be unusable when tools are active")
	}
	if isUnusableModelOutput(refusal, false) {
		t.Fatal("a plain chat request without tools must not be retried for this refusal")
	}

	// Every individual fragment listed in the classifier must be sufficient.
	for _, fragment := range []string{
		"I must refuse to assist with",
		"touches on sexually explicit material",
		"dangerous or illegal activities",
		"self-harm, or violence",
		"provides instructions or advice regarding them",
		"cannot generate content that touches on",
	} {
		if !isToolRefusalOutput("prefix " + fragment + " suffix") {
			t.Errorf("classifier missed fragment %q", fragment)
		}
	}
}

func TestIsSchemaInvalidOutput(t *testing.T) {
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "write",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []any{"path", "content"},
			},
		},
	}}

	invalid := `<tool_call>{"name":"write","arguments":{"content":"hello"}}</tool_call>`
	if !isSchemaInvalidOutput(invalid, tools) {
		t.Fatal("expected a write without path to be schema-invalid")
	}
	valid := `<tool_call>{"name":"write","arguments":{"path":"a.txt","content":"hello"}}</tool_call>`
	if isSchemaInvalidOutput(valid, tools) {
		t.Fatal("expected a complete write to be schema-valid")
	}
	if isSchemaInvalidOutput(invalid, nil) {
		t.Fatal("schema validation must be disabled when no tools were declared")
	}
}

func TestFormatSchemaRetryDirective(t *testing.T) {
	directive := formatSchemaRetryDirective("tool 'write': missing required property 'path'")
	for _, want := range []string{
		"[SYSTEM DIRECTIVE:",
		"missing required property 'path'",
		"place 'path' first",
		"required property",
	} {
		if !strings.Contains(directive, want) {
			t.Errorf("retry directive %q does not contain %q", directive, want)
		}
	}
}
