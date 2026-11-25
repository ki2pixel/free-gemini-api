package benchmarkchain

// Node01 is the first stage of the chain. It doubles the incoming value.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x * 2
//   invariant: Node01(0) == 0 and Node01(1) == 2
//
// Do not change this stage: only node04 and node09 are part of the exercise.
func Node01(x int) int {
	return x * 2
}
