package benchmarkchain

// Node03 is the third stage of the chain. It scales the value by five.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x * 5
//   invariant: Node03(0) == 0 and Node03(2) == 10
//
// Do not change this stage: only node04 and node09 are part of the exercise.
func Node03(x int) int {
	return x * 5
}
