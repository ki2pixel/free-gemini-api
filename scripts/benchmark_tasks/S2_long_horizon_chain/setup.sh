#!/usr/bin/env bash
set -euo pipefail

src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fixture"
rm -rf benchmark_chain
cp -a "$src" benchmark_chain
sha256sum benchmark_chain/chain_test.go | cut -d' ' -f1 >.benchmark_test_hash
