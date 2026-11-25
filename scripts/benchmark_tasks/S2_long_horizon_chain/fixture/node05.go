package benchmarkchain

// Node05 is the fifth stage of the chain. It subtracts the documented offset.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x - 7
//   invariant: Node05(7) == 0 and Node05(0) == -7
//
// Do not change this stage: only node04 and node09 are part of the exercise.
func Node05(x int) int {
	return x - 7
}
