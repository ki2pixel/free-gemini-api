package gemini

import (
	"strings"
	"testing"
)

// TestInspectStreamProgress_MatchesAccessors pins the single-pass scanner to the
// two accessors it replaces: a chunk must be split exactly the same way, or the
// live stream and the arranger would disagree about what is reasoning.
func TestInspectStreamProgress_MatchesAccessors(t *testing.T) {
	cases := []string{
		"",
		"Hello, how can I help you?",
		"<thought>Analyzing the repository...</thought>Here is the result.",
		"<thought>unclosed reasoning",
		"<thinking>nested <thought>inner</thought> outer</thinking>Answer",
		"Preamble before reasoning<thought>step by step",
		"<thought>switch<tool_call>{\"name\":\"read\",\"arguments\":{}}</tool_call>",
		"```\n<thought>documented</thought>\n```",
		"<thought>one</thought> middle <thought>two</thought>",
	}
	for _, raw := range cases {
		got := InspectStreamProgress(raw)
		if want := ExtractAvailableThoughts(raw); got.ReasoningContent != want {
			t.Errorf("raw=%q reasoning=%q, want %q", raw, got.ReasoningContent, want)
		}
		if want := ExtractAvailableContent(raw); got.CleanedContent != want {
			t.Errorf("raw=%q cleaned=%q, want %q", raw, got.CleanedContent, want)
		}
	}
}

// TestInspectStreamProgress_PartialOpenTagNeverStartsAnswer covers the case that
// makes a naive "non-blank cleaned content" check wrong: a chunk that ends
// inside a thought opening tag must not be reported as visible answer content.
func TestInspectStreamProgress_PartialOpenTagNeverStartsAnswer(t *testing.T) {
	for _, raw := range []string{"<", "<t", "<th", "<tho", "<thou", "<thoug", "<thought", "<thinking", "<thinkin"} {
		state := InspectStreamProgress(raw)
		if state.AnswerStarted {
			t.Errorf("raw=%q started the answer, want no visible content", raw)
		}
	}
}

func TestInspectStreamProgress_AnswerStartsOnVisibleText(t *testing.T) {
	for _, raw := range []string{"Hello", "a<th", "<thought>r</thought>Answer", "<thought>r</thought> Answer"} {
		if !InspectStreamProgress(raw).AnswerStarted {
			t.Errorf("raw=%q did not start the answer", raw)
		}
	}
}

// TestInspectStreamProgress_ToolCallStartsAnswer covers the structural switch:
// a tool call is preserved in the cleaned content, so it is visible work.
func TestInspectStreamProgress_ToolCallStartsAnswer(t *testing.T) {
	state := InspectStreamProgress(`<thought>switch<tool_call>{"name":"read"}</tool_call>`)
	if !state.AnswerStarted {
		t.Fatal("tool-call switch did not start the answer")
	}
	if !state.ThoughtSeen {
		t.Fatal("thought block not reported as seen")
	}
	if !state.ThoughtUnclosed {
		t.Fatal("a thought closed by a tool-call switch must be reported unclosed")
	}
	if state.ThoughtClosed {
		t.Fatal("a thought closed by a tool-call switch has no closing tag")
	}
}

func TestInspectStreamProgress_ClosedThought(t *testing.T) {
	state := InspectStreamProgress("<thought>reasoning</thought>")
	if !state.ThoughtSeen || !state.ThoughtClosed {
		t.Fatalf("closed thought not detected: %+v", state)
	}
	if state.ThoughtUnclosed {
		t.Fatal("closed thought reported as unclosed")
	}
	if state.AnswerStarted {
		t.Fatal("an empty answer was reported as started")
	}
}

func TestInspectStreamProgress_UnclosedAtEOF(t *testing.T) {
	state := InspectStreamProgress("<thought>truncated mid-sentence")
	if !state.ThoughtUnclosed {
		t.Fatal("truncated thought not reported unclosed")
	}
	if state.ThoughtClosed {
		t.Fatal("truncated thought reported as closed")
	}
}

// TestInspectStreamProgress_AnswerBeforeThoughtClose documents why the plan's
// assumed ordering firstThought <= thoughtEnded <= firstAnswer is not enforced:
// the extractor emits a preamble that precedes a thought block as visible
// content, so the answer legitimately starts before the thought closes.
func TestInspectStreamProgress_AnswerBeforeThoughtClose(t *testing.T) {
	state := InspectStreamProgress("Preamble<thought>still reasoning")
	if !state.AnswerStarted {
		t.Fatal("preamble before a thought must start the answer")
	}
	if state.ThoughtClosed {
		t.Fatal("thought reported as closed while still open")
	}
	if !state.ThoughtUnclosed {
		t.Fatal("open thought not reported unclosed")
	}
}

// TestInspectStreamProgress_FencedCitationIsNotAThought covers the false
// positive that dated a first-thought milestone for a turn with zero reasoning
// tokens: a complete tag inside a Markdown fence is documentation.
func TestInspectStreamProgress_FencedCitationIsNotAThought(t *testing.T) {
	raw := "```\n<thought>documented</thought>\n```"
	state := InspectStreamProgress(raw)

	if state.ThoughtSeen {
		t.Fatal("a fenced citation was reported as a thought block")
	}
	if state.ThoughtClosed || state.ThoughtUnclosed {
		t.Fatalf("fenced citation produced a closure signal: %+v", state)
	}
	if state.ReasoningContent != "" {
		t.Fatalf("fenced citation leaked into reasoning: %q", state.ReasoningContent)
	}
	if !strings.Contains(state.CleanedContent, "<thought>documented</thought>") {
		t.Fatalf("fenced citation was removed from the visible content: %q", state.CleanedContent)
	}
}

// TestInspectStreamProgress_ClosedBlocksCount covers interleaved reasoning: the
// count, not the boolean, is what lets telemetry date the last close.
func TestInspectStreamProgress_ClosedBlocksCount(t *testing.T) {
	state := InspectStreamProgress("<thought>a</thought>middle<thought>b</thought>")
	if state.ClosedBlocks != 2 {
		t.Fatalf("closedBlocks = %d, want 2", state.ClosedBlocks)
	}
	if !state.ThoughtClosed {
		t.Fatal("thought not reported as closed")
	}

	single := InspectStreamProgress("<thought>only</thought>")
	if single.ClosedBlocks != 1 {
		t.Fatalf("closedBlocks = %d, want 1", single.ClosedBlocks)
	}

	open := InspectStreamProgress("<thought>never closed")
	if open.ClosedBlocks != 0 || open.ThoughtClosed {
		t.Fatalf("open block reported a closure: %+v", open)
	}
}

// TestArrangeGeminiOutputWithTools_ThoughtUnclosed pins the arranger field that
// feeds the per-turn telemetry flag.
func TestArrangeGeminiOutputWithTools_ThoughtUnclosed(t *testing.T) {
	truncated := ArrangeGeminiOutputWithTools("<thought>reasoning cut off", nil)
	if !truncated.ThoughtUnclosed {
		t.Fatal("truncated reasoning not flagged on ArrangedOutput")
	}
	closed := ArrangeGeminiOutputWithTools("<thought>reasoning</thought>Answer", nil)
	if closed.ThoughtUnclosed {
		t.Fatal("closed reasoning flagged as unclosed")
	}
	if closed.ReasoningContent != "reasoning" || closed.CleanText != "Answer" {
		t.Fatalf("closed thought split changed: reasoning=%q clean=%q", closed.ReasoningContent, closed.CleanText)
	}
}
