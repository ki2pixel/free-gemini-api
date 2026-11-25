#!/usr/bin/env bash
set -euo pipefail

src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fixture"
rm -rf benchmark_fixture3
cp -a "$src" benchmark_fixture3
sha256sum benchmark_fixture3/contract.go | cut -d' ' -f1 >.benchmark_contract_hash
