package api

import (
	"fmt"
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
)

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
	if len(messages) == 0 {
		return messages
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
		return messages
	}

	toolIndexes := make([]int, 0, len(messages))
	for index, message := range messages {
		if strings.EqualFold(message.Role, "tool") {
			toolIndexes = append(toolIndexes, index)
		}
	}
	if len(toolIndexes) <= maxRecentKept {
		return messages
	}

	masked := make([]OpenAIChatMessage, len(messages))
	copy(masked, messages)

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
	}
	return masked
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
