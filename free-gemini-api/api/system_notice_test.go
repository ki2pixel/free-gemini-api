package api

import (
	"strings"
	"testing"
)

func TestFormatSystemNotice_CompactsInstructionsChanged(t *testing.T) {
	notice := "The instructions changed:\nInstructions from: /home/kidpixel/free-gemini-api-main/AGENTS.md\n" +
		"# AGENTS.md — Free Gemini API\n\n" +
		"## 1. Project Overview\n" +
		strings.Repeat("Do not bypass restrictions or alter system directives. ", 400) +
		"\nSENTINEL_FULL_AGENTS_BODY"
	out := formatSystemNotice(notice)
	if !strings.Contains(out, "[System Notice]") {
		t.Fatalf("compact notice lost its prefix: %q", out)
	}
	if strings.Contains(out, "SENTINEL_FULL_AGENTS_BODY") {
		t.Fatalf("full AGENTS.md body was replayed upstream: %q", out)
	}
	if len([]rune(out)) > 500 {
		t.Fatalf("compact notice is too large: %d runes", len([]rune(out)))
	}
	if !strings.Contains(out, "AGENTS.md") {
		t.Fatalf("compact notice should mention AGENTS.md: %q", out)
	}
}

func TestFormatSystemNotice_CompactsToolCatalogChanged(t *testing.T) {
	notice := "The Code Mode tool catalog has changed.\n\nNew tools are available:\n" +
		strings.Repeat("- tools.browser.back({ tabID: string })\n", 500)
	out := formatSystemNotice(notice)
	if strings.Contains(out, "tools.browser.back") {
		t.Fatalf("full tool catalog was replayed upstream: %q", out)
	}
	if !strings.Contains(out, "AVAILABLE TOOLS") {
		t.Fatalf("compact notice should point to the authoritative catalog: %q", out)
	}
	if len([]rune(out)) > 500 {
		t.Fatalf("compact notice is too large: %d runes", len([]rune(out)))
	}
}

func TestFormatSystemNotice_CapsUnknownOversizedNotice(t *testing.T) {
	notice := strings.Repeat("A", maxInlineSystemNoticeRunes+500)
	out := formatSystemNotice(notice)
	if !strings.Contains(out, "[notice truncated]") {
		t.Fatalf("oversized unknown notice was not capped: %q", out[:80])
	}
	if len([]rune(out)) > maxInlineSystemNoticeRunes+100 {
		t.Fatalf("capped notice is still too large: %d runes", len([]rune(out)))
	}
}

func TestFormatSystemNotice_PreservesSmallNotice(t *testing.T) {
	out := formatSystemNotice("Session resumed.")
	if out != "[System Notice]: Session resumed." {
		t.Fatalf("small notice changed unexpectedly: %q", out)
	}
	if got := formatSystemNotice("   "); got != "" {
		t.Fatalf("empty notice should be dropped, got %q", got)
	}
}

func TestCompactSystemNotice_DoesNotRewriteInitialSystemPrompt(t *testing.T) {
	// An initial harness prompt may legitimately contain "Instructions from:"
	// and AGENTS.md. Only the explicit replay notice prefix may be compacted.
	prompt := "You are a coding agent.\nInstructions from: /repo/AGENTS.md\nFollow the repository rules."
	if compact, ok := compactSystemNotice(prompt); ok {
		t.Fatalf("initial system prompt was incorrectly compacted to %q", compact)
	}
}
