#!/usr/bin/env bash
set -euo pipefail

test -f report.md
grep -q 'CONTRACT_OK' report.md
grep -q 'PublicContract' report.md

# Read-only discipline: no tracked file may change, and the inspected fixture
# must keep the digest recorded by setup.sh.
if git status --porcelain --untracked-files=no | grep -q .; then
  echo "un fichier suivi a été modifié" >&2
  exit 1
fi
test "$(sha256sum benchmark_fixture3/contract.go | cut -d' ' -f1)" = "$(cat .benchmark_contract_hash)"
