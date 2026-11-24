package gemini

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatToolsInstruction_PositiveContinuityAndCadence(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write",
				"description": "Write a file",
			},
		},
	}
	out := FormatToolsInstruction(tools)

	for _, forbidden := range []string{"0 remaining", "maximum number of tool", "search calls", "Disregard any", "<tools>"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("tool instructions must not contain %q", forbidden)
		}
	}
	for _, required := range []string{"Read-Parallel", "Write-Serial", "Metadata-First", "permanently active and available on every turn", "Exact-anchor rule", "Poka-Yoke", "Counter-S7", "exact verbatim substring", "anti-pseudo-report", "Do not emit a final completion report or summary if any planned modification or verification step is still pending", "Execute all tool calls until all files are modified", "Continuous and active tool access", "Continue invoking tools turn after turn until all requested file modifications, builds, and verifications are fully completed"} {
		if !strings.Contains(out, required) {
			t.Errorf("tool instructions must contain %q", required)
		}
	}
}

func TestPromptHasNoPinkElephant(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write",
				"description": "Write a file",
			},
		},
	}
	out := FormatToolsInstruction(tools)
	for _, forbidden := range []string{"0 remaining", "maximum number of tool", "search calls"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("tool instructions must not contain the quota literal %q", forbidden)
		}
	}
}

func TestArrangeGeminiOutput_Plain(t *testing.T) {
	raw := "Hello! How can I help you today?"
	res := ArrangeGeminiOutput(raw)
	if res.IsToolCall {
		t.Errorf("Expected IsToolCall to be false")
	}
	if res.CleanText != raw {
		t.Errorf("Expected CleanText %q, got %q", raw, res.CleanText)
	}
	if res.FinishReason != "stop" {
		t.Errorf("Expected finish_reason 'stop', got %q", res.FinishReason)
	}
}

func TestArrangeGeminiOutput_Thinking(t *testing.T) {
	raw := `<thought>
I should explain photosynthesis clearly and concisely.
</thought>
Photosynthesis is the process by which green plants use sunlight to synthesize nutrients.`

	res := ArrangeGeminiOutput(raw)
	if res.IsToolCall {
		t.Errorf("Expected IsToolCall to be false")
	}
	if !strings.Contains(res.ReasoningContent, "photosynthesis clearly") {
		t.Errorf("Expected reasoning content, got %q", res.ReasoningContent)
	}
	if strings.Contains(res.CleanText, "<thought>") {
		t.Errorf("CleanText should not contain thought tags, got %q", res.CleanText)
	}
	if !strings.HasPrefix(res.CleanText, "Photosynthesis") {
		t.Errorf("CleanText should start with 'Photosynthesis', got %q", res.CleanText)
	}
}

func TestArrangeGeminiOutput_ToolCall(t *testing.T) {
	raw := `<thought>
The user wants to know the weather in Tokyo.
</thought>
<tool_call>
{"name": "get_current_weather", "arguments": {"location": "Tokyo", "unit": "celsius"}}
</tool_call>`

	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true")
	}
	if res.FinishReason != "tool_calls" {
		t.Errorf("Expected finish_reason 'tool_calls', got %q", res.FinishReason)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.Function.Name != "get_current_weather" {
		t.Errorf("Expected function name 'get_current_weather', got %q", tc.Function.Name)
	}
	if !strings.Contains(tc.Function.Arguments, `"Tokyo"`) {
		t.Errorf("Expected arguments to contain 'Tokyo', got %q", tc.Function.Arguments)
	}
	if !strings.HasPrefix(tc.ID, "call_") {
		t.Errorf("Expected ID to start with 'call_', got %q", tc.ID)
	}
}

func TestArrangeGeminiOutput_CodeBlockFallback(t *testing.T) {
	raw := "```json\n{\"name\": \"search_database\", \"parameters\": {\"query\": \"SELECT * FROM users\"}}\n```"
	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true via JSON block fallback")
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Function.Name != "search_database" {
		t.Errorf("Expected 'search_database', got %q", res.ToolCalls[0].Function.Name)
	}
}

func TestArrangeGeminiOutput_AutoLinkifiedURLArguments(t *testing.T) {
	// Gemini's web stream renders bare URLs as "[https://x](https://x)".
	// That markdown must not survive into tool-call arguments.
	raw := "<tool_call>\n{\"name\": \"browser_open\", \"arguments\": {\"url\": \"[https://www.reddit.com/r/Eyeballs](https://www.reddit.com/r/Eyeballs)\"}}\n</tool_call>"
	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true")
	}
	args := res.ToolCalls[0].Function.Arguments
	if strings.Contains(args, "](") {
		t.Fatalf("Arguments still contain markdown link syntax: %q", args)
	}
	if !strings.Contains(args, `"https://www.reddit.com/r/Eyeballs"`) {
		t.Errorf("Expected clean URL in arguments, got %q", args)
	}
}

func TestArrangeGeminiOutput_DirectArgumentKeys(t *testing.T) {
	// Some models put the argument fields at the top level instead of
	// nesting them under "arguments".
	raw := `{"name": "execute", "code": "console.log(2+2)"}`
	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true for direct-argument JSON")
	}
	if res.ToolCalls[0].Function.Name != "execute" {
		t.Errorf("Expected 'execute', got %q", res.ToolCalls[0].Function.Name)
	}
	if !strings.Contains(res.ToolCalls[0].Function.Arguments, `"code":"console.log(2+2)"`) {
		t.Errorf("Expected code argument, got %q", res.ToolCalls[0].Function.Arguments)
	}
}

func TestArrangeGeminiOutput_ConcatenatedToolCalls(t *testing.T) {
	// A truncated first attempt followed by the complete call.
	raw := `{"name": "execute", "code": "const tab = await tools.open({ url: "{"name": "execute", "code": "run().done()"}`
	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true for concatenated JSON")
	}
	if !strings.Contains(res.ToolCalls[0].Function.Arguments, "run().done()") {
		t.Errorf("Expected the complete object to be used, got %q", res.ToolCalls[0].Function.Arguments)
	}
}

func TestArrangeGeminiOutput_CleanTextUnwrapsAutoLinks(t *testing.T) {
	raw := "See [https://example.com/path](https://example.com/path) for details."
	res := ArrangeGeminiOutput(raw)
	if strings.Contains(res.CleanText, "](") {
		t.Errorf("CleanText should not contain the auto-link artifact, got %q", res.CleanText)
	}
	if !strings.Contains(res.CleanText, "https://example.com/path") {
		t.Errorf("CleanText should keep the bare URL, got %q", res.CleanText)
	}
}

func TestArrangeGeminiOutput_GenuineMarkdownLinkKept(t *testing.T) {
	raw := "Read the [docs](https://example.com/docs) page."
	res := ArrangeGeminiOutput(raw)
	if !strings.Contains(res.CleanText, "[docs](https://example.com/docs)") {
		t.Errorf("Genuine markdown link (label != href) should be preserved, got %q", res.CleanText)
	}
}

func TestArrangeGeminiOutput_SandboxCleanup(t *testing.T) {
	raw := "```python?code_reference&code_event_index=1\nprint(45 * 2)\n```\n\n```text?code_stdout&code_event_index=1\n90\n```\n\n90"
	res := ArrangeGeminiOutput(raw)
	if strings.Contains(res.CleanText, "code_reference") {
		t.Errorf("CleanText should not contain internal sandbox params, got %q", res.CleanText)
	}
	if !strings.Contains(res.CleanText, "```python") {
		t.Errorf("Expected cleaned ```python fence, got %q", res.CleanText)
	}
	if !strings.Contains(res.CleanText, "```text") {
		t.Errorf("Expected cleaned ```text fence, got %q", res.CleanText)
	}
}

func TestExtractAvailableThoughts(t *testing.T) {
	// Case 1: unclosed thought
	r1 := "<thought>Analyzing the repository..."
	if got := ExtractAvailableThoughts(r1); got != "Analyzing the repository..." {
		t.Errorf("Expected 'Analyzing the repository...', got %q", got)
	}

	// Case 2: partial close tag
	r2 := "<thought>Analyzing the repository...</th"
	if got := ExtractAvailableThoughts(r2); got != "Analyzing the repository..." {
		t.Errorf("Expected 'Analyzing the repository...', got %q", got)
	}

	// Case 3: complete thought
	r3 := "<thought>Analyzing the repository...</thought>"
	if got := ExtractAvailableThoughts(r3); got != "Analyzing the repository..." {
		t.Errorf("Expected 'Analyzing the repository...', got %q", got)
	}

	// Case 4: multiple thoughts
	r4 := "<thought>Part 1</thought>between<thought>Part 2</thought>"
	if got := ExtractAvailableThoughts(r4); got != "Part 1\n\nPart 2" {
		t.Errorf("Expected 'Part 1\\n\\nPart 2', got %q", got)
	}

	// Case 5: thinking tag
	r5 := "<thinking>Step by step logic</thinking>"
	if got := ExtractAvailableThoughts(r5); got != "Step by step logic" {
		t.Errorf("Expected 'Step by step logic', got %q", got)
	}

	// Case 6: no thoughts
	r6 := "Just plain answer"
	if got := ExtractAvailableThoughts(r6); got != "" {
		t.Errorf("Expected empty string, got %q", got)
	}
}

func TestExtractAvailableContent(t *testing.T) {
	// Case 1: thought then content
	r1 := "<thought>Deep analysis</thought>Here is the final result."
	if got := ExtractAvailableContent(r1); got != "Here is the final result." {
		t.Errorf("Expected 'Here is the final result.', got %q", got)
	}

	// Case 2: still inside thought
	r2 := "<thought>Deep analysis in progress..."
	if got := ExtractAvailableContent(r2); got != "" {
		t.Errorf("Expected empty content while in thought, got %q", got)
	}

	// Case 3: plain content
	r3 := "Hello, how can I help you?"
	if got := ExtractAvailableContent(r3); got != "Hello, how can I help you?" {
		t.Errorf("Expected plain text, got %q", got)
	}
}

func TestArrangeGeminiOutput_MalformedClosingToolCall(t *testing.T) {
	// Replicates the exact case where Gemini closes a tool call with ``` instead of </tool_call>
	raw := `<thought>
Updating memory bank
</thought>
<tool_call>
{"name": "edit", "arguments": {"path": "memory-bank/decisionLog.md"}}
</tool_call>
<tool_call>
{"name": "write", "content": "# Progress\n\nDone", "path": "memory-bank/progress.md"}
` + "```"

	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true")
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("Expected 2 tool calls, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Function.Name != "edit" {
		t.Errorf("Expected first tool 'edit', got %q", res.ToolCalls[0].Function.Name)
	}
	if res.ToolCalls[1].Function.Name != "write" {
		t.Errorf("Expected second tool 'write', got %q", res.ToolCalls[1].Function.Name)
	}
	if res.CleanText != "" {
		t.Errorf("Expected empty CleanText, got %q", res.CleanText)
	}
}

func TestArrangeGeminiOutput_UnescapedNewlinesAndQuotesInWriteTool(t *testing.T) {
	// Replicates the real case from debug/create-or-update-agents-md.json where Gemini
	// outputs raw newlines (ASCII 10) and unescaped quotes ($(gofmt -l .)) inside the "content" argument.
	raw := "<thought>\nI need to write or update AGENTS.md for this repository.\n</thought>\n<tool_call>\n" +
		"{\"name\": \"write\", \"arguments\": {\"content\": \"# AGENTS.md — Agent Guidelines for Free Gemini API\n\n" +
		"This file provides high-signal context and exact operational commands for AI agents working in this repository.\n\n" +
		"## 🚀 Quick Reference & Commands\n\n" +
		"All Go commands must be run inside the `free-gemini-api` directory:\n" +
		"- **Build / Run:** `cd free-gemini-api && go run main.go`\n" +
		"- **Tests (with race detector):** `cd free-gemini-api && go test -race -short -coverprofile=coverage.out ./...`\n" +
		"- **Formatting check:** `cd free-gemini-api && test -z \"$(gofmt -l .)\"`\n" +
		"- **Vetting:** `cd free-gemini-api && go vet ./...`\n" +
		"- **Docker Compose:** `docker compose up -d --build` (from `free-gemini-api`)\n\", \"path\": \"AGENTS.md\"}}\n" +
		"</tool_call>"

	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true for write tool with raw newlines and quotes")
	}
	if res.FinishReason != "tool_calls" {
		t.Errorf("Expected finish_reason 'tool_calls', got %q", res.FinishReason)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.Function.Name != "write" {
		t.Errorf("Expected tool name 'write', got %q", tc.Function.Name)
	}
	if !strings.Contains(tc.Function.Arguments, `"path":"AGENTS.md"`) && !strings.Contains(tc.Function.Arguments, `"path": "AGENTS.md"`) {
		t.Errorf("Expected path AGENTS.md in arguments, got %q", tc.Function.Arguments)
	}
	if !strings.Contains(tc.Function.Arguments, "gofmt") {
		t.Errorf("Expected gofmt command preserved in arguments, got %q", tc.Function.Arguments)
	}
	if res.CleanText != "" {
		t.Errorf("Expected empty CleanText, got %q", res.CleanText)
	}
}

func TestNormalizeJSONStringLiterals(t *testing.T) {
	// Raw string with literal newline, tab, and unescaped inner quotes
	input := "{\"content\": \"line1\nline2 with \"quotes\" inside\tand tab\", \"path\": \"test.txt\"}"
	normalized := normalizeJSONStringLiterals(input)

	if strings.Contains(normalized, "\n") {
		t.Errorf("Normalized JSON still contains raw newline: %q", normalized)
	}
	if !strings.Contains(normalized, `\n`) {
		t.Errorf("Expected escaped \\n in normalized JSON, got %q", normalized)
	}
	if !strings.Contains(normalized, `\"quotes\"`) {
		t.Errorf("Expected escaped \\\"quotes\\\" in normalized JSON, got %q", normalized)
	}
}

func TestParseSingleToolCall_InvalidArgumentsRejected(t *testing.T) {
	payload := `{"name":"write","arguments":"not-json","path":"x","content":"y"}`
	if _, ok := parseSingleToolCall(payload); ok {
		t.Fatal("expected explicitly malformed arguments to be rejected")
	}
}

func TestParseSingleToolCall_NoArgumentsAccepted(t *testing.T) {
	tc, ok := parseSingleToolCall(`{"name":"list"}`)
	if !ok {
		t.Fatal("expected a no-argument tool call to be accepted")
	}
	if tc.Function.Arguments != "{}" {
		t.Errorf("expected empty arguments object, got %q", tc.Function.Arguments)
	}
}

func TestExtractHeuristicToolCall(t *testing.T) {
	rawMalformed := "{\"name\": \"write\", \"arguments\": {\"path\": \"README.md\", \"content\": \"Malformed without closing quote and invalid json: test"
	tc, ok := extractHeuristicToolCall(rawMalformed)
	if !ok {
		t.Fatalf("Expected heuristic extraction to succeed on malformed JSON")
	}
	if tc.Function.Name != "write" {
		t.Errorf("Expected name 'write', got %q", tc.Function.Name)
	}
	if !strings.Contains(tc.Function.Arguments, `"path":"README.md"`) {
		t.Errorf("Expected path in arguments, got %q", tc.Function.Arguments)
	}
}

func TestArrangeGeminiOutput_ToolsSchemaLeakPreambleCleaned(t *testing.T) {
	// Replicates the exact case from debug/create-or-update-agents-md.json where Gemini
	// echoed the tools JSON schema inside <tools>[ ... before emitting <tool_call>.
	raw := "<thought>\nI will check if AGENTS.md exists and create it.\n</thought>\n" +
		"<tools>[\n  {\n    \"function\": {\n      \"description\": \"Execute a shell command and return its output. Commands run on Linux using bash.\",\n" +
		"      \"name\": \"shell\",\n      \"parameters\": {\n        \"additionalProperties\": false,\n        \"properties" +
		"<tool_call>\n{\"name\": \"glob\", \"arguments\": {\"pattern\": \"AGENTS.md\"}}\n</tool_call>\n" +
		"<tool_call>\n{\"name\": \"write\", \"arguments\": {\"content\": \"# Test\", \"path\": \"AGENTS.md\"}}\n</tool_call>"

	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true")
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("Expected 2 tool calls, got %d", len(res.ToolCalls))
	}
	if res.CleanText != "" {
		t.Errorf("Expected CleanText to be empty (prompt leak cleaned), got %q", res.CleanText)
	}
}

func TestArrangeGeminiOutput_TruncatedToolCallWithUpstreamDisclaimer(t *testing.T) {
	// Replicates the real case from debug/plan-d-audit-post-impl-mentation-free-gemini-api.json
	// where upstream safety triggered right at the end of the content/arguments object,
	// dropping the outer closing brace and </tool_call> and appending a canned refusal disclaimer.
	raw := "<tool_call>\n" +
		"{\"name\": \"write\", \"arguments\": {\"content\": \"# Rapport d'Audit de Post-Implémentation — Audit 2026-11 (Free Gemini API) [Neon-Optimized]\n\n" +
		"**Date de l'audit initial :** 21 novembre 2026  \n" +
		"**Date du post-audit / validation :** 22 novembre 2026  \n" +
		"**Cible :** `free-gemini-api` (v3.4.3 - Mode Local-First / Agentic Task)  \n" +
		"**Indice de Maturité Global Initial :** 38/100 (Niveau 2/5 - NO-GO)  \n" +
		"**Indice de Maturité Global Actualisé :** 74,25/100 (Niveau 4/5 - GO Local / IDE)  \n\n" +
		"---\n\n" +
		"## 1. Synthèse Exécutive & Verdict de Post-Audit\n\n" +
		"Le runner local unifié `scripts/audit_check.sh` compense le retrait de CI/CD.\n\", \"path\": \"audit-2026-11/RAPPORT_POST_AUDIT.md\"}" +
		"I'm just a language model and can't help with that."

	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected IsToolCall to be true for truncated tool call with upstream disclaimer")
	}
	if res.FinishReason != "tool_calls" {
		t.Errorf("Expected finish_reason 'tool_calls', got %q", res.FinishReason)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.Function.Name != "write" {
		t.Errorf("Expected tool name 'write', got %q", tc.Function.Name)
	}
	if !strings.Contains(tc.Function.Arguments, "RAPPORT_POST_AUDIT.md") {
		t.Errorf("Expected path RAPPORT_POST_AUDIT.md in arguments, got %q", tc.Function.Arguments)
	}
	if !strings.Contains(tc.Function.Arguments, "audit_check.sh") {
		t.Errorf("Expected content preserved in arguments, got %q", tc.Function.Arguments)
	}
	if strings.Contains(res.CleanText, "language model") {
		t.Errorf("CleanText should not contain upstream disclaimer, got %q", res.CleanText)
	}
	if res.CleanText != "" {
		t.Errorf("Expected empty CleanText, got %q", res.CleanText)
	}
}

func TestArrangeGeminiOutput_TruncatedToolCallEOF(t *testing.T) {
	// Case where generation is cut off mid-string at EOF without closing tags or
	// braces. The content is incomplete, so the call must be rejected instead of
	// being auto-repaired into a truncated write.
	raw := "<tool_call>\n{\"name\": \"write\", \"arguments\": {\"path\": \"test.txt\", \"content\": \"Unfinished line"

	res := ArrangeGeminiOutput(raw)
	if res.IsToolCall {
		t.Fatalf("Expected mid-value truncated write to be rejected, got %d tool call(s)", len(res.ToolCalls))
	}
	if len(res.ToolCalls) != 0 {
		t.Fatalf("Expected 0 tool calls, got %d", len(res.ToolCalls))
	}
	if res.FinishReason != "stop" {
		t.Errorf("Expected finish_reason 'stop', got %q", res.FinishReason)
	}
	if !HasTruncatedMutationToolCall(raw) {
		t.Errorf("Expected HasTruncatedMutationToolCall to detect the mid-value truncation")
	}
}

func TestArrangeGeminiOutput_CompleteWriteQuotingDisclaimerAccepted(t *testing.T) {
	// A legitimate document may quote the upstream refusal phrase. A complete
	// JSON object must not be mistaken for a truncated call just because its
	// content contains that phrase.
	raw := "<tool_call>\n{\"name\": \"write\", \"arguments\": {\"path\": \"notes.txt\", \"content\": \"Upstream said: I'm just a language model and can't help with that.\"}}\n</tool_call>"

	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected complete write quoting a disclaimer to be accepted")
	}
	if HasTruncatedMutationToolCall(raw) {
		t.Errorf("Complete write must not be classified as truncated")
	}
}

func TestHasTruncatedMutationToolCall_TagMentionedBeforeObjectDoesNotPanic(t *testing.T) {
	// A thought block may mention `<tool_call>` before the real call. The
	// truncation scanner must search for the next tag after the current
	// object's opening brace, otherwise it can compute limit < braceIdx and
	// panic with "slice bounds out of range".
	raw := "<thought>I will emit a <tool_call> block now.</thought>\n" +
		"<tool_call>{\"name\":\"edit\",\"arguments\":{\"path\":\"a.txt\",\"oldString\":\"x\",\"newString\":\"y\"}}</tool_call>"

	if HasTruncatedMutationToolCall(raw) {
		t.Fatal("a complete edit preceded by a <tool_call> mention must not be classified as truncated")
	}
}

func TestHasTruncatedMutationToolCall_CompleteWriteQuotingToolCallTagAccepted(t *testing.T) {
	raw := `<tool_call>{"name":"write","arguments":{"path":"docs.md","content":"Example: <tool_call>{\"name\":\"read\"}</tool_call>"}}</tool_call>`
	if HasTruncatedMutationToolCall(raw) {
		t.Fatal("a complete write whose content quotes a tool_call tag must not be classified as truncated")
	}
}

func TestHasTruncatedMutationToolCall_ConcatenatedCallsStillDetected(t *testing.T) {
	raw := `<tool_call>{"name":"edit","arguments":{"path":"a.txt","oldString":"x","newString":"y"}}` +
		`<tool_call>{"name":"edit","arguments":{"path":"b.txt","oldString":"unterminated`

	if !HasTruncatedMutationToolCall(raw) {
		t.Fatal("a truncated mutation after a complete call must still be detected")
	}
}

func TestArrangeGeminiOutput_TruncatedContentFirstWriteRejected(t *testing.T) {
	// Content-first payload inside a markdown fence: the tool name is never
	// emitted before the cutoff, so the guard must fall back to the presence
	// of mutation-specific keys.
	raw := "<tool_call>\n```json\n{\"content\": \"line1\\nline2 truncated"

	res := ArrangeGeminiOutput(raw)
	if res.IsToolCall {
		t.Fatalf("Expected content-first mid-value write to be rejected, got %d tool call(s)", len(res.ToolCalls))
	}
	if !HasTruncatedMutationToolCall(raw) {
		t.Errorf("Expected HasTruncatedMutationToolCall to detect the content-first truncation")
	}
}

func TestArrangeGeminiOutput_TruncatedRawJSONWriteRejected(t *testing.T) {
	// No <tool_call> wrapper: the raw-JSON fallback must reject the same
	// mid-value truncation instead of repairing it.
	raw := `{"name": "write", "arguments": {"path": "test.txt", "content": "Unfinished line`

	res := ArrangeGeminiOutput(raw)
	if res.IsToolCall {
		t.Fatalf("Expected raw-JSON mid-value write to be rejected, got %d tool call(s)", len(res.ToolCalls))
	}
	if !HasTruncatedMutationToolCall(raw) {
		t.Errorf("Expected HasTruncatedMutationToolCall to detect the raw-JSON truncation")
	}
}

func TestArrangeGeminiOutput_SafeEnvelopeAutoClose(t *testing.T) {
	// Only the outer closing braces are missing: every string value is already
	// closed, so repairing the envelope is safe and the write can execute.
	raw := "<tool_call>\n{\"name\": \"write\", \"arguments\": {\"path\": \"test.txt\", \"content\": \"complete content\""

	res := ArrangeGeminiOutput(raw)
	if !res.IsToolCall {
		t.Fatalf("Expected safe envelope auto-close to produce a tool call")
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Function.Name != "write" {
		t.Errorf("Expected tool name 'write', got %q", res.ToolCalls[0].Function.Name)
	}
	if !strings.Contains(res.ToolCalls[0].Function.Arguments, "complete content") {
		t.Errorf("Expected complete content preserved in arguments, got %q", res.ToolCalls[0].Function.Arguments)
	}
	if HasTruncatedMutationToolCall(raw) {
		t.Errorf("Safe envelope truncation must not be classified as mid-value")
	}
}

func TestArrangeGeminiOutput_ReportsRepairs(t *testing.T) {
	hasRepair := func(res *ArrangedOutput, want string) bool {
		for _, kind := range res.Repairs {
			if kind == want {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "brace envelope",
			raw:  `<tool_call>{"name":"write","arguments":{"path":"test.txt","content":"complete content"`,
			want: "brace_envelope",
		},
		{
			name: "control chars",
			raw:  "<tool_call>\n{\"name\": \"write\", \"arguments\": {\"path\": \"test.txt\", \"content\": \"line1\nline2\"}}\n</tool_call>",
			want: "control_chars",
		},
		{
			name: "disclaimer",
			raw:  `<tool_call>{"name":"list","arguments":{"path":"."}}</tool_call> I'm just a language model and can't help with that.`,
			want: "disclaimer",
		},
		{
			name: "orphan tags",
			raw:  "Done.\n" + strings.Repeat("</script> </tool_call> ", 3),
			want: "orphan_tags",
		},
	}
	for _, tc := range cases {
		res := ArrangeGeminiOutput(tc.raw)
		if !hasRepair(res, tc.want) {
			t.Errorf("%s: repairs = %v, want %q", tc.name, res.Repairs, tc.want)
		}
	}
}

func TestSanitizeGeminiContent_RemovesRepeatedOrphanClosingTags(t *testing.T) {
	leak := strings.Repeat("</script> </tool_call> ", 16)
	res := ArrangeGeminiOutput("Documentation complete.\n" + leak + "\nEnd.")
	if strings.Contains(res.CleanText, "</script>") || strings.Contains(res.CleanText, "</tool_call>") {
		t.Fatalf("orphan closing tags were not removed: %q", res.CleanText)
	}
	if !strings.Contains(res.CleanText, "Documentation complete.") || !strings.Contains(res.CleanText, "End.") {
		t.Fatalf("sanitizer removed legitimate text: %q", res.CleanText)
	}
	for _, kind := range res.Repairs {
		if kind == "orphan_tags" {
			return
		}
	}
	t.Fatalf("repairs = %v, want orphan_tags", res.Repairs)
}

func TestSanitizeGeminiContent_PreservesPairedClosingTags(t *testing.T) {
	raw := "Example:\n<script>console.log('ok')</script>\nDone."
	res := ArrangeGeminiOutput(raw)
	if !strings.Contains(res.CleanText, "</script>") {
		t.Fatalf("a matched closing tag must be preserved, got %q", res.CleanText)
	}
	if !strings.Contains(res.CleanText, "<script>") {
		t.Fatalf("a matched opening tag must be preserved, got %q", res.CleanText)
	}
}

func TestSanitizeGeminiContent_PreservesSingleUnmatchedClosingTag(t *testing.T) {
	raw := "Inline documentation mentions </script> as a marker."
	res := ArrangeGeminiOutput(raw)
	if !strings.Contains(res.CleanText, "</script>") {
		t.Fatalf("a single unmatched closing tag must be preserved, got %q", res.CleanText)
	}
}

func TestFindUpstreamDisclaimerOffset_UTF8FrenchApostrophes(t *testing.T) {
	// Typographic apostrophes are 3 bytes each. The previous implementation
	// normalized them to ASCII (1 byte) before computing the offset, then sliced
	// the original string with that smaller index, cutting into valid JSON.
	prefix := strings.Repeat("L’audit d’aujourd’hui n’a rien révélé. ", 200)
	disclaimer := "I’m just a language model and can’t help with that."
	raw := prefix + disclaimer

	start, end := findUpstreamDisclaimerOffset(raw)
	if start != len(prefix) {
		t.Fatalf("findUpstreamDisclaimerOffset start = %d, want %d (byte offset shifted by typographic apostrophes)", start, len(prefix))
	}
	if end != len(raw) {
		t.Fatalf("findUpstreamDisclaimerOffset end = %d, want %d", end, len(raw))
	}
	if got := raw[start:end]; got != disclaimer {
		t.Errorf("Disclaimer slice = %q, want %q", got, disclaimer)
	}
	if !strings.HasSuffix(raw[:start], "révélé. ") {
		t.Errorf("Prefix slice does not end on the expected UTF-8 boundary: %q", raw[:start])
	}
	if !utf8.ValidString(raw[:start]) {
		t.Errorf("Prefix slice is not valid UTF-8 after offset computation")
	}
}

func TestAutoCloseJSONBraces(t *testing.T) {
	cases := []struct {
		input      string
		expected   string
		isMidValue bool
	}{
		{
			input:      `{"a": "b"`,
			expected:   `{"a": "b"}`,
			isMidValue: false,
		},
		{
			input:      `{"a": {"b": [1, 2`,
			expected:   `{"a": {"b": [1, 2]}}`,
			isMidValue: false,
		},
		{
			input:      `{"name": "write", "arguments": {"content": "hello`,
			expected:   `{"name": "write", "arguments": {"content": "hello"}}`,
			isMidValue: true,
		},
		{
			input:      `{"name": "write", "arguments": {"path": "test.txt",`,
			expected:   `{"name": "write", "arguments": {"path": "test.txt"}}`,
			isMidValue: true,
		},
		{
			input:      `{"name": "write", "arguments": {"content":`,
			expected:   `{"name": "write", "arguments": {"content":""}}`,
			isMidValue: true,
		},
		{
			input:      `{"name": "write", "arguments": {"path": "test.txt", "content": "complete"`,
			expected:   `{"name": "write", "arguments": {"path": "test.txt", "content": "complete"}}`,
			isMidValue: false,
		},
	}

	for _, tc := range cases {
		got, isMidValue := autoCloseJSONBraces(tc.input)
		if got != tc.expected {
			t.Errorf("autoCloseJSONBraces(%q) = %q, want %q", tc.input, got, tc.expected)
		}
		if isMidValue != tc.isMidValue {
			t.Errorf("autoCloseJSONBraces(%q) isMidValue = %v, want %v", tc.input, isMidValue, tc.isMidValue)
		}
	}
}

func TestArrangeGeminiOutputWithTools_ValidSchema(t *testing.T) {
	raw := `<tool_call>{"name":"write","arguments":{"path":"notes.txt","content":"hello"}}</tool_call>`
	res := ArrangeGeminiOutputWithTools(raw, openCodeToolsFixture())
	if !res.IsToolCall {
		t.Fatal("expected a schema-valid write to be accepted")
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Function.Name != "write" {
		t.Errorf("expected write, got %q", res.ToolCalls[0].Function.Name)
	}
	if res.FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason tool_calls, got %q", res.FinishReason)
	}
}

func TestArrangeGeminiOutputWithTools_MissingPathRejected(t *testing.T) {
	raw := `<tool_call>{"name":"write","arguments":{"content":"hello"}}</tool_call>`
	res := ArrangeGeminiOutputWithTools(raw, openCodeToolsFixture())
	if res.IsToolCall || len(res.ToolCalls) != 0 {
		t.Fatalf("expected the schema-invalid write to be rejected, got %d call(s)", len(res.ToolCalls))
	}
	if res.FinishReason != "stop" {
		t.Errorf("expected finish_reason stop, got %q", res.FinishReason)
	}
	if strings.Contains(res.CleanText, "<tool_call>") {
		t.Errorf("rejected tool block leaked into CleanText: %q", res.CleanText)
	}
}

func TestArrangeGeminiOutputWithTools_InvalidCallDroppedButValidKept(t *testing.T) {
	raw := "<tool_call>\n" +
		`{"name":"write","arguments":{"content":"missing path"}}` + "\n</tool_call>\n" +
		"<tool_call>\n" +
		`{"name":"read","arguments":{"path":"notes.txt"}}` + "\n</tool_call>"
	res := ArrangeGeminiOutputWithTools(raw, openCodeToolsFixture())
	if !res.IsToolCall {
		t.Fatal("expected the valid read to survive")
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "read" {
		t.Fatalf("expected only the read call, got %+v", res.ToolCalls)
	}
}

func TestThrottleMutatingToolCalls_SingleWriteAllowed(t *testing.T) {
	calls := []ToolCall{
		{Type: "function", Function: ToolCallFunction{Name: "read", Arguments: `{"path":"a"}`}},
		{Type: "function", Function: ToolCallFunction{Name: "write", Arguments: `{"path":"a","content":"x"}`}},
		{Type: "function", Function: ToolCallFunction{Name: "edit", Arguments: `{"path":"b","old_string":"x","new_string":"y"}`}},
	}
	out := ThrottleMutatingToolCalls(calls)
	if len(out) != 2 {
		t.Fatalf("expected read + first write, got %d calls", len(out))
	}
	if out[0].Function.Name != "read" || out[1].Function.Name != "write" {
		t.Errorf("unexpected throttled calls: %q, %q", out[0].Function.Name, out[1].Function.Name)
	}
}

func TestThrottleMutatingToolCalls_ParallelReadsWithOneWrite(t *testing.T) {
	calls := []ToolCall{
		{Function: ToolCallFunction{Name: "read"}},
		{Function: ToolCallFunction{Name: "grep"}},
		{Function: ToolCallFunction{Name: "glob"}},
		{Function: ToolCallFunction{Name: "write"}},
		{Function: ToolCallFunction{Name: "read"}},
		{Function: ToolCallFunction{Name: "edit"}},
		{Function: ToolCallFunction{Name: "list"}},
	}
	out := ThrottleMutatingToolCalls(calls)
	if len(out) != 4 {
		t.Fatalf("expected the batch to stop immediately after the first mutation, got %d calls", len(out))
	}
	names := make([]string, len(out))
	for i, tc := range out {
		names[i] = tc.Function.Name
	}
	want := []string{"read", "grep", "glob", "write"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("throttled names = %v, want %v", names, want)
		}
	}
}

func TestThrottleMutatingToolCalls_ReadOnly(t *testing.T) {
	calls := []ToolCall{
		{Function: ToolCallFunction{Name: "read"}},
		{Function: ToolCallFunction{Name: "grep"}},
		{Function: ToolCallFunction{Name: "glob"}},
		{Function: ToolCallFunction{Name: "list"}},
	}
	out := ThrottleMutatingToolCalls(calls)
	if len(out) != len(calls) {
		t.Fatalf("read-only batches must be preserved, got %d/%d", len(out), len(calls))
	}
	if got := ThrottleMutatingToolCalls(nil); got != nil {
		t.Fatalf("nil input should stay nil, got %#v", got)
	}
}

func TestFormatToolsInstruction_CompactOutput(t *testing.T) {
	tools := openCodeToolsFixture()
	fn := tools[0]["function"].(map[string]any)
	fn["description"] = strings.Repeat("Write a file to disk with strict metadata ordering. ", 20)
	params := fn["parameters"].(map[string]any)
	params["title"] = "WriteFileParameters"
	params["$schema"] = "http://json-schema.org/draft-07/schema#"
	props := params["properties"].(map[string]any)
	props["path"].(map[string]any)["description"] = strings.Repeat("Absolute or relative path on the host filesystem. ", 20)

	indented, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}
	out := FormatToolsInstruction(tools)
	const fence = "```json\n"
	start := strings.Index(out, fence)
	if start == -1 {
		t.Fatalf("compact tool catalog block not found in instruction")
	}
	catalogStart := start + len(fence)
	relEnd := strings.Index(out[catalogStart:], "\n```")
	if relEnd == -1 {
		t.Fatalf("compact tool catalog closing fence not found")
	}
	compact := out[catalogStart : catalogStart+relEnd]
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(compact), &decoded); err != nil {
		t.Fatalf("compact catalog is not valid JSON: %v\n%s", err, compact)
	}
	if len(decoded) != len(tools) {
		t.Fatalf("compact catalog has %d tools, want %d", len(decoded), len(tools))
	}

	if len(compact) >= len(indented)*6/10 {
		t.Errorf("compact catalog is %d bytes, want at least 40%% below the %d-byte indented catalog", len(compact), len(indented))
	}
	if strings.Contains(compact, "\n  ") {
		t.Errorf("compact catalog still contains indentation: %q", compact)
	}
	pathIdx := strings.Index(compact, `"path"`)
	contentIdx := strings.Index(compact, `"content"`)
	if pathIdx == -1 || contentIdx == -1 {
		t.Fatalf("compact catalog lost path/content properties: %s", compact)
	}
	if pathIdx > contentIdx {
		t.Errorf("path must be serialized before content: %s", compact)
	}
	if !strings.Contains(compact, `"required":["path","content"]`) {
		t.Errorf("required keys must be explicit and path-first: %s", compact)
	}
}

func TestThrottleMutatingToolCalls_ShellAndPatch(t *testing.T) {
	calls := []ToolCall{
		{Function: ToolCallFunction{Name: "shell"}},
		{Function: ToolCallFunction{Name: "patch"}},
		{Function: ToolCallFunction{Name: "read"}},
	}
	out := ThrottleMutatingToolCalls(calls)
	if len(out) != 1 || out[0].Function.Name != "shell" {
		t.Fatalf("expected only the first mutation (shell), got %+v", out)
	}
}

func TestArrangeGeminiOutputWithTools_UnclosedThoughtLeaksNothing(t *testing.T) {
	// Exact v3.5.3 Hydrogen/Hydrogen-Task stop shape: the stream ends inside
	// the reasoning block without a closing tag.
	raw := "<thought>\nThe user provided a system/user prompt detailing an alignment plan for updating project documentations (such as AGENTS"

	res := ArrangeGeminiOutputWithTools(raw, openCodeToolsFixture())
	if res.IsToolCall {
		t.Fatal("a thought-only turn must not be classified as a tool call")
	}
	if !strings.Contains(res.ReasoningContent, "alignment plan") {
		t.Errorf("ReasoningContent should capture the unclosed thought, got %q", res.ReasoningContent)
	}
	if res.CleanText != "" {
		t.Errorf("CleanText must be empty for a thought-only turn, got %q", res.CleanText)
	}
	for _, tag := range []string{"<thought>", "</thought>", "<thinking>", "</thinking>"} {
		if strings.Contains(res.CleanText, tag) {
			t.Errorf("CleanText leaked %q: %q", tag, res.CleanText)
		}
		if strings.Contains(res.ReasoningContent, tag) {
			t.Errorf("ReasoningContent leaked %q: %q", tag, res.ReasoningContent)
		}
	}
}

func TestArrangeGeminiOutputWithTools_UnclosedThoughtBeforeToolCall(t *testing.T) {
	// The model switches directly to a tool call without closing its thought.
	raw := "<thought>\nI should read AGENTS.md before editing it.\n" +
		"<tool_call>\n{\"name\":\"read\",\"arguments\":{\"path\":\"AGENTS.md\"}}\n</tool_call>"

	res := ArrangeGeminiOutputWithTools(raw, openCodeToolsFixture())
	if !res.IsToolCall {
		t.Fatalf("expected the preserved tool call to be parsed, got CleanText %q", res.CleanText)
	}
	if res.FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls', got %q", res.FinishReason)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Function.Name != "read" {
		t.Errorf("expected 'read', got %q", res.ToolCalls[0].Function.Name)
	}
	if !strings.Contains(res.ToolCalls[0].Function.Arguments, "AGENTS.md") {
		t.Errorf("expected the preserved arguments to target AGENTS.md, got %q", res.ToolCalls[0].Function.Arguments)
	}
	if !strings.Contains(res.ReasoningContent, "read AGENTS.md") {
		t.Errorf("ReasoningContent should capture the thought before the call, got %q", res.ReasoningContent)
	}
	if strings.Contains(res.ReasoningContent, "<tool_call>") || strings.Contains(res.ReasoningContent, "</thought>") {
		t.Errorf("ReasoningContent must not contain XML tags, got %q", res.ReasoningContent)
	}
	if strings.TrimSpace(res.CleanText) != "" {
		t.Errorf("expected empty CleanText for a pure tool turn, got %q", res.CleanText)
	}
}

func TestArrangeGeminiOutputWithTools_NestedThoughtTags(t *testing.T) {
	// Exact v3.5.3 Neon Msg 03 shape: an orphan <thought> is opened inside the
	// first thought and the stream stops before any closing tag.
	raw := "<thought>\nLet's review what was requested and what has been done:\n" +
		"1. `audit-2026-11/README.md` was edited to fix the code block (line 55 added ```bash).\n" +
		"2. Now let's check tests or coverage metrics... or check if any<thought>\n" +
		"J'ai effectué la correction du bloc de code manquant dans `audit-2"

	res := ArrangeGeminiOutputWithTools(raw, openCodeToolsFixture())
	if res.IsToolCall {
		t.Fatal("a nested thought-only turn must not be classified as a tool call")
	}
	if !strings.Contains(res.ReasoningContent, "Let's review what was requested") {
		t.Errorf("ReasoningContent should capture the first thought segment, got %q", res.ReasoningContent)
	}
	if !strings.Contains(res.ReasoningContent, "J'ai effectué la correction") {
		t.Errorf("ReasoningContent should capture the nested segment, got %q", res.ReasoningContent)
	}
	if res.CleanText != "" {
		t.Errorf("CleanText must be empty for a nested thought-only turn, got %q", res.CleanText)
	}
	for _, tag := range []string{"<thought>", "</thought>", "<thinking>", "</thinking>"} {
		if strings.Contains(res.ReasoningContent, tag) {
			t.Errorf("ReasoningContent leaked nested/orphan tag %q: %q", tag, res.ReasoningContent)
		}
	}
}

func TestArrangeGeminiOutputWithTools_ClosedThoughtQuotingToolCallTag(t *testing.T) {
	// A closed thought that merely quotes the <tool_call> opening tag is
	// reasoning, not an action switch. The implicit "any<thought>" nesting
	// shape must not leak the rest of the reasoning into visible content.
	raw := "<thought>I will emit a <tool_call> block now, but first let me verify.</thought>\nVisible answer."

	res := ArrangeGeminiOutputWithTools(raw, openCodeToolsFixture())
	if res.IsToolCall {
		t.Fatal("a literal <tool_call> mention inside a closed thought must not be executed")
	}
	if res.CleanText != "Visible answer." {
		t.Errorf("CleanText must contain only the visible answer, got %q", res.CleanText)
	}
	for _, fragment := range []string{"block now", "first let me verify", "<tool_call>"} {
		if strings.Contains(res.CleanText, fragment) {
			t.Errorf("CleanText leaked reasoning fragment %q: %q", fragment, res.CleanText)
		}
	}
	if !strings.Contains(res.ReasoningContent, "block now") {
		t.Errorf("ReasoningContent should keep the quoted mention, got %q", res.ReasoningContent)
	}
}

func TestArrangeGeminiOutputWithTools_NestedClosedThoughts(t *testing.T) {
	// Nested opening tags are depth-counted so the first inner closing tag
	// cannot end the outer block and leak its tail as visible content.
	raw := "<thought>Outer <thought>inner</thought> tail.</thought>Visible."

	res := ArrangeGeminiOutputWithTools(raw, openCodeToolsFixture())
	if res.IsToolCall {
		t.Fatal("nested reasoning must not be classified as a tool call")
	}
	if res.CleanText != "Visible." {
		t.Errorf("CleanText must contain only the visible tail, got %q", res.CleanText)
	}
	for _, fragment := range []string{"Outer", "inner", "tail."} {
		if !strings.Contains(res.ReasoningContent, fragment) {
			t.Errorf("ReasoningContent should capture %q, got %q", fragment, res.ReasoningContent)
		}
	}
}

func decodeCompactSchema(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal compact schema: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal compact schema: %v", err)
	}
	return decoded
}

func TestCompactParametersSchema_RequiredFiltering(t *testing.T) {
	t.Run("drops phantom required entries", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactParametersSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"type": "string"}},
			"required":   []any{"a", "b"},
		}))
		required, ok := decoded["required"].([]any)
		if !ok {
			t.Fatalf("required must survive when one entry is valid: %#v", decoded)
		}
		if len(required) != 1 || required[0] != "a" {
			t.Fatalf("required = %#v, want [\"a\"]", required)
		}
	})

	t.Run("omits required when every entry is phantom", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactParametersSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"type": "string"}},
			"required":   []any{"b", "c"},
		}))
		if _, exists := decoded["required"]; exists {
			t.Fatalf("an entirely phantom required list must be omitted: %#v", decoded)
		}
	})

	t.Run("keeps required for an opaque object without properties", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactParametersSchema(map[string]any{
			"type":     "object",
			"required": []any{"a"},
		}))
		required, ok := decoded["required"].([]any)
		if !ok || len(required) != 1 || required[0] != "a" {
			t.Fatalf("opaque object required list must be preserved: %#v", decoded)
		}
	})

	t.Run("filters nested object requirements", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactPropertySchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"config": map[string]any{
					"type":       "object",
					"properties": map[string]any{"x": map[string]any{"type": "string"}},
					"required":   []any{"x", "y"},
				},
			},
		}, 0))
		props, ok := decoded["properties"].(map[string]any)
		if !ok {
			t.Fatalf("properties missing from compacted object: %#v", decoded)
		}
		config, ok := props["config"].(map[string]any)
		if !ok {
			t.Fatalf("nested config schema missing: %#v", props)
		}
		required, ok := config["required"].([]any)
		if !ok || len(required) != 1 || required[0] != "x" {
			t.Fatalf("nested required = %#v, want [\"x\"]", config["required"])
		}
	})
}

func TestCompactPropertySchema_ArrayMissingItems(t *testing.T) {
	t.Run("injects a default items schema", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactPropertySchema(map[string]any{"type": "array"}, 0))
		items, ok := decoded["items"].(map[string]any)
		if !ok || items["type"] != "string" {
			t.Fatalf("array without items must get {\"type\":\"string\"}: %#v", decoded)
		}
	})

	t.Run("is case insensitive", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactPropertySchema(map[string]any{"type": "ARRAY"}, 0))
		items, ok := decoded["items"].(map[string]any)
		if !ok || items["type"] != "string" {
			t.Fatalf("ARRAY without items must get a default items schema: %#v", decoded)
		}
	})

	t.Run("preserves declared items", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactPropertySchema(map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "integer"},
		}, 0))
		items, ok := decoded["items"].(map[string]any)
		if !ok || items["type"] != "integer" {
			t.Fatalf("declared items must be preserved: %#v", decoded)
		}
	})

	t.Run("replaces null items", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactPropertySchema(map[string]any{
			"type":  "array",
			"items": nil,
		}, 0))
		items, ok := decoded["items"].(map[string]any)
		if !ok || items["type"] != "string" {
			t.Fatalf("null items must be replaced by the default: %#v", decoded)
		}
	})

	t.Run("recurses into nested arrays", func(t *testing.T) {
		decoded := decodeCompactSchema(t, compactPropertySchema(map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "array"},
		}, 0))
		items, _ := decoded["items"].(map[string]any)
		nested, ok := items["items"].(map[string]any)
		if !ok || nested["type"] != "string" {
			t.Fatalf("nested array must also receive items: %#v", decoded)
		}
	})
}

// compactCatalog decodes the tool catalog embedded in a FormatToolsInstruction
// prompt, which is the exact text the model receives.
func compactCatalog(t *testing.T, out string) []map[string]any {
	t.Helper()
	const fence = "```json\n"
	start := strings.Index(out, fence)
	if start == -1 {
		t.Fatalf("tool catalog not found")
	}
	start += len(fence)
	relEnd := strings.Index(out[start:], "\n```")
	if relEnd == -1 {
		t.Fatalf("tool catalog closing fence not found")
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(out[start:start+relEnd]), &decoded); err != nil {
		t.Fatalf("tool catalog is not valid JSON: %v", err)
	}
	return decoded
}

func TestInjectStrictParameterSignatures(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		want   []string
		absent []string
	}{
		{
			name: "required markers and declaration order",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
					"timeout": map[string]any{"type": "integer"},
				},
				"required": []any{"path", "content"},
			},
			want: []string{
				"path string, REQUIRED",
				"content string, REQUIRED",
				"timeout integer",
			},
			absent: []string{"timeout integer, REQUIRED"},
		},
		{
			name: "enum and array item types",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":  map[string]any{"type": "string", "enum": []any{"fast", "safe"}},
					"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"flag":  map[string]any{"type": "boolean"},
				},
				"required": []any{"mode"},
			},
			want: []string{
				"mode string enum[fast|safe]",
				"paths array of string",
				"flag boolean",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := []map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name":        "demo",
					"description": "Demo tool",
					"parameters":  tc.schema,
				},
			}}
			catalog := compactCatalog(t, FormatToolsInstruction(tools))
			if len(catalog) != 1 {
				t.Fatalf("expected 1 tool, got %d", len(catalog))
			}
			description, _ := catalog[0]["description"].(string)
			if !strings.Contains(description, "STRICT PARAMETERS:") {
				t.Fatalf("signature missing from description: %q", description)
			}
			for _, expected := range tc.want {
				if !strings.Contains(description, expected) {
					t.Errorf("signature %q missing from %q", expected, description)
				}
			}
			for _, unexpected := range tc.absent {
				if strings.Contains(description, unexpected) {
					t.Errorf("signature must not contain %q: %q", unexpected, description)
				}
			}
		})
	}
}

func TestInjectStrictParameterSignatures_ComputedOnCompactedSchema(t *testing.T) {
	// `ghost` is declared required but absent from properties: the compaction
	// drops it from the emitted schema, so the signature must not advertise it.
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "write",
			"description": "Write a file",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []any{"path", "content", "ghost"},
			},
		},
	}}
	catalog := compactCatalog(t, FormatToolsInstruction(tools))
	description, _ := catalog[0]["description"].(string)
	if strings.Contains(description, "ghost") {
		t.Fatalf("signature advertised a parameter absent from the schema: %q", description)
	}
	if !strings.Contains(description, "path string, REQUIRED") {
		t.Fatalf("declared parameters missing from signature: %q", description)
	}
}

func TestInjectStrictParameterSignatures_Idempotent(t *testing.T) {
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "read",
			"description": "Read a file",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []any{"path"},
			},
		},
	}}
	first := compactCatalog(t, FormatToolsInstruction(tools))
	// Feed the produced description back in: the guard must not double it.
	fn := tools[0]["function"].(map[string]any)
	fn["description"] = first[0]["description"]
	second := compactCatalog(t, FormatToolsInstruction(tools))

	firstDesc, _ := first[0]["description"].(string)
	secondDesc, _ := second[0]["description"].(string)
	if firstDesc != secondDesc {
		t.Fatalf("signature injection is not idempotent:\nfirst=%q\nsecond=%q", firstDesc, secondDesc)
	}
	if strings.Count(secondDesc, "STRICT PARAMETERS:") != 1 {
		t.Fatalf("signature injected twice: %q", secondDesc)
	}
}

func TestInjectStrictParameterSignatures_NoParameters(t *testing.T) {
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "list",
			"description": "List a directory",
		},
	}}
	catalog := compactCatalog(t, FormatToolsInstruction(tools))
	description, _ := catalog[0]["description"].(string)
	if strings.Contains(description, "STRICT PARAMETERS:") {
		t.Fatalf("a parameterless tool must not advertise a signature: %q", description)
	}
}

func TestFormatToolsInstruction_Hardened(t *testing.T) {
	out := FormatToolsInstruction([]map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "write",
			"description": "Write a file",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []any{"path"},
			},
		},
	}})
	for _, required := range []string{
		"Metadata-First",
		"NEVER guess parameter names from prior training data",
		"STRICT PARAMETERS",
		"the schema wins",
	} {
		if !strings.Contains(out, required) {
			t.Errorf("hardened rule 4 must contain %q", required)
		}
	}
	// The hardening must live inside the existing numbered rule, not in a new
	// block: the prefix of the prompt has to stay stable for session reuse.
	if strings.Count(out, "Metadata-First") != 1 {
		t.Errorf("rule 4 must be merged, not duplicated")
	}
}
