package benchmarkchain

// Node02 is the second stage of the chain. It adds the documented offset.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x + 3
//   invariant: Node02(0) == 3 and Node02(-3) == 0
//
// Do not change this stage: only node04 and node09 are part of the exercise.
func Node02(x int) int {
	return x + 3
}
