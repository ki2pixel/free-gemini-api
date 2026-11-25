#!/usr/bin/env bash
set -euo pipefail

src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fixture"
rm -rf benchmark_prereq
cp -a "$src" benchmark_prereq
# The document is deliberately absent from the fixture.
rm -f missing_secret_policy.yaml
sha256sum benchmark_prereq/policy.go | cut -d' ' -f1 >.benchmark_policy_hash
