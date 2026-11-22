package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIToolCallStreamIndexSerialization(t *testing.T) {
	idx := 0
	chunk := OpenAIChatCompletionChunk{
		ID:      "chatcmpl-test",
		Object:  "chat.completion.chunk",
		Created: 1,
		Model:   "gemini-4-neon",
		Choices: []OpenAIStreamChoice{
			{
				Index: 0,
				Delta: OpenAIDelta{
					Role: "assistant",
					ToolCalls: []OpenAIToolCall{
						{
							Index: &idx,
							ID:    "call_test",
							Type:  "function",
							Function: OpenAIToolCallFunction{
								Name:      "read",
								Arguments: `{"path":"main.go"}`,
							},
						},
					},
				},
			},
		},
	}

	raw, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if !strings.Contains(string(raw), `"index":0`) {
		t.Fatalf("streaming tool_call delta must serialize index 0, got %s", raw)
	}
}

func TestOpenAIToolCallCompletionOmitsIndex(t *testing.T) {
	tc := OpenAIToolCall{
		ID:   "call_test",
		Type: "function",
		Function: OpenAIToolCallFunction{
			Name:      "read",
			Arguments: `{"path":"main.go"}`,
		},
	}

	raw, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if strings.Contains(string(raw), `"index"`) {
		t.Fatalf("complete chat.completion tool_call must omit index, got %s", raw)
	}
}
