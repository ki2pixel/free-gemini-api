package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Doom-loop intervention actions returned by DoomLoopTracker.CheckIntervention.
const (
	ActionNone            = "none"
	ActionWarnRepeat      = "warn_repeat"
	ActionWarnPassiveRead = "warn_passive_read"
	ActionHalt            = "halt"
)

const (
	// doomLoopHistoryLimit bounds the per-session fingerprint history. Cycle
	// detection only needs the last few calls, so a small ring is enough.
	doomLoopHistoryLimit = 32

	// Signal A: identical consecutive calls.
	doomLoopRepeatWarnThreshold = 3
	doomLoopRepeatHaltThreshold = 5

	// LoopRoute: an identical call whose previous occurrence failed is a much
	// stronger signal than a plain repeat, so it warns and halts earlier.
	doomLoopRepeatFailedWarnThreshold = 2
	doomLoopRepeatFailedHaltThreshold = 4

	// Signal B: consecutive read-only calls without a mutation.
	doomLoopPassiveReadThreshold = 4

	// A/B/A/B and A/B/C/A/B/C cycles are warned at 2 full repetitions and
	// stopped at 3 full repetitions. Identical calls keep their dedicated
	// 3/5 thresholds above.
	doomLoopCycleHaltRepeats = 3

	doomLoopMaxReadPaths = 8
)

type doomLoopCall struct {
	fingerprint string
	name        string
	isMutation  bool
	callID      string
	failed      bool
}

// DoomLoopTracker is a mutex-protected finite-state tracker for one session.
// It records canonical tool-call fingerprints and exposes the next
// intervention the chat pipeline should apply.
type DoomLoopTracker struct {
	mu sync.Mutex

	history          []doomLoopCall
	repeatCount      int
	lastCallName     string
	consecutiveReads int
	readPaths        []string
}

// NewDoomLoopTracker creates an empty tracker.
func NewDoomLoopTracker() *DoomLoopTracker {
	return &DoomLoopTracker{}
}

// RecordToolCall records one emitted tool call. The fingerprint is derived
// from the tool name and a canonical (sorted-key) form of its JSON arguments,
// so semantically identical calls with a different key order count as repeats.
func (t *DoomLoopTracker) RecordToolCall(name, argsJSON string, isMutation bool) {
	t.RecordToolCallWithID("", name, argsJSON, isMutation)
}

// RecordToolCallWithID is RecordToolCall plus the host-visible call id, which
// lets the next turn attach the tool result's success or failure to this entry.
func (t *DoomLoopTracker) RecordToolCallWithID(callID, name, argsJSON string, isMutation bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	name = strings.TrimSpace(name)
	if name == "" {
		name = "unknown"
	}

	fingerprint := canonicalCallFingerprint(name, argsJSON)
	if n := len(t.history); n > 0 && t.history[n-1].fingerprint == fingerprint {
		t.repeatCount++
	} else {
		t.repeatCount = 1
	}
	t.lastCallName = name

	t.history = append(t.history, doomLoopCall{
		fingerprint: fingerprint,
		name:        name,
		isMutation:  isMutation,
		callID:      callID,
	})
	if len(t.history) > doomLoopHistoryLimit {
		t.history = append([]doomLoopCall(nil), t.history[len(t.history)-doomLoopHistoryLimit:]...)
	}

	if isMutation {
		// A mutation ends the passive-inspection streak and starts a fresh
		// evidence set for the next warning.
		t.consecutiveReads = 0
		t.readPaths = nil
		return
	}

	if isReadTool(name) {
		t.consecutiveReads++
		t.addReadPathLocked(readTarget(name, argsJSON))
	}
}

// RecordToolOutcome attaches the result of a previously emitted call to its
// history entry. The bounded history is scanned backwards, so the most recent
// match wins and no second index has to be kept in sync.
func (t *DoomLoopTracker) RecordToolOutcome(callID string, isError bool) {
	if t == nil || strings.TrimSpace(callID) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for index := len(t.history) - 1; index >= 0; index-- {
		if t.history[index].callID == callID {
			t.history[index].failed = isError
			return
		}
	}
}

// failedRepeatCountLocked counts the trailing history entries that share the
// last fingerprint and failed. Callers must hold t.mu.
func (t *DoomLoopTracker) failedRepeatCountLocked() int {
	n := len(t.history)
	if n == 0 {
		return 0
	}
	fingerprint := t.history[n-1].fingerprint
	count := 0
	for index := n - 1; index >= 0; index-- {
		if t.history[index].fingerprint != fingerprint || !t.history[index].failed {
			break
		}
		count++
	}
	return count
}

// CheckIntervention returns the strongest pending action and a compact detail
// string. Detail prefixes ("repeat:", "cycle:", "passive_read:") are also used
// as Prometheus signal labels by the caller.
func (t *DoomLoopTracker) CheckIntervention() (string, string) {
	if t == nil {
		return ActionNone, ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	failedRepeats := t.failedRepeatCountLocked()

	if t.repeatCount >= doomLoopRepeatHaltThreshold {
		return ActionHalt, fmt.Sprintf("repeat:identical tool call '%s' repeated %d times", t.lastCallName, t.repeatCount)
	}
	if failedRepeats >= doomLoopRepeatFailedHaltThreshold {
		return ActionHalt, fmt.Sprintf("repeat:identical tool call '%s' failed %d consecutive times", t.lastCallName, failedRepeats)
	}
	if names, repeats := t.detectCycleLocked(); repeats >= doomLoopCycleHaltRepeats {
		return ActionHalt, fmt.Sprintf("cycle:rotating tool-call cycle %s repeated %d times", strings.Join(names, " -> "), repeats)
	}
	if failedRepeats >= doomLoopRepeatFailedWarnThreshold {
		return ActionWarnRepeat, fmt.Sprintf("repeat:identical tool call '%s' failed and was repeated %d times", t.lastCallName, failedRepeats)
	}
	if t.repeatCount >= doomLoopRepeatWarnThreshold {
		return ActionWarnRepeat, fmt.Sprintf("repeat:tool call '%s' repeated %d times", t.lastCallName, t.repeatCount)
	}
	if names, repeats := t.detectCycleLocked(); repeats >= 2 {
		return ActionWarnRepeat, fmt.Sprintf("cycle:rotating tool-call cycle %s repeated %d times", strings.Join(names, " -> "), repeats)
	}
	if t.consecutiveReads > doomLoopPassiveReadThreshold {
		files := strings.Join(t.readPaths, ", ")
		if files == "" {
			files = "(fichiers déjà inspectés)"
		}
		return ActionWarnPassiveRead, "passive_read:" + files
	}
	return ActionNone, ""
}

// Reset clears the tracker when an explicit new task starts.
func (t *DoomLoopTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.history = nil
	t.repeatCount = 0
	t.lastCallName = ""
	t.consecutiveReads = 0
	t.readPaths = nil
}

// detectCycleLocked looks for the shortest repeating suffix of length 2 or 3
// (A/B/A/B or A/B/C/A/B/C) and returns the pattern names plus the number of
// consecutive full repetitions. Callers must hold t.mu.
func (t *DoomLoopTracker) detectCycleLocked() ([]string, int) {
	n := len(t.history)
	for patternLen := 2; patternLen <= 3; patternLen++ {
		if n < patternLen*2 {
			continue
		}
		start := n - patternLen*2
		matches := true
		for i := 0; i < patternLen; i++ {
			if t.history[start+i].fingerprint != t.history[start+patternLen+i].fingerprint {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}

		repeats := 2
		for {
			prevStart := n - (repeats+1)*patternLen
			if prevStart < 0 {
				break
			}
			match := true
			for i := 0; i < patternLen; i++ {
				if t.history[prevStart+i].fingerprint != t.history[n-patternLen+i].fingerprint {
					match = false
					break
				}
			}
			if !match {
				break
			}
			repeats++
		}

		names := make([]string, patternLen)
		for i := 0; i < patternLen; i++ {
			names[i] = t.history[n-patternLen+i].name
		}
		return names, repeats
	}
	return nil, 0
}

func (t *DoomLoopTracker) addReadPathLocked(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	for _, existing := range t.readPaths {
		if existing == path {
			return
		}
	}
	t.readPaths = append(t.readPaths, path)
	if len(t.readPaths) > doomLoopMaxReadPaths {
		t.readPaths = t.readPaths[len(t.readPaths)-doomLoopMaxReadPaths:]
	}
}

// canonicalCallFingerprint hashes the normalized tool name and a canonical
// JSON representation of its arguments. encoding/json sorts map keys, making
// the fingerprint insensitive to argument-order changes.
func canonicalCallFingerprint(name, argsJSON string) string {
	canonical := strings.ToLower(strings.TrimSpace(name)) + "\x00" + canonicalJSON(argsJSON)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func canonicalJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return raw
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(encoded)
}

// isReadTool reports whether name belongs to the read-only inspection family
// used by Signal B. Unknown tools neither increment nor reset the streak.
func isReadTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "glob", "grep", "list", "ls", "search", "find", "view", "cat":
		return true
	default:
		return false
	}
}

// readTarget extracts a human-readable file/pattern from a read call so the
// passive-read warning can list the files already inspected.
func readTarget(name, argsJSON string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return name
	}
	for _, key := range []string{
		"path", "file_path", "filePath", "filepath", "target_file",
		"directory", "dir",
	} {
		if value, ok := args[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	if value, ok := args["pattern"].(string); ok {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return name
}

// doomLoopSignal maps an intervention detail to the Prometheus signal label.
func doomLoopSignal(action, detail string) string {
	switch {
	case strings.HasPrefix(detail, "cycle:"):
		return "cycle"
	case strings.HasPrefix(detail, "passive_read:"):
		return "passive_read"
	case action == ActionWarnPassiveRead:
		return "passive_read"
	default:
		return "repeat"
	}
}

// doomLoopHumanDetail strips the internal signal prefix before a detail is
// shown to the client or written to the log.
func doomLoopHumanDetail(detail string) string {
	for _, prefix := range []string{"repeat:", "cycle:", "passive_read:"} {
		if strings.HasPrefix(detail, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(detail, prefix))
		}
	}
	return strings.TrimSpace(detail)
}

// requestStartsFreshTask reports whether the client replayed only system/user
// messages. That shape marks the beginning of a new task (or a client-side
// retry of the opening request) and clears stale loop state.
func requestStartsFreshTask(messages []OpenAIChatMessage) bool {
	for _, message := range messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant", "tool":
			return false
		}
	}
	return true
}

// requestStartsNewUserTurn reports whether the last non-system message is a
// user message. A new user instruction is an explicit task boundary and must
// clear a prior circuit-breaker state, even when the replayed transcript still
// contains earlier assistant/tool messages.
func requestStartsNewUserTurn(messages []OpenAIChatMessage) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		role := strings.ToLower(strings.TrimSpace(messages[index].Role))
		if role == "system" {
			continue
		}
		return role == "user"
	}
	return false
}

// formatDoomLoopDirective builds a maximum-recency role:user reminder. It is
// appended after the transcript and before the final "Assistant:" turn cue so
// the model sees it at the end of the prompt without invalidating the cached
// prefix of the conversation itself.
func formatDoomLoopDirective(action, detail string) string {
	switch action {
	case ActionWarnPassiveRead:
		files := strings.TrimPrefix(detail, "passive_read:")
		if strings.TrimSpace(files) == "" {
			files = "(fichiers déjà inspectés)"
		}
		return fmt.Sprintf("\nUser: [ANTI-LOOP DIRECTIVE: Tu as déjà inspecté les fichiers %s. Interdiction de relire ces fichiers. Émets immédiatement l'appel d'outil d'écriture (write ou edit) pour appliquer le changement prévu.]\n", files)
	case ActionWarnRepeat:
		reason := strings.TrimPrefix(detail, "repeat:")
		reason = strings.TrimPrefix(reason, "cycle:")
		if strings.TrimSpace(reason) == "" {
			reason = "appel identique ou cycle d'appels"
		}
		return fmt.Sprintf("\nUser: [ANTI-LOOP DIRECTIVE: Action répétée détectée (%s). Change immédiatement de stratégie : n'émets plus le même appel d'outil. Applique la prochaine mutation (write ou edit) ou explique précisément le blocage.]\n", reason)
	default:
		return ""
	}
}
