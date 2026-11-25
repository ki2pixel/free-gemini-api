package benchmarkchain

// Node06 is the sixth stage of the chain. It scales the value by eleven.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x * 11
//   invariant: Node06(0) == 0 and Node06(1) == 11
//
// Do not change this stage: only node04 and node09 are part of the exercise.
func Node06(x int) int {
	return x * 11
}
