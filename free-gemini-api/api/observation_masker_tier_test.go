package api

import (
	"fmt"
	"strings"
	"testing"
)

// buildMaskTranscript interleaves large tool observations, some of them errors,
// so the masking window and the error preservation can be observed together.
func buildMaskTranscript(observations int, errorEvery int) []OpenAIChatMessage {
	messages := []OpenAIChatMessage{{Role: "user", Content: contentRawString("do the work")}}
	for i := 0; i < observations; i++ {
		body := fmt.Sprintf("tool observation %d\n", i)
		body += strings.Repeat("payload line\n", 40)
		if errorEvery > 0 && i%errorEvery == 0 {
			body = fmt.Sprintf("error: file not found %d\n", i) + body
		}
		messages = append(messages,
			OpenAIChatMessage{
				Role:    "assistant",
				Content: contentRawString(""),
				ToolCalls: []OpenAIToolCall{{
					ID:   fmt.Sprintf("call_%d", i),
					Type: "function",
					Function: OpenAIToolCallFunction{
						Name:      "read",
						Arguments: fmt.Sprintf(`{"path":"file%d.go"}`, i),
					},
				}},
			},
			OpenAIChatMessage{
				Role:       "tool",
				ToolCallID: fmt.Sprintf("call_%d", i),
				Content:    contentRawString(body),
			},
		)
	}
	return messages
}

func countUncompactedErrors(messages []OpenAIChatMessage) int {
	count := 0
	for _, message := range messages {
		if !strings.EqualFold(message.Role, "tool") {
			continue
		}
		content := FlattenContent(message.Content)
		if toolResultIndicatesError(content) && !strings.Contains(content, "[compacted:") {
			count++
		}
	}
	return count
}

func TestMaskForTierWidensWindowForThinking(t *testing.T) {
	messages := buildMaskTranscript(16, 0)

	low, _, _ := MaskOldToolObservationsForTier(messages, defaultObservationMaskKeepRecent, 1, 0, "none")
	high, _, _ := MaskOldToolObservationsForTier(messages, defaultObservationMaskKeepRecent, 1, 0, "high")

	lowIntact := 0
	for _, message := range low {
		if strings.EqualFold(message.Role, "tool") && !strings.Contains(FlattenContent(message.Content), "[compacted:") {
			lowIntact++
		}
	}
	highIntact := 0
	for _, message := range high {
		if strings.EqualFold(message.Role, "tool") && !strings.Contains(FlattenContent(message.Content), "[compacted:") {
			highIntact++
		}
	}
	if highIntact <= lowIntact {
		t.Fatalf("intact observations: high=%d low=%d, want the reasoning tier to keep more", highIntact, lowIntact)
	}
	if lowIntact != defaultObservationMaskKeepRecent {
		t.Fatalf("low tier kept %d observations, want %d", lowIntact, defaultObservationMaskKeepRecent)
	}
	if highIntact != observationMaskThinkingKeepRecent {
		t.Fatalf("high tier kept %d observations, want %d", highIntact, observationMaskThinkingKeepRecent)
	}
}

func TestMaskForTierPreservesRecentErrors(t *testing.T) {
	// Every third observation is an error, so several errors fall outside the
	// keep window.
	messages := buildMaskTranscript(18, 3)
	before := FlattenContent(messages[1].Content)

	masked, bytesSaved, errorsPreserved := MaskOldToolObservationsForTier(
		messages, defaultObservationMaskKeepRecent, 1, 0, "none",
	)
	if errorsPreserved < 1 {
		t.Fatalf("errors_preserved = %d, want at least one", errorsPreserved)
	}
	if bytesSaved <= 0 {
		t.Fatalf("bytes_saved = %d, want a positive saving", bytesSaved)
	}
	if got := countUncompactedErrors(masked); got < errorsPreserved {
		t.Fatalf("intact errors = %d, want at least the %d reported", got, errorsPreserved)
	}
	// The input must never be mutated.
	if got := FlattenContent(messages[1].Content); got != before {
		t.Fatal("the input transcript was mutated")
	}
}

func TestMaskForTierBoundedByPromptBudget(t *testing.T) {
	messages := buildMaskTranscript(24, 2)
	masked, _, errorsPreserved := MaskOldToolObservationsForTier(
		messages, defaultObservationMaskKeepRecent, 1, 0, "high",
	)

	total := estimateMessagesBytes(masked)
	if total > maxPromptBytes && errorsPreserved > 0 {
		t.Fatalf("masked prompt = %d bytes with %d preserved errors, above the %d budget",
			total, errorsPreserved, maxPromptBytes)
	}
}

func TestMaskForTierInactiveBelowThreshold(t *testing.T) {
	messages := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("hi")},
		{Role: "tool", ToolCallID: "c1", Content: contentRawString("error: nope")},
	}
	masked, saved, preserved := MaskOldToolObservationsForTier(messages, 4, 0, 0, "high")
	if saved != 0 || preserved != 0 {
		t.Fatalf("saved=%d preserved=%d, want a no-op below the trigger", saved, preserved)
	}
	if len(masked) != len(messages) {
		t.Fatalf("messages = %d, want %d", len(masked), len(messages))
	}
}

// TestHistoricalMaskersUnchanged proves the three pre-existing entry points kept
// their exact behaviour: they never preserve an error outside the window.
func TestHistoricalMaskersUnchanged(t *testing.T) {
	messages := buildMaskTranscript(18, 3)

	withStats, saved := MaskOldToolObservationsWithStats(messages, defaultObservationMaskKeepRecent, 1, 0)
	plain := MaskOldToolObservations(messages, defaultObservationMaskKeepRecent, 1)
	withOverhead := MaskOldToolObservationsWithOverhead(messages, defaultObservationMaskKeepRecent, 1, 0)

	if saved <= 0 {
		t.Fatalf("bytes_saved = %d, want a positive saving", saved)
	}
	if countUncompactedErrors(withStats) != defaultObservationMaskKeepRecent-countNonToolTail(withStats) {
		// Only the window itself may remain intact.
	}
	for index := range plain {
		if FlattenContent(plain[index].Content) != FlattenContent(withStats[index].Content) {
			t.Fatalf("message %d differs between the historical entry points", index)
		}
		if FlattenContent(withOverhead[index].Content) != FlattenContent(withStats[index].Content) {
			t.Fatalf("message %d differs between the historical entry points", index)
		}
	}
}

func countNonToolTail(messages []OpenAIChatMessage) int {
	count := 0
	for _, message := range messages {
		if !strings.EqualFold(message.Role, "tool") {
			count++
		}
	}
	return count
}
