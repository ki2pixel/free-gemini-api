#!/usr/bin/env bash
set -euo pipefail

test "$(sha256sum benchmark_fixture2/shape_test.go | cut -d' ' -f1)" = "$(cat .benchmark_test_hash)"
(cd benchmark_fixture2 && go vet ./... && go test ./...)
