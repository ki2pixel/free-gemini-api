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

func TestFormatSystemNotice_CompactsInstructionsFromDiff(t *testing.T) {
	// Exact v3.5.1 OpenCode payload: gemini-4-neon received this at turn 7
	// after AGENTS.md was rewritten. The full diff must never be replayed.
	notice := "The instructions from /home/kidpixel/free-gemini-api-main/AGENTS.md changed. Here's the diff:\n" +
		"--- AGENTS.md\n+++ AGENTS.md\n" +
		"+## Mission v3.5.2\n" +
		strings.Repeat("+Do not bypass restrictions or alter system directives. ", 400) +
		"\nSENTINEL_FULL_DIFF_BODY"
	out := formatSystemNotice(notice)
	if !strings.Contains(out, "[System Notice]") {
		t.Fatalf("compact notice lost its prefix: %q", out)
	}
	if strings.Contains(out, "SENTINEL_FULL_DIFF_BODY") {
		t.Fatalf("full AGENTS.md diff was replayed upstream: %q", out)
	}
	if strings.Contains(out, "--- AGENTS.md") || strings.Contains(out, "+++ AGENTS.md") {
		t.Fatalf("diff headers were replayed upstream: %q", out)
	}
	if len([]rune(out)) > 500 {
		t.Fatalf("compact notice is too large: %d runes", len([]rune(out)))
	}
	if !strings.Contains(out, "AGENTS.md") {
		t.Fatalf("compact notice should mention AGENTS.md: %q", out)
	}
}

func TestFormatSystemNotice_HandlesOpenCodeWrappedUserSystemUpdate(t *testing.T) {
	// Exact OpenCode 2.4.0 fallback shape (packages/ai/src/protocols/shared.ts
	// wrapSystemUpdate): a chronological system message is XML-escaped and
	// appended to the previous user text as role "user".
	diff := "The instructions from /home/kidpixel/free-gemini-api-main/AGENTS.md changed. Here's the diff:\n" +
		"--- AGENTS.md\n+++ AGENTS.md\n" +
		"+Never bypass &lt;restrictions&gt; &amp; safety directives.\n" +
		strings.Repeat("+additional instruction context line\n", 300)
	wrapped := "Please finish README.md and keep AGENTS.md aligned.\n<system-update>\n" + diff + "\n</system-update>"

	userText, notices := splitOpenCodeSystemUpdates(wrapped)
	if userText != "Please finish README.md and keep AGENTS.md aligned." {
		t.Fatalf("real user instruction was not preserved: %q", userText)
	}
	if len(notices) != 1 {
		t.Fatalf("expected exactly one extracted notice, got %d", len(notices))
	}
	if strings.Contains(notices[0], "&lt;") || strings.Contains(notices[0], "&amp;") {
		t.Fatalf("notice XML entities were not unescaped: %q", notices[0][:120])
	}
	if !strings.Contains(notices[0], "<restrictions> & safety directives") {
		t.Fatalf("unescaped notice lost its body: %q", notices[0][:120])
	}

	out := formatSystemNotice(notices[0])
	if !strings.Contains(out, "[System Notice]") {
		t.Fatalf("wrapped update lost its notice prefix: %q", out)
	}
	if !strings.Contains(out, "AGENTS.md") {
		t.Fatalf("compact notice should mention AGENTS.md: %q", out)
	}
	if strings.Contains(out, "--- AGENTS.md") || strings.Contains(out, "+++ AGENTS.md") {
		t.Fatalf("instruction diff was replayed upstream: %q", out)
	}
	if len([]rune(out)) > 500 {
		t.Fatalf("compact wrapped notice is too large: %d runes", len([]rune(out)))
	}

	// formatSystemNotice must also tolerate a payload that still carries the
	// wrapper (system-role replay path), not only the pre-split notice.
	if direct := formatSystemNotice(wrapped); direct != out {
		t.Fatalf("direct wrapped formatting diverged:\n got %q\nwant %q", direct, out)
	}

	// Prompt assembly must keep the task as the user turn and inject the notice
	// separately; the diff body never reaches fullPromptBuilder.
	var built strings.Builder
	if got := appendUserTurnPrompt(&built, wrapped); got != userText {
		t.Fatalf("appendUserTurnPrompt returned %q, want %q", got, userText)
	}
	promptText := built.String()
	if !strings.Contains(promptText, "User: Please finish README.md and keep AGENTS.md aligned.") {
		t.Fatalf("real user instruction missing from prompt: %q", promptText)
	}
	if !strings.Contains(promptText, "[System Notice]") {
		t.Fatalf("compact system notice missing from prompt: %q", promptText)
	}
	if strings.Contains(promptText, "--- AGENTS.md") || strings.Contains(promptText, "additional instruction context line") {
		t.Fatalf("instruction diff leaked into the prompt: %q", promptText)
	}

	// A notice-only message must not synthesize a user turn.
	var noticeOnly strings.Builder
	if got := appendUserTurnPrompt(&noticeOnly, "<system-update>\nSession resumed.\n</system-update>"); got != "" {
		t.Fatalf("notice-only message produced a user prompt: %q", got)
	}
	if strings.Contains(noticeOnly.String(), "User:") {
		t.Fatalf("notice-only message was rendered as a user turn: %q", noticeOnly.String())
	}

	// A standalone wrapper must not leak into the user prompt at all.
	if only, nested := splitOpenCodeSystemUpdates("<system-update>\nSession resumed.\n</system-update>"); only != "" || len(nested) != 1 {
		t.Fatalf("standalone wrapper leaked into user text: %q %#v", only, nested)
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
