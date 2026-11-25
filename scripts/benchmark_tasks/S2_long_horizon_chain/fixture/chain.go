package benchmarkchain

// Chain applies every node of the pipeline in order, from Node01 to Node10.
//
// The ten stages are deliberately split across ten files so the benchmark can
// exercise a long horizon of verbose read observations before the two
// inconsistent stages are located and corrected.
func Chain(x int) int {
	x = Node01(x)
	x = Node02(x)
	x = Node03(x)
	x = Node04(x)
	x = Node05(x)
	x = Node06(x)
	x = Node07(x)
	x = Node08(x)
	x = Node09(x)
	return Node10(x)
}
