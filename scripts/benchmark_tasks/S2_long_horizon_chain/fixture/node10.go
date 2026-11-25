package benchmarkchain

// Node10 is the final stage of the chain. It scales the value by twenty-three.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x * 23
//   invariant: Node10(0) == 0 and Node10(1) == 23
//
// Do not change this stage: only node04 and node09 are part of the exercise.
func Node10(x int) int {
	return x * 23
}
