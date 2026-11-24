package gemini

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzParseResponse(f *testing.F) {
	f.Add(`[["wrb.fr",null,"[null,[\"c_123\",\"r_456\"],null,null,[[\"choice\",[\"hello\"]]]]"]]`)
	f.Add(`not json`)
	f.Add(`[["wrb.fr",null,"{}"]]`)
	f.Add("")

	f.Fuzz(func(t *testing.T, raw string) {
		res := &GeminiResponse{}
		parseResponse(raw, res)
	})
}

func FuzzValidateModelConfig(f *testing.F) {
	f.Add("gemini-3.8-flash", "56fdd199312815e2", "free")
	f.Add("", "", "")
	f.Add("UPPERCASE", "not-hex", "unknown")

	f.Fuzz(func(t *testing.T, id, modeID, tier string) {
		cfg := &ModelConfig{
			ID:      id,
			ModeID:  modeID,
			MinTier: tier,
			Source:  "custom",
		}
		_ = ValidateModelConfig(cfg)
	})
}

func FuzzNextStreamDelta(f *testing.F) {
	f.Add("hello", "hello world")
	f.Add("hello", "hello")
	f.Add("abc", "xyz")
	f.Add("", "new")

	f.Fuzz(func(t *testing.T, sent, snapshot string) {
		delta, newSent := nextStreamDelta(sent, sent, snapshot)
		if delta != "" && newSent == sent {
			t.Fatal("a non-empty delta must advance the sent snapshot")
		}
		// A suppressed delta must not lose text: the upstream mark may move
		// only when the snapshot is a replay of what the client already has.
		if delta == "" && newSent != sent && !strings.Contains(newSent, sent) {
			t.Fatalf("mark moved to %q without extending %q", newSent, sent)
		}
	})
}

func FuzzBuildRequestStruct(f *testing.F) {
	f.Add("prompt", false)
	f.Add("", true)

	f.Fuzz(func(t *testing.T, prompt string, temporary bool) {
		req := buildRequestStruct(prompt, []string{"en-GB"}, []interface{}{nil}, temporary)
		if len(req) != temporaryChatFlagIndex+1 {
			t.Fatalf("unexpected request length: %d", len(req))
		}
		if _, err := json.Marshal(req); err != nil {
			t.Fatalf("request is not marshalable: %v", err)
		}
	})
}

func FuzzHasTruncatedMutationToolCall(f *testing.F) {
	f.Add(`<tool_call>{"name":"edit","arguments":{"path":"a.txt","oldString":"x","newString":"y"}}</tool_call>`)
	f.Add("<thought>I will emit a <tool_call> block.</thought>")
	f.Add(`<tool_call>{"name":"write","arguments":{"path":"a.txt","content":"unterminated`)
	f.Add("")

	f.Fuzz(func(t *testing.T, raw string) {
		_ = HasTruncatedMutationToolCall(raw)
	})
}

func FuzzQuarantineUntaggedToolJSON(f *testing.F) {
	f.Add(`{"name":"write","arguments":{"path":"a.txt","content":"hello"}}`)
	f.Add(`{"name":"read","arguments":{"path":"AGENTS.md"}}`)
	f.Add(`{"name":"write","arguments":{"path":"a.txt","content":"unterminated`)
	f.Add(`{"content":"large","name":"write"}`)
	f.Add(`{"name":"unknown","arguments":{}}`)
	f.Add(`not json {{ [[ "name": "read" `)
	f.Add(strings.Repeat("x", 4096))

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<20 {
			raw = raw[:1<<20]
		}
		tools := openCodeToolsFixture()
		catalog, err := DefaultToolSchemaRegistry.CompileTools(tools)
		if err != nil {
			t.Fatalf("compile tool catalog: %v", err)
		}
		// The quarantine must terminate on arbitrary, nested, truncated and
		// invalid-UTF-8 payloads without panicking.
		_, _, _ = QuarantineUntaggedToolJSON(raw, catalog)
		arranged := ArrangeGeminiOutputWithTools(raw, tools)
		if arranged == nil {
			t.Fatal("ArrangeGeminiOutputWithTools returned nil")
		}
	})
}

func FuzzSanitizeThoughtArtifacts(f *testing.F) {
	f.Add("Le plan<thought d'action pour")
	f.Add("<thought>reasoning</thought>Visible.")
	f.Add("```markdown\n<thought>example</thought>\n```")
	f.Add("</thought></thinking>")
	f.Add("<THINKING\nnext step")
	f.Add("<thoughtful>not a tag</thoughtful>")
	f.Add("")

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<20 {
			raw = raw[:1<<20]
		}
		out := sanitizeThoughtArtifacts(raw)
		if len(out) > len(raw) {
			t.Fatalf("sanitizer grew the text: %d > %d", len(out), len(raw))
		}
		_ = ArrangeGeminiOutput(raw)
	})
}
