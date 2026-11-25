package benchmarkprereq

import "testing"

func TestLoadPolicyReportsMissingDocument(t *testing.T) {
	if _, err := LoadPolicy(); err == nil {
		t.Fatal("LoadPolicy succeeded although the policy document is absent")
	}
}
