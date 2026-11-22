package gemini

import (
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

	for _, forbidden := range []string{"0 remaining", "maximum number of tool", "Disregard any", "<tools>"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("tool instructions must not contain %q", forbidden)
		}
	}
	for _, required := range []string{"Read-Parallel", "Write-Serial", "Metadata-First", "available on every turn"} {
		if !strings.Contains(out, required) {
			t.Errorf("tool instructions must contain %q", required)
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
