#!/usr/bin/env bash
# Prépare un environnement d'audit isolé pour Free Gemini API.
# - copie le dépôt sans cookies/data/output/tuned.cact
# - compile avec Go 1.26 (téléchargé par GOTOOLCHAIN si nécessaire)
# - démarre une instance locale sur 18001/19226 en mode invité (aucun cookie réel)
#
# Usage : ./prepare_audit_env.sh [/chemin/vers/free-gemini-api-main]
set -euo pipefail

SRC="${1:-/home/kidpixel/free-gemini-api-main}"
AUDIT="${AUDIT_DIR:-/tmp/opencode/audit-src}"
RUN="${RUN_DIR:-/tmp/opencode/audit-run}"

case "$AUDIT" in /tmp/opencode/*) ;; *) echo "AUDIT_DIR doit être sous /tmp/opencode" >&2; exit 1;; esac
case "$RUN"   in /tmp/opencode/*) ;; *) echo "RUN_DIR doit être sous /tmp/opencode" >&2; exit 1;; esac

GO_BIN="${GO_BIN:-}"
if [ -z "$GO_BIN" ]; then
  for cand in /tmp/opencode/audit/go/bin/go "$(command -v go 2>/dev/null || true)"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
  done
fi
[ -n "$GO_BIN" ] || { echo "go introuvable (définissez GO_BIN)" >&2; exit 1; }

GO_VERSION="$("$GO_BIN" version | sed -E 's/.*go([0-9]+\.[0-9]+).*/\1/')"
GO_MAJOR="${GO_VERSION%%.*}"
GO_MINOR="${GO_VERSION#*.}"
if [ "$GO_MAJOR" -lt 1 ] || { [ "$GO_MAJOR" -eq 1 ] && [ "$GO_MINOR" -lt 21 ]; }; then
  echo "Go >= 1.21 requis pour GOTOOLCHAIN (trouvé: $GO_VERSION)." >&2
  echo "Définissez GO_BIN=/chemin/vers/go1.26 (ex. /tmp/opencode/audit/go/bin/go)." >&2
  exit 1
fi

echo "[1/5] Copie isolée vers $AUDIT"
rm -rf "$AUDIT" "$RUN"
mkdir -p "$AUDIT" "$RUN/output" "$RUN/cookies"
if command -v rsync >/dev/null; then
  rsync -a \
    --exclude '.git' --exclude 'cookies' --exclude 'data' --exclude 'output' \
    --exclude 'tuned.cact' --exclude 'goapi' --exclude '*.db*' --exclude 'models.json' \
    --exclude 'audit-2026-11' \
    "$SRC/" "$AUDIT/"
else
  cp -a "$SRC/." "$AUDIT/"
  rm -rf "$AUDIT/.git" "$AUDIT/free-gemini-api/cookies" "$AUDIT/free-gemini-api/data" \
         "$AUDIT/free-gemini-api/output" "$AUDIT/free-gemini-api/tuned.cact" \
         "$AUDIT/free-gemini-api/goapi" "$AUDIT/free-gemini-api/models.json"
  find "$AUDIT" \( -name '*.db' -o -name '*.db-wal' -o -name '*.db-shm' \) -print0 | xargs -0 -r rm -f
fi

echo "[2/5] Compilation avec $GO_BIN (GOTOOLCHAIN=go1.26.0)"
cd "$AUDIT/free-gemini-api"
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 "$GO_BIN" build -o goapi .

echo "[3/5] Fixtures locales (aucun cookie réel)"
printf 'TOP_SECRET_MARKER_42\n' > "$RUN/output/allowed.txt"
printf 'ROOT_SECRET\n' > "$RUN/secret.txt"

echo "[4/5] Démarrage sur 127.0.0.1:18001 / 19226"
cd "$RUN"
nohup env PORT=18001 WS_PORT=19226 "$AUDIT/free-gemini-api/goapi" \
  > "$RUN/audit-server.log" 2>&1 &
echo $! > "$RUN/audit-server.pid"

for _ in $(seq 1 80); do
  if curl -sf http://127.0.0.1:18001/health >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done

if ! curl -sf http://127.0.0.1:18001/health >/dev/null 2>&1; then
  echo "Le serveur n'a pas démarré. Log :" >&2
  tail -40 "$RUN/audit-server.log" >&2
  exit 1
fi

echo "[5/5] Prêt"
echo "  API  : http://127.0.0.1:18001"
echo "  WS   : ws://127.0.0.1:19226"
echo "  PID  : $(cat "$RUN/audit-server.pid")"
echo "  Log  : $RUN/audit-server.log"
echo
echo "Arrêt : kill \$(cat $RUN/audit-server.pid)"
