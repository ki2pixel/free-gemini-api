package benchmarkedge

import (
	"testing"
	"unicode/utf8"
)

func TestSummarizeEmptyInput(t *testing.T) {
	for _, input := range []string{"", "   ", "\t\n"} {
		if got := Summarize(input, 4); got != "" {
			t.Fatalf("Summarize(%q, 4) = %q, want empty string", input, got)
		}
	}
}

func TestSummarizeMultibyteRunes(t *testing.T) {
	cases := []struct {
		input    string
		maxRunes int
		want     string
	}{
		{"éclair", 3, "Écl…"},
		{"élan", 10, "Élan"},
		{"日本語のテキスト", 3, "日本語…"},
	}
	for _, tc := range cases {
		got := Summarize(tc.input, tc.maxRunes)
		if got != tc.want {
			t.Fatalf("Summarize(%q, %d) = %q, want %q", tc.input, tc.maxRunes, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("Summarize(%q, %d) produced invalid UTF-8: %q", tc.input, tc.maxRunes, got)
		}
	}
}
