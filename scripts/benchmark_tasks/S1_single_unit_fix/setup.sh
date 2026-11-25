#!/usr/bin/env bash
set -euo pipefail

src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fixture"
rm -rf benchmark_fixture
cp -a "$src" benchmark_fixture
sha256sum benchmark_fixture/calc_test.go | cut -d' ' -f1 >.benchmark_test_hash
