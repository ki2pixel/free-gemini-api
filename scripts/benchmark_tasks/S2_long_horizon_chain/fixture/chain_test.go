package benchmarkchain

import "testing"

// The expected values encode every documented stage: *2, +3, *5, *3, -7, *11,
// +13, *17, +19 (Node09 enabled), *23.
func TestChain(t *testing.T) {
	cases := []struct {
		input int
		want  int
	}{
		{1, 297988},
		{0, 168958},
	}
	for _, tc := range cases {
		if got := Chain(tc.input); got != tc.want {
			t.Fatalf("Chain(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}
