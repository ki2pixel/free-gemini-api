#!/usr/bin/env bash
set -euo pipefail

src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fixture"
rm -rf benchmark_fixture2
cp -a "$src" benchmark_fixture2
sha256sum benchmark_fixture2/shape_test.go | cut -d' ' -f1 >.benchmark_test_hash
