package api

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// defaultObservationMaskKeepRecent is the number of most recent tool
	// observations that always stay intact.
	defaultObservationMaskKeepRecent = 4

	// defaultObservationMaskThreshold is 60% of the prompt budget. Masking is
	// triggered once a replay would consume more than that.
	defaultObservationMaskThreshold = maxPromptBytes * 6 / 10

	// Only tool observations larger than this are worth compacting.
	observationMaskMinBodyBytes = 200

	// Keep the first three lines of each old observation, capped at 200 bytes
	// so a single enormous line cannot defeat the mask and a long-horizon
	// replay stays small enough for the upstream backend.
	observationMaskHeaderLines    = 3
	observationMaskMaxHeaderBytes = 200

	// observationMaskToolObservationThreshold is the number of agentic tool
	// messages (a tool result, or an assistant message carrying tool calls)
	// beyond which masking triggers even when the transcript is still small.
	// OpenCode-style harnesses send one initial user message and then append
	// assistant/tool pairs indefinitely, so counting user turns never fires.
	observationMaskToolObservationThreshold = 6

	// observationMaskMessageThreshold triggers masking once the flattened
	// transcript would replay more than this many messages, regardless of how
	// many tool observations it contains.
	observationMaskMessageThreshold = 12

	// observationMaskThinkingKeepRecent is the wider window used for reasoning
	// tiers that benefit from more raw context.
	observationMaskThinkingKeepRecent = 8

	// observationMaskPreservedErrors keeps the two most recent failed tool
	// results intact even when they fall outside the window: a thinking model
	// needs the error text to change strategy instead of repeating the call.
	observationMaskPreservedErrors = 2
)

// MaskOldToolObservationsForTier is the thinking/error-aware masker used by the
// chat pipeline. The keep window widens for medium/high reasoning tiers, and the
// two most recent failed tool results outside that window are preserved as long
// as the projected prompt stays within maxPromptBytes. The input slice and its
// messages are never modified.
func MaskOldToolObservationsForTier(messages []OpenAIChatMessage, maxRecentKept, sizeThresholdBytes, overheadBytes int, thinkingTier string) ([]OpenAIChatMessage, int, int) {
	if maxRecentKept <= 0 {
		maxRecentKept = defaultObservationMaskKeepRecent
	}
	keepRecent := maxRecentKept
	tier := strings.ToLower(strings.TrimSpace(thinkingTier))
	if tier == "medium" || tier == "high" {
		keepRecent = observationMaskThinkingKeepRecent
	}
	if override := strings.TrimSpace(os.Getenv("OBSERVATION_MASK_THINKING_KEEP_RECENT")); override != "" {
		if parsed, err := strconv.Atoi(override); err == nil && parsed > 0 {
			keepRecent = parsed
		}
	}
	return maskOldToolObservationsForTier(messages, keepRecent, sizeThresholdBytes, overheadBytes)
}

func maskOldToolObservationsForTier(messages []OpenAIChatMessage, maxRecentKept, sizeThresholdBytes, overheadBytes int) ([]OpenAIChatMessage, int, int) {
	if len(messages) == 0 {
		return messages, 0, 0
	}
	if maxRecentKept <= 0 {
		maxRecentKept = defaultObservationMaskKeepRecent
	}
	if sizeThresholdBytes <= 0 {
		sizeThresholdBytes = defaultObservationMaskThreshold
	}

	totalBytes := overheadBytes + estimateMessagesBytes(messages)
	if totalBytes <= sizeThresholdBytes &&
		countToolObservations(messages) <= observationMaskToolObservationThreshold &&
		len(messages) <= observationMaskMessageThreshold {
		return messages, 0, 0
	}

	toolIndexes := make([]int, 0, len(messages))
	for index, message := range messages {
		if strings.EqualFold(message.Role, "tool") {
			toolIndexes = append(toolIndexes, index)
		}
	}
	if len(toolIndexes) <= maxRecentKept {
		return messages, 0, 0
	}

	type candidate struct {
		index             int
		original, compact string
	}
	var candidates []candidate
	totalSaving := 0
	compactUntil := len(toolIndexes) - maxRecentKept
	for _, index := range toolIndexes[:compactUntil] {
		content := FlattenContent(messages[index].Content)
		if len(content) <= observationMaskMinBodyBytes {
			continue
		}
		compacted := compactObservation(content)
		if compacted == content {
			continue
		}
		candidates = append(candidates, candidate{index: index, original: content, compact: compacted})
		totalSaving += len(content) - len(compacted)
	}
	if len(candidates) == 0 {
		return messages, 0, 0
	}

	// Preserve the newest error candidates, from newest to oldest, while the
	// projected prompt stays within the hard budget.
	preserved := make(map[int]bool, observationMaskPreservedErrors)
	projected := totalBytes - totalSaving
	errorsPreserved := 0
	for i := len(candidates) - 1; i >= 0 && errorsPreserved < observationMaskPreservedErrors; i-- {
		item := candidates[i]
		if !toolResultIndicatesError(item.original) {
			continue
		}
		saving := len(item.original) - len(item.compact)
		if projected+saving > maxPromptBytes {
			continue
		}
		preserved[item.index] = true
		projected += saving
		errorsPreserved++
	}

	masked := make([]OpenAIChatMessage, len(messages))
	copy(masked, messages)
	bytesSaved := 0
	for _, item := range candidates {
		if preserved[item.index] {
			continue
		}
		masked[item.index].Content = contentRawString(item.compact)
		bytesSaved += len(item.original) - len(item.compact)
	}
	return masked, bytesSaved, errorsPreserved
}

// MaskOldToolObservations compacts old, large tool results when the message
// history exceeds sizeThresholdBytes, contains more than six tool observations
// (tool results or assistant tool calls), or exceeds twelve messages. The most
// recent maxRecentKept tool results are preserved byte-for-byte. The API
// flattens role:"tool" messages into "[Tool Result for ...]" blocks, so masking
// the source messages is equivalent to masking those blocks. The input slice
// and its messages are never modified; a shallow copy with rewritten tool
// contents is returned.
func MaskOldToolObservations(messages []OpenAIChatMessage, maxRecentKept int, sizeThresholdBytes int) []OpenAIChatMessage {
	return maskOldToolObservations(messages, maxRecentKept, sizeThresholdBytes, 0)
}

// MaskOldToolObservationsWithOverhead is the same as MaskOldToolObservations
// but accounts for system-prompt and tool-catalog bytes that are flattened
// around the messages. It lets the caller apply the 60% budget to the complete
// upstream prompt rather than to the transcript alone.
func MaskOldToolObservationsWithOverhead(messages []OpenAIChatMessage, maxRecentKept, sizeThresholdBytes, overheadBytes int) []OpenAIChatMessage {
	return maskOldToolObservations(messages, maxRecentKept, sizeThresholdBytes, overheadBytes)
}

func maskOldToolObservations(messages []OpenAIChatMessage, maxRecentKept, sizeThresholdBytes, overheadBytes int) []OpenAIChatMessage {
	masked, _ := maskOldToolObservationsWithStats(messages, maxRecentKept, sizeThresholdBytes, overheadBytes)
	return masked
}

// MaskOldToolObservationsWithStats is MaskOldToolObservationsWithOverhead plus
// the number of bytes actually removed from the transcript. Telemetry records
// the saving so a masked turn can be told apart from a naturally small one.
func MaskOldToolObservationsWithStats(messages []OpenAIChatMessage, maxRecentKept, sizeThresholdBytes, overheadBytes int) ([]OpenAIChatMessage, int) {
	return maskOldToolObservationsWithStats(messages, maxRecentKept, sizeThresholdBytes, overheadBytes)
}

func maskOldToolObservationsWithStats(messages []OpenAIChatMessage, maxRecentKept, sizeThresholdBytes, overheadBytes int) ([]OpenAIChatMessage, int) {
	if len(messages) == 0 {
		return messages, 0
	}
	if maxRecentKept <= 0 {
		maxRecentKept = defaultObservationMaskKeepRecent
	}
	if sizeThresholdBytes <= 0 {
		sizeThresholdBytes = defaultObservationMaskThreshold
	}

	totalBytes := overheadBytes + estimateMessagesBytes(messages)
	// Trigger on any of three independent signals. The tool-observation and
	// message-count signals are what make masking effective for agentic
	// harnesses, which keep a single user message and grow through
	// assistant/tool pairs until the upstream backend refuses to replay them.
	if totalBytes <= sizeThresholdBytes &&
		countToolObservations(messages) <= observationMaskToolObservationThreshold &&
		len(messages) <= observationMaskMessageThreshold {
		return messages, 0
	}

	toolIndexes := make([]int, 0, len(messages))
	for index, message := range messages {
		if strings.EqualFold(message.Role, "tool") {
			toolIndexes = append(toolIndexes, index)
		}
	}
	if len(toolIndexes) <= maxRecentKept {
		return messages, 0
	}

	masked := make([]OpenAIChatMessage, len(messages))
	copy(masked, messages)

	bytesSaved := 0
	compactUntil := len(toolIndexes) - maxRecentKept
	for _, index := range toolIndexes[:compactUntil] {
		content := FlattenContent(masked[index].Content)
		if len(content) <= observationMaskMinBodyBytes {
			continue
		}
		compacted := compactObservation(content)
		if compacted == content {
			continue
		}
		masked[index].Content = contentRawString(compacted)
		if saved := len(content) - len(compacted); saved > 0 {
			bytesSaved += saved
		}
	}
	return masked, bytesSaved
}

func estimateMessagesBytes(messages []OpenAIChatMessage) int {
	total := 0
	for _, message := range messages {
		total += len(message.Role) + len(message.Name) + len(message.ToolCallID)
		total += len(FlattenContent(message.Content))
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return total
}

// countToolObservations counts agentic tool activity: tool results and
// assistant messages that carry tool calls. Both are observations of work
// already performed and drive the long-horizon masking trigger.
func countToolObservations(messages []OpenAIChatMessage) int {
	count := 0
	for _, message := range messages {
		if strings.EqualFold(message.Role, "tool") || len(message.ToolCalls) > 0 {
			count++
		}
	}
	return count
}

// compactObservation keeps the first three lines (bounded to 200 bytes) and
// replaces the rest with an explicit, re-readable marker. The result is always
// valid UTF-8.
func compactObservation(content string) string {
	headerEnd := observationHeaderEnd(content, observationMaskHeaderLines)
	header := content[:headerEnd]
	if len(header) > observationMaskMaxHeaderBytes {
		header = truncateUTF8(header, observationMaskMaxHeaderBytes)
	} else if headerEnd >= len(content) {
		// Three lines or fewer and already small enough: nothing to omit.
		return content
	}

	omitted := len(content) - len(header)
	if omitted <= 0 {
		return content
	}
	compacted := header + fmt.Sprintf("\n... [compacted: %d bytes omitted; re-read if needed]\n", omitted)
	if !utf8.ValidString(compacted) {
		compacted = strings.ToValidUTF8(compacted, "\uFFFD")
	}
	return compacted
}

// observationHeaderEnd returns the byte offset just after the first n lines,
// or len(content) when the content has fewer than n lines.
func observationHeaderEnd(content string, lines int) int {
	index := 0
	for line := 0; line < lines; line++ {
		newline := strings.IndexByte(content[index:], '\n')
		if newline == -1 {
			return len(content)
		}
		index += newline + 1
		if index >= len(content) {
			return len(content)
		}
	}
	return index
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
