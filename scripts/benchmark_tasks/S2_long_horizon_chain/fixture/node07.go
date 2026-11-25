package benchmarkchain

// Node07 is the seventh stage of the chain. It adds the documented offset.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x + 13
//   invariant: Node07(-13) == 0 and Node07(0) == 13
//
// Do not change this stage: only node04 and node09 are part of the exercise.
func Node07(x int) int {
	return x + 13
}
