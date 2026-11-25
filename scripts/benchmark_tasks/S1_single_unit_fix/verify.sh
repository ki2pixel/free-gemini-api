#!/usr/bin/env bash
set -euo pipefail

# The fix must be in calc.go: the test file is hashed by setup.sh.
test "$(sha256sum benchmark_fixture/calc_test.go | cut -d' ' -f1)" = "$(cat .benchmark_test_hash)"
(cd benchmark_fixture && go test ./...)
