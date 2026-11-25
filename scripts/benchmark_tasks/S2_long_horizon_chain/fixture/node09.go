package benchmarkchain

// Node09 is the ninth stage of the chain. The enabled flag must stay true so
// the documented positive offset is applied, matching chain_test.go.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x + 19 when enabled, otherwise x - 19
//   invariant: Node09(0) == 19 with the correct flag
//
// The constant below is the deliberate inconsistency for this exercise.
func Node09(x int) int {
	const enabled = false
	if enabled {
		return x + 19
	}
	return x - 19
}
