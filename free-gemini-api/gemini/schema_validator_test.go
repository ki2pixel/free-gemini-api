package gemini

import (
	"strings"
	"sync"
	"testing"
)

func openCodeToolsFixture() []map[string]any {
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write",
				"description": "Write a file to disk",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    stringProperty("Absolute or relative file path"),
						"content": stringProperty("Full file content"),
					},
					"required":             []any{"path", "content"},
					"additionalProperties": false,
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read",
				"description": "Read a file",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": stringProperty("File path")},
					"required":   []any{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "edit",
				"description": "Replace a string in a file",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":       stringProperty("File path"),
						"old_string": stringProperty("Text to replace"),
						"new_string": stringProperty("Replacement text"),
					},
					"required": []any{"path", "old_string", "new_string"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "shell",
				"description": "Run a shell command",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": stringProperty("Command to execute"),
						"timeout": map[string]any{"type": "number"},
					},
					"required": []any{"command"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "list",
				"description": "List a directory",
			},
		},
	}
}

func TestSchemaValidator_ValidCalls(t *testing.T) {
	catalog, err := DefaultToolSchemaRegistry.CompileTools(openCodeToolsFixture())
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}
	cases := []struct {
		name string
		args string
	}{
		{"write", `{"path":"a.txt","content":"hello"}`},
		{"read", `{"path":"a.txt"}`},
		{"edit", `{"path":"a.txt","old_string":"a","new_string":"b"}`},
		{"shell", `{"command":"go test ./...","timeout":120}`},
		{"list", `{}`},
	}
	for _, tc := range cases {
		if ok, detail := catalog.ValidateCall(tc.name, tc.args); !ok {
			t.Errorf("ValidateCall(%s, %s) rejected: %s", tc.name, tc.args, detail)
		}
	}
}

func TestSchemaValidator_EmptyOldStringRejected(t *testing.T) {
	catalog, err := DefaultToolSchemaRegistry.CompileTools(openCodeToolsFixture())
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}
	for _, args := range []string{
		`{"path":"a.txt","old_string":"","new_string":"b"}`,
		`{"path":"a.txt","old_string":"   \n\t","new_string":"b"}`,
	} {
		ok, detail := catalog.ValidateCall("edit", args)
		if ok {
			t.Fatalf("expected an edit with an empty anchor to be rejected: %s", args)
		}
		if !strings.Contains(detail, "must not be empty") {
			t.Errorf("detail %q does not explain the empty anchor", detail)
		}
	}
}

func TestValidateToolArgumentSemantics_CamelCaseAnchors(t *testing.T) {
	for _, args := range []map[string]any{
		{"path": "a.txt", "oldString": "", "newString": "b"},
		{"path": "a.txt", "old_string": "x", "oldString": "   ", "newString": "b"},
		{"path": "a.txt", "oldText": "\n\t", "newString": "b"},
	} {
		ok, detail := validateToolArgumentSemantics("edit", args)
		if ok {
			t.Fatalf("expected an empty edit anchor to be rejected: %#v", args)
		}
		if !strings.Contains(detail, "must not be empty") {
			t.Errorf("detail %q does not explain the empty anchor", detail)
		}
	}
}

func TestSchemaValidator_EmptyPathRejected(t *testing.T) {
	catalog, err := DefaultToolSchemaRegistry.CompileTools(openCodeToolsFixture())
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}
	ok, detail := catalog.ValidateCall("read", `{"path":"   "}`)
	if ok {
		t.Fatal("expected a read with a blank path to be rejected")
	}
	if !strings.Contains(detail, "'path' must not be empty") {
		t.Errorf("detail %q does not explain the blank path", detail)
	}
}

func TestHasInvalidSchemaToolCall_EmptyOldString(t *testing.T) {
	raw := `<tool_call>{"name":"edit","arguments":{"path":"a.txt","old_string":"","new_string":"b"}}</tool_call>`
	invalid, detail := HasInvalidSchemaToolCall(raw, openCodeToolsFixture())
	if !invalid {
		t.Fatal("expected an empty old_string to be reported as invalid")
	}
	if !strings.Contains(detail, "must not be empty") {
		t.Errorf("detail %q does not explain the empty anchor", detail)
	}
}

func TestHasInvalidSchemaToolCall_IgnoresProseAndJSONFragments(t *testing.T) {
	tools := openCodeToolsFixture()

	// Exact v3.5.2 failure shape: the final synthesis mentions the read tool
	// and quotes a JSON fragment, but never frames a tool call. It must not be
	// rejected as a call missing "path".
	prose := []string{
		"La mise à jour de l'ensemble des documentations a été réalisée. J'ai utilisé `read` puis `edit` pour aligner AGENTS.md et README.md.",
		"Le format attendu est {\"name\":\"read\"} ; aucun appel n'est nécessaire à ce stade.",
		"Final summary: all target documentation files have been updated. For reference, {" +
			"\"name\": \"write\", \"arguments\": {\"content\": \"example\"}} describes the shape only.",
		"<thought>Je pourrais appeler {\"name\":\"read\"} pour vérifier, mais tout est déjà lu.</thought>La synthèse finale est prête.",
	}
	for _, raw := range prose {
		if invalid, detail := HasInvalidSchemaToolCall(raw, tools); invalid {
			t.Errorf("prose summary was rejected as an invalid tool call: %s (raw: %q)", detail, raw)
		}
	}

	// A Markdown fence explicitly formatted as a tool call is still validated.
	fenced := "Voici l'appel à rejouer :\n```json\n{\"name\":\"write\",\"arguments\":{\"content\":\"hello\"}}\n```"
	if invalid, detail := HasInvalidSchemaToolCall(fenced, tools); !invalid {
		t.Fatal("expected a fenced write without path to be flagged")
	} else if !strings.Contains(detail, "path") {
		t.Errorf("detail %q does not mention path", detail)
	}
}

func TestSchemaValidator_MissingRequiredProperty(t *testing.T) {
	catalog, err := DefaultToolSchemaRegistry.CompileTools(openCodeToolsFixture())
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}
	ok, detail := catalog.ValidateCall("write", `{"content":"hello"}`)
	if ok {
		t.Fatal("expected a write without path to be rejected")
	}
	for _, want := range []string{"missing required property", "'path'"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q does not contain %q", detail, want)
		}
	}
}

func TestSchemaValidator_TypeMismatch(t *testing.T) {
	catalog, err := DefaultToolSchemaRegistry.CompileTools(openCodeToolsFixture())
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}
	ok, detail := catalog.ValidateCall("write", `{"path":123,"content":"hello"}`)
	if ok {
		t.Fatal("expected a numeric path to be rejected")
	}
	for _, want := range []string{"type mismatch", "'path'", "number", "string"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q does not contain %q", detail, want)
		}
	}
}

func TestSchemaValidator_UndeclaredTool(t *testing.T) {
	catalog, err := DefaultToolSchemaRegistry.CompileTools(openCodeToolsFixture())
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}
	ok, detail := catalog.ValidateCall("exec", `{"command":"rm -rf /"}`)
	if ok {
		t.Fatal("expected an undeclared tool to be rejected")
	}
	if !strings.Contains(detail, "not declared") {
		t.Errorf("detail %q does not mention the undeclared tool", detail)
	}
}

func TestSchemaValidator_InvalidArgumentsJSON(t *testing.T) {
	catalog, err := DefaultToolSchemaRegistry.CompileTools(openCodeToolsFixture())
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}
	ok, detail := catalog.ValidateCall("read", `{"path":`)
	if ok {
		t.Fatal("expected malformed arguments JSON to be rejected")
	}
	if !strings.Contains(detail, "invalid JSON arguments") {
		t.Errorf("detail %q does not mention invalid JSON", detail)
	}
}

func TestSchemaValidator_AdditionalPropertiesRejected(t *testing.T) {
	catalog, err := DefaultToolSchemaRegistry.CompileTools(openCodeToolsFixture())
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}
	ok, detail := catalog.ValidateCall("write", `{"path":"a.txt","content":"x","mode":"append"}`)
	if ok {
		t.Fatal("expected additionalProperties=false to reject an unknown key")
	}
	if !strings.Contains(detail, "additional") && !strings.Contains(detail, "mode") {
		t.Errorf("detail %q does not describe the additional property", detail)
	}
}

func TestSchemaValidator_Draft07Schema(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name": "write",
				"parameters": map[string]any{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"type":    "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					},
					"required": []any{"path", "content"},
				},
			},
		},
	}
	catalog, err := NewToolSchemaRegistry().CompileTools(tools)
	if err != nil {
		t.Fatalf("draft-07 schema failed to compile: %v", err)
	}
	if ok, detail := catalog.ValidateCall("write", `{"content":"x"}`); ok {
		t.Fatalf("draft-07 schema should reject missing path: %s", detail)
	}
}

func TestSchemaValidator_CacheConcurrency(t *testing.T) {
	registry := NewToolSchemaRegistry()
	tools := openCodeToolsFixture()
	first, err := registry.CompileTools(tools)
	if err != nil {
		t.Fatalf("CompileTools returned error: %v", err)
	}

	const goroutines = 32
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			catalog, err := registry.CompileTools(tools)
			if err != nil {
				errs <- err.Error()
				return
			}
			if catalog != first {
				errs <- "cache miss for identical tool block"
				return
			}
			if ok, detail := catalog.ValidateCall("write", `{"path":"a.txt","content":"ok"}`); !ok {
				errs <- detail
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

func TestToolSchemaRegistry_LRUEviction(t *testing.T) {
	registry := NewToolSchemaRegistry()
	registry.maxEntries = 1
	toolsA := []map[string]any{{
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
	toolsB := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name": "glob",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"pattern": map[string]any{"type": "string"}},
				"required":   []any{"pattern"},
			},
		},
	}}
	firstA, err := registry.CompileTools(toolsA)
	if err != nil {
		t.Fatalf("CompileTools(A) returned error: %v", err)
	}
	if _, err := registry.CompileTools(toolsB); err != nil {
		t.Fatalf("CompileTools(B) returned error: %v", err)
	}
	secondA, err := registry.CompileTools(toolsA)
	if err != nil {
		t.Fatalf("CompileTools(A again) returned error: %v", err)
	}
	if secondA == firstA {
		t.Fatal("expected the least-recently-used catalog to be evicted")
	}
	if ok, detail := secondA.ValidateCall("read", `{"path":"x"}`); !ok {
		t.Fatalf("recompiled catalog rejected a valid call: %s", detail)
	}
}

func TestHasInvalidSchemaToolCall(t *testing.T) {
	tools := openCodeToolsFixture()

	invalid := `<tool_call>{"name":"write","arguments":{"content":"hello"}}</tool_call>`
	if bad, detail := HasInvalidSchemaToolCall(invalid, tools); !bad {
		t.Fatal("expected a write without path to be flagged")
	} else if !strings.Contains(detail, "path") {
		t.Errorf("detail %q does not mention path", detail)
	}

	valid := `<tool_call>{"name":"write","arguments":{"path":"a.txt","content":"hello"}}</tool_call>`
	if bad, detail := HasInvalidSchemaToolCall(valid, tools); bad {
		t.Fatalf("valid call flagged as invalid: %s", detail)
	}

	if bad, _ := HasInvalidSchemaToolCall(invalid, nil); bad {
		t.Fatal("validation must be disabled when no tools were declared")
	}
}

func BenchmarkToolSchemaRegistry_CompileTools(b *testing.B) {
	registry := NewToolSchemaRegistry()
	tools := openCodeToolsFixture()
	if _, err := registry.CompileTools(tools); err != nil {
		b.Fatalf("warm-up CompileTools: %v", err)
	}
	for b.Loop() {
		if _, err := registry.CompileTools(tools); err != nil {
			b.Fatalf("CompileTools: %v", err)
		}
	}
}

func TestHasInvalidSchemaToolCall_UnclosedThoughtBeforeRealCall(t *testing.T) {
	// A malformed JSON fragment inside an unclosed thought must not be
	// validated as an emitted call. The real <tool_call> after the thought is
	// preserved and validated normally.
	raw := "<thought>I could call {\"name\":\"write\",\"arguments\":{\"content\":\"x\"}} " +
		"but a read is safer.<tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"AGENTS.md\"}}</tool_call>"

	if invalid, detail := HasInvalidSchemaToolCall(raw, openCodeToolsFixture()); invalid {
		t.Fatalf("the thought fragment must be ignored, got schema violation: %s", detail)
	}
}
