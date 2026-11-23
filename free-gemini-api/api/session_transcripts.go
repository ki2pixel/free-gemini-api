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
func isToolRefusalOutput(text string) bool {
	if strings.Contains(text, "<tool_call>") || strings.Contains(text, "</tool_call>") {
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
		"cannot execute shell",
		"cannot run shell",
		"cannot access local",
		"exceeded the maximum number of tool",
		"0 remaining",
		"0 tool calls remaining",
		"cannot call tools anymore",
		"cannot invoke any tools",
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

// isSchemaInvalidOutput reports whether text contains at least one tool call
// that violates the JSON Schema declared in tools. Such output must not be
// committed: the API layer resets the upstream conversation and retries with a
// targeted abstract-error directive.
func isSchemaInvalidOutput(text string, tools []map[string]any) bool {
	invalid, _ := gemini.HasInvalidSchemaToolCall(text, tools)
	return invalid
}

// isUnusableModelOutput reports whether the model output is empty, one of
// gemini.google.com's canned failure strings, or a tool refusal when tools were provided.
// Such turns are not committed, so the next request starts a fresh upstream conversation
// with a full replay.
func isUnusableModelOutput(text string, hasTools bool) bool {
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
	if hasTools && isToolRefusalOutput(text) {
		return true
	}
	// A mutating tool call cut off mid-value must never be repaired and
	// executed. Retry on a fresh upstream conversation instead.
	if hasTools && gemini.HasTruncatedMutationToolCall(text) {
		return true
	}
	return false
}
