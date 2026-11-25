#!/usr/bin/env bash
set -euo pipefail

# The fix must live in summarize.go: the test file is hashed by setup.sh.
test "$(sha256sum benchmark_edge/summarize_test.go | cut -d' ' -f1)" = "$(cat .benchmark_test_hash)"
(cd benchmark_edge && go test ./...)
