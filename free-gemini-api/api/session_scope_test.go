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
			"Do not emit conversational progress updates or completion summaries.",
			"You still have files to inspect and modify.",
			"Call the required read/edit tools directly.",
		} {
			if !strings.Contains(directive, want) {
				t.Errorf("pseudo-report directive for %s does not contain %q", model, want)
			}
		}
	}
}

func TestFormatPseudoReportRetryDirective(t *testing.T) {
	directive := formatPseudoReportRetryDirective()
	for _, want := range []string{
		"[SYSTEM DIRECTIVE:",
		"Do not emit conversational progress updates or completion summaries.",
		"You still have files to inspect and modify.",
		"Call the required read/edit tools directly.",
	} {
		if !strings.Contains(directive, want) {
			t.Errorf("pseudo-report directive %q does not contain %q", directive, want)
		}
	}
}

func TestMaxChatAttempts(t *testing.T) {
	if got := maxChatAttempts(true, true); got != 3 {
		t.Fatalf("streaming tool request should get 3 attempts, got %d", got)
	}
	if got := maxChatAttempts(false, true); got != 3 {
		t.Fatalf("non-streaming tool request should get 3 attempts, got %d", got)
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
	sessionTurnState = make(map[string]*sessionTurnEntry, maxTrackedSessions)
	sessionTurnMu.Unlock()
	t.Cleanup(func() {
		sessionTurnMu.Lock()
		sessionTurnState = saved
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
