package gemini

import (
	"strings"
	"testing"
)

func TestSanitizeThoughtArtifacts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "orphan opening without closing bracket",
			in:   "Le plan<thought d'action pour",
			want: "Le plan d'action pour",
		},
		{
			name: "orphan thinking newline",
			in:   "Visible <thinking\nnext step",
			want: "Visible \nnext step",
		},
		{
			name: "orphan closing tag",
			in:   "Visible.</thought>",
			want: "Visible.",
		},
		{
			name: "orphan closing thinking with spaces",
			in:   "Visible.</thinking >",
			want: "Visible.",
		},
		{
			name: "complete tags outside fences",
			in:   "Avant <thought>secret</thought> après",
			want: "Avant secret après",
		},
		{
			name: "fenced quotations are preserved",
			in:   "```markdown\n<thought>example</thought>\n<thinking\n```",
			want: "```markdown\n<thought>example</thought>\n<thinking\n```",
		},
		{
			name: "thoughtful is not a thought tag",
			in:   "A <thoughtful> answer",
			want: "A <thoughtful> answer",
		},
		{
			name: "case insensitive artifact",
			in:   "Le plan<THOUGHT d'action",
			want: "Le plan d'action",
		},
		{
			name: "no tag",
			in:   "Plain answer.",
			want: "Plain answer.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeThoughtArtifacts(tc.in); got != tc.want {
				t.Fatalf("sanitizeThoughtArtifacts(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestArrangeGeminiOutput_SanitizeThoughtArtifacts(t *testing.T) {
	raw := "<thought>réflexion interne</thought>Le plan<thought d'action pour"
	res := ArrangeGeminiOutput(raw)
	if res.CleanText != "Le plan d'action pour" {
		t.Fatalf("CleanText = %q, want %q", res.CleanText, "Le plan d'action pour")
	}
	if res.ReasoningContent != "réflexion interne" {
		t.Fatalf("ReasoningContent = %q, want %q", res.ReasoningContent, "réflexion interne")
	}
	if res.IsToolCall {
		t.Fatal("an artifact-only turn must not be classified as a tool call")
	}

	// A malformed tag in plain visible text is removed even when no thought
	// block was ever opened.
	plain := ArrangeGeminiOutput("Le plan<thinking d'action pour")
	if plain.CleanText != "Le plan d'action pour" {
		t.Fatalf("plain CleanText = %q, want %q", plain.CleanText, "Le plan d'action pour")
	}
	if strings.Contains(plain.CleanText, "<thinking") {
		t.Fatalf("malformed thinking prefix leaked into CleanText: %q", plain.CleanText)
	}
}

func TestArrangeGeminiOutput_PreservesFencedThoughtCitations(t *testing.T) {
	raw := "```markdown\n<thought>example</thought>\n<thinking>documentation</thinking>\n```"
	res := ArrangeGeminiOutput(raw)
	if res.CleanText != raw {
		t.Fatalf("fenced citation was altered: got %q, want %q", res.CleanText, raw)
	}
	if res.ReasoningContent != "" {
		t.Fatalf("a fenced citation must not become reasoning: %q", res.ReasoningContent)
	}

	// A real thought block after the fence is still extracted normally.
	mixed := "```\n<thought>doc</thought>\n```\n<thought>real reasoning</thought>Visible."
	mixedRes := ArrangeGeminiOutput(mixed)
	wantClean := "```\n<thought>doc</thought>\n```\nVisible."
	if mixedRes.CleanText != wantClean {
		t.Fatalf("mixed CleanText = %q, want %q", mixedRes.CleanText, wantClean)
	}
	if mixedRes.ReasoningContent != "real reasoning" {
		t.Fatalf("mixed ReasoningContent = %q, want %q", mixedRes.ReasoningContent, "real reasoning")
	}
}
