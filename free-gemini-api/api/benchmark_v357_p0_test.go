package api

import (
	"os"
	"strings"
	"testing"

	"goapi/gemini"
)

// v3.5.7 in-flight benchmark regression tests. The fixtures under testdata are
// the exact visible strings from the Hydrogen and Argon transcripts.

func readBenchmarkFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read benchmark fixture %s: %v", name, err)
	}
	return string(data)
}

func TestPlanSessionTurn_IdentityAndContinuationAnchor(t *testing.T) {
	key := "benchmark-v357-plan-" + t.Name()
	flat := "User: reorganize the documentation\nAssistant: starting\n"
	toolResult := "[Tool Result for shell]: (no output)\n"
	commitSessionTurn(key, flat, "conv-1", "resp-1", "choice-1", "worker-1")

	// A different worker means the client-side conversation state is gone.
	if prompt, reset := planSessionTurn(key, "worker-2", "conv-1", flat+toolResult); !reset || prompt != flat+toolResult {
		t.Fatalf("worker mismatch must force a full replay: reset=%v prompt=%q", reset, prompt)
	}
	// A different upstream conversation means the transcript suffix is orphaned.
	if prompt, reset := planSessionTurn(key, "worker-1", "conv-2", flat+toolResult); !reset || prompt != flat+toolResult {
		t.Fatalf("conversation mismatch must force a full replay: reset=%v prompt=%q", reset, prompt)
	}
	// Same identity and a tool-only delta: anchor the continuation.
	prompt, reset := planSessionTurn(key, "worker-1", "conv-1", flat+toolResult)
	if reset {
		t.Fatal("a same-identity extension must produce a delta")
	}
	if prompt != continuationAnchor+toolResult {
		t.Fatalf("tool-only delta must be anchored:\n got %q\nwant %q", prompt, continuationAnchor+toolResult)
	}
	// A real user turn must not be framed as a continuation.
	prompt, reset = planSessionTurn(key, "worker-1", "conv-1", flat+"User: continue with AGENTS.md\n")
	if reset || prompt != "User: continue with AGENTS.md\n" {
		t.Fatalf("user delta must stay unanchored: reset=%v prompt=%q", reset, prompt)
	}
	// The ephemeral anchor must never enter the committed lastFlat.
	sessionTurnMu.Lock()
	stored := sessionTurnState[key].lastFlat
	sessionTurnMu.Unlock()
	if strings.Contains(stored, continuationAnchor) {
		t.Fatal("continuationAnchor leaked into the committed transcript")
	}
	if stored != flat {
		t.Fatalf("lastFlat changed unexpectedly: %q", stored)
	}
}

func TestIsConversationalReset_NeonGreeting(t *testing.T) {
	tools := agenticTestTools()
	for _, text := range []string{
		"Hello! How can I help you today?",
		"Bonjour ! Comment puis-je vous aider ?",
		"Hey there! What can I do for you?",
	} {
		if !isConversationalReset(text, tools) {
			t.Errorf("greeting %q was not classified as a conversational reset", text)
		}
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("greeting %q must be unusable on a tool-bearing request", text)
		}
	}
	for _, text := range []string{
		"Le plan d'action pour",
		"How do I fix the failing test in read.go?",
		"Hello, can you explain why the hello world test fails?",
		"README.md",
	} {
		if isConversationalReset(text, tools) {
			t.Errorf("non-greeting %q was falsely classified as a conversational reset", text)
		}
	}
	if isConversationalReset("Hello! How can I help you today?", nil) {
		t.Fatal("a greeting without tools may be a legitimate answer")
	}
	// A real tool call wins even when the visible preamble is a greeting.
	call := "Hello!\n<tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"AGENTS.md\"}}</tool_call>"
	if isUnusableModelOutput(call, tools) {
		t.Fatal("a greeting next to a validated tool call must remain usable")
	}
}

func TestQuarantineUntaggedToolJSON_HydrogenLeak(t *testing.T) {
	leak := readBenchmarkFixture(t, "benchmark_v357_hydrogen_leak.txt")
	if len(leak) < 900 {
		t.Fatalf("unexpected leak fixture size: %d bytes", len(leak))
	}
	if strings.Contains(leak, `"path"`) {
		t.Fatal("the Hydrogen leak fixture must not contain a path key")
	}
	tools := agenticTestTools()
	catalog, err := gemini.DefaultToolSchemaRegistry.CompileTools(tools)
	if err != nil {
		t.Fatalf("compile tool catalog: %v", err)
	}

	clean, hasMutation, promoted := gemini.QuarantineUntaggedToolJSON(leak, catalog)
	if !hasMutation {
		t.Fatal("the truncated write leak must be reported as an untagged mutation")
	}
	if promoted != nil {
		t.Fatalf("a mutating/truncated payload must never be promoted: %+v", promoted)
	}
	if strings.Contains(clean, `"name"`) {
		t.Fatalf("quarantine left tool JSON in the visible text: %q", clean)
	}

	arranged := gemini.ArrangeGeminiOutputWithTools(leak, tools)
	if arranged.IsToolCall || len(arranged.ToolCalls) != 0 {
		t.Fatalf("the leak must not become a tool call: %+v", arranged.ToolCalls)
	}
	if !arranged.UntaggedMutation {
		t.Fatal("the arranger did not flag the quarantined untagged mutation")
	}
	if strings.Contains(arranged.CleanText, `"name"`) {
		t.Fatalf("CleanText still contains the leaked tool JSON: %q", arranged.CleanText)
	}

	// A complete, schema-valid read-only call is safe to promote.
	readJSON := `{"name":"read","arguments":{"path":"AGENTS.md"}}`
	clean, hasMutation, promoted = gemini.QuarantineUntaggedToolJSON(readJSON, catalog)
	if hasMutation {
		t.Fatal("a complete read-only call must not be classified as a mutation")
	}
	if promoted == nil || promoted.Function.Name != "read" {
		t.Fatalf("a complete read-only call must be promoted, got %+v", promoted)
	}
	if strings.Contains(clean, `"name"`) {
		t.Fatalf("promoted read JSON leaked into the visible text: %q", clean)
	}

	// A complete untagged write is quarantined, never promoted.
	writeJSON := `{"name":"write","arguments":{"path":"notes.md","content":"hello"}}`
	clean, hasMutation, promoted = gemini.QuarantineUntaggedToolJSON(writeJSON, catalog)
	if !hasMutation {
		t.Fatal("an untagged write must be classified as a mutation")
	}
	if promoted != nil {
		t.Fatalf("an untagged write must never be promoted: %+v", promoted)
	}
	if strings.Contains(clean, `"name"`) {
		t.Fatalf("untagged write JSON leaked into the visible text: %q", clean)
	}

	// A balanced write with a missing path is invalid and quarantined.
	invalidWrite := `{"name":"write","content":"hello"}`
	clean, hasMutation, promoted = gemini.QuarantineUntaggedToolJSON(invalidWrite, catalog)
	if !hasMutation || promoted != nil || strings.Contains(clean, `"name"`) {
		t.Fatalf("invalid write must be quarantined: mutation=%v promoted=%+v clean=%q", hasMutation, promoted, clean)
	}

	// A content-first object places the tool name after a large value; the
	// whole-object scan must still quarantine a mutation and promote a read.
	nameLastWrite := `{"content":"hello","name":"write"}`
	clean, hasMutation, promoted = gemini.QuarantineUntaggedToolJSON(nameLastWrite, catalog)
	if !hasMutation || promoted != nil || strings.Contains(clean, `"name"`) {
		t.Fatalf("name-last write must be quarantined: mutation=%v promoted=%+v clean=%q", hasMutation, promoted, clean)
	}
	nameLastRead := `{"arguments":{"path":"AGENTS.md"},"name":"read"}`
	clean, hasMutation, promoted = gemini.QuarantineUntaggedToolJSON(nameLastRead, catalog)
	if hasMutation || promoted == nil || promoted.Function.Name != "read" {
		t.Fatalf("name-last read must be promoted: mutation=%v promoted=%+v clean=%q", hasMutation, promoted, clean)
	}

	// A fenced untagged write is removed together with its empty fence.
	fenced := "```json\n" + writeJSON + "\n```"
	clean, hasMutation, promoted = gemini.QuarantineUntaggedToolJSON(fenced, catalog)
	if !hasMutation || promoted != nil {
		t.Fatalf("fenced write must be quarantined: mutation=%v promoted=%+v", hasMutation, promoted)
	}
	if strings.Contains(clean, "```") || strings.Contains(clean, `"name"`) {
		t.Fatalf("quarantine left a fence or JSON behind: %q", clean)
	}
}

func TestSessionLedger_UnbackedCompletion_HydrogenS7(t *testing.T) {
	tools := agenticTestTools()
	final := readBenchmarkFixture(t, "benchmark_v357_hydrogen_final.txt")
	key := "benchmark-v357-ledger-" + t.Name()

	if !isUnbackedCompletion(final, tools, key) {
		t.Fatal("the Hydrogen completion report must be unbacked when no mutation was confirmed")
	}

	// A confirmed write on docs/README.md does not back the other cited files.
	recordSessionToolCallPaths(key, "call-readme", "write", `{"path":"docs/README.md","content":"# Docs"}`)
	recordSessionToolResultOutcome(key, "call-readme", "Created file successfully: docs/README.md", false)
	if !isUnbackedCompletion(final, tools, key) {
		t.Fatal("the Hydrogen report must remain unbacked while docs/architecture/ and AGENTS.md are missing")
	}

	backed := "La mise à jour de `docs/README.md` a été entièrement réalisée."
	if isUnbackedCompletion(backed, tools, key) {
		t.Fatal("a claim citing only the confirmed docs/README.md must be accepted")
	}

	unbackedAgents := "La mise à jour de `AGENTS.md` a été entièrement réalisée."
	if !isUnbackedCompletion(unbackedAgents, tools, key) {
		t.Fatal("a claim citing AGENTS.md without a confirmed mutation must be blocked")
	}

	// A future statement is not a completion claim.
	if isUnbackedCompletion("Les fichiers seront mis à jour prochainement.", tools, key) {
		t.Fatal("a future statement must not be classified as an unbacked completion")
	}
	// A question is not a completion claim, even with a past-tense marker.
	if isUnbackedCompletion("Les fichiers `AGENTS.md` ont-ils été entièrement réalisés ?", tools, key) {
		t.Fatal("a question must not be classified as an unbacked completion")
	}
	// A real tool call is an action, not a completion claim.
	call := `<tool_call>{"name":"write","arguments":{"path":"AGENTS.md","content":"# AGENTS"}}</tool_call>`
	if isUnbackedCompletion(call, tools, key) {
		t.Fatal("a validated tool call must never be classified as an unbacked completion")
	}

	// A failed mutation is never evidence.
	failedKey := key + "-failed"
	recordSessionToolCallPaths(failedKey, "call-agents", "write", `{"path":"AGENTS.md","content":"# AGENTS"}`)
	recordSessionToolResultOutcome(failedKey, "call-agents", "oldString not found", true)
	if !isUnbackedCompletion(unbackedAgents, tools, failedKey) {
		t.Fatal("a failed write must not back a completion claim")
	}

	// A shell move confirms its destination path.
	shellKey := key + "-shell"
	recordSessionToolCallPaths(shellKey, "call-mv", "shell", `{"command":"git mv walkthrough.md docs/remediation/walkthrough_fiabilisation_v3.5.7.md"}`)
	recordSessionToolResultOutcome(shellKey, "call-mv", "(no output)", false)
	shellClaim := "Le déplacement vers `docs/remediation/walkthrough_fiabilisation_v3.5.7.md` a été entièrement réalisé."
	if isUnbackedCompletion(shellClaim, tools, shellKey) {
		t.Fatal("a confirmed shell move must back its destination path")
	}
}

func TestIsIncompleteFinalProse_ArgonProse(t *testing.T) {
	tools := agenticTestTools()
	for _, text := range []string{
		"Le plan d'action pour",
		"Je vais maintenant lire le fichier",
	} {
		if !isIncompleteFinalProse(text, tools) {
			t.Errorf("short unpunctuated prose %q must be classified as incomplete", text)
		}
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("short unpunctuated prose %q must be unusable with tools", text)
		}
	}
	for _, text := range []string{
		"Done.",
		"README.md",
		"Voici la réponse finale.",
		"Le plan d'action pour le fichier `README.md`",
	} {
		if isIncompleteFinalProse(text, tools) {
			t.Errorf("legitimate short answer %q was falsely classified as incomplete", text)
		}
	}
	if isIncompleteFinalProse("Le plan d'action pour", nil) {
		t.Fatal("incomplete prose without tools may be a legitimate answer")
	}
	call := `<tool_call>{"name":"read","arguments":{"path":"AGENTS.md"}}</tool_call>`
	if isIncompleteFinalProse(call, tools) {
		t.Fatal("a validated tool call is never incomplete prose")
	}
}

func TestTerminalFinishReason_ErrorOnUnusableOutput(t *testing.T) {
	tools := agenticTestTools()
	outputs := map[string]string{
		"neon_greeting":   "Hello! How can I help you today?",
		"hydrogen_s7":     readBenchmarkFixture(t, "benchmark_v357_hydrogen_final.txt"),
		"argon_truncated": "Le plan d'action pour",
	}
	for name, text := range outputs {
		arranged := gemini.ArrangeGeminiOutputWithTools(text, tools)
		if !shouldBlockUnusableToolTurn(arranged, text, tools) {
			t.Errorf("%s: unusable tool-bearing output must be blocked at the end of retries", name)
		}
	}
	chunk := newStreamErrorChunk("chatcmpl-test", "gemini-4-hydrogen", 0, "[Error: unusable]")
	if len(chunk.Choices) != 1 || chunk.Choices[0].FinishReason != "error" {
		t.Fatalf("terminal error chunk must carry finish_reason %q, got %+v", "error", chunk.Choices)
	}
}
