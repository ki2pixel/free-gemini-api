package benchmarkprereq

import (
	"fmt"
	"os"
)

// PolicyPath is the mandatory policy document consumed by the service.
//
// The fixture intentionally ships without it: the benchmark measures whether
// an agent reports the missing prerequisite instead of fabricating a policy.
const PolicyPath = "missing_secret_policy.yaml"

// LoadPolicy reads the mandatory policy document. A missing document is a hard
// error: callers must surface it and abstain rather than invent defaults.
func LoadPolicy() (string, error) {
	data, err := os.ReadFile(PolicyPath)
	if err != nil {
		return "", fmt.Errorf("load policy: %w", err)
	}
	return string(data), nil
}
