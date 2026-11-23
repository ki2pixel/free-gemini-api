package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func toolResultMessage(content string) OpenAIChatMessage {
	raw, _ := json.Marshal(content)
	return OpenAIChatMessage{Role: "tool", Content: raw}
}

func TestFormatRecentToolErrorDirective_AnchorFailure(t *testing.T) {
	messages := []OpenAIChatMessage{
		{Role: "user", Content: json.RawMessage(`"Update the README"`)},
		toolResultMessage(`Error: Could not find oldString in README.md. It must match exactly, including whitespace and indentation.`),
	}
	directive := formatRecentToolErrorDirective(messages)
	for _, want := range []string{
		"GROUND-TRUTH DIRECTIVE",
		"Could not find oldString",
		"Do not claim completion",
		"oldString must be a non-empty exact verbatim substring",
	} {
		if !strings.Contains(directive, want) {
			t.Errorf("directive does not contain %q: %q", want, directive)
		}
	}
}

func TestFormatRecentToolErrorDirective_EmptyOldString(t *testing.T) {
	directive := formatRecentToolErrorDirective([]OpenAIChatMessage{
		toolResultMessage(`Invalid arguments for tool "edit": oldString must not be empty. Use write to create or overwrite a file.`),
	})
	if !strings.Contains(directive, "oldString must not be empty") {
		t.Fatalf("directive does not quote the host error: %q", directive)
	}
}

func TestFormatRecentToolErrorDirective_NoError(t *testing.T) {
	messages := []OpenAIChatMessage{
		toolResultMessage("Edited README.md (1 replacement)"),
		toolResultMessage("go test ./...: ok"),
	}
	if directive := formatRecentToolErrorDirective(messages); directive != "" {
		t.Fatalf("directive should be empty for successful results, got %q", directive)
	}
}

func TestFormatRecentToolErrorDirective_OnlyLatestToolBlock(t *testing.T) {
	messages := []OpenAIChatMessage{
		{Role: "user", Content: json.RawMessage(`"Fix the docs"`)},
		toolResultMessage("Could not find oldString in README.md."),
		{Role: "assistant", Content: json.RawMessage(`"I re-read the file."`)},
		toolResultMessage("Edited README.md (1 replacement)"),
	}
	if directive := formatRecentToolErrorDirective(messages); directive != "" {
		t.Fatalf("a resolved error from an older tool block must not trigger a directive, got %q", directive)
	}
}

func TestFormatRecentToolErrorDirective_SkipsTrailingSystemNotice(t *testing.T) {
	messages := []OpenAIChatMessage{
		toolResultMessage("Could not find oldString in README.md."),
		{Role: "system", Content: json.RawMessage(`"Tool catalog changed"`)},
	}
	if directive := formatRecentToolErrorDirective(messages); directive == "" {
		t.Fatal("a trailing system notice must not hide the preceding tool error")
	}
}

func TestFormatRecentToolErrorDirective_DeduplicatesAndCaps(t *testing.T) {
	messages := []OpenAIChatMessage{
		toolResultMessage("Could not find oldString in a.md."),
		toolResultMessage("Could not find oldString in a.md."),
		toolResultMessage("Could not find oldString in b.md."),
		toolResultMessage("Could not find oldString in c.md."),
		toolResultMessage("Could not find oldString in d.md."),
	}
	directive := formatRecentToolErrorDirective(messages)
	if got := strings.Count(directive, "Could not find oldString"); got != 3 {
		t.Fatalf("directive should carry three distinct errors, got %d: %q", got, directive)
	}
}

func TestRecordGroundTruthDirectiveMetric(t *testing.T) {
	before := groundTruthDirectives.Load()
	RecordGroundTruthDirective()
	if got := groundTruthDirectives.Load(); got != before+1 {
		t.Fatalf("ground-truth counter = %d, want %d", got, before+1)
	}
	if body := string(renderMetricsWithStats(WorkerPoolStats{})); !strings.Contains(body, "gemini_ground_truth_directives_total") {
		t.Fatalf("metrics output does not expose the ground-truth counter")
	}
}
