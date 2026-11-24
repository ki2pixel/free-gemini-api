package api

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"goapi/gemini"
)

// v3.5.7 Wave P2 regression and formal-property tests: thought-artifact
// sanitization, bilingual S7 detection with validation evidence, and the four
// invariants fixed by the in-flight benchmark arbitration.

func TestArgonSanitizedThoughtArtifact(t *testing.T) {
	// Exact Argon v3.5.7 shape: a malformed <thought prefix without '>' inside
	// the reasoning block, a 21-character visible fragment, no tool call.
	raw := `<thought>
L'utilisateur a présenté un plan. L'écriture du hub<thought
L'utilisateur a présenté la structure et les vérifications (0 restants).
</thought>Le plan d'action pour`

	res := gemini.ArrangeGeminiOutputWithTools(raw, agenticTestTools())
	if res.CleanText != "Le plan d'action pour" {
		t.Fatalf("CleanText = %q, want %q", res.CleanText, "Le plan d'action pour")
	}
	if !strings.Contains(res.ReasoningContent, "<thought") {
		t.Fatalf("ReasoningContent must preserve the raw malformed token, got %q", res.ReasoningContent)
	}
	if strings.Contains(res.CleanText, "<thought") {
		t.Fatalf("the malformed token leaked into CleanText: %q", res.CleanText)
	}
	if res.IsToolCall || len(res.ToolCalls) != 0 {
		t.Fatalf("the Argon charge must not become a tool call: %+v", res.ToolCalls)
	}
}

func TestProperty_MidValueMutationRejection(t *testing.T) {
	tools := agenticTestTools()
	rng := rand.New(rand.NewSource(3572))

	for i := 0; i < 200; i++ {
		content := benchmarkRandomToken(rng, 8+(i%48))
		path := fmt.Sprintf("docs/generated/file-%d.md", i)
		var payload string
		switch i % 3 {
		case 0:
			payload = fmt.Sprintf(`{"name":"write","arguments":{"path":%q,"content":"%s`, path, content)
		case 1:
			payload = fmt.Sprintf(`{"name":"edit","arguments":{"path":%q,"old_string":"%s`, path, content)
		default:
			payload = fmt.Sprintf(`{"name":"shell","arguments":{"command":"go test ./... && echo %s`, content)
		}

		variants := []string{
			payload,
			"<tool_call>" + payload,
			"<tool_call>\n" + payload + "\n</tool_call>",
			"```json\n" + payload + "\n```",
		}
		for _, raw := range variants {
			if !gemini.HasTruncatedMutationToolCall(raw) {
				t.Fatalf("generated payload was not recognized as a mid-value truncation: %q", raw)
			}
			arranged := gemini.ArrangeGeminiOutputWithTools(raw, tools)
			if arranged.IsToolCall || len(arranged.ToolCalls) != 0 {
				t.Fatalf("truncated mutation was promoted to a tool call: %+v (raw=%q)", arranged.ToolCalls, raw)
			}
		}
	}
}

func TestProperty_QuarantineCompleteness(t *testing.T) {
	tools := agenticTestTools()
	declaredJSON := regexp.MustCompile(`(?i)"(?:name|tool|function)"\s*:\s*"(?:write|read)"`)
	rng := rand.New(rand.NewSource(3573))

	inputs := []string{
		`{"name":"write","arguments":{"path":"a.md","content":"hello"}}`,
		`{"name":"read","arguments":{"path":"AGENTS.md"}}`,
		`{"name":"write","arguments":{"path":"a.md","content":"unterminated`,
		`{"name":"read","arguments":{"path":"unterminated`,
		`{"content":"hello","name":"write"}`,
		`{"arguments":{"path":"AGENTS.md"},"name":"read"}`,
		`{"function":{"name":"write","arguments":{"path":"b.md","content":"x"}}}`,
		"```json\n" + `{"name":"write","arguments":{"path":"c.md","content":"x"}}` + "\n```",
		`intro {"name":"write","arguments":{"path":"d.md","content":"x"}} outro`,
	}
	for i := 0; i < 100; i++ {
		token := benchmarkRandomToken(rng, 12+i%32)
		inputs = append(inputs, fmt.Sprintf(
			`{"name":%q,"arguments":{"path":"docs/%s.md","content":"%s"}}`,
			[]string{"write", "read"}[i%2], token, benchmarkRandomToken(rng, 16),
		))
	}

	for _, raw := range inputs {
		arranged := gemini.ArrangeGeminiOutputWithTools(raw, tools)
		if declaredJSON.MatchString(arranged.CleanText) {
			t.Fatalf("CleanText still contains a declared-tool JSON object: %q (raw=%q)", arranged.CleanText, raw)
		}
	}
}

func TestProperty_DeltaIdentitySafety(t *testing.T) {
	base := "User: reorganize the documentation\nAssistant: starting\n"
	identities := []struct {
		worker string
		conv   string
	}{
		{"worker-1", "conv-1"},
		{"worker-2", "conv-1"},
		{"worker-1", "conv-2"},
		{"", "conv-1"},
		{"worker-1", ""},
	}

	for i, committed := range identities {
		if committed.worker == "" || committed.conv == "" {
			continue
		}
		key := fmt.Sprintf("p2-delta-%s-%d", t.Name(), i)
		cleanupSessionTrackerTest(t, key)
		commitSessionTurn(key, base, committed.conv, "resp-1", "choice-1", committed.worker)

		for _, current := range identities {
			flat := base + "[Tool Result for shell]: (no output)\n"
			plan := planSessionTurnWithReason(key, current.worker, current.conv, flat)
			sameIdentity := current.worker == committed.worker && current.conv == committed.conv
			if !sameIdentity {
				if !plan.needReset || plan.prompt != flat {
					t.Fatalf("identity mismatch (%+v vs %+v) must force a full replay: %+v", current, committed, plan)
				}
				continue
			}
			if plan.needReset {
				t.Fatalf("same identity must allow a delta: %+v", plan)
			}
			if !strings.HasPrefix(plan.prompt, continuationAnchor) {
				t.Fatalf("tool-only delta was not anchored: %q", plan.prompt)
			}
		}

		// Under a proven identity, only an exact prefix extension is a delta.
		for _, flat := range []string{
			base,
			base + "User: next step\n",
		} {
			plan := planSessionTurnWithReason(key, committed.worker, committed.conv, flat)
			if flat == base {
				if !plan.needReset || plan.reason != "identical" {
					t.Fatalf("identical transcript must replay: %+v", plan)
				}
				continue
			}
			if plan.needReset || plan.prompt != "User: next step\n" {
				t.Fatalf("valid user delta must stay unanchored: %+v", plan)
			}
		}
		plan := planSessionTurnWithReason(key, committed.worker, committed.conv, "divergent history")
		if !plan.needReset || plan.reason != "prefix_mismatch" {
			t.Fatalf("divergent history must replay: %+v", plan)
		}
	}
}

func TestProperty_MonotonicRetryTermination(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		for _, hasTools := range []bool{true, false} {
			functional := maxChatAttempts(streaming, hasTools)
			bound := maxChatLoopIterations(streaming, hasTools)
			if bound != functional+maxTransportRetries+maxEmptyGenerationRetries {
				t.Fatalf("loop bound is not the sum of the three budgets: %d", bound)
			}
			if hasTools && bound > 8 {
				t.Fatalf("tool-bearing loop bound = %d, want <= 8", bound)
			}
			// Every budget is monotone and capped: no combination can add more
			// iterations than the declared retry slots.
			for transport := 0; transport <= maxTransportRetries; transport++ {
				for empty := 0; empty <= maxEmptyGenerationRetries; empty++ {
					iterations := functional + transport + empty
					if hasTools && iterations > 8 {
						t.Fatalf("retry combination exceeds 8 iterations: %d", iterations)
					}
				}
			}
		}
	}

	if got := maxToolChatAttempts + maxTransportRetries + maxEmptyGenerationRetries; got != 8 {
		t.Fatalf("tool-bearing worst-case iterations = %d, want 8", got)
	}
	for retries := 0; retries <= maxTransportRetries+1; retries++ {
		want := retries < maxTransportRetries
		if got := shouldRetryTransport(gemini.ErrConversationExpired, retries); got != want {
			t.Fatalf("shouldRetryTransport(retries=%d) = %v, want %v", retries, got, want)
		}
	}
}

func TestPseudoReportLexicon_P2CompletionForms(t *testing.T) {
	tools := agenticTestTools()
	snippets := []string{
		"tous les objectifs ont été atteints",
		"l'ensemble des étapes a été exécuté",
		"l'ensemble des étapes a été exécutée",
		"les vérifications ont confirmé",
		"les modifications ci-dessus ont été appliquées",
		"les fichiers ont été créés et organisés",
		"l'opération est terminée avec succès",
		"the implementation is complete",
		"all planned steps have been executed",
		"all files have been successfully created",
		"the tasks have been completed as requested",
		"here is the summary of the completed work",
	}
	for _, snippet := range snippets {
		text := "Préambule. " + snippet + "."
		if !isPseudoProgressReport(text, tools) {
			t.Errorf("pseudo-report marker %q was not detected", snippet)
		}
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("pseudo-report marker %q must be unusable with tools", snippet)
		}
	}
}

func TestUnbackedCompletion_ValidationEvidence(t *testing.T) {
	tools := agenticTestTools()
	claim := "Les tests unitaires validés avec succès."

	key := "p2-validation-" + t.Name()
	cleanupSessionLedgerTest(t, key)
	if !isUnbackedCompletion(claim, tools, key) {
		t.Fatal("a test-success claim without any validation command must be blocked")
	}

	// A confirmed mutation does not prove that tests ran.
	recordSessionToolCallPaths(key, "call-write", "write", `{"path":"docs/README.md","content":"# Docs"}`)
	recordSessionToolResultOutcome(key, "call-write", "Created docs/README.md", false)
	if !isUnbackedCompletion(claim, tools, key) {
		t.Fatal("a mutation must not back a test-success claim")
	}

	// A non-validation shell command is not evidence either.
	recordSessionToolCallPaths(key, "call-ls", "shell", `{"command":"ls -la"}`)
	recordSessionToolResultOutcome(key, "call-ls", "docs", false)
	if !isUnbackedCompletion(claim, tools, key) {
		t.Fatal("a non-validation shell command must not back a test-success claim")
	}

	// A successful validation command backs the claim.
	recordSessionToolCallPaths(key, "call-test", "shell", `{"command":"go test ./..."}`)
	recordSessionToolResultOutcome(key, "call-test", "ok goapi/...", false)
	if isUnbackedCompletion(claim, tools, key) {
		t.Fatal("a successful go test command must back the test-success claim")
	}

	// A failed validation command is never evidence.
	failedKey := key + "-failed"
	cleanupSessionLedgerTest(t, failedKey)
	recordSessionToolCallPaths(failedKey, "call-test", "shell", `{"command":"go test ./..."}`)
	recordSessionToolResultOutcome(failedKey, "call-test", "FAIL goapi/gemini", true)
	if !isUnbackedCompletion(claim, tools, failedKey) {
		t.Fatal("a failed test command must not back the claim")
	}

	// audit_check.sh and go build are both validation evidence.
	auditKey := key + "-audit"
	cleanupSessionLedgerTest(t, auditKey)
	recordSessionToolCallPaths(auditKey, "call-audit", "shell", `{"command":"./scripts/audit_check.sh"}`)
	recordSessionToolResultOutcome(auditKey, "call-audit", "ALL CHECKS PASSED", false)
	if isUnbackedCompletion("audit_check.sh a validé la suite.", tools, auditKey) {
		t.Fatal("a successful audit command must back the audit claim")
	}

	buildKey := key + "-build"
	cleanupSessionLedgerTest(t, buildKey)
	recordSessionToolCallPaths(buildKey, "call-build", "shell", `{"command":"go build ./..."}`)
	recordSessionToolResultOutcome(buildKey, "call-build", "", false)
	if isUnbackedCompletion("Compilation réussie.", tools, buildKey) {
		t.Fatal("a successful go build command must back the compilation claim")
	}

	// The harness replays the assistant call and tool result on the next turn;
	// replaying them must populate the same evidence.
	replayKey := key + "-replay"
	cleanupSessionLedgerTest(t, replayKey)
	recordSessionLedgerFromMessages(replayKey, []OpenAIChatMessage{
		{
			Role: "assistant",
			ToolCalls: []OpenAIToolCall{{
				ID:   "call-replay",
				Type: "function",
				Function: OpenAIToolCallFunction{
					Name:      "shell",
					Arguments: `{"command":"go test -short ./..."}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call-replay",
			Content:    json.RawMessage(`"ok goapi/..."`),
		},
	})
	if isUnbackedCompletion(claim, tools, replayKey) {
		t.Fatal("replayed validation evidence must back the claim")
	}
}

func TestUnbackedCompletion_PathPunctuationNormalization(t *testing.T) {
	tools := agenticTestTools()
	key := "p2-path-punctuation-" + t.Name()
	cleanupSessionLedgerTest(t, key)

	recordSessionToolCallPaths(key, "call-readme", "write", `{"path":"docs/README.md","content":"# Docs"}`)
	recordSessionToolResultOutcome(key, "call-readme", "Created docs/README.md", false)

	claim := "La mise à jour de `docs/README.md.` a été entièrement réalisée."
	if isUnbackedCompletion(claim, tools, key) {
		t.Fatal("a claim path ending with sentence punctuation must normalize to the confirmed path")
	}

	recordSessionToolCallPaths(key, "call-agents", "write", `{"path":"AGENTS.md","content":"# AGENTS"}`)
	recordSessionToolResultOutcome(key, "call-agents", "Created AGENTS.md", false)
	basenameClaim := "La mise à jour de `AGENTS.md.` a été entièrement réalisée."
	if isUnbackedCompletion(basenameClaim, tools, key) {
		t.Fatal("a bare filename ending with sentence punctuation must normalize to the confirmed path")
	}
}

func benchmarkRandomToken(rng *rand.Rand, n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789_ -"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(b)
}

func cleanupSessionLedgerTest(t *testing.T, sessionKey string) {
	t.Helper()
	t.Cleanup(func() {
		sessionLedgerMu.Lock()
		delete(sessionLedgers, sessionKey)
		sessionLedgerMu.Unlock()
	})
}
