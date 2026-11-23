package api

import (
	"encoding/json"
	"strings"
)

type OpenAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OpenAIToolCall struct {
	// Index is set only on streaming deltas. OpenAI-compatible clients
	// (OpenCode / Vercel AI SDK) require it to reassemble tool calls; it is
	// omitted from complete chat.completion responses.
	Index    *int                   `json:"index,omitempty"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function OpenAIToolCallFunction `json:"function"`
}

type OpenAIChatMessage struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
}

// FlattenContent converts an OpenAI content field (a plain string, or an array
// of content parts as sent by AI-SDK clients such as OpenCode/Vercel AI) into
// plain text. Clients like OpenCode send:
//
//	"content": [{"type": "text", "text": "..."}]
//
// which fails to bind into a *string and yields "Invalid request".
func FlattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if t, ok := p["text"].(string); ok {
				b.WriteString(t)
			}
		}
		return b.String()
	}
	return string(raw)
}

// contentRawString marshals a Go string into a RawMessage JSON string literal
// for outbound response messages.
func contentRawString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

type OpenAIChatCompletionRequest struct {
	Model      string              `json:"model"`
	Messages   []OpenAIChatMessage `json:"messages"`
	Tools      []map[string]any    `json:"tools,omitempty"`
	ToolChoice any                 `json:"tool_choice,omitempty"`
	Stream     bool                `json:"stream"`
	User       *string             `json:"user,omitempty"`
	// ReasoningEffort selects a deeper thinking variant of the requested model
	// family: "low"/"medium"/"high" map to the registry's -thinking-* entries,
	// "none" drops an explicit tier. Unknown values and families without
	// thinking variants are ignored silently, because harnesses send this field
	// by default. It never mutates the shared ModelConfig.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type OpenAIChoice struct {
	Index        int               `json:"index"`
	Message      OpenAIChatMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAIChatCompletionResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

type OpenAIDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
}

type OpenAIStreamChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type OpenAIChatCompletionChunk struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []OpenAIStreamChoice `json:"choices"`
}

type ImageGenerationRequest struct {
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	ResponseFormat string `json:"response_format"`
	UserID         string `json:"user_id"`
}

type ImageData struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

type ImageGenerationResponse struct {
	Created int64       `json:"created"`
	Data    []ImageData `json:"data"`
}
