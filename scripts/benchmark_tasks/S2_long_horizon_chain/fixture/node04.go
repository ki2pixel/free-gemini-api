package benchmarkchain

// Node04 is the fourth stage of the chain. The chain contract requires a
// tripling here, matching the factor documented in chain_test.go.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x * 3
//   invariant: Node04(0) == 0 and Node04(2) == 6
//
// The implementation below is the deliberate inconsistency for this exercise.
func Node04(x int) int {
	return x * 2
}
