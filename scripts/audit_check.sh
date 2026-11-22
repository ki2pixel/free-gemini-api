#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR/free-gemini-api"

echo "=== [1/3] Vérification du formatage Go (gofmt) ==="
if [ -n "$(gofmt -l .)" ]; then
  echo "Erreur: des fichiers ne sont pas formatés correctement (gofmt)"
  gofmt -l .
  exit 1
fi
echo "OK: gofmt clean"

echo "=== [2/3] Analyse statique Go vet (via Docker golang:1.26-alpine) ==="
docker run --rm -v "$(pwd):/app" -w /app golang:1.26-alpine go vet ./...
echo "OK: go vet clean"

echo "=== [3/3] Exécution des tests unitaires et couverture (via Docker golang:1.26-alpine) ==="
docker run --rm -v "$(pwd):/app" -w /app golang:1.26-alpine go test -short -cover ./...
echo "=== TOUS LES CONTRÔLES D'AUDIT SONT PASSÉS AVEC SUCCÈS (exit 0) ==="
