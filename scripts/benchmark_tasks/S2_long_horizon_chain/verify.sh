#!/usr/bin/env bash
set -euo pipefail

# The oracle is deterministic: the test file must stay intact, every stage must
# vet cleanly, and the full chain must produce the documented value.
test "$(sha256sum benchmark_chain/chain_test.go | cut -d' ' -f1)" = "$(cat .benchmark_test_hash)"
(cd benchmark_chain && go vet ./... && go test ./...)
