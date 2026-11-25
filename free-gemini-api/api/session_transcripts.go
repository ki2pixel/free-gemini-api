package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	lastAccess   time.Time // LRU timestamp, refreshed on every session access
}

var (
	sessionTurnMu    sync.Mutex
	sessionTurnState = map[string]*sessionTurnEntry{}
	// totalFlatBytes tracks the sum of len(lastFlat) across all entries. It is
	// protected by sessionTurnMu and never exceeds maxFlatMemorySize.
	totalFlatBytes int64
	// maxFlatMemorySize is the global transcript budget. It is a variable (not
	// a constant) so tests can lower it without allocating 64 MiB.
	maxFlatMemorySize int64 = defaultSessionMemoryBudgetMB * 1024 * 1024
)

const (
	// maxTrackedSessions bounds the number of retained sessions.
	maxTrackedSessions = 256
	// defaultSessionMemoryBudgetMB bounds the cumulative lastFlat transcript
	// memory. Eviction is always safe: an evicted session simply replays its
	// full transcript on the next turn.
	defaultSessionMemoryBudgetMB = 64
)

// ConfigureSessionMemoryBudget applies the optional SESSION_MEMORY_BUDGET_MB
// override. It must run after the process environment (including .env) has
// been loaded, which is why main calls it explicitly instead of using init().
// Invalid or non-positive values keep the 64 MiB default.
func ConfigureSessionMemoryBudget() {
	raw := strings.TrimSpace(os.Getenv("SESSION_MEMORY_BUDGET_MB"))
	if raw == "" {
		return
	}
	mb, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || mb <= 0 {
		log.Printf("⚠️ Ignoring invalid SESSION_MEMORY_BUDGET_MB=%q; keeping %d MiB", raw, defaultSessionMemoryBudgetMB)
		return
	}
	sessionTurnMu.Lock()
	maxFlatMemorySize = mb * 1024 * 1024
	sessionTurnMu.Unlock()
}

// continuationAnchor frames a delta that carries only tool results. Such a
// delta has no user instruction, so an upstream model that lost the thread can
// answer with a greeting or a fresh summary. The anchor is ephemeral: it is
// prepended to the outbound delta only and never stored in lastFlat, so it
// cannot create a false prefix mismatch on the next turn.
const continuationAnchor = "[Continuation] Le dernier appel d'outil est terminé. Reprends le plan en cours au prochain point non réalisé. Ne salue pas, ne redémarre pas et ne produis pas de synthèse finale tant que des modifications ou vérifications restent à faire.\n\n"

// planSessionTurn decides what to send upstream:
//   - the full flattened transcript plus a conversation reset (new session,
//     divergent history, or dead conversation), or
//   - just the delta since the last committed turn, continuing the live
//     upstream conversation.
//
// A delta is only safe when the session identity is proven: the same worker
// must still own the session and its live upstream conversation id must be the
// one the last turn was committed into. An empty or different worker/conversation
// means the transcript suffix is semantically orphaned, so a full replay on a
// fresh conversation is mandatory.
func planSessionTurn(sessionKey, curWorkerID, curConvID, flat string) (sendPrompt string, needReset bool) {
	plan := planSessionTurnWithReason(sessionKey, curWorkerID, curConvID, flat)
	return plan.prompt, plan.needReset
}

// sessionTurnPlan carries the decision plus the observability fields the
// handler logs and exposes on /metrics.
type sessionTurnPlan struct {
	prompt     string
	needReset  bool
	reason     string // "delta" for a continuation, otherwise the replay reason
	deltaBytes int    // transcript suffix size when a delta is sent
}

// planSessionTurnWithReason is planSessionTurn with the replay reason and
// delta length needed by the structured turn summary.
func planSessionTurnWithReason(sessionKey, curWorkerID, curConvID, flat string) sessionTurnPlan {
	sessionTurnMu.Lock()
	defer sessionTurnMu.Unlock()

	reset := func(reason string) sessionTurnPlan {
		return sessionTurnPlan{prompt: flat, needReset: true, reason: reason}
	}

	e := sessionTurnState[sessionKey]
	if e == nil {
		return reset("new")
	}
	e.lastAccess = time.Now()
	if !e.convValid {
		return reset("forced")
	}
	if e.lastFlat == "" {
		return reset("new")
	}
	if e.lastWorkerID == "" || curWorkerID == "" || e.lastWorkerID != curWorkerID {
		log.Printf("🔄 Session continuation reset for %q: worker_mismatch", sessionKey)
		return reset("worker_mismatch")
	}
	if e.lastConvID == "" || curConvID == "" || e.lastConvID != curConvID {
		log.Printf("🔄 Session continuation reset for %q: conv_mismatch", sessionKey)
		return reset("conv_mismatch")
	}
	if flat == e.lastFlat {
		// Identical transcript (e.g. client retry after a client-side error):
		// replay in full on a reset conversation so the turn is not dropped.
		return reset("identical")
	}
	if strings.HasPrefix(flat, e.lastFlat) {
		delta := flat[len(e.lastFlat):]
		if delta == "" {
			return reset("identical")
		}
		deltaBytes := len(delta)
		if !deltaContainsUserInstruction(delta) {
			delta = continuationAnchor + delta
		}
		return sessionTurnPlan{prompt: delta, needReset: false, reason: "delta", deltaBytes: deltaBytes}
	}
	return reset("prefix_mismatch")
}

// deltaContainsUserInstruction reports whether a transcript delta carries a
// real user turn. The flattened prompt renders user messages as lines starting
// with "User: "; tool results use "[Tool Result for ...]" and system notices
// use "[System Notice]". A delta with no such line is anchored for continuity.
func deltaContainsUserInstruction(delta string) bool {
	for _, line := range strings.Split(delta, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t\r"), "User: ") {
			return true
		}
	}
	return false
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
	newFlatBytes := int64(len(flat))
	if entry != nil {
		// Remove the entry from the accounting before eviction so the LRU scan
		// cannot select the entry being updated and delete it from the map.
		totalFlatBytes -= int64(len(entry.lastFlat))
		if totalFlatBytes < 0 {
			totalFlatBytes = 0
		}
		delete(sessionTurnState, sessionKey)
	}
	ensureSessionSlotLocked(newFlatBytes)
	if entry == nil {
		entry = &sessionTurnEntry{}
	}
	sessionTurnState[sessionKey] = entry
	// Update the existing entry in place so the session's doom-loop tracker
	// survives every successful turn.
	if newFlatBytes > maxFlatMemorySize {
		// A single transcript larger than the whole budget cannot be retained
		// without violating the memory invariant. Keep the conversation
		// pointer but drop the transcript: the next turn replays in full.
		entry.lastFlat = ""
	} else {
		entry.lastFlat = flat
		totalFlatBytes += newFlatBytes
	}
	entry.lastConvID = convID
	entry.lastRespID = respID
	entry.lastChoiceID = choiceID
	entry.lastWorkerID = workerID
	entry.convValid = true
	entry.lastAccess = time.Now()
	if entry.tracker == nil {
		entry.tracker = NewDoomLoopTracker()
	}
}

// ensureSessionSlotLocked enforces both bounds before inserting a new session:
// the session count and the cumulative transcript byte budget.
func ensureSessionSlotLocked(requiredBytes int64) {
	for len(sessionTurnState) >= maxTrackedSessions && len(sessionTurnState) > 0 {
		evictOldestSessionLocked()
	}
	evictLRULocked(requiredBytes)
}

// evictLRULocked removes least-recently-used sessions until requiredBytes can
// be added without exceeding maxFlatMemorySize. It is always safe: an evicted
// session falls back to a full replay on its next turn.
func evictLRULocked(requiredBytes int64) {
	for totalFlatBytes+requiredBytes > maxFlatMemorySize && len(sessionTurnState) > 0 {
		evictOldestSessionLocked()
	}
}

func evictOldestSessionLocked() {
	oldestKey := ""
	var oldest *sessionTurnEntry
	for key, entry := range sessionTurnState {
		if oldest == nil || entry.lastAccess.Before(oldest.lastAccess) {
			oldestKey = key
			oldest = entry
		}
	}
	if oldest == nil {
		return
	}
	totalFlatBytes -= int64(len(oldest.lastFlat))
	if totalFlatBytes < 0 {
		totalFlatBytes = 0
	}
	delete(sessionTurnState, oldestKey)
}

// getOrCreateSessionTracker returns the thread-safe doom-loop tracker bound to
// a session. The tracker lives inside the bounded sessionTurnEntry map, so it
// is evicted together with the rest of the session's continuation state.
func getOrCreateSessionTracker(sessionKey string) *DoomLoopTracker {
	sessionTurnMu.Lock()
	defer sessionTurnMu.Unlock()

	if entry := sessionTurnState[sessionKey]; entry != nil {
		entry.lastAccess = time.Now()
		if entry.tracker == nil {
			entry.tracker = NewDoomLoopTracker()
		}
		return entry.tracker
	}
	ensureSessionSlotLocked(0)
	entry := &sessionTurnEntry{tracker: NewDoomLoopTracker(), lastAccess: time.Now()}
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
		tracker.RecordToolCallWithID(
			call.ID,
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
		e.lastAccess = time.Now()
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
		e.lastAccess = time.Now()
	}
}

// isConversationalReset reports whether a tool-bearing turn produced only a
// greeting or a generic help offer. Such output means the upstream model lost
// the conversation context; it is never a valid final answer to a request that
// declares tools. The classifier is deliberately narrow: code questions,
// requests that mention paths or tags, and greetings followed by an actual
// instruction are not classified.
func isConversationalReset(text string, tools []map[string]any) bool {
	if len(tools) == 0 {
		return false
	}
	clean := strings.TrimSpace(text)
	if clean == "" {
		return false
	}
	lower := strings.ToLower(clean)
	if utf8.RuneCountInString(lower) > 160 {
		return false
	}
	if strings.ContainsAny(lower, "`/\\{}") || strings.Contains(lower, "http") {
		return false
	}
	trimmed := strings.TrimLeft(lower, " \t\r\n#*>-•")
	greeting := ""
	for _, lead := range []string{"hello", "hi", "hey", "bonjour", "salut", "good morning", "good afternoon", "good evening", "greetings"} {
		if !strings.HasPrefix(trimmed, lead) {
			continue
		}
		rest := trimmed[len(lead):]
		if rest == "" || strings.ContainsRune("!,.?:; ", rune(rest[0])) {
			greeting = lead
			break
		}
	}
	helpMarkers := []string{
		"how can i help", "how may i help", "how can i assist", "what can i do for you",
		"comment puis-je vous aider", "comment puis-je t'aider",
		"que puis-je faire pour vous", "en quoi puis-je vous aider", "en quoi puis-je t'aider",
	}
	if greeting != "" {
		rest := strings.TrimLeft(trimmed[len(greeting):], " \t\r\n!,.?:;")
		if rest == "" {
			return true
		}
		for _, marker := range helpMarkers {
			if strings.Contains(rest, marker) {
				return true
			}
		}
		if utf8.RuneCountInString(rest) <= 80 &&
			(strings.Contains(rest, "help") || strings.Contains(rest, "aide") || strings.Contains(rest, "assist")) &&
			strings.HasSuffix(rest, "?") {
			return true
		}
		return false
	}
	for _, marker := range helpMarkers {
		if strings.Contains(trimmed, marker) {
			return true
		}
	}
	if utf8.RuneCountInString(trimmed) < 80 &&
		(strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "!")) &&
		(strings.Contains(trimmed, "aide") || strings.Contains(trimmed, "help") || strings.Contains(trimmed, "assist")) {
		return true
	}
	return false
}

// isIncompleteFinalProse reports whether a tool-bearing turn stopped on a very
// short visible fragment that has no sentence-ending punctuation. It is the
// Argon v3.5.7 shape: the upstream stream ended after 21 characters and the
// proxy returned a successful stop. The predicate intentionally excludes any
// text that contains a path, a code span, a colon, or a URL: those are
// legitimate short answers ("README.md", "Done.").
func isIncompleteFinalProse(text string, tools []map[string]any) bool {
	if len(tools) == 0 {
		return false
	}
	clean := strings.TrimSpace(text)
	if clean == "" {
		return false
	}
	if gemini.ArrangeGeminiOutputWithTools(clean, tools).IsToolCall {
		return false
	}
	runes := []rune(clean)
	if len(runes) < 10 || len(runes) > 80 {
		return false
	}
	if strings.ContainsAny(clean, "\n`/.:") || strings.Contains(strings.ToLower(clean), "http") {
		return false
	}
	switch runes[len(runes)-1] {
	case '.', '!', '?', '…', '。', '！', '？':
		return false
	}
	return true
}

// maxSessionLedgerPaths bounds the per-session evidence ledger. A session that
// performs more mutations than this evicts its oldest confirmed paths; the
// only consequence is a conservative retry, never a false success.
const maxSessionLedgerPaths = 256

// sessionLedger records the filesystem mutations confirmed by tool results.
// It is the deterministic evidence behind completion claims: a path cited in a
// final report must have been written by a tool whose result did not report an
// error. The ledger is per session, bounded, and protected by its own mutex.
type sessionLedger struct {
	mu                 sync.Mutex
	mutationsConfirmed map[string]struct{}
	pendingMutations   map[string][]string
	// validationsConfirmed records successful shell commands that actually
	// validated the workspace (tests, vet, formatting, build). A completion
	// report claiming that tests/build passed must be backed by one of them.
	validationsConfirmed map[string]struct{}
	pendingValidations   map[string]string
	mutationOrder        []string
	validationOrder      []string
}

var (
	sessionLedgerMu sync.Mutex
	sessionLedgers  = map[string]*sessionLedger{}
)

func getOrCreateSessionLedger(sessionKey string) *sessionLedger {
	if sessionKey == "" {
		return nil
	}
	sessionLedgerMu.Lock()
	defer sessionLedgerMu.Unlock()
	if ledger := sessionLedgers[sessionKey]; ledger != nil {
		return ledger
	}
	if len(sessionLedgers) >= maxTrackedSessions {
		for key := range sessionLedgers {
			delete(sessionLedgers, key)
			break
		}
	}
	ledger := &sessionLedger{
		mutationsConfirmed:   make(map[string]struct{}),
		pendingMutations:     make(map[string][]string),
		validationsConfirmed: make(map[string]struct{}),
		pendingValidations:   make(map[string]string),
	}
	sessionLedgers[sessionKey] = ledger
	return ledger
}

// recordSessionToolCallPaths remembers the paths a mutation call targets,
// keyed by tool_call_id, until the host returns the corresponding tool result.
func recordSessionToolCallPaths(sessionKey, callID, toolName, argsJSON string) {
	if sessionKey == "" || callID == "" {
		return
	}
	paths := mutationPathsForToolCall(toolName, argsJSON)
	validation := validationCommandForToolCall(toolName, argsJSON)
	if len(paths) == 0 && validation == "" {
		return
	}
	if len(paths) > maxSessionLedgerPaths {
		paths = paths[:maxSessionLedgerPaths]
	}
	ledger := getOrCreateSessionLedger(sessionKey)
	if ledger == nil {
		return
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if len(paths) > 0 {
		if len(ledger.pendingMutations) >= maxSessionLedgerPaths {
			for id := range ledger.pendingMutations {
				delete(ledger.pendingMutations, id)
				break
			}
		}
		ledger.pendingMutations[callID] = paths
	}
	if validation != "" {
		if len(ledger.pendingValidations) >= maxSessionLedgerPaths {
			for id := range ledger.pendingValidations {
				delete(ledger.pendingValidations, id)
				break
			}
		}
		ledger.pendingValidations[callID] = validation
	}
}

// recordSessionToolResultOutcome promotes the pending paths of a tool call to
// confirmed mutations only when the tool result is an explicit success. A
// failed mutation is never evidence, even if a later prose report claims it.
func recordSessionToolResultOutcome(sessionKey, toolCallID, resultText string, isError bool) {
	if sessionKey == "" || toolCallID == "" {
		return
	}
	ledger := getOrCreateSessionLedger(sessionKey)
	if ledger == nil {
		return
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	paths := ledger.pendingMutations[toolCallID]
	delete(ledger.pendingMutations, toolCallID)
	validation := ledger.pendingValidations[toolCallID]
	delete(ledger.pendingValidations, toolCallID)
	if isError || toolResultIndicatesError(resultText) {
		return
	}
	for _, p := range paths {
		ledger.addMutationLocked(p)
	}
	if validation != "" {
		ledger.addValidationLocked(validation)
	}
}

func (l *sessionLedger) addMutationLocked(p string) {
	p = normalizeSessionPath(p)
	if p == "" {
		return
	}
	if _, ok := l.mutationsConfirmed[p]; ok {
		return
	}
	if len(l.mutationsConfirmed) >= maxSessionLedgerPaths {
		for len(l.mutationOrder) > 0 {
			oldest := l.mutationOrder[0]
			l.mutationOrder = l.mutationOrder[1:]
			if _, ok := l.mutationsConfirmed[oldest]; ok {
				delete(l.mutationsConfirmed, oldest)
				break
			}
		}
	}
	l.mutationsConfirmed[p] = struct{}{}
	l.mutationOrder = append(l.mutationOrder, p)
}

func (l *sessionLedger) addValidationLocked(command string) {
	command = normalizeValidationCommand(command)
	if command == "" {
		return
	}
	if _, ok := l.validationsConfirmed[command]; ok {
		return
	}
	if len(l.validationsConfirmed) >= maxSessionLedgerPaths {
		for len(l.validationOrder) > 0 {
			oldest := l.validationOrder[0]
			l.validationOrder = l.validationOrder[1:]
			if _, ok := l.validationsConfirmed[oldest]; ok {
				delete(l.validationsConfirmed, oldest)
				break
			}
		}
	}
	l.validationsConfirmed[command] = struct{}{}
	l.validationOrder = append(l.validationOrder, command)
}

// recordSessionLedgerFromMessages replays the request transcript into the
// ledger. The harness sends the full history on every turn, so the assistant
// tool calls and their tool results are correlated by tool_call_id here.
func recordSessionLedgerFromMessages(sessionKey string, messages []OpenAIChatMessage) {
	if sessionKey == "" || len(messages) == 0 {
		return
	}
	for _, message := range messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			for _, call := range message.ToolCalls {
				recordSessionToolCallPaths(sessionKey, call.ID, call.Function.Name, call.Function.Arguments)
			}
		case "tool":
			content := FlattenContent(message.Content)
			recordSessionToolResultOutcome(sessionKey, message.ToolCallID, content, toolResultIndicatesError(content))
		}
	}
}

// recordSessionLedgerToolCalls records the calls actually emitted to the host.
// It keeps the ledger complete even when a client sends only the tool result
// message on the next turn without replaying the assistant call.
func recordSessionLedgerToolCalls(sessionKey string, calls []gemini.ToolCall) {
	for _, call := range calls {
		recordSessionToolCallPaths(sessionKey, call.ID, call.Function.Name, call.Function.Arguments)
	}
}

func toolResultIndicatesError(resultText string) bool {
	lower := strings.ToLower(strings.TrimSpace(resultText))
	if lower == "" {
		return false
	}
	for _, marker := range recentToolErrorMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.HasPrefix(lower, "error:") ||
		strings.HasPrefix(lower, "error ") ||
		strings.HasPrefix(lower, "failed:")
}

// validationCommandForToolCall returns the successful validation command a
// shell call represents, or "" when the call does not validate the workspace.
// The command is recorded as pending until the matching tool result proves it
// completed without an error.
func validationCommandForToolCall(toolName, argsJSON string) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch name {
	case "shell", "bash", "exec", "run", "terminal":
	default:
		return ""
	}
	var args map[string]any
	if strings.TrimSpace(argsJSON) != "" {
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}
	for _, key := range []string{"command", "cmd"} {
		command, ok := args[key].(string)
		if !ok || !isValidationShellCommand(command) {
			continue
		}
		return strings.TrimSpace(command)
	}
	return ""
}

// isValidationShellCommand reports whether a shell command actually validates
// the repository. These are the deterministic evidence commands behind a
// "tests passed" or "build succeeded" completion claim.
func isValidationShellCommand(command string) bool {
	lower := strings.ToLower(command)
	for _, marker := range []string{
		"go test",
		"go vet",
		"gofmt",
		"go build",
		"audit_check.sh",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func normalizeValidationCommand(command string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(command))), " ")
}

// sessionHasSuccessfulValidation reports whether the session ledger holds at
// least one confirmed validation command. A failed or absent command can never
// back a "tests passed" completion claim.
func sessionHasSuccessfulValidation(sessionKey string) bool {
	if sessionKey == "" {
		return false
	}
	sessionLedgerMu.Lock()
	ledger := sessionLedgers[sessionKey]
	sessionLedgerMu.Unlock()
	if ledger == nil {
		return false
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return len(ledger.validationsConfirmed) > 0
}

// mutationPathsForToolCall extracts the normalized paths a call can mutate.
// Shell commands are parsed for the common file-moving/creating verbs and
// redirects; a glob is never treated as evidence for a concrete path.
func mutationPathsForToolCall(toolName, argsJSON string) []string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	var args map[string]any
	if strings.TrimSpace(argsJSON) != "" {
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}
	var raw []string
	switch name {
	case "write", "edit", "multiedit", "patch", "apply_patch", "replace":
		for _, key := range []string{"path", "file_path", "filePath", "filepath"} {
			if value, ok := args[key].(string); ok {
				raw = append(raw, value)
			}
		}
		for _, listKey := range []string{"files", "edits"} {
			items, ok := args[listKey].([]any)
			if !ok {
				continue
			}
			for _, item := range items {
				obj, ok := item.(map[string]any)
				if !ok {
					continue
				}
				for _, key := range []string{"path", "file_path", "filePath", "filepath"} {
					if value, ok := obj[key].(string); ok {
						raw = append(raw, value)
					}
				}
			}
		}
	case "shell", "bash", "exec", "run", "terminal":
		for _, key := range []string{"command", "cmd"} {
			if value, ok := args[key].(string); ok {
				raw = append(raw, shellMutationPaths(value)...)
			}
		}
	}
	return uniqueNormalizedPaths(raw)
}

func shellMutationPaths(command string) []string {
	fields := strings.Fields(command)
	var out []string
	for i := 0; i < len(fields); i++ {
		token := strings.Trim(fields[i], `"'`)
		switch {
		case token == ">" || token == ">>" || token == "tee":
			if i+1 < len(fields) {
				out = append(out, strings.Trim(fields[i+1], `"'`))
			}
		case strings.HasPrefix(token, ">") && len(token) > 1:
			out = append(out, strings.TrimPrefix(strings.TrimPrefix(token, ">>"), ">"))
		case token == "mkdir" || token == "touch" || token == "rm" || token == "rmdir" || token == "unlink":
			for j := i + 1; j < len(fields); j++ {
				arg := strings.Trim(fields[j], `"'`)
				if strings.HasPrefix(arg, "-") {
					continue
				}
				if strings.ContainsAny(arg, "|&;") {
					break
				}
				out = append(out, arg)
			}
		case token == "mv" || token == "cp":
			operands := shellOperands(fields[i+1:])
			if len(operands) >= 2 {
				out = append(out, operands[len(operands)-2:]...)
			}
		case token == "git" && i+1 < len(fields) && (fields[i+1] == "mv" || fields[i+1] == "rm"):
			operands := shellOperands(fields[i+2:])
			if len(operands) >= 2 {
				out = append(out, operands[len(operands)-2:]...)
			}
		}
	}
	return out
}

func shellOperands(fields []string) []string {
	var out []string
	for _, field := range fields {
		arg := strings.Trim(field, `"'`)
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.ContainsAny(arg, "|&;") {
			break
		}
		out = append(out, arg)
	}
	return out
}

func uniqueNormalizedPaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		normalized := normalizeSessionPath(p)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeSessionPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, "`'\"")
	p = strings.TrimPrefix(p, "file://")
	p = strings.ReplaceAll(p, "\\", "/")
	// Claim reports frequently end a cited path with sentence punctuation
	// ("`docs/README.md.`"). The file itself does not contain that punctuation,
	// so strip it before comparing with the confirmed mutation set.
	p = strings.TrimRight(p, ".,;:!?")
	if p == "" || p == "." {
		return ""
	}
	if strings.Contains(p, "*") {
		return ""
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == "/" {
		return ""
	}
	return strings.TrimSuffix(cleaned, "/")
}

// completionClaimSnippets is the observed v3.5.7 completion lexicon. It is a
// safety net for claims that cite no concrete path; the ledger is the strong
// invariant when paths are present.
var completionClaimSnippets = []string{
	"ont été entièrement réalisés",
	"ont été entièrement réalisées",
	"ont été entièrement exécutés",
	"ont été entièrement exécutées",
	"entièrement réalisé",
	"entièrement réalisée",
	"résumé des livrables",
	"livrables réalisés",
	"dans le working tree",
	"conformément au plan d'action validé",
	"the task is complete",
	"all files have been updated",
	"the plan has been fully executed",
	"everything is in place",
	// Bilingual completion forms added with the Wave P2 lexicon extension.
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
	// Earlier completion forms already covered by pseudoReportSnippets.
	"a été réalisée et vérifiée",
	"a été effectuée avec succès",
	"ont été mis à jour et synchronisés",
	"a été appliqué avec succès",
	"a été appliquée avec succès",
	"a été formalisé",
	"a été formalisée",
	"exécuté avec succès",
	"exécutée avec succès",
	"a été entièrement exécuté",
	"a été entièrement exécutée",
}

var completionClaimPairs = [][2]string{
	{"tous les fichiers", "ont été"},
	{"l'ensemble des", "a été"},
}

// validationClaimSnippets are completion claims about test, lint, build or
// audit execution. They require a successful validation command in the session
// ledger: a prose report alone is never evidence that a command ran.
var validationClaimSnippets = []string{
	"les tests sont passés",
	"les tests ont réussi",
	"tests unitaires validés",
	"tests unitaires réussis",
	"tests validés avec succès",
	"tous les tests sont passés",
	"tous les tests passent",
	"la suite de tests a réussi",
	"audit_check.sh a validé",
	"audit_check.sh a réussi",
	"l'audit a réussi",
	"compilation réussie",
	"la compilation a réussi",
	"le build a réussi",
	"build réussi",
	"go test a réussi",
	"go build a réussi",
	"go vet a réussi",
	"gofmt a validé",
	"tests passed",
	"all tests passed",
	"all tests pass",
	"tests are passing",
	"build succeeded",
	"the build succeeded",
	"compilation succeeded",
	"compilation is successful",
	"the audit passed",
	"validation passed",
	"validations passed",
}

var (
	backtickedClaimRegex = regexp.MustCompile("`([^`\\n]{1,200})`")
	claimedPathSuffixes  = []string{".md", ".go", ".json", ".yml", ".yaml", ".sh", ".txt", ".sql", ".mod", ".sum", ".html", ".css", ".js", ".ts", ".tsx", ".jsx", ".py", ".rs", ".toml", ".env"}
)

func isCompletionClaim(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	// A question is a request for information, not a completion claim, even
	// when it quotes a past-tense completion form.
	if strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "？") {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, snippet := range completionClaimSnippets {
		if strings.Contains(lower, snippet) {
			return true
		}
	}
	for _, pair := range completionClaimPairs {
		if strings.Contains(lower, pair[0]) && strings.Contains(lower, pair[1]) {
			return true
		}
	}
	return false
}

// isValidationClaim reports whether text claims that tests, lint, build or the
// project audit ran successfully. Such a claim is only acceptable when the
// session ledger contains a matching successful shell command.
func isValidationClaim(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "？") {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, snippet := range validationClaimSnippets {
		if strings.Contains(lower, snippet) {
			return true
		}
	}
	return false
}

// isUnbackedCompletion reports whether a tool-bearing turn claims completion
// while citing a path that no successful tool result confirmed. A claim with
// no cited path falls back to the observed completion lexicon. The classifier
// never applies when a validated tool call is present or when the request
// declares no tools.
func isUnbackedCompletion(cleanText string, tools []map[string]any, sessionKey string) bool {
	if len(tools) == 0 || strings.TrimSpace(cleanText) == "" {
		return false
	}
	arranged := gemini.ArrangeGeminiOutputWithTools(cleanText, tools)
	if arranged.IsToolCall || len(arranged.ToolCalls) > 0 {
		return false
	}
	text := arranged.CleanText
	claim := isCompletionClaim(text)
	validationClaim := isValidationClaim(text)
	if !claim && !validationClaim {
		return false
	}
	// A test/build/audit success claim is only backed by a shell command that
	// actually ran and returned without an error. A cited file mutation does
	// not prove that the suite was executed.
	if validationClaim && !sessionHasSuccessfulValidation(sessionKey) {
		return true
	}
	if !claim {
		return false
	}
	paths := extractClaimedPaths(text)
	if len(paths) == 0 {
		return true
	}
	mutations := sessionMutationSnapshot(sessionKey)
	for _, p := range paths {
		if !pathBackedBy(p, mutations) {
			return true
		}
	}
	return false
}

func extractClaimedPaths(text string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, match := range backtickedClaimRegex.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		raw := strings.TrimSpace(match[1])
		if !looksLikeClaimedPath(raw) {
			continue
		}
		normalized := normalizeSessionPath(raw)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func looksLikeClaimedPath(raw string) bool {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ".,;:!?")
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") || strings.Contains(raw, "://") {
		return false
	}
	if strings.Contains(raw, "*") || strings.HasPrefix(raw, "-") {
		return false
	}
	if strings.Contains(raw, "/") {
		return true
	}
	lower := strings.ToLower(raw)
	for _, suffix := range claimedPathSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	switch lower {
	case "dockerfile", "makefile", "license":
		return true
	}
	return false
}

func pathBackedBy(claim string, mutations map[string]struct{}) bool {
	claim = normalizeSessionPath(claim)
	if claim == "" || len(mutations) == 0 {
		return false
	}
	if _, ok := mutations[claim]; ok {
		return true
	}
	// A directory claim is backed when a confirmed mutation lives inside it.
	for mutation := range mutations {
		if strings.HasPrefix(mutation, claim+"/") {
			return true
		}
	}
	return false
}

func sessionMutationSnapshot(sessionKey string) map[string]struct{} {
	if sessionKey == "" {
		return nil
	}
	sessionLedgerMu.Lock()
	ledger := sessionLedgers[sessionKey]
	sessionLedgerMu.Unlock()
	if ledger == nil {
		return nil
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if len(ledger.mutationsConfirmed) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(ledger.mutationsConfirmed))
	for p := range ledger.mutationsConfirmed {
		out[p] = struct{}{}
	}
	return out
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
		// Canned upstream failure strings emitted by gemini.google.com when its
		// own generation aborts. Observed in the v3.5.5 Challenge 9 transcripts
		// (debug/8_*): the error prose was returned to the host as a normal
		// successful stop, ending the run at the first message.
		"i encountered an error doing what you asked",
		"could you try again?",
		"sorry, something went wrong",
		"something went wrong. please try your request again",
		"please try your request again",
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
	// Premature completion claims observed in the v3.5.4 Challenge 8
	// transcripts: the model declares the plan applied/formalised, or hands
	// the remaining edits back to the user, instead of calling the tools.
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
	// Passive transition shapes: the model describes the edits it should make
	// instead of emitting the corresponding tool calls.
	"voici les modifications clés",
	"modifications clés à appliquer",
	"modifications à appliquer dans les fichiers",
	"les points clés à mettre à jour",
	"points clés à mettre à jour",
	"voici les modifications à apporter",
	// Premature completion claims observed in the v3.5.5 Challenge 9
	// transcript (Hydrogen-Task Msg 05): the model closes the plan with a
	// success conjugation of "exécuter" and a "bilan des actions et
	// vérifications" section while AGENTS.md is still unmodified. Every
	// singular/plural and masculine/feminine inflection is listed explicitly:
	// the classifier is a deterministic substring match, not a stemmer.
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
	// Premature completion report observed in the v3.5.7 Hydrogen transcript
	// (msg 05): the claim uses plural past-tense forms and the "working tree"
	// wording that the earlier lexicon missed.
	"ont été entièrement réalisés",
	"ont été entièrement réalisées",
	"ont été entièrement exécutés",
	"ont été entièrement exécutées",
	"entièrement réalisé",
	"entièrement réalisée",
	"résumé des livrables",
	"livrables réalisés",
	"dans le working tree",
	"conformément au plan d'action validé",
	"the task is complete",
	"all files have been updated",
	"the plan has been fully executed",
	"everything is in place",
	// Wave P2 bilingual completion lexicon extension.
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
	"oldstring not found",
	"old_string not found",
	"oldstring must not be empty",
	"old_string must not be empty",
	"file not found",
	"permission denied",
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
	lowerText := strings.ToLower(text)
	for _, phrase := range []string{
		"hard time fulfilling your request",
		"i seem to be encountering an error",
	} {
		if strings.Contains(lowerText, phrase) {
			return true
		}
	}
	// Without tools the same prose is a legitimate final answer.
	if len(tools) == 0 {
		return false
	}
	// A mutating tool call cut off mid-value must never be repaired and
	// executed. Retry on a fresh upstream conversation instead. This is
	// checked before the tool-call shortcut because the truncated call is
	// not a validated action.
	if gemini.HasTruncatedMutationToolCall(text) {
		return true
	}
	arranged := gemini.ArrangeGeminiOutputWithTools(text, tools)
	// An untagged mutation was quarantined from CleanText. Even if a valid
	// tagged call is also present, the turn is not safe to commit: the model
	// was cut off inside a second payload and a retry is required.
	if arranged.UntaggedMutation {
		return true
	}
	// A real, arranger-validated tool call always wins over the prose
	// classifiers below: a payload that merely quotes a canned error, a
	// refusal, or a completion marker is still a legitimate action.
	if arranged.IsToolCall || len(arranged.ToolCalls) > 0 {
		return false
	}
	// A thought-only turn has no visible content and no call: the model
	// stopped while still reasoning (a closed thought, an unclosed thought, or
	// a thought cut off before a tool call). Nothing was executed, so the turn
	// must be replayed on a clean conversation instead of being returned as a
	// successful stop.
	if strings.TrimSpace(arranged.CleanText) == "" {
		return true
	}
	// A greeting on a tool-bearing request signals a lost upstream context;
	// it is never a valid final answer.
	if isConversationalReset(arranged.CleanText, tools) {
		return true
	}
	// A very short, unpunctuated visible fragment signals an upstream stream
	// cut off after the reasoning block, not a completed answer.
	if isIncompleteFinalProse(arranged.CleanText, tools) {
		return true
	}
	// Canned upstream failure strings emitted by gemini.google.com when its
	// own generation aborts. They are intercepted on tool-bearing requests
	// only: a plain chat may legitimately apologize and offer to try again,
	// but an agentic turn that answers prose instead of calling its tools
	// executed nothing and must be replayed. This is the v3.5.5 Challenge 9
	// Hydrogen shape, where the canned error reached OpenCode as a normal
	// successful stop and ended the run at its first message.
	for _, phrase := range []string{
		"i encountered an error doing what you asked",
		"could you try again?",
		"sorry, something went wrong",
		"something went wrong. please try your request again",
		"please try your request again",
	} {
		if strings.Contains(lowerText, phrase) {
			return true
		}
	}
	if isToolRefusalOutput(text, tools) {
		return true
	}
	// A purely textual progress/completion summary on a tool-bearing request
	// means the model stopped before calling the next required tool.
	if isPseudoProgressReport(text, tools) {
		return true
	}
	return false
}

// isEmptyGenerationOutput reports whether a tool-bearing generation produced
// strictly no visible content and no tool call: the upstream stream was
// aborted, or the model stopped inside a thought block. Such a turn executed
// nothing. The retry loops charge it to a dedicated technical budget so a cut
// stream or a thought-only hiccup cannot starve the functional budget reserved
// for pseudo-reports, refusals, schema errors, and truncations.
func isEmptyGenerationOutput(text string, tools []map[string]any) bool {
	if len(tools) == 0 {
		return strings.TrimSpace(text) == ""
	}
	arranged := gemini.ArrangeGeminiOutputWithTools(text, tools)
	return !arranged.IsToolCall && len(arranged.ToolCalls) == 0 && strings.TrimSpace(arranged.CleanText) == ""
}

// shouldBlockUnusableToolTurn reports whether the final tool-bearing output
// must be replaced by an explicit error instead of being returned as a
// successful stop. It covers an empty/thought-only turn, a quarantined
// untagged mutation, and any output already classified as unusable (greeting,
// incomplete prose, refusal, pseudo-report, or canned failure). A real,
// arranger-validated tool call is never blocked.
func shouldBlockUnusableToolTurn(arranged *gemini.ArrangedOutput, rawText string, tools []map[string]any) bool {
	if arranged == nil {
		return false
	}
	if arranged.UntaggedMutation {
		return true
	}
	if arranged.IsToolCall || len(arranged.ToolCalls) > 0 {
		return false
	}
	if strings.TrimSpace(arranged.CleanText) == "" {
		return true
	}
	return len(tools) > 0 && isUnusableModelOutput(rawText, tools)
}
