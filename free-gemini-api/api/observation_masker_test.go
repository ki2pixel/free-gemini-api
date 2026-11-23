package api

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func maskerToolMessage(name, content string) OpenAIChatMessage {
	return OpenAIChatMessage{
		Role:       "tool",
		Name:       name,
		ToolCallID: "call_" + name,
		Content:    contentRawString(content),
	}
}

func TestObservationMaskerShortTranscriptUntouched(t *testing.T) {
	messages := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("inspect the repository")},
		maskerToolMessage("read", "short observation"),
	}
	original := FlattenContent(messages[1].Content)

	masked := MaskOldToolObservations(messages, 4, 1<<20)
	if len(masked) != len(messages) {
		t.Fatalf("message count = %d, want %d", len(masked), len(messages))
	}
	if got := FlattenContent(masked[1].Content); got != original {
		t.Fatalf("short transcript was modified: %q", got)
	}
}

func TestObservationMaskerCompactsOldKeepsRecent(t *testing.T) {
	const toolCount = 6
	body := strings.Repeat("ligne de log très volumineuse avec accents éàü\n", 80)
	messages := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("build the project")},
	}
	for i := 0; i < toolCount; i++ {
		messages = append(messages, maskerToolMessage(
			fmt.Sprintf("read-%d", i),
			fmt.Sprintf("fichier %d — en-tête\nseconde ligne\nthird line\n%s", i, body),
		))
	}
	originalFirst := FlattenContent(messages[1].Content)
	originalLast := FlattenContent(messages[toolCount].Content)

	masked := MaskOldToolObservations(messages, 4, 100)
	if len(masked) != len(messages) {
		t.Fatalf("message count = %d, want %d", len(masked), len(messages))
	}

	// The two oldest tool results (6 total - 4 recent) must be compacted.
	for i := 1; i <= 2; i++ {
		content := FlattenContent(masked[i].Content)
		if !strings.Contains(content, "compacted:") {
			t.Errorf("old observation %d was not compacted: %q", i, content)
		}
		if !strings.Contains(content, "fichier "+fmt.Sprint(i-1)) {
			t.Errorf("old observation %d lost its header: %q", i, content)
		}
		if !strings.Contains(content, "re-read if needed") {
			t.Errorf("old observation %d is missing the re-read marker: %q", i, content)
		}
		if masked[i].Role != "tool" {
			t.Errorf("old observation %d role = %q, want tool", i, masked[i].Role)
		}
		if masked[i].ToolCallID != messages[i].ToolCallID {
			t.Errorf("old observation %d tool_call_id changed", i)
		}
		if !utf8.ValidString(content) {
			t.Errorf("old observation %d is not valid UTF-8", i)
		}
	}

	// The four most recent tool results must be byte-identical.
	for i := 3; i <= toolCount; i++ {
		if got, want := FlattenContent(masked[i].Content), FlattenContent(messages[i].Content); got != want {
			t.Errorf("recent observation %d was modified", i)
		}
	}

	// The input must never be mutated.
	if got := FlattenContent(messages[1].Content); got != originalFirst {
		t.Fatal("MaskOldToolObservations mutated the input slice")
	}
	if got := FlattenContent(messages[toolCount].Content); got != originalLast {
		t.Fatal("MaskOldToolObservations mutated the most recent input message")
	}
}

func TestObservationMaskerTurnThresholdTriggers(t *testing.T) {
	// Total size stays far below the byte threshold; the 16 user turns alone
	// must trigger block masking.
	messages := make([]OpenAIChatMessage, 0, 32)
	for turn := 0; turn < 16; turn++ {
		messages = append(messages, OpenAIChatMessage{
			Role:    "user",
			Content: contentRawString(fmt.Sprintf("turn %d", turn)),
		})
		messages = append(messages, maskerToolMessage(
			fmt.Sprintf("read-%d", turn),
			fmt.Sprintf("header %d\nsecond\nthird\n%s", turn, strings.Repeat("x", 300)),
		))
	}

	masked := MaskOldToolObservations(messages, 4, 1<<20)
	compacted := 0
	for _, message := range masked {
		if message.Role == "tool" && strings.Contains(FlattenContent(message.Content), "compacted:") {
			compacted++
		}
	}
	if compacted != 12 {
		t.Fatalf("compacted tool results = %d, want 12 (16 - 4 recent)", compacted)
	}
}

func TestObservationMaskerSkipsSmallBodies(t *testing.T) {
	messages := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("task")},
	}
	for i := 0; i < 6; i++ {
		messages = append(messages, maskerToolMessage("read", fmt.Sprintf("small observation %d", i)))
	}
	masked := MaskOldToolObservations(messages, 4, 1)
	for i := 1; i < len(masked); i++ {
		if got, want := FlattenContent(masked[i].Content), FlattenContent(messages[i].Content); got != want {
			t.Fatalf("small observation %d was compacted: %q", i, got)
		}
	}
}

func TestObservationMaskerHugeSingleLineIsUTF8Safe(t *testing.T) {
	huge := strings.Repeat("é", 5000) // 10 KiB on a single line
	messages := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("task")},
		maskerToolMessage("read", huge),
		maskerToolMessage("read", "recent 1"),
		maskerToolMessage("read", "recent 2"),
		maskerToolMessage("read", "recent 3"),
		maskerToolMessage("read", "recent 4"),
	}

	masked := MaskOldToolObservations(messages, 4, 100)
	content := FlattenContent(masked[1].Content)
	if !strings.Contains(content, "compacted:") {
		t.Fatalf("huge single-line observation was not compacted")
	}
	if !utf8.ValidString(content) {
		t.Fatal("compacted output is not valid UTF-8")
	}
	if len(content) >= len(huge) {
		t.Fatalf("compaction did not reduce the payload: %d >= %d", len(content), len(huge))
	}
}

func TestObservationMaskerDefaults(t *testing.T) {
	if got := MaskOldToolObservations(nil, 0, 0); got != nil {
		t.Fatalf("nil messages = %#v, want nil", got)
	}

	messages := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("task")},
		maskerToolMessage("read", strings.Repeat("small\n", 100)),
	}
	masked := MaskOldToolObservations(messages, 0, 0)
	if got, want := FlattenContent(masked[1].Content), FlattenContent(messages[1].Content); got != want {
		t.Fatal("default threshold must not compact a tiny transcript")
	}
}

func TestObservationMaskerOverheadTriggers(t *testing.T) {
	body := strings.Repeat("header\nsecond\nthird\n", 40)
	messages := []OpenAIChatMessage{
		{Role: "user", Content: contentRawString("task")},
		maskerToolMessage("read-1", body),
		maskerToolMessage("read-2", body),
		maskerToolMessage("read-3", body),
		maskerToolMessage("read-4", body),
		maskerToolMessage("read-5", body),
	}

	// The transcript alone is below 1 MiB, but the simulated system/tool
	// overhead pushes the full prompt over the threshold.
	masked := MaskOldToolObservationsWithOverhead(messages, 4, 1<<20, 2<<20)
	if !strings.Contains(FlattenContent(masked[1].Content), "compacted:") {
		t.Fatal("overhead must count toward the masking threshold")
	}
	if got, want := FlattenContent(masked[2].Content), FlattenContent(messages[2].Content); got != want {
		t.Fatal("the four most recent observations must stay intact")
	}
}
