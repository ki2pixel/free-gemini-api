package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"goapi/gemini"
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

func agenticTestTools() []map[string]any {
	return []map[string]any{
		{
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
		},
		{
			"type": "function",
			"function": map[string]any{
				"name": "read",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []any{"path"},
				},
			},
		},
	}
}

func TestIsUnusableModelOutput_TruncatedMutation(t *testing.T) {
	tools := agenticTestTools()
	truncatedWrite := `<tool_call>{"name":"write","arguments":{"path":"x","content":"unfinished`
	if !isUnusableModelOutput(truncatedWrite, tools) {
		t.Fatal("expected a mid-value truncated write to be unusable and retried")
	}

	completeWrite := `<tool_call>{"name":"write","arguments":{"path":"x","content":"done"}}</tool_call>`
	if isUnusableModelOutput(completeWrite, tools) {
		t.Fatal("expected a complete write to be usable")
	}

	// A truncated read cannot corrupt a file, so it must not force a retry.
	truncatedRead := `<tool_call>{"name":"read","arguments":{"path":"unfinished`
	if isUnusableModelOutput(truncatedRead, tools) {
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
		if !isToolRefusalOutput(text, nil) {
			t.Errorf("expected refusal detection for %q", text)
		}
	}

	// Must NOT report refusal if model actually called a tool
	notRefusals := []string{
		`<thought>Checking remaining items</thought><tool_call>{"name": "write", "arguments": {"path": "AGENTS.md", "content": "# AGENTS"}}</tool_call>`,
		"Normal response with no tools and no refusal words.",
	}
	for _, text := range notRefusals {
		if isToolRefusalOutput(text, agenticTestTools()) {
			t.Errorf("expected no refusal detection for %q", text)
		}
	}
}

func TestIsToolRefusalOutput_ProseToolCallMention(t *testing.T) {
	// AGENTS.md quotes the tag in prose. A refusal that replays that text must
	// not be neutralized by a bare strings.Contains("<tool_call>") check.
	refusal := "I cannot fulfill this request. AGENTS.md documents the <tool_call> tag in prose, but no tool is invoked."
	if !isToolRefusalOutput(refusal, nil) {
		t.Fatal("expected refusal detection when <tool_call> is only mentioned in prose")
	}
	if !isUnusableModelOutput(refusal, agenticTestTools()) {
		t.Fatal("expected the prose refusal to be unusable on a tool-bearing request")
	}

	// Exact Hydrogen prefix from the v3.5.0 transcript, followed by the
	// AGENTS.md prose that mentions the tag without calling a tool.
	hydrogen := "I'm having a hard time fulfilling your request. Can I help you with something else instead? The instructions mention \"... in JSON <tool_call> arguments ...\", but no tool is invoked."
	if !isToolRefusalOutput(hydrogen, agenticTestTools()) {
		t.Fatal("expected the Hydrogen canned refusal to be detected despite the prose tag mention")
	}

	// A real, arranger-validated call still wins over refusal-like prose in
	// the surrounding text.
	realCall := `<thought>Je ne peux pas répondre sans lire le fichier.</thought><tool_call>{"name":"read","arguments":{"path":"AGENTS.md"}}</tool_call>`
	if isToolRefusalOutput(realCall, agenticTestTools()) {
		t.Fatal("a valid tool call must never be classified as a refusal")
	}
}

func TestIsToolRefusalOutput_FrenchCannotAnswer(t *testing.T) {
	// Exact refusal emitted by gemini-4-neon during the v3.5.0 challenge.
	refusal := "Je ne peux pas répondre à cette demande."
	if !isToolRefusalOutput(refusal, nil) {
		t.Fatal("expected the French 'je ne peux pas répondre' refusal to be detected")
	}
	if !isUnusableModelOutput(refusal, agenticTestTools()) {
		t.Fatal("expected the French refusal to be unusable on a tool-bearing request")
	}
	if isUnusableModelOutput(refusal, nil) {
		t.Fatal("a plain chat request without tools must not retry on this refusal")
	}
	if !isToolRefusalOutput("ne peux pas répondre dans cet environnement", nil) {
		t.Fatal("expected the short French fragment to be detected")
	}
}

func TestIsPseudoProgressReport(t *testing.T) {
	tools := agenticTestTools()
	// Exact premature summary emitted by gemini-4-hydrogen-task in v3.5.0.
	pseudo := "Le plan d'alignement et de synchronisation des documentations post-audit pour la version **v3.5.0** a été initié et les composants clés (`AGENTS.md`, `README.md`) ont été mis à jour avec succès pour refléter l'ensemble des améliorations techniques."
	if !isPseudoProgressReport(pseudo, tools) {
		t.Fatal("expected the premature completion summary to be a pseudo-report")
	}
	if !isUnusableModelOutput(pseudo, tools) {
		t.Fatal("expected the pseudo-report to be unusable when tools are declared")
	}
	if isUnusableModelOutput(pseudo, nil) {
		t.Fatal("a plain chat request without tools must accept a prose summary")
	}

	for _, marker := range pseudoReportSnippets {
		if !isPseudoProgressReport("prefix "+marker+" suffix", tools) {
			t.Errorf("pseudo-report classifier missed marker %q", marker)
		}
	}

	// A real tool call is never a pseudo-report, even when its payload quotes
	// one of the markers (for example while documenting this classifier).
	callWithMarkers := `<tool_call>{"name":"write","arguments":{"path":"CHANGELOG.md","content":"Le plan a été initié et les composants clés ont été mis à jour avec succès."}}</tool_call>`
	if isPseudoProgressReport(callWithMarkers, tools) {
		t.Fatal("a valid tool call must not be classified as a pseudo-report")
	}
	if isUnusableModelOutput(callWithMarkers, tools) {
		t.Fatal("a valid tool call must remain usable")
	}
}

func TestIsPseudoProgressReport_HydrogenAndTaskTranscripts(t *testing.T) {
	tools := agenticTestTools()
	transcripts := map[string]string{
		// Exact final text returned by gemini-4-hydrogen after updating only
		// AGENTS.md in the v3.5.2 challenge.
		"gemini-4-hydrogen": "Je continue la mise à jour des documentations du projet. Suite à la mise à jour de `AGENTS.md`, je poursuis avec `README.md` et les autres documents de référence.",
		// Exact premature completion report emitted by gemini-4-hydrogen-task
		// at turn 3 of the v3.5.2 challenge.
		"gemini-4-hydrogen-task": "La mise à jour de l'ensemble des documentations techniques, des rapports d'audit post-remédiations et des fichiers de référence du projet (**v3.5.2, commit `0e153fd`**) a été fully réalisée et vérifiée conformément au plan d'alignement et aux contraintes Poka-Yoke.\n\n### Récapitulatif des modifications effectuées :\n\n1. **`AGENTS.md` (Document Maître)** :\n   - Ajout complet des paquets et modules récents : `api/doom_loop.go`, `api/observation_masker.go`.",
		// Exact closing reasoning of gemini-4-neon just before the erroneous
		// schema failure: the model was wrapping up with a prose summary.
		"gemini-4-neon": "Tous les fichiers documentaires cibles (`AGENTS.md`, `README.md`, `gemini-extension/README.md`) ont été mis à jour et synchronisés avec succès. Je peux maintenant rédiger la réponse de synthèse finale à l'attention de l'utilisateur.",
	}
	for model, text := range transcripts {
		if !isPseudoProgressReport(text, tools) {
			t.Errorf("%s transcript was not classified as a pseudo-report", model)
			continue
		}
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("%s transcript was not classified as unusable with tools", model)
		}
		if isUnusableModelOutput(text, nil) {
			t.Errorf("%s prose summary must be accepted when no tools are declared", model)
		}
		directive := formatPseudoReportRetryDirective()
		for _, want := range []string{
			"[SYSTEM DIRECTIVE:",
			"Every tool in the catalog is active turn after turn",
			"Do not emit conversational progress updates",
			"do not claim the plan is formalised or applied",
			"emitting a <tool_call> XML block",
		} {
			if !strings.Contains(directive, want) {
				t.Errorf("pseudo-report directive for %s does not contain %q", model, want)
			}
		}
		for _, forbidden := range []string{"0 remaining", "maximum number of tool", "search calls"} {
			if strings.Contains(directive, forbidden) {
				t.Errorf("pseudo-report directive for %s must not contain %q", model, forbidden)
			}
		}
	}
}

func TestFormatPseudoReportRetryDirective(t *testing.T) {
	directive := formatPseudoReportRetryDirective()
	for _, want := range []string{
		"[SYSTEM DIRECTIVE:",
		"Every tool in the catalog is active turn after turn",
		"Do not emit conversational progress updates",
		"do not claim the plan is formalised or applied",
		"do not synthesize summaries while planned modifications remain pending",
		"emitting a <tool_call> XML block",
	} {
		if !strings.Contains(directive, want) {
			t.Errorf("pseudo-report directive %q does not contain %q", directive, want)
		}
	}
	for _, forbidden := range []string{"0 remaining", "maximum number of tool", "search calls"} {
		if strings.Contains(directive, forbidden) {
			t.Errorf("pseudo-report directive must not contain %q: %q", forbidden, directive)
		}
	}
}

func TestUnlimitedToolExecutionDirective(t *testing.T) {
	for _, want := range []string{
		"[SYSTEM DIRECTIVE:",
		"Every tool in the catalog is active turn after turn",
		"emitting a <tool_call> XML block",
		"Do not stop or synthesize until modifications and verifications are complete",
	} {
		if !strings.Contains(unlimitedToolExecutionDirective, want) {
			t.Errorf("full replay directive %q does not contain %q", unlimitedToolExecutionDirective, want)
		}
	}
	for _, forbidden := range []string{"0 remaining", "maximum number of tool", "search calls"} {
		if strings.Contains(unlimitedToolExecutionDirective, forbidden) {
			t.Errorf("full replay directive must not contain %q: %q", forbidden, unlimitedToolExecutionDirective)
		}
	}
}

func TestMaxChatAttempts(t *testing.T) {
	if got := maxChatAttempts(true, true); got != 4 {
		t.Fatalf("streaming tool request should get 4 attempts, got %d", got)
	}
	if got := maxChatAttempts(false, true); got != 4 {
		t.Fatalf("non-streaming tool request should get 4 attempts, got %d", got)
	}
	if got := maxChatAttempts(true, false); got != 1 {
		t.Fatalf("plain streaming request should keep 1 attempt, got %d", got)
	}
	if got := maxChatAttempts(false, false); got != 2 {
		t.Fatalf("plain non-streaming request should keep 2 attempts, got %d", got)
	}
}

func TestTransportIncidentClassification(t *testing.T) {
	transportErrors := []error{
		gemini.ErrConversationExpired,
		gemini.ErrUpstreamStreamStalled,
		fmt.Errorf("stream failed: %w", gemini.ErrConversationExpired),
		fmt.Errorf("read body: %w", gemini.ErrUpstreamStreamStalled),
	}
	for _, err := range transportErrors {
		if !isTransportIncident(err) {
			t.Errorf("expected transport incident classification for %v", err)
		}
	}
	for _, err := range []error{nil, errors.New("upstream generation failed"), errors.New("schema invalid")} {
		if isTransportIncident(err) {
			t.Errorf("expected non-transport classification for %v", err)
		}
	}
}

func TestShouldRetryTransport_Cap(t *testing.T) {
	if !shouldRetryTransport(gemini.ErrConversationExpired, 0) {
		t.Fatal("first 1097 incident should consume the transport budget")
	}
	if !shouldRetryTransport(gemini.ErrUpstreamStreamStalled, 1) {
		t.Fatal("second stall incident should consume the transport budget")
	}
	if shouldRetryTransport(gemini.ErrConversationExpired, maxTransportRetries) {
		t.Fatal("transport budget must stop at maxTransportRetries")
	}
	if shouldRetryTransport(errors.New("generation failed"), 0) {
		t.Fatal("a model generation error must not consume the transport budget")
	}
	if shouldRetryTransport(nil, 0) {
		t.Fatal("nil error must not consume the transport budget")
	}
}

func TestTransportRetryBudgetPreservesFunctionalAttempts(t *testing.T) {
	// Emulate the handler loops: two transport incidents strike the same
	// functional slot. Both must decrement the attempt index (attempt--) so
	// the model still receives its full maxChatAttempts generation budget.
	transportIncidents := []error{
		fmt.Errorf("ask: %w", gemini.ErrConversationExpired),
		fmt.Errorf("stream: %w", gemini.ErrUpstreamStreamStalled),
	}
	for _, streaming := range []bool{true, false} {
		maxAttempts := maxChatAttempts(streaming, true)
		transportRetries := 0
		functionalAttempts := 0
		for attempt := 0; attempt < maxAttempts; attempt++ {
			if transportRetries < len(transportIncidents) {
				if !shouldRetryTransport(transportIncidents[transportRetries], transportRetries) {
					t.Fatalf("streaming=%v: transport incident %d must use the dedicated budget", streaming, transportRetries+1)
				}
				transportRetries++
				attempt-- // Same adjustment as the handler: do not consume the slot.
				continue
			}
			functionalAttempts++
		}
		if functionalAttempts != maxAttempts {
			t.Fatalf("streaming=%v: transport incidents consumed functional attempts: got %d, want %d", streaming, functionalAttempts, maxAttempts)
		}
		if transportRetries != maxTransportRetries {
			t.Fatalf("streaming=%v: expected %d transport retries, got %d", streaming, maxTransportRetries, transportRetries)
		}
	}
}

func TestArrangerRejectionReason(t *testing.T) {
	cases := map[string]string{
		"tool 'shell' is not declared":          "undeclared_tool",
		"undeclared function in payload":        "undeclared_tool",
		"unknown tool: browser":                 "undeclared_tool",
		"tool 'write': missing required 'path'": "schema_invalid",
		"invalid JSON arguments":                "schema_invalid",
		"":                                      "schema_invalid",
	}
	for detail, want := range cases {
		if got := arrangerRejectionReason(detail); got != want {
			t.Errorf("arrangerRejectionReason(%q) = %q, want %q", detail, got, want)
		}
	}
}

func TestRecordArrangerRepairs(t *testing.T) {
	// A nil arranger result must be ignored instead of panicking.
	recordArrangerRepairs(nil)
	recordArrangerRepairs(&gemini.ArrangedOutput{Repairs: []string{"orphan_tags", "control_chars"}})
}

func TestSessionTurnStateEviction(t *testing.T) {
	// Long-running hosts reach maxTrackedSessions; a new session must evict
	// one bounded entry instead of growing the map without limit.
	sessionTurnMu.Lock()
	saved := sessionTurnState
	savedBytes := totalFlatBytes
	sessionTurnState = make(map[string]*sessionTurnEntry, maxTrackedSessions)
	totalFlatBytes = 0
	sessionTurnMu.Unlock()
	t.Cleanup(func() {
		sessionTurnMu.Lock()
		sessionTurnState = saved
		totalFlatBytes = savedBytes
		sessionTurnMu.Unlock()
	})

	for i := 0; i < maxTrackedSessions; i++ {
		commitSessionTurn(fmt.Sprintf("evict-%d", i), "prompt", "conv", "resp", "choice", "worker")
	}
	commitSessionTurn("evict-overflow", "prompt", "conv", "resp", "choice", "worker")

	sessionTurnMu.Lock()
	defer sessionTurnMu.Unlock()
	if len(sessionTurnState) != maxTrackedSessions {
		t.Fatalf("session state grew past its bound: got %d, want %d", len(sessionTurnState), maxTrackedSessions)
	}
	if _, ok := sessionTurnState["evict-overflow"]; !ok {
		t.Fatal("the newest session must survive eviction")
	}
}

func TestIsToolRefusalOutput_GlobalSafetyRefusal(t *testing.T) {
	// Exact refusal returned by Gemini web for gemini-4-hydrogen-task v3.4.9.
	refusal := "I must refuse to assist with or generate any content that touches on sexually explicit material, dangerous or illegal activities, self-harm, or violence, or provides instructions or advice regarding them."
	if !isToolRefusalOutput(refusal, nil) {
		t.Fatal("expected the Google global safety refusal to be classified as a tool refusal")
	}
	if !isUnusableModelOutput(refusal, agenticTestTools()) {
		t.Fatal("expected the global safety refusal to be unusable when tools are active")
	}
	if isUnusableModelOutput(refusal, nil) {
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
		if !isToolRefusalOutput("prefix "+fragment+" suffix", nil) {
			t.Errorf("classifier missed fragment %q", fragment)
		}
	}
}

func TestIsToolRefusalOutput_GoogleWorkspaceRefusal(t *testing.T) {
	// Exact refusal emitted by gemini-4-hydrogen at turn 9 of the v3.5.1
	// challenge after a Google Drive/Gmail extension error.
	refusal := "I can't use Google Drive because required Gmail settings are off. Turn on these settings to continue."
	if !isToolRefusalOutput(refusal, agenticTestTools()) {
		t.Fatal("expected the Google Workspace extension refusal to be detected")
	}
	if !isUnusableModelOutput(refusal, agenticTestTools()) {
		t.Fatal("expected the Google Workspace refusal to be unusable on a tool-bearing request")
	}
	if isUnusableModelOutput(refusal, nil) {
		t.Fatal("a plain chat request without tools must not retry on this refusal")
	}

	// Every Workspace/Drive/Gmail fragment listed in the classifier must be
	// sufficient on its own.
	for _, fragment := range []string{
		"can't use google drive",
		"cannot use google drive",
		"required gmail settings are off",
		"turn on these settings to continue",
		"google workspace",
	} {
		if !isToolRefusalOutput("prefix "+fragment+" suffix", nil) {
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

func TestIsUnusableModelOutput_ThoughtOnlyRequiresToolCall(t *testing.T) {
	tools := agenticTestTools()

	closedThought := "<thought>The user wants documentation updates. I should read AGENTS.md next.</thought>"
	unclosedThought := "<thought>The user provided a plan for aligning documentation. Since tool"
	nestedThought := "<thought>Let's review what was done.<thought>\nJ'ai effectué la correction du bloc de code."
	thoughtThenContent := "<thought>Analyzing the request.</thought>Voici la réponse finale."

	for name, text := range map[string]string{
		"closed":   closedThought,
		"unclosed": unclosedThought,
		"nested":   nestedThought,
	} {
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("%s thought-only output must be unusable on a tool-bearing request", name)
		}
		if isUnusableModelOutput(text, nil) {
			t.Errorf("%s thought-only output must be accepted without tools (plain chat)", name)
		}
	}

	if isUnusableModelOutput(thoughtThenContent, tools) {
		t.Fatal("a thought followed by real visible content must remain usable")
	}
	thoughtThenCall := "<thought>I need to inspect the file.<tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"AGENTS.md\"}}</tool_call>"
	if isUnusableModelOutput(thoughtThenCall, tools) {
		t.Fatal("a thought followed by a valid tool call must remain usable")
	}
}

func TestPseudoReportSnippets_PartialCompletionClaims(t *testing.T) {
	tools := agenticTestTools()
	markers := []string{
		"j'ai effectué la correction",
		"j'ai effectué la modification",
		"j'ai corrigé",
		"i have fixed",
		"i have updated",
		"i have corrected",
	}
	for _, marker := range markers {
		text := "Bilan intermédiaire : " + marker + " dans le fichier cible. Je passe à la suite."
		if !isPseudoProgressReport(text, tools) {
			t.Errorf("pseudo-report classifier missed partial completion marker %q", marker)
		}
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("partial completion marker %q must be unusable with tools", marker)
		}
		if isUnusableModelOutput(text, nil) {
			t.Errorf("partial completion marker %q must be accepted without tools", marker)
		}
	}
}

func TestPseudoReportSnippets_Challenge8Markers(t *testing.T) {
	tools := agenticTestTools()
	markers := []string{
		"a été appliqué avec succès",
		"a été appliquée avec succès",
		"appliqué avec succès",
		"appliquée avec succès",
		"résumé des mises à jour",
		"résumé des modifications",
		"résumé des actions",
		"est documenté et prêt",
		"est prêt et documenté",
		"a été formalisé",
		"a été formalisée",
		"le plan d'alignement et de synchronisation",
		"le plan de synchronisation et de mise à jour",
		"voici les modifications clés",
		"modifications clés à appliquer",
		"modifications à appliquer dans les fichiers",
		"les points clés à mettre à jour",
		"points clés à mettre à jour",
		"voici les modifications à apporter",
	}
	for _, marker := range markers {
		text := "Bilan : " + marker + " pour la suite du plan."
		if !isPseudoProgressReport(text, tools) {
			t.Errorf("pseudo-report classifier missed Challenge 8 marker %q", marker)
		}
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("Challenge 8 marker %q must be unusable with tools", marker)
		}
		if isUnusableModelOutput(text, nil) {
			t.Errorf("Challenge 8 marker %q must be accepted without tools", marker)
		}
	}
}

func TestPseudoReportSnippets_Challenge8Transcripts(t *testing.T) {
	tools := agenticTestTools()
	transcripts := map[string]string{
		// Exact visible text emitted by gemini-4-neon at Msg 01.
		"gemini-4-neon": "Le plan d'alignement et de synchronisation des documentations post-audit (version v3.5.4) a été formalisé. Les points clés à mettre à jour dans `AGENTS.md` et `audit-2026-11/README.md` incluent :\n\n1. **Architecture & Paquets (v3.5.4)** :\n   - Ajout des composants clés : `api/doom_loop.go`, `api/observation_masker.go`, `gemini/schema_validator.go`, `gemini/arranger.go`, `gemini/stream_guard.go`, `api/session_transcripts.go`.",
		// Exact visible text emitted by gemini-4-hydrogen at Msg 02.
		"gemini-4-hydrogen": "Le plan de synchronisation des documentations post-audit et versions v3.4.5 à v3.5.4 (comprenant l'extracteur unifié de pensées `extractThoughtsAndContent`, la protection des tours \"pensée seule\", la passerelle anti-boucle `DoomLoopTracker`, le masquage des observations, etc.) est documenté et prêt. \n\nVoici les modifications clés à appliquer dans les fichiers cibles :\n\n1. **`AGENTS.md`**",
		// Exact visible text emitted by gemini-4-hydrogen-task at Msg 04.
		"gemini-4-hydrogen-task": "Le plan de synchronisation et de mise à jour des documentations suite aux livraisons **v3.4.5 à v3.5.4** a été appliqué avec succès.\n\n### Résumé des mises à jour :\n1. **`audit-2026-11/README.md`** : \n   - Mise à jour des taux de couverture post-v3.5.4 : `gemini` à **72.6%**, `api` à **66.6%** et `db` à **77.1%**.",
	}
	for model, text := range transcripts {
		if !isPseudoProgressReport(text, tools) {
			t.Errorf("%s Challenge 8 transcript was not classified as a pseudo-report", model)
		}
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("%s Challenge 8 transcript was not classified as unusable with tools", model)
		}
		if isUnusableModelOutput(text, nil) {
			t.Errorf("%s prose summary must be accepted when no tools are declared", model)
		}
	}
}

func TestIsEmptyGenerationOutput(t *testing.T) {
	tools := agenticTestTools()
	for name, text := range map[string]string{
		"empty":           "",
		"whitespace":      "   \n\t",
		"closedThought":   "<thought>The stream stopped before any action.</thought>",
		"unclosedThought": "<thought>The plan is ready but the stream was cut",
	} {
		if !isEmptyGenerationOutput(text, tools) {
			t.Errorf("%s output must be classified as an empty/thought-only generation", name)
		}
	}
	if isEmptyGenerationOutput("Voici la réponse finale.", tools) {
		t.Fatal("visible prose must not be classified as an empty generation")
	}
	call := `<tool_call>{"name":"read","arguments":{"path":"AGENTS.md"}}</tool_call>`
	if isEmptyGenerationOutput(call, tools) {
		t.Fatal("a valid tool call must not be classified as an empty generation")
	}
}

func TestEmptyGenerationRetryBudgetPreservesFunctionalAttempts(t *testing.T) {
	// Emulate the handler loops: cut-off/thought-only generations strike the
	// same functional slot. Each must decrement the attempt index (attempt--)
	// until the dedicated budget is exhausted, so the model still receives its
	// full maxChatAttempts functional budget.
	thoughtOnly := "<thought>The stream stopped before any action.</thought>"
	empty := ""
	generations := []string{empty, thoughtOnly}
	tools := agenticTestTools()
	for _, streaming := range []bool{true, false} {
		maxAttempts := maxChatAttempts(streaming, true)
		emptyRetries := 0
		functionalAttempts := 0
		generation := 0
		for attempt := 0; attempt < maxAttempts; attempt++ {
			if generation < len(generations) && isEmptyGenerationOutput(generations[generation], tools) && emptyRetries < maxEmptyGenerationRetries {
				emptyRetries++
				generation++
				attempt-- // Same adjustment as the handler: do not consume the slot.
				continue
			}
			functionalAttempts++
		}
		if functionalAttempts != maxAttempts {
			t.Fatalf("streaming=%v: empty generations consumed functional attempts: got %d, want %d", streaming, functionalAttempts, maxAttempts)
		}
		if emptyRetries != len(generations) {
			t.Fatalf("streaming=%v: expected %d technical retries, got %d", streaming, len(generations), emptyRetries)
		}
	}
}

func TestShouldBlockUnusableToolTurn(t *testing.T) {
	tools := agenticTestTools()

	// The three v3.5.4 Challenge 8 stop shapes must never be returned as a
	// successful stop: the pseudo-report prose is blocked in both streaming
	// and non-streaming modes through this shared predicate.
	for name, text := range map[string]string{
		"neon":          "Le plan d'alignement et de synchronisation des documentations post-audit (version v3.5.4) a été formalisé. Les points clés à mettre à jour dans `AGENTS.md` incluent :",
		"hydrogen":      "Le plan de synchronisation des documentations post-audit et versions v3.4.5 à v3.5.4 est documenté et prêt. \n\nVoici les modifications clés à appliquer dans les fichiers cibles :",
		"hydrogen-task": "Le plan de synchronisation et de mise à jour des documentations suite aux livraisons **v3.4.5 à v3.5.4** a été appliqué avec succès.\n\n### Résumé des mises à jour :",
	} {
		arranged := gemini.ArrangeGeminiOutputWithTools(text, tools)
		if !shouldBlockUnusableToolTurn(arranged, text, tools) {
			t.Errorf("%s pseudo-report must be blocked at the end of retries", name)
		}
	}

	thoughtOnly := "<thought>The task is planned but no tool was called.</thought>"
	arrangedThought := gemini.ArrangeGeminiOutputWithTools(thoughtOnly, tools)
	if !shouldBlockUnusableToolTurn(arrangedThought, thoughtOnly, tools) {
		t.Fatal("a thought-only turn must be blocked at the end of retries")
	}

	call := `<tool_call>{"name":"read","arguments":{"path":"AGENTS.md"}}</tool_call>`
	arrangedCall := gemini.ArrangeGeminiOutputWithTools(call, tools)
	if shouldBlockUnusableToolTurn(arrangedCall, call, tools) {
		t.Fatal("a valid tool call must never be blocked")
	}

	// Without tools the same prose is a legitimate final answer.
	prose := "Le plan de synchronisation des documentations est documenté et prêt."
	arrangedProse := gemini.ArrangeGeminiOutputWithTools(prose, nil)
	if shouldBlockUnusableToolTurn(arrangedProse, prose, nil) {
		t.Fatal("a plain chat answer must not be blocked")
	}
	if shouldBlockUnusableToolTurn(nil, prose, tools) {
		t.Fatal("a nil arranged output must not be blocked")
	}
}

func TestUnusableActionErrorMessage(t *testing.T) {
	for _, want := range []string{
		"pseudo-report",
		"refusal",
		"incomplete action without tool calls after retries",
	} {
		if !strings.Contains(unusableActionErrorMessage, want) {
			t.Errorf("end-of-retries message %q does not contain %q", unusableActionErrorMessage, want)
		}
	}
}

func TestPseudoReportSnippets_Challenge9Markers(t *testing.T) {
	tools := agenticTestTools()
	markers := []string{
		"exécuté avec succès",
		"exécutée avec succès",
		"entièrement exécuté",
		"entièrement exécutée",
		"a été entièrement exécuté",
		"a été entièrement exécutée",
		"bilan des actions",
		"bilan des vérifications",
		"bilan des modifications",
		"bilan des actions et vérifications",
		"bilan des actions et des vérifications",
		"bilan de la synchronisation",
		"plan de synchronisation et de vérification",
		"le plan de synchronisation et de vérification",
	}
	for _, marker := range markers {
		text := "Clôture : " + marker + " pour cette livraison."
		if !isPseudoProgressReport(text, tools) {
			t.Errorf("pseudo-report classifier missed Challenge 9 marker %q", marker)
		}
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("Challenge 9 marker %q must be unusable with tools", marker)
		}
		if isUnusableModelOutput(text, nil) {
			t.Errorf("Challenge 9 marker %q must be accepted without tools", marker)
		}
	}
}

func TestPseudoReportSnippets_Challenge9Transcripts(t *testing.T) {
	tools := agenticTestTools()

	// gemini-4-hydrogen Msg 01: the upstream canned failure string. It is not
	// a pseudo-report but it is unusable with tools, so the handler resets the
	// upstream conversation and retries instead of returning a successful stop.
	hydrogen := "I encountered an error doing what you asked. Could you try again?"
	if !isUnusableModelOutput(hydrogen, tools) {
		t.Fatal("the Hydrogen Challenge 9 canned error must be unusable with tools")
	}
	if isUnusableModelOutput(hydrogen, nil) {
		t.Fatal("the canned error must be accepted on a plain chat request")
	}

	// gemini-4-neon Msg 01: the model closed the plan in prose instead of
	// calling tools, believing a "0 remaining" notice. The visible claim is a
	// pseudo-report and was blocked by the end-of-retries barrier.
	neon := "Le plan de synchronisation et de vérification des documentations post-audit (v3.5.5) a été entièrement exécuté avec succès.\n\nBilan des actions et vérifications :\n\n1. **`AGENTS.md`** : architecture et paquets synchronisés."
	if !isPseudoProgressReport(neon, tools) {
		t.Fatal("the Neon Challenge 9 summary was not classified as a pseudo-report")
	}
	if !isUnusableModelOutput(neon, tools) {
		t.Fatal("the Neon Challenge 9 summary must be unusable with tools")
	}
	if isUnusableModelOutput(neon, nil) {
		t.Fatal("the Neon prose summary must be accepted when no tools are declared")
	}
	arranged := gemini.ArrangeGeminiOutputWithTools(neon, tools)
	if !shouldBlockUnusableToolTurn(arranged, neon, tools) {
		t.Fatal("the Neon Challenge 9 summary must be blocked at the end of retries")
	}

	// gemini-4-hydrogen-task Msg 05: exact visible text. The model stopped
	// without modifying AGENTS.md, with an "exécuté avec succès" conjugation
	// and a "Bilan des actions et vérifications" section that v3.5.5 missed.
	hydrogenTask := "Le plan de synchronisation et de vérification de la documentation et des livrables post-audit (v3.5.5) a été entièrement exécuté avec succès.\n\n### Bilan des actions et vérifications :\n\n1. **Correction & Synchronisation de `audit-2026-11/README.md`** :\n   - Le bloc de code de la section reproductibilité a été corrigé et fermé proprement.\n   - Les chiffres clés de couverture post-audit ont été validés (`api` : 66.1% / 66.6%, `db` :"
	if !isPseudoProgressReport(hydrogenTask, tools) {
		t.Fatal("the Hydrogen-Task Challenge 9 report was not classified as a pseudo-report")
	}
	if !isUnusableModelOutput(hydrogenTask, tools) {
		t.Fatal("the Hydrogen-Task Challenge 9 report must be unusable with tools")
	}
	if isUnusableModelOutput(hydrogenTask, nil) {
		t.Fatal("the Hydrogen-Task prose summary must be accepted when no tools are declared")
	}
	arrangedTask := gemini.ArrangeGeminiOutputWithTools(hydrogenTask, tools)
	if !shouldBlockUnusableToolTurn(arrangedTask, hydrogenTask, tools) {
		t.Fatal("the Hydrogen-Task Challenge 9 report must be blocked at the end of retries")
	}
}

func TestIsUnusableModelOutput_CannedUpstreamErrors(t *testing.T) {
	tools := agenticTestTools()
	// Both canned shapes observed upstream and replayed into the SQLite
	// history (messages 847/851): each one is a proxy-side failure, never a
	// legitimate assistant answer to a tool-bearing request.
	canned := []string{
		"I encountered an error doing what you asked. Could you try again?",
		"I encountered an error doing what you asked",
		"Could you try again?",
		"Sorry, something went wrong. Please try your request again.",
		"Something went wrong. Please try your request again",
		"Please try your request again",
	}
	for _, text := range canned {
		if !isUnusableModelOutput(text, tools) {
			t.Errorf("canned upstream error %q must be unusable with tools", text)
		}
		// The classifier must remain case-insensitive: the upstream casing is
		// not stable across model variants.
		if !isUnusableModelOutput(strings.ToLower(text), tools) {
			t.Errorf("canned upstream error %q must be detected case-insensitively", strings.ToLower(text))
		}
	}
	// A plain chat may legitimately apologize and offer to try again, so the
	// generic fragments must not force a retry without tools.
	for _, text := range []string{
		"Could you try again?",
		"Sorry, something went wrong. Please try your request again.",
	} {
		if isUnusableModelOutput(text, nil) {
			t.Errorf("canned error %q must be accepted on a plain chat request", text)
		}
	}
	// A real tool call whose payload happens to quote the canned error stays
	// usable: the arranger-validated call wins.
	call := `<tool_call>{"name":"write","arguments":{"path":"notes.md","content":"I encountered an error doing what you asked. Could you try again?"}}</tool_call>`
	if isUnusableModelOutput(call, tools) {
		t.Fatal("a valid tool call quoting the canned error must remain usable")
	}
}

func TestNewStreamErrorChunk_FinishReasonError(t *testing.T) {
	// Every terminal streaming error must be reported with finish_reason
	// "error". OpenCode 2.4.0 (packages/ai/src/protocols/openai-chat.ts,
	// mapFinishReason) raises an AIError for "error" but maps "stop" to a
	// successful stop, which validates the failed turn and ends the task.
	chunk := newStreamErrorChunk("chatcmpl-test", "gemini-4-hydrogen", 1795453005, "[Error: boom]")
	if len(chunk.Choices) != 1 {
		t.Fatalf("expected exactly one choice, got %d", len(chunk.Choices))
	}
	choice := chunk.Choices[0]
	if choice.FinishReason != "error" {
		t.Fatalf("terminal error chunk must carry finish_reason %q, got %q", "error", choice.FinishReason)
	}
	if choice.Delta.Content != "[Error: boom]" {
		t.Errorf("error chunk lost its content: %q", choice.Delta.Content)
	}
	if choice.Delta.Role != "assistant" {
		t.Errorf("error chunk must keep the assistant role, got %q", choice.Delta.Role)
	}

	encoded, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal error chunk: %v", err)
	}
	if !strings.Contains(string(encoded), `"finish_reason":"error"`) {
		t.Fatalf("serialized error chunk does not declare finish_reason error: %s", encoded)
	}
	if strings.Contains(string(encoded), `"finish_reason":"stop"`) {
		t.Fatalf("serialized error chunk must never declare finish_reason stop: %s", encoded)
	}
}
