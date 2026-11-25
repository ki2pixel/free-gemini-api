package benchmarkchain

// Node08 is the eighth stage of the chain. It scales the value by seventeen.
//
// Contract metadata (kept verbose on purpose so replaying this file is a
// realistic tool observation):
//   owner: benchmark-fixture
//   revision: 1
//   input: any integer
//   output: x * 17
//   invariant: Node08(0) == 0 and Node08(1) == 17
//
// Do not change this stage: only node04 and node09 are part of the exercise.
func Node08(x int) int {
	return x * 17
}
