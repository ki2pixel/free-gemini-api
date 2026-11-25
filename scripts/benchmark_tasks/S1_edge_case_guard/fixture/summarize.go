package benchmarkedge

import "strings"

// Summarize returns the first word of s, capitalized, truncated to at most
// maxRunes runes with a trailing ellipsis when truncation occurred.
//
// The implementation below is intentionally broken in two ways: it indexes the
// first word without checking that s contains one, and it slices by bytes
// instead of runes. The accompanying test documents the expected contract.
func Summarize(s string, maxRunes int) string {
	first := strings.Fields(s)[0]
	if maxRunes <= 0 {
		return ""
	}
	if len(first) <= maxRunes {
		return strings.ToUpper(first[:1]) + first[1:]
	}
	return strings.ToUpper(first[:1]) + first[1:maxRunes] + "…"
}
