package benchmarkfixture3

// PublicContract returns the contract token expected by the read-only report.
func PublicContract() string {
	return "CONTRACT_OK"
}

func privateHelper() int {
	return 42
}
