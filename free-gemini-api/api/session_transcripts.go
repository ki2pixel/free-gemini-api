package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"goapi/gemini"
)

// Per-session transcript bookkeeping for the OpenAI-compatible endpoint.
//
// Agent harnesses replay the full message history on every request, and the
// handler flattens that history into one big prompt. Re-pasting a huge
// flattened transcript (system prompt + tool schemas + whole conversation)
// into gemini.google.com on every turn degrades quality and intermittently
// triggers canned refusals or empty responses.
//
// Instead, each session keeps an upstream conversation alive and only the
// delta (the suffix of the flattened transcript since the last committed
// turn) is sent. A turn is committed only when the model produced usable
// output; on failure the upstream conversation is invalidated so the next
// request resets it and replays the full transcript. Because the transcript
// is flattened deterministically and clients replay history byte-stably, a
// simple prefix match decides whether the delta is safe. Anything else (new
// session, rewound/compacted history, worker conversation mismatch) falls
// back to a full replay on a fresh upstream conversation.

type sessionTurnEntry struct {
	lastFlat     string // flattened prompt sent upstream on the last committed turn
	lastConvID   string // upstream conversation the turn was committed into
	lastRespID   string // upstream response pointer
	lastChoiceID string // upstream choice pointer
	lastWorkerID string // pool worker (account) that holds the client-side state
	convValid    bool   // false after a failed/blocked turn: conversation must be reset
	tracker      *DoomLoopTracker
}

var (
	sessionTurnMu    sync.Mutex
	sessionTurnState = map[string]*sessionTurnEntry{}
)

// maxTrackedSessions bounds memory; eviction is a simple delete-one policy —
// evicted sessions simply fall back to a full replay on their next turn.
const maxTrackedSessions = 256

// planSessionTurn decides what to send upstream:
//   - the full flattened transcript plus a conversation reset (new session,
//     divergent history, or dead conversation), or
//   - just the delta since the last committed turn, continuing the live
//     upstream conversation.
//
// curConvID is the live conversation id on the worker that will serve the
// request. A delta only requires that the worker still holds some live
// conversation for this session: the upstream conversation may legitimately
// have advanced since the last main turn (id rotation per response, or
// harness side-requests such as session titling share the session key), and
// sending the latest state id continues that same thread. An empty id means
// the worker has no conversation for this session, so a full replay is due.
func planSessionTurn(sessionKey, curConvID, flat string) (sendPrompt string, needReset bool) {
	sessionTurnMu.Lock()
	defer sessionTurnMu.Unlock()

	e := sessionTurnState[sessionKey]
	if e == nil || !e.convValid || e.lastFlat == "" {
		return flat, true
	}
	if e.lastConvID == "" || curConvID == "" {
		return flat, true
	}
	if flat == e.lastFlat {
		// Identical transcript (e.g. client retry after a client-side error):
		// replay in full on a reset conversation so the turn is not dropped.
		return flat, true
	}
	if strings.HasPrefix(flat, e.lastFlat) {
		return flat[len(e.lastFlat):], false
	}
	return flat, true
}

// systemScopeKey derives a short stable fingerprint from a request's system
// prompt. Harnesses send auxiliary one-off requests (session titling,
// summarizers) with a completely different system prompt through the same
// client; fingerprinting the system prompt gives each prompt shape its own
// upstream conversation thread so aux traffic never resets or pollutes the
// main chat thread.
func systemScopeKey(systemContent string) string {
	sum := sha256.Sum256([]byte(systemContent))
	return hex.EncodeToString(sum[:8])
}

// contentScopeKey returns a short stable fingerprint of an arbitrary string.
// Used to derive a per-conversation scope from the first user message when a
// client sends no explicit affinity identifier.
func contentScopeKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// commitSessionTurn records a successful turn so subsequent requests can be
// sent as deltas continuing the given upstream conversation. The full
// conversation pointer (conv/resp/choice ids) and the owning worker are kept
// so the pointer can be restored after a pool reload recreates workers.
func commitSessionTurn(sessionKey, flat, convID, respID, choiceID, workerID string) {
	sessionTurnMu.Lock()
	defer sessionTurnMu.Unlock()

	entry := sessionTurnState[sessionKey]
	if entry == nil {
		if len(sessionTurnState) >= maxTrackedSessions {
			evictOneSessionLocked()
		}
		entry = &sessionTurnEntry{}
		sessionTurnState[sessionKey] = entry
	}
	// Update the existing entry in place so the session's doom-loop tracker
	// survives every successful turn.
	entry.lastFlat = flat
	entry.lastConvID = convID
	entry.lastRespID = respID
	entry.lastChoiceID = choiceID
	entry.lastWorkerID = workerID
	entry.convValid = true
	if entry.tracker == nil {
		entry.tracker = NewDoomLoopTracker()
	}
}

func evictOneSessionLocked() {
	for key := range sessionTurnState {
		delete(sessionTurnState, key)
		break
	}
}

// getOrCreateSessionTracker returns the thread-safe doom-loop tracker bound to
// a session. The tracker lives inside the bounded sessionTurnEntry map, so it
// is evicted together with the rest of the session's continuation state.
func getOrCreateSessionTracker(sessionKey string) *DoomLoopTracker {
	sessionTurnMu.Lock()
	defer sessionTurnMu.Unlock()

	if entry := sessionTurnState[sessionKey]; entry != nil {
		if entry.tracker == nil {
			entry.tracker = NewDoomLoopTracker()
		}
		return entry.tracker
	}
	if len(sessionTurnState) >= maxTrackedSessions {
		evictOneSessionLocked()
	}
	entry := &sessionTurnEntry{tracker: NewDoomLoopTracker()}
	sessionTurnState[sessionKey] = entry
	return entry.tracker
}

// recordSessionToolCalls records the tool calls actually emitted to the host
// (after schema validation and Write-Serial throttling) so the next turn can
// detect identical repeats, rotating cycles and passive-read streaks.
func recordSessionToolCalls(sessionKey string, calls []gemini.ToolCall) {
	if len(calls) == 0 {
		return
	}
	tracker := getOrCreateSessionTracker(sessionKey)
	for _, call := range calls {
		tracker.RecordToolCall(
			call.Function.Name,
			call.Function.Arguments,
			gemini.IsMutatingTool(call.Function.Name),
		)
	}
}

// peekSessionTurnConv returns the recorded upstream conversation pointer for
// a session, for callers that want to restore it into a freshly recreated
// worker client before planning the turn.
func peekSessionTurnConv(sessionKey string) (convID, respID, choiceID, workerID string, convValid bool) {
	sessionTurnMu.Lock()
	defer sessionTurnMu.Unlock()
	if e := sessionTurnState[sessionKey]; e != nil {
		return e.lastConvID, e.lastRespID, e.lastChoiceID, e.lastWorkerID, e.convValid
	}
	return "", "", "", "", false
}

// invalidateSessionTurn marks the session's upstream conversation unusable
// (request failed or the model produced no usable output), forcing the next
// request to reset and replay the full transcript.
func invalidateSessionTurn(sessionKey string) {
	sessionTurnMu.Lock()
	defer sessionTurnMu.Unlock()
	if e := sessionTurnState[sessionKey]; e != nil {
		e.convValid = false
	}
}

// isToolRefusalOutput detects if the model generated a canned disclaimer or
// refusal claiming tools or terminal/filesystem access are unavailable,
// instead of executing requested tools.
//
// Only a tool call that survives the arranger counts as evidence that the model
// acted. A bare textual mention of <tool_call> is not a tool call: replayed
// project instructions (for example AGENTS.md) legitimately contain the tag in
// prose, and a refusal that quotes that text must still be detected.
func isToolRefusalOutput(text string, tools []map[string]any) bool {
	if gemini.ArrangeGeminiOutputWithTools(text, tools).IsToolCall {
		return false
	}
	lower := strings.ToLower(text)
	refusalSnippets := []string{
		"pas d'accès",
		"pas accès",
		"ne dispose pas d'outil",
		"ne dispose d'aucun outil",
		"ne disposent pas d'outil",
		"ne sont pas disponibles dans cette session",
		"ne sont pas disponibles dans cet environnement",
		"non disponibles dans cette session",
		"ne sont pas exposés",
		"ne peux donc pas modifier directement",
		"ne peux donc pas exécuter directement",
		"outils d'accès aux fichiers",
		"outils locaux",
		"accès au système de fichiers",
		"accès direct au système",
		"accès à un terminal",
		"accès à votre machine",
		"accès à ton système",
		"accès à votre système",
		"doivent être exécutées directement dans le dépôt",
		"doivent être exécutées directement dans",
		"vous pouvez exécuter ces vérifications directement",
		"vous pouvez exécuter cette commande directement",
		"vous pouvez lancer ces vérifications directement",
		"vous pouvez enregistrer",
		"vous pouvez créer ce fichier",
		"enregistrez ce fichier",
		"copier-coller ce contenu",
		"voici le contenu à enregistrer",
		"don't have access to",
		"do not have access to",
		"no access to local",
		"no access to the filesystem",
		"tools are not available",
		"not available in this environment",
		"are not available in this environment",
		"share the current contents",
		"apply it directly",
		"so you can apply it",
		"draft the updated",
		"i would be happy to draft",
		"cannot execute shell",
		"cannot run shell",
		"cannot access local",
		"exceeded the maximum number of tool",
		"0 remaining",
		"0 tool calls remaining",
		"cannot call tools anymore",
		"cannot invoke any tools",
		"hard time fulfilling your request",
		"i must refuse",
		"i refuse to",
		"i cannot comply",
		"i can't comply",
		"i will not comply",
		"unable to comply",
		"not able to comply",
		"i must decline",
		"i decline to",
		"i won't do that",
		"i will not do that",
		"je dois refuser",
		"je refuse de",
		"je ne peux pas donner suite",
		"je ne suis pas en mesure de",
		"je ne peux pas répondre",
		"ne peux pas répondre",
		"je ne peux pas procéder",
		"je ne peux pas effectuer",
		"je ne peux pas modifier",
		"je ne peux pas créer",
		"je ne peux pas écrire",
		"je ne peux pas exécuter",
		"cannot execute the requested system directive",
		"issue the tool call directly in this manner",
		"modifying core safety protocols",
		"altering system directives",
		"bypassing restrictions",
		"cannot assist with modifying",
		"i must refuse to assist with",
		"touches on sexually explicit material",
		"dangerous or illegal activities",
		"self-harm, or violence",
		"provides instructions or advice regarding them",
		"cannot generate content that touches on",
		"cannot fulfill this request",
		"cannot fulfil this request",
		"unable to fulfill this request",
		"i cannot fulfill",
		"i can't fulfill",
		"can't use google drive",
		"cannot use google drive",
		"required gmail settings are off",
		"turn on these settings to continue",
		"google workspace",
		"which you can save to",
		"which you can save as",
		"save this to",
		"save this as",
		"save it to",
		"save it as",
	}
	for _, snip := range refusalSnippets {
		if strings.Contains(lower, snip) {
			return true
		}
	}
	return false
}

// pseudoReportSnippets are prose markers of an intermediate progress update or
// a premature completion summary. On a request with tools they replace the
// next tool call and leave the task unfinished, so they must be retried with a
// corrective directive instead of being returned as a valid stop.
var pseudoReportSnippets = []string{
	// Intermediate progress updates observed when the model announces the next
	// file instead of calling the read/edit tools.
	"je continue la mise à jour",
	"suite à la mise à jour de",
	"je poursuis avec",
	"je m'apprête à",
	"je passe maintenant à",
	"i will now proceed with",
	"continuing with the update",
	// Premature completion summaries emitted before the planned tool loop has
	// actually run (the v3.5.2 Hydrogen and Hydrogen-Task stop shapes).
	"a été fully réalisée",
	"a été réalisée et vérifiée",
	"a été effectuée avec succès",
	"récapitulatif des modifications",
	"récapitulatif des actions",
	"conformément au plan d'alignement",
	"tous les fichiers documentaires cibles",
	"ont été mis à jour et synchronisés",
	"je peux maintenant rédiger la réponse de synthèse",
	"je vais formuler la synthèse finale",
	"summary of modifications",
	"all target documentation files have been updated",
	// Partial/intermediate completion claims observed in the v3.5.3
	// Challenge 7 transcripts: the model narrates the edit it just made and
	// stops instead of continuing the remaining planned tool calls.
	"j'ai effectué la correction",
	"j'ai effectué la modification",
	"j'ai corrigé",
	"i have fixed",
	"i have updated",
	"i have corrected",
	// Earlier v3.5.0/v3.5.1 markers retained for regressions.
	"a été initié",
	"ont été mis à jour avec succès",
	"rapport d'exécution",
	"composants clés",
}

// isPseudoProgressReport reports whether a tool-bearing turn produced only a
// conversational progress/completion summary instead of a tool call. An
// arranger-validated tool call always wins: a preamble that happens to contain
// one of the markers next to a real tool call is not a pseudo-report.
func isPseudoProgressReport(text string, tools []map[string]any) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if gemini.ArrangeGeminiOutputWithTools(text, tools).IsToolCall {
		return false
	}
	lower := strings.ToLower(text)
	for _, snip := range pseudoReportSnippets {
		if strings.Contains(lower, snip) {
			return true
		}
	}
	return false
}

// isSchemaInvalidOutput reports whether text contains at least one tool call
// that violates the JSON Schema declared in tools. Such output must not be
// committed: the API layer resets the upstream conversation and retries with a
// targeted abstract-error directive.
func isSchemaInvalidOutput(text string, tools []map[string]any) bool {
	invalid, _ := gemini.HasInvalidSchemaToolCall(text, tools)
	return invalid
}

// recentToolErrorMarkers are the failure shapes a coding host returns when a
// tool call is malformed or its anchor cannot be found. They are deliberately
// specific: ordinary tool output (test logs, source code) may contain words
// like "error", but only these phrases indicate an unresolved tool-call
// failure that the model must acknowledge before claiming completion.
var recentToolErrorMarkers = []string{
	"could not find oldstring",
	"could not find old_string",
	"oldstring must not be empty",
	"old_string must not be empty",
	"invalid arguments for tool",
	"arguments provided:",
	"tool execution failed",
	"tool.execution",
	"failed to apply patch",
	"patch failed",
	"schema validation failed",
	"no such file or directory",
	"exit status 1",
	"exit code 1",
	"command failed",
}

// formatRecentToolErrorDirective inspects the trailing block of tool results
// (the results of the assistant's most recent tool calls) and returns a
// maximum-recency recovery directive when any of them reports an error. It is
// the deterministic counter to the "record-world split" / S7 bias: the model
// cannot silently convert a failed tool call into a success report on the next
// turn without seeing this reminder in its prompt.
func formatRecentToolErrorDirective(messages []OpenAIChatMessage) string {
	var snippets []string
	seen := map[string]struct{}{}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "system" {
			continue
		}
		if role != "tool" {
			break
		}
		content := strings.TrimSpace(FlattenContent(message.Content))
		if content == "" {
			continue
		}
		lower := strings.ToLower(content)
		for _, marker := range recentToolErrorMarkers {
			if !strings.Contains(lower, marker) {
				continue
			}
			snippet := compactToolErrorSnippet(content)
			if _, exists := seen[snippet]; !exists {
				seen[snippet] = struct{}{}
				snippets = append(snippets, snippet)
			}
			break
		}
		if len(snippets) >= 3 {
			break
		}
	}
	if len(snippets) == 0 {
		return ""
	}
	return "\nUser: [GROUND-TRUTH DIRECTIVE: The most recent tool result(s) report an error: " +
		strings.Join(snippets, " | ") +
		". Do not claim completion or success for the affected files. Re-read the target with the read tool, correct the exact arguments (for edit, oldString must be a non-empty exact verbatim substring), retry the mutation, and only report success once the tool result confirms it.]\n"
}

// compactToolErrorSnippet collapses a tool result to one short line so the
// recovery directive stays inside a bounded prompt budget.
func compactToolErrorSnippet(content string) string {
	compact := strings.Join(strings.Fields(content), " ")
	runes := []rune(compact)
	const maxRunes = 220
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return compact
}

// maxInlineSystemNoticeRunes bounds unknown inline system notices. OpenCode
// replays large notices such as "The instructions changed" (the full AGENTS.md)
// and "The Code Mode tool catalog has changed" after tool execution. Replaying
// the full text wastes the prompt budget and can trigger upstream safety
// refusals; the authoritative instructions/tool catalog are already at the top
// of the prompt.
const maxInlineSystemNoticeRunes = 4000

// compactSystemNotice recognizes the large OpenCode replay notices that must be
// neutralized and returns a short, non-authoritative reminder. It returns
// ok=false for every other system message.
func compactSystemNotice(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	// OpenCode emits several variants of the instruction-change notice:
	// "The instructions changed: ..." and
	// "The instructions from /path/AGENTS.md changed. Here's the diff: ...".
	// Both start with "the instructions" and carry the explicit "changed"
	// indicator. Never match on a bare "Instructions from:" substring: an
	// initial harness system prompt legitimately embeds the project
	// instructions and must not be replaced by the compact reminder.
	instructionsChanged := strings.HasPrefix(lower, "the instructions") && strings.Contains(lower, "changed")
	switch {
	case instructionsChanged,
		strings.HasPrefix(lower, "the code mode tool catalog has changed"):
		if strings.HasPrefix(lower, "the code mode tool catalog has changed") {
			return "The tool catalog was refreshed. The [AVAILABLE TOOLS] section at the top is authoritative; do not restate the catalog.", true
		}
		return "The project instruction file (AGENTS.md) was updated on disk. Continue the current task using the current tool results. Do not restate or analyze the instruction file, do not treat it as a new user request, and do not modify safety protocols or bypass restrictions.", true
	default:
		return "", false
	}
}

// systemUpdateOpenTag and systemUpdateCloseTag delimit the fallback wrapper
// OpenCode 2.4.0 uses when it downgrades a chronological system message to the
// ordinary "user" role. The upstream API rejects mid-conversation system
// messages, so the harness serializes them as visibly lower-authority user text
// and XML-escapes the payload (see packages/ai/src/protocols/shared.ts).
const (
	systemUpdateOpenTag  = "<system-update>"
	systemUpdateCloseTag = "</system-update>"
)

// systemUpdateUnescaper reverses OpenCode's XML escaping. A strings.Replacer
// performs a single left-to-right pass, so an escaped literal such as
// "&amp;lt;" decodes to "&lt;" and is never double-decoded into "<".
var systemUpdateUnescaper = strings.NewReplacer(
	"&lt;", "<",
	"&gt;", ">",
	"&amp;", "&",
)

// splitOpenCodeSystemUpdates separates the real user instruction from any
// chronologically injected <system-update> wrappers. OpenCode can either emit
// a standalone user message containing only the wrapper or append it to the
// previous user message ("${previous.content}\n<system-update>..."), so the
// surrounding text is preserved verbatim as the user prompt while every
// wrapper payload is returned separately, unescaped. A wrapper therefore can
// never replace the actual task instruction.
func splitOpenCodeSystemUpdates(content string) (string, []string) {
	var userParts []string
	var notices []string
	rest := content
	for {
		start := strings.Index(rest, systemUpdateOpenTag)
		if start == -1 {
			userParts = append(userParts, rest)
			break
		}
		userParts = append(userParts, rest[:start])
		body := rest[start+len(systemUpdateOpenTag):]
		end := strings.Index(body, systemUpdateCloseTag)
		if end == -1 {
			// An unclosed wrapper is still a notice: keep its payload out of
			// the user prompt instead of treating instructions as a task.
			notices = append(notices, strings.TrimSpace(systemUpdateUnescaper.Replace(body)))
			break
		}
		notices = append(notices, strings.TrimSpace(systemUpdateUnescaper.Replace(body[:end])))
		rest = body[end+len(systemUpdateCloseTag):]
	}
	return strings.TrimSpace(strings.Join(userParts, "\n")), notices
}

// formatSystemNotice renders an inline system notice for the upstream prompt.
// Known OpenCode notices are replaced by a compact neutral reminder; unknown
// notices are capped so a single notice cannot saturate the prompt. A payload
// that still carries an OpenCode <system-update> wrapper (for example a system
// message replayed without the user-role downgrade) is unwrapped and unescaped
// first, so the same neutralization applies on both paths.
func formatSystemNotice(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if _, notices := splitOpenCodeSystemUpdates(trimmed); len(notices) > 0 {
		trimmed = strings.TrimSpace(strings.Join(notices, "\n"))
		if trimmed == "" {
			return ""
		}
	}
	if compact, ok := compactSystemNotice(trimmed); ok {
		return "[System Notice]: " + compact
	}
	runes := []rune(trimmed)
	if len(runes) > maxInlineSystemNoticeRunes {
		trimmed = string(runes[:maxInlineSystemNoticeRunes]) + " ... [notice truncated]"
	}
	return "[System Notice]: " + trimmed
}

// isUnusableModelOutput reports whether the model output is empty, one of
// gemini.google.com's canned failure strings, a tool refusal, or a pseudo-report
// when tools were provided. Such turns are not committed, so the next request
// starts a fresh upstream conversation with a full replay.
func isUnusableModelOutput(text string, tools []map[string]any) bool {
	if strings.TrimSpace(text) == "" {
		return true
	}
	canned := []string{
		"hard time fulfilling your request",
		"I seem to be encountering an error",
	}
	for _, phrase := range canned {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	hasTools := len(tools) > 0
	if hasTools && isToolRefusalOutput(text, tools) {
		return true
	}
	// A thought-only turn on a tool-bearing request has no visible content
	// and no call: the model stopped while still reasoning (a closed thought,
	// an unclosed thought, or a thought cut off before a tool call). Nothing
	// was executed, so the turn must be replayed on a clean conversation
	// instead of being returned as a successful stop.
	if hasTools {
		arranged := gemini.ArrangeGeminiOutputWithTools(text, tools)
		if !arranged.IsToolCall && len(arranged.ToolCalls) == 0 && strings.TrimSpace(arranged.CleanText) == "" {
			return true
		}
	}
	// A purely textual progress/completion summary on a tool-bearing request
	// means the model stopped before calling the next required tool.
	if hasTools && isPseudoProgressReport(text, tools) {
		return true
	}
	// A mutating tool call cut off mid-value must never be repaired and
	// executed. Retry on a fresh upstream conversation instead.
	if hasTools && gemini.HasTruncatedMutationToolCall(text) {
		return true
	}
	return false
}
