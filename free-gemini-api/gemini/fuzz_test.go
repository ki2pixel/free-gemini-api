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
