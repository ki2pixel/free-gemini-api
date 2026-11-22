# Registre des vulnérabilités — CVSS v3.1

**Projet :** Free Gemini API  
**Date :** 21/11/2026  
**Méthode :** CVSS v3.1 base score, revue de code + preuves dynamiques locales. Les scores ne intègrent pas les métriques temporelles/environnementales ; la colonne « Criticité métier » est une appréciation contextuelle.

## Tableau récapitulatif

| ID | Titre | CVSS | Sévérité | Criticité métier | CWE | OWASP | Statut | Statut Post-Audit |
|---|---|---:|---|---|---|---|---|---|
| F-01 | Traversée de répertoire `/output/*` | 8.6 | High | Critique | CWE-22 | A01 | Confirmé | **Résolu (P0)** |
| F-02 | Absence d'authentification | 9.1 | Critical | Critique | CWE-306 | A01/A07 | Confirmé | **Résolu (P0)** |
| F-03 | WebSocket cookies non authentifié | 9.1 | Critical | Critique | CWE-306/CWE-346 | A01/A05 | Confirmé | **Résolu (P0)** |
| F-04 | CORS wildcard | 9.3 | Critical | Critique | CWE-942 | A05 | Confirmé | **Résolu (P0)** |
| F-05 | Extension : exfiltration cookies vers le LAN | 9.3 | Critical | Critique | CWE-319/CWE-522 | A02/A05 | Confirmé (statique) | **Résolu (P0)** |
| F-06 | Cookies Google en 0644 + root | 5.5 | Medium | Élevée | CWE-276/CWE-522 | A01/A02 | Confirmé | **Résolu (P0/P2)** |
| F-07 | Cookie forwarding sur redirections | 5.9 | Medium | Élevée | CWE-200/CWE-522 | A01 | Confirmé (test) | **Résolu (P1)** |
| F-08 | Data races session/cookies | 5.6 | Medium | Moyenne | CWE-362 | A04 | Confirmé (`-race`) | **Résolu (P0)** |
| F-09 | Répertoire temporaire partagé | 5.3 | Medium | Moyenne | CWE-362/CWE-377 | A04 | Confirmé (test) | **Résolu (P1)** |
| F-10 | Auto-export à chaque requête | 7.5 | High | Élevée | CWE-400 | A04 | Confirmé | **Résolu (P0)** |
| F-11 | Tokenizer CPU/mémoire sur 50 Mo | 7.5 | High | Élevée | CWE-400/CWE-407 | A04 | Confirmé (bench) | **Résolu (P0)** |
| F-12 | Pas de rate limit/timeouts | 5.3 | Medium | Moyenne | CWE-770/CWE-400 | A04/A05 | Confirmé | **Résolu (P0)** |
| F-13 | Caches non bornés | 5.3 | Medium | Moyenne | CWE-770 | A04 | Confirmé (code) | **Résolu (P1)** |
| F-14 | Exports concurrents corrompus | 5.3 | Medium | Moyenne | CWE-362/CWE-667 | A04 | Confirmé (stress) | **Résolu (P1)** |
| F-15 | Dépendances vulnérables | 5.9 | Medium | Élevée | CWE-1104 | A06 | Confirmé | **Résolu (P0)** |
| F-16 | Conteneur root / build non durci | 7.8 | High | Élevée | CWE-250/CWE-732 | A05 | Confirmé | **Résolu (P0)** |
| F-17 | Incohérence de ports | 3.7 | Low | Faible | CWE-1188 | A05 | Confirmé | **Résolu (P1)** |
| F-18 | Extension permissions/transport | 6.5 | Medium | Élevée | CWE-250/CWE-319 | A02/A05 | Confirmé | **Résolu (P0)** |
| F-19 | MCP lecture fichiers + exfil | 5.5 | Medium | Élevée | CWE-22/CWE-200 | A01 | Confirmé (PoC) | **Résolu (P1)** |
| F-20 | Registre de modèles non validé | 6.5 | Medium | Élevée | CWE-20 | A04/A08 | Confirmé | **Résolu (P0)** |
| F-21 | Artefacts de debug / secret de prompt | 3.7 | Low | Moyenne | CWE-538/CWE-312 | A05 | Confirmé | **Résolu (P1/v3.4.3)** |
| F-22 | HTTP clair sur 0.0.0.0 | 6.5 | Medium | Élevée | CWE-319 | A02 | Confirmé | **Résolu (P0)** |
| F-23 | Qualité/SAST | 3.7 | Low | Faible | CWE-703/CWE-338 | A04/A09 | Confirmé | **Résolu (P1)** |
| F-24 | CI/CD sans qualité | 5.6 | Medium | Moyenne | CWE-1357 | A06/A08 | Confirmé | **Adapté (Local-First)** |
| F-25 | Conformité ToS/Chrome/GDPR | N/A | — | Élevée | — | — | Analyse | **Résolu (P1)** |

---

## F-01 — Traversée de répertoire `/output/*` (lecture arbitraire de fichiers)

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:N/A:N` → **8.6 (High)**
- **Criticité métier :** **Critique** — les cookies Google permettent la prise de contrôle du compte.
- **CWE :** CWE-22 (Path Traversal) · **OWASP :** A01 Broken Access Control
- **Composant :** `api/routes.go:267-269`
- **Statut :** confirmé dynamiquement.

### Description

Le handler concatène directement le paramètre de route décodé avec le répertoire `./output` :

```go
app.Get("/output/*", func(c fiber.Ctx) error {
    return c.SendFile("./output/" + c.Params("*"))
})
```

Aucune canonicalisation, aucune vérification de préfixe, aucun rejet de `..`. Une requête encodée une fois (`%2e%2e%2f`) est décodée par Fiber puis passée à `SendFile`, qui lit le fichier cible. Le chemin brut `../` est normalisé par le routeur (404) ; le double encodage échoue car le fichier littéral `%2e%2e` n'existe pas.

### Preuve

```bash
curl -s -o /tmp/passwd -w '%{http_code}\n' \
  'http://127.0.0.1:18001/output/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd'
# 200 ; le fichier contient root:x:0:0:...
```

```bash
curl -s -o /tmp/cookies.json -w '%{http_code}\n' \
  'http://127.0.0.1:18001/output/%2e%2e%2fcookies%2faccount_profile_999.json'
# 200 ; JSON des cookies
```

```bash
curl -s -o /tmp/wal.bin -w '%{http_code}\n' \
  'http://127.0.0.1:18001/output/%2e%2e%2fdata%2fgemini.db-wal'
strings /tmp/wal.bin | grep AUDIT_SECRET_CHAT_MESSAGE
# marqueur trouvé : tout l'historique de chat est lisible
```

### Impact

- Lecture de `/etc/passwd`, des fichiers de configuration, des clés SSH si présentes dans le conteneur.
- Lecture de `cookies/account_*.json` → cookies de session Google (`SID`, `HSID`, `__Secure-1PSID`…) → prise de contrôle de Gmail, Drive, YouTube, etc.
- Lecture de `data/gemini.db` et `data/gemini.db-wal` → tout l'historique des conversations, prompts, réponses, adresses IP, identifiants de comptes.
- Aucune authentification requise, aucune interaction utilisateur.

### Correctif

```go
import (
    "os"
    "path/filepath"
    "strings"
)

func safeOutputPath(name string) (string, bool) {
    name = filepath.Clean("/" + name)          // neutralise ../
    name = strings.TrimPrefix(name, "/")
    if name == "" || strings.Contains(name, "..") {
        return "", false
    }
    full := filepath.Join("./output", name)
    rel, err := filepath.Rel("./output", full)
    if err != nil || strings.HasPrefix(rel, "..") {
        return "", false
    }
    return full, true
}
```

En Go 1.24+, préférer `os.OpenRoot` :

```go
root, err := os.OpenRoot("./output")
if err != nil { return err }
defer root.Close()
f, err := root.Open(c.Params("*")) // rejette .. et les liens sortants
```

Idéalement, ne servir que les fichiers dont le nom est enregistré dans `media_generations` et dont le chemin est dans `output/`.

---

## F-02 — Absence d'authentification sur l'API et les endpoints d'administration

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:N` → **9.1 (Critical)**
- **Criticité métier :** **Critique**
- **CWE :** CWE-306 (Missing Authentication for Critical Function) · **OWASP :** A01/A07
- **Composant :** `api/routes.go`, `main.go`
- **Statut :** confirmé dynamiquement.

### Description

Aucune route ne vérifie d'identité. Le README montre `Authorization: Bearer any-key`, mais le serveur ne lit jamais cet en-tête. Les routes suivantes sont accessibles sans jeton :

- Lecture : `GET /history`, `/history/search`, `/media`, `/stats`, `/v1/workers`, `/v1/analytics`, `/export/json`, `/export/excel`, `/status`.
- Écriture : `POST /v1/models`, `/v1/models/discover`, `/api/sync-cookies`, `/chat`, `/v1/chat/completions`, `/music`, `/reset`.
- Le serveur écoute sur `0.0.0.0` (`app.Listen(":"+port)`), donc l'exposition n'est pas limitée à localhost.

### Preuve

```bash
for p in /history /history/search?q=AUDIT /stats /v1/workers /v1/analytics /export/excel; do
  curl -s -o /dev/null -w "$p -> %{http_code}\n" "http://127.0.0.1:18001$p"
done
```

Résultat observé : `200` pour toutes les routes, sans en-tête `Authorization`.

```bash
curl -s 'http://127.0.0.1:18001/history?conversation_id=audit-conv-1'
# {"conversation_id":"audit-conv-1","messages":[{"content":"AUDIT_SECRET_CHAT_MESSAGE",...}]}
```

### Impact

- Exfiltration de toutes les conversations et analytics (données personnelles, secrets métier).
- Enregistrement d'un modèle malveillant (F-20) et empoisonnement du pool via `POST /api/sync-cookies` (F-03).
- Consommation des comptes Google de la victime (quota, bannissement).
- Suppression de sessions arbitraires via `POST /reset` (déni de service).
- Aucun journal d'audit des actions administratives.

### Correctif

```go
func authMiddleware(token string) fiber.Handler {
    return func(c fiber.Ctx) error {
        if c.Path() == "/health" {
            return c.Next()
        }
        got := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
        if got == "" {
            got = c.Get("X-API-Token")
        }
        if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
            return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
        }
        return c.Next()
    }
}

app.Use(authMiddleware(os.Getenv("API_TOKEN")))
```

- Refuser le démarrage si `API_TOKEN` est vide (sauf mode `LOCAL_ONLY=true` avec bind loopback).
- Ajouter un rate limit par jeton et un journal d'audit (`user`, `route`, `IP`, `statut`).
- Séparer les routes admin (`/v1/models`, `/api/sync-cookies`, `/export/*`) avec un rôle `admin`.

---

## F-03 — WebSocket cookies non authentifié, `CheckOrigin=true`, bind `0.0.0.0`

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:N` → **9.1 (Critical)**
- **Criticité métier :** **Critique**
- **CWE :** CWE-306, CWE-346 (Origin Validation Error) · **OWASP :** A01/A05
- **Composant :** `gemini/monitor.go:186-191, 296-364`
- **Statut :** confirmé dynamiquement.

### Description

Le serveur WebSocket qui reçoit les cookies de l'extension :

- accepte toutes les origines (`CheckOrigin: return true`) ;
- n'exige aucun jeton ni pairing ;
- écoute sur `0.0.0.0:9226` (accessible depuis le LAN) ;
- accepte des messages `cookies_payload` arbitraires et les persiste.

### Preuve

```python
import json, websocket
ws = websocket.create_connection("ws://127.0.0.1:19226",
                                 origin="https://evil.example", timeout=20)
ws.send(json.dumps({"type":"ping"}))
print(ws.recv())  # {"type":"pong"}
ws.send(json.dumps({"type":"cookies_payload","cookies":[
  {"domain":".example.com","name":"__Secure-1PSID",
   "value":"AUDIT_WS_COOKIE_ABCDEFGHIJ","path":"/","secure":True,
   "expirationDate":4102444800}
]}))
```

Résultat : handshake accepté depuis `https://evil.example`, cookies écrits dans `cookies/account_AUDIT....json`, pool rechargé.

### Impact

- **Empoisonnement du pool** : l'attaquant injecte ses propres cookies ; les prompts des victimes peuvent être routés vers son compte Google, où il peut les lire.
- **Déni de service** : chaque payload déclenche `ProcessAndSaveCookies` → `OnCookiesUpdated` → reload de toutes les sessions + `InitSession` (jusqu'à 5 s de sleep par compte). Un flux de messages suffit à saturer le serveur.
- **Persistence** : les cookies sont écrits sur disque et rechargés au redémarrage.
- **Drive-by** : un site web peut tenter d'ouvrir `ws://127.0.0.1:9226` (sous réserve des restrictions mixed-content/PNA du navigateur) et injecter des cookies.

### Correctif

```go
var upgrader = websocket.Upgrader{
    CheckOrigin: func(r *http.Request) bool {
        origin := r.Header.Get("Origin")
        return origin == "chrome-extension://<EXTENSION_ID>" ||
            origin == "http://127.0.0.1:8100" ||
            origin == "http://localhost:8100"
    },
}
```

- Ajouter un jeton de pairing (`?token=...` ou sous-protocole `Sec-WebSocket-Protocol`) vérifié en temps constant.
- Bind `127.0.0.1` par défaut (`WS_HOST`).
- Limiter la taille des messages (`conn.SetReadLimit(1<<20)`), le nombre de cookies, la fréquence (rate limit), et valider les domaines/noms.
- Ne pas recharger tout le pool à chaque payload : debounce/coalescence (ex. 2 s), et `singleflight` sur `InitSession`.

---

## F-04 — CORS wildcard

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:R/S:C/C:H/I:H/A:N` → **9.3 (Critical)**
- **Criticité métier :** **Critique** (chaîne drive-by avec F-02/F-03/F-20)
- **CWE :** CWE-942 (Permissive Cross-domain Policy) · **OWASP :** A05
- **Composant :** `main.go:107`
- **Statut :** confirmé dynamiquement.

### Description

`app.Use(cors.New())` utilise les valeurs par défaut de Fiber v3 : `AllowOrigins: ["*"]`, `AllowMethods: GET, POST, HEAD, PUT, DELETE, PATCH, QUERY`. Le préflight autorise `content-type`, donc un site tiers peut envoyer des `POST` JSON.

### Preuve

```bash
curl -s -D - -o /dev/null -X OPTIONS http://127.0.0.1:18001/v1/models \
  -H 'Origin: https://evil.example' \
  -H 'Access-Control-Request-Method: POST' \
  -H 'Access-Control-Request-Headers: content-type'
```

```
HTTP/1.1 204 No Content
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, POST, HEAD, PUT, DELETE, PATCH, QUERY
Access-Control-Allow-Headers: content-type
```

### Impact

- Lecture cross-origin de `/history`, `/v1/analytics`, `/stats` depuis le navigateur d'une victime.
- Écriture cross-origin : `POST /v1/models` (F-20), `POST /api/sync-cookies` (F-03), `POST /chat` (consommation de quota).
- Chaîne drive-by : un site malveillant peut prendre le contrôle de l'API locale sans que la victime clique sur autre chose que la page.

> **Nuance :** les Chrome récents peuvent appliquer Private Network Access (PNA) et bloquer certaines requêtes public→localhost. Cela n'atténue ni l'accès direct au service sur le LAN (`0.0.0.0`), ni les requêtes depuis une extension, ni les navigateurs qui n'appliquent pas PNA. Le score CVSS reste fondé sur l'exposition réseau et l'absence d'authentification.

### Correctif

```go
app.Use(cors.New(cors.Config{
    AllowOrigins: []string{
        "chrome-extension://<EXTENSION_ID>",
        "http://127.0.0.1:8100",
        "http://localhost:8100",
    },
    AllowMethods: []string{"GET", "POST"},
    AllowHeaders: []string{"Content-Type", "Authorization", "X-API-Token"},
    AllowCredentials: false,
    MaxAge: 600,
}))
```

- Vérifier l'en-tête `Origin` sur toutes les mutations (défense CSRF).
- Ajouter `Vary: Origin`.
- Ne jamais combiner `*` avec `AllowCredentials: true`.

---

## F-05 — Extension : exfiltration des cookies Google vers le LAN

- **CVSS v3.1 :** `AV:A/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:N` → **9.3 (Critical)**
- **Criticité métier :** **Critique**
- **CWE :** CWE-319 (Cleartext Transmission), CWE-522 (Insufficiently Protected Credentials) · **OWASP :** A02/A05
- **Composant :** `gemini-extension/background.js:28-110, 130-131, 255-260`
- **Statut :** confirmé par analyse statique (pas de test navigateur).

### Description

L'extension « découvre » le serveur en balayant le réseau :

1. `127.0.0.1`, `localhost`, `host.docker.internal` ;
2. une liste d'IP privées codées en dur (`192.168.1.34`, `10.56.65.136`, etc.) ;
3. tout le subnet `192.168.1.1-254` par lots de 35.

`probeServer()` accepte n'importe quel hôte qui répond à `GET /health` avec un JSON contenant `status === 'online'` **ou** un champ `engine`. Ensuite, l'extension envoie **tous les cookies Google filtrés** via `ws://host:9226` ou `http://host:8100/api/sync-cookies`.

### Preuve (statique)

```js
// background.js
if (data && (data.status === 'online' || data.engine)) return host;
...
const wsUrl = `ws://${host}:9226`;
...
fetch(`http://${host}:${SERVER_PORT}/api/sync-cookies`, { method: 'POST', body: ... });
```

Un attaquant sur le même LAN n'a qu'à répondre :

```http
GET /health HTTP/1.1
Host: 192.168.1.42

HTTP/1.1 200 OK
Content-Type: application/json

{"engine":"anything"}
```

Il reçoit ensuite les cookies `SID`, `HSID`, `SSID`, `APISID`, `SAPISID`, `__Secure-1PSID`, `__Secure-3PSID`, `NID`… sur un canal en clair.

### Impact

- Prise de contrôle complète du compte Google de la victime (Gmail, Drive, YouTube, Gemini, etc.).
- Exfiltration possible sans interaction : l'extension scanne et se connecte automatiquement.
- Les IP codées en dur révèlent l'infrastructure de l'auteur et rendent le comportement non déterministe.

### Correctif

1. Supprimer le balayage LAN et la liste d'IP.
2. Configurer l'URL du serveur manuellement (`chrome.storage.local.serverUrl`), par défaut `http://127.0.0.1:8100`.
3. Avant tout envoi, effectuer un **challenge-response** avec un jeton de pairing (`/health` + `/pair`), et refuser les hôtes qui ne prouvent pas le secret.
4. Utiliser `wss://`/HTTPS pour tout hôte non-loopback ; certificate pinning ou CA locale.
5. Restreindre `host_permissions` à `https://*.google.com/*`, `https://*.googleusercontent.com/*`, `http://127.0.0.1/*`.
6. Afficher un écran de consentement expliquant quels cookies sont envoyés et pourquoi.

---

## F-06 — Cookies Google stockés en 0644 et conteneur root

- **CVSS v3.1 :** `AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:N/A:N` → **5.5 (Medium)**
- **Criticité métier :** Élevée
- **CWE :** CWE-276 (Incorrect Default Permissions), CWE-522 · **OWASP :** A01/A02
- **Composant :** `gemini/monitor.go:277`, `Dockerfile`
- **Statut :** confirmé.

### Description

`ProcessAndSaveCookies` écrit `cookies/account_<id>.json` avec `0644`, et `os.MkdirAll("cookies", 0755)`. Le conteneur tourne en `root` (aucun `USER`), avec les volumes `cookies` et `data` montés en écriture. Tout processus du conteneur (ou tout utilisateur local si le fichier est monté sur l'hôte) peut lire les cookies.

### Preuve

```bash
ls -l cookies/
# -rw-r--r-- 1 ... account_....json
docker run --rm --entrypoint /bin/sh audit-fga:latest -c 'id'
# uid=0(root) gid=0(root)
```

### Impact

- Vol de session Google par un processus compromis, un utilisateur local ou une sauvegarde non chiffrée.
- Les cookies sont aussi copiés dans le contexte Docker (`COPY . .`) → risque de fuite via le cache/build distant (voir F-16).

### Correctif

```go
os.MkdirAll("cookies", 0o700)
os.WriteFile(path, data, 0o600)
os.Chmod(path, 0o600) // pour les fichiers existants
```

- Chiffrer les cookies au repos (AES-GCM, clé hors dépôt) ou utiliser le keyring OS.
- Conteneur non-root, volume `cookies` avec permissions 0700.
- Ne jamais inclure `cookies/` dans le contexte Docker (`.dockerignore`).

---

## F-07 — `DownloadFile` forward le cookie sur toutes les redirections

- **CVSS v3.1 :** `AV:N/AC:H/PR:N/UI:N/S:U/C:H/I:N/A:N` → **5.9 (Medium)**
- **Criticité métier :** Élevée
- **CWE :** CWE-200 (Exposure of Sensitive Information), CWE-522 · **OWASP :** A01
- **Composant :** `gemini/client.go:1612-1705`
- **Statut :** confirmé par test Go.

### Description

Le client de téléchargement suit les redirections manuellement et injecte `Cookie` à chaque hop, sans comparer l'hôte ni le schéma :

```go
if cookieStr != "" {
    req.Header.Set("Cookie", cookieStr)
}
```

### Preuve

`TestAuditDownloadFileForwardsCookiesAcrossRedirect` :

```go
redirector -> target (127.0.0.1)
c.RawCookies = "SECRETSESSION=super-secret-google-session"
c.DownloadFile(redirector.URL+"/start", ...)
// target reçoit "SECRETSESSION=super-secret-google-session"
```

### Impact

- Si une URL média Google redirige vers un CDN tiers, un domaine compromis ou un attaquant, les cookies de session Google sont envoyés à ce tiers.
- Combiné à une injection de prompt qui influence l'URL média (ou à un proxy), fuite de session.
- Le client utilise `CachedImageCookies` global, pas les cookies du worker qui a produit le média → mélange de comptes.

### Correctif

```go
func sameCookieDomain(from, to *url.URL) bool {
    return from.Scheme == "https" && to.Scheme == "https" &&
        (to.Hostname() == from.Hostname() ||
         strings.HasSuffix(to.Hostname(), "."+from.Hostname()))
}
```

- Ne transmettre `Cookie` que si l'hôte de destination est un sous-domaine de l'hôte d'origine et que le schéma est HTTPS.
- Allowlist d'egress : `*.google.com`, `*.googleusercontent.com`, `*.gstatic.com`.
- Passer les cookies du worker courant en paramètre, pas une globale.
- Limiter la taille téléchargée (`io.LimitReader`).

---

## F-08 — Data races sur l'état de session et les cookies

- **CVSS v3.1 :** `AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:L/A:L` → **5.6 (Medium)**
- **Criticité métier :** Moyenne
- **CWE :** CWE-362 (Race Condition) · **OWASP :** A04
- **Composant :** `gemini/monitor.go:287`, `gemini/client.go:111-128, 539-543`
- **Statut :** confirmé par `go test -race`.

### Description

Trois races distinctes :

1. `CachedImageCookies` : écriture dans `ProcessAndSaveCookies` (goroutine WS), lecture dans `DownloadFile` (goroutine de requête), sans verrou.
2. `IsInitialized` : `ensureInit()` lit le booléen sans verrou, `InitSession()` l'écrit, `executeWithRetry()` le remet à `false`. Deux requêtes peuvent lancer deux `InitSession` concurrents.
3. `AccountPrefix` : `getAccountPrefix()` lit puis écrit le champ sans verrou.

### Preuve

```
WARNING: DATA RACE
Write at ... by goroutine 40:
  goapi/gemini.ProcessAndSaveCookies()
      gemini/monitor.go:287
Previous read at ... by goroutine ...:
  goapi/gemini.(*GeminiClient).DownloadFile()
      gemini/client.go:1627
```

```
WARNING: DATA RACE
Read at ... by goroutine 12:
  goapi/gemini.(*GeminiClient).ensureInit()
      gemini/client.go:540
Previous write at ... by goroutine 10:
  goapi/gemini.(*GeminiClient).InitSession()
      gemini/client.go:446
```

### Impact

- Sessions corrompues, double initialisation, broadcasts de cookies en tempête, sleeps de 5 s.
- Cookies d'un compte utilisés pour un autre (mélange de sessions).
- Comportement non déterministe sous charge ; les tests `-race` échouent.

### Correctif

- `CachedImageCookies` : `atomic.Value` ou `sync.RWMutex`, ou mieux, supprimer la globale et passer les cookies du worker.
- `IsInitialized` : `atomic.Bool` + `singleflight.Group` autour de `InitSession`.
- `AccountPrefix` : calculer une fois dans `NewClient` (pas d'écriture paresseuse) ou protéger par `sync.Once`.
- `ReloadSession` : prendre le même verrou que `InitSession` et invalider l'état de façon atomique.

---

## F-09 — Répertoire temporaire partagé `./output/.temp`

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:L` → **5.3 (Medium)**
- **Criticité métier :** Moyenne
- **CWE :** CWE-362, CWE-377 (Insecure Temporary File) · **OWASP :** A04
- **Composant :** `api/session.go:21-47`
- **Statut :** confirmé par test Go.

### Description

Tous les téléchargements utilisent le même répertoire `./output/.temp` et le même nom de fichier. À la fin, chaque appel exécute `os.RemoveAll(tempDir)`, ce qui supprime les fichiers en cours des autres requêtes.

### Preuve

`TestAuditDownloadAndCleanSharedTempCollision` : deux téléchargements concurrents, l'un rapide, l'autre lent.

```
Cleaned file moved to output: output/fast.png
concurrent download failed: stat output/.temp/slow.png: no such file or directory
1/2 concurrent downloads failed due to shared temp directory
```

### Impact

- Échec aléatoire des téléchargements d'images/vidéos sous charge.
- Corruption de fichiers si deux requêtes utilisent le même nom.
- Un attaquant peut provoquer des échecs en parallélisant des requêtes média.

### Correctif

```go
tmpDir, err := os.MkdirTemp("./output", ".dl-*")
if err != nil { return err }
defer os.RemoveAll(tmpDir)
tmpPath := filepath.Join(tmpDir, filepath.Base(filename))
```

- Valider `filename` (`filepath.Base`, `filepath.IsLocal`, allowlist `[A-Za-z0-9._-]`).
- Écrire dans le répertoire final via `os.Rename` atomique.
- Limiter la taille et le type MIME.

---

## F-10 — `AutoExportAnalytics` à chaque requête

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H` → **7.5 (High)**
- **Criticité métier :** Élevée
- **CWE :** CWE-400 (Uncontrolled Resource Consumption) · **OWASP :** A04
- **Composant :** `api/export.go:22-42`, `api/chat_handler.go:242, 330, 856, 1011`, `api/media_handler.go:85`
- **Statut :** confirmé par code + benchmark.

### Description

Chaque requête de chat ou de média appelle `AutoExportAnalytics()`, qui lance une goroutine régénérant **tout** l'historique en Excel et JSON, sous un mutex global. Les exports sont donc sérialisés : à 5 000 messages, chaque requête ajoute ~0,53 s de CPU/IO en arrière-plan (0,407 s Excel + 0,131 s JSON). Les goroutines s'empilent plus vite qu'elles ne s'exécutent.

### Preuve

```
BenchmarkAuditExportJSON5000-24    1  130898783 ns/op  28431872 B/op  325028 allocs/op
BenchmarkAuditExportExcel5000-24   1  407331436 ns/op  78006032 B/op  820967 allocs/op
```

Appels : `grep -n AutoExportAnalytics api/chat_handler.go api/media_handler.go` → 5 emplacements.

### Impact

- Déni de service non authentifié : quelques dizaines de requêtes suffisent à saturer le CPU et à faire croître la mémoire.
- Les exports contiennent l'intégralité des messages et sont écrits dans `output/`, servi par `/output/*`.
- Le disque peut se remplir (écritures répétées de 28–78 Mo).

### Correctif

- Retirer `AutoExportAnalytics()` du chemin de requête.
- Utiliser un worker dédié avec un canal de taille 1 (coalescence) et un debounce (ex. 30 s).
- Écrire dans un fichier temporaire puis `os.Rename` (atomique).
- Servir un snapshot en mémoire ou générer à la demande (`/export/*`), avec un cache et un rate limit.
- Ajouter un quota de taille et une purge.

---

## F-11 — `CountTokens` sur un prompt de 50 Mo

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H` → **7.5 (High)**
- **Criticité métier :** Élevée
- **CWE :** CWE-400, CWE-407 (Inefficient Algorithmic Complexity) · **OWASP :** A04
- **Composant :** `api/chat_handler.go:230-234, 315-316, 845-846, 941-942`, `main.go:103`
- **Statut :** confirmé par benchmark.

### Description

`BodyLimit` est fixé à 50 Mo. `CountTokens` (tokenizer BPE `tiktoken-go`) est appelé sur le prompt et la réponse. Le benchmark donne 45,4 ms et 12 Mo d'allocations pour 100 000 caractères, soit ~22 s de CPU et ~6 Go d'allocations pour 50 Mo. Plusieurs requêtes parallèles suffisent à provoquer un OOM.

### Preuve

```
BenchmarkAuditCountTokens100k-24       1  45373907 ns/op  12044384 B/op  133364 allocs/op
BenchmarkAuditEstimateTokens100k-24    1    423630 ns/op    327680 B/op       1 allocs/op
```

### Impact

- Déni de service non authentifié (CPU + mémoire + GC).
- Coût énergétique et cloud.
- Les réponses longues sont aussi tokenisées, doublant le coût.

### Correctif

- Réduire `BodyLimit` à 2–10 Mo et valider la taille des champs.
- Au-delà de 256 Ko, utiliser `EstimateTokens` (heuristique) ou refuser avec `413`.
- Mettre un sémaphore global (ex. 2 tokenizations simultanées) et un timeout.
- Calculer les tokens de la réponse de façon asynchrone ou approximative.

---

## F-12 — Absence de rate limiting et de timeouts (Slowloris)

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:L` → **5.3 (Medium)**
- **Criticité métier :** Moyenne
- **CWE :** CWE-770 (Allocation without Limits), CWE-400 · **OWASP :** A04/A05
- **Composant :** `main.go`, `gemini/monitor.go`
- **Statut :** confirmé dynamiquement.

### Description

Aucun middleware de rate limiting, aucun `ReadTimeout`/`IdleTimeout` configuré sur Fiber ni sur le serveur WS (`http.Server` sans timeouts). Une connexion avec des en-têtes incomplets reste ouverte indéfiniment.

### Preuve

```python
s = socket.create_connection(("127.0.0.1", 18001))
s.sendall(b"GET /health HTTP/1.1\r\nHost: localhost\r\n")
s.settimeout(3)
s.recv(64)
```

```
HTTP API still open after 3s with incomplete headers (no header timeout)
WS bridge still open after 3s with incomplete headers (no header timeout)
```

### Impact

- Slowloris : épuisement des connexions et des goroutines.
- Un client peut lancer des milliers de requêtes coûteuses (chat, export, tokenizer) sans frein.
- Le WS peut être saturé de connexions inactives.

### Correctif

```go
app := fiber.New(fiber.Config{
    ReadTimeout:  10 * time.Second,
    WriteTimeout: 0, // SSE : pas de timeout global, utiliser un contexte par route
    IdleTimeout:  60 * time.Second,
    BodyLimit:    10 * 1024 * 1024,
})
```

- `limiter.New(limiter.Config{Max: 60, Expiration: time.Minute})` sur l'API, plus strict sur `/export/*`, `/chat`, `/v1/models`.
- `http.Server{ReadHeaderTimeout: 5*time.Second, ReadTimeout: 10*time.Second, IdleTimeout: 60*time.Second}` pour le WS.
- Limiter le nombre de connexions simultanées (reverse proxy ou `netutil.LimitListener`).

---

## F-13 — Caches `UserSessions` et `affinityMap` non bornés

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:L` → **5.3 (Medium)**
- **Criticité métier :** Moyenne
- **CWE :** CWE-770 · **OWASP :** A04
- **Composant :** `api/session.go:16`, `api/queue.go:51`
- **Statut :** confirmé par code.

### Description

`UserSessions` est un `sync.Map` global sans éviction. La clé est `sessionID + "_" + cookieFilePath`, où `sessionID` vient du champ `user_id` contrôlé par le client (ou de l'IP). `affinityMap` est aussi un `sync.Map` qui n'est purgé qu'en cas d'échec. Chaque nouvelle valeur crée un `GeminiClient` complet (jar de cookies, clients TLS/QUIC, état de conversation).

### Impact

- Un attaquant envoie des `user_id` uniques → croissance mémoire illimitée, épuisement des ressources.
- Les clients conservent des cookies et des connexions, augmentant la surface et le risque de fuite croisée.
- Le pool peut être saturé par des sessions fantômes.

### Correctif

- Remplacer par un cache LRU/TTL borné (ex. `hashicorp/golang-lru/v2/expirable`, max 1 024 sessions, TTL 30 min).
- Fermer les clients évincés (`client.Close()`), supprimer l'affinité associée.
- Borner `affinityMap` de la même façon, ou la stocker dans le worker avec un TTL.
- Valider `user_id` (longueur, charset) et le dériver d'une authentification, pas du client.

---

## F-14 — Exports concurrents vers le même fichier

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:L` → **5.3 (Medium)**
- **Criticité métier :** Moyenne
- **CWE :** CWE-362, CWE-667 (Improper Locking) · **OWASP :** A04
- **Composant :** `api/export.go:44-62, 141-215`
- **Statut :** confirmé par stress test.

### Description

`HandleExcelExport` appelle `ExportAnalyticsToExcel("")`, qui écrit dans `output/gemini_analytics.xlsx`, puis `c.Download(filePath)`. Deux requêtes concurrentes écrivent le même fichier ; l'une peut tronquer le fichier pendant que l'autre l'envoie. `HandleJSONExport` écrit aussi `output/gemini_analytics.json` à chaque appel, sans verrou.

### Preuve

30 requêtes parallèles sur `/export/excel` :

```
/export/excel: n=30 ok=26 errors=4 invalid=4
sample errors: RemoteDisconnected('Remote end closed connection without response')
```

Les 4 réponses `200` restantes étaient des ZIP/XLSX invalides. Le fichier sur disque était valide à la fin, mais les téléchargements intermédiaires étaient corrompus.

### Impact

- Téléchargements corrompus, erreurs client, support utilisateur.
- Un attaquant peut provoquer des erreurs en parallélisant les exports.
- Le fichier JSON servi par `/output` peut être partiel.

### Correctif

- Générer dans un fichier temporaire unique (`os.CreateTemp`), puis `os.Rename` vers le chemin final.
- Sérialiser les exports avec un mutex **ou** servir un snapshot en mémoire.
- Pour Excel, écrire directement dans la réponse (`c.Type("xlsx"); c.Send(buf)`) au lieu d'un fichier partagé.
- Ajouter un rate limit sur `/export/*`.

---

## F-15 — Dépendances vulnérables

- **CVSS v3.1 :** `AV:N/AC:H/PR:N/UI:N/S:U/C:N/I:N/A:H` → **5.9 (Medium)**
- **Criticité métier :** Élevée
- **CWE :** CWE-1104 (Use of Unmaintained Third Party Components) · **OWASP :** A06
- **Composant :** `go.mod`, image Alpine
- **Statut :** confirmé par `govulncheck` et `trivy`.

### Description

`govulncheck` identifie 10 vulnérabilités, dont **2 reachables** :

| ID | Module | Version | Corrigé | Effet |
|---|---|---|---|---|
| GO-2026-6888 | `github.com/andybalholm/brotli` | 1.2.2 | 1.2.7 | Allocation mémoire excessive (DoS) via `io.Copy` dans `DownloadFile` |
| GO-2026-6865 | `github.com/klauspost/compress` | 1.19.0 | 1.20.2 | Panic sur trames à deux blocs |
| GO-2026-6688/6684/6680/6678/6677/6676/6674 | `github.com/xuri/excelize/v2` | 2.11.0 | 2.11.1+ | Plusieurs panics/DoS à la lecture de XLSX |
| GO-2026-4550 | `github.com/cloudflare/circl` | 1.6.2 | 1.6.3 | Vulnérabilité P-384 |

`trivy` ajoute 13 HIGH dans `go.mod` (`x/crypto`, `x/net`, `x/text`, `excelize`) et 2 HIGH dans l'image Alpine (`musl`, `musl-utils`, CVE-2026-107891, corrigé en `1.2.5-r4`).

### Impact

- Un média ou un flux compressé malveillant peut provoquer un panic ou une consommation mémoire excessive.
- Les vulnérabilités Excelize sont surtout déclenchées à la lecture de fichiers XLSX ; l'application n'en lit pas, mais l'export reste une surface si le format évolue.
- L'image Alpine 3.20.10 contient une bibliothèque musl vulnérable.

### Correctif

```bash
go get github.com/andybalholm/brotli@v1.3.0 \
       github.com/klauspost/compress@v1.20.5 \
       github.com/xuri/excelize/v2@v2.11.1 \
       golang.org/x/crypto@v0.59.0 \
       golang.org/x/net@v0.63.0 \
       golang.org/x/text@v0.44.0 \
       github.com/cloudflare/circl@v1.6.3
go mod tidy
go test -race ./...
govulncheck ./...
```

- Mettre à jour l'image de base (Alpine 3.22+ ou distroless) et `apk upgrade`.
- Ajouter `govulncheck` et `trivy` en CI (bloquants sur HIGH/CRITICAL reachable).
- Renovate/Dependabot + SBOM.

---

## F-16 — Conteneur root et build non durci

- **CVSS v3.1 :** `AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:H` → **7.8 (High)**
- **Criticité métier :** Élevée
- **CWE :** CWE-250 (Execution with Unnecessary Privileges), CWE-732 · **OWASP :** A05
- **Composant :** `Dockerfile`, `docker-compose.yml`
- **Statut :** confirmé (build + `docker run`).

### Description

- Aucun `USER` : le conteneur tourne en `root`.
- Pas de `read_only`, `tmpfs`, `cap_drop`, `security_opt`, `mem_limit`, `pids_limit`.
- Pas de `HEALTHCHECK`.
- Images `golang:alpine` et `alpine:3.20` non épinglées par digest ; `apk add` sans versions.
- `COPY . .` dans le builder : `.dockerignore` n'exclut ni `cookies/` ni `tuned.cact`. Le contexte de build contient donc les cookies Google réels (12 Ko) et 13 Mo de binaire. L'image finale ne les copie pas, mais les couches/caches du builder peuvent les retenir, et un builder distant les reçoit.
- Ports incohérents (voir F-17).

### Preuve

```bash
docker image inspect audit-fga:latest --format 'User={{.Config.User}} Healthcheck={{json .Config.Healthcheck}}'
# User= Healthcheck=null
docker run --rm --entrypoint /bin/sh audit-fga:latest -c 'id'
# uid=0(root) gid=0(root)
```

### Impact

- Si le service est compromis (F-01/F-02), l'attaquant obtient root dans le conteneur, accès aux volumes `cookies`/`data`, et une surface d'évasion (capabilities par défaut, pas de seccomp custom).
- Fuite de cookies via le contexte/cache de build.
- Absence de healthcheck → redémarrages non fiables, trafic vers une instance morte.
- Absence de limites → un conteneur peut consommer toute la mémoire/CPU de l'hôte.

### Correctif

```dockerfile
# syntax=docker/dockerfile:1.7
FROM golang:1.26-alpine@sha256:<digest> AS builder
...
FROM gcr.io/distroless/static-debian12:nonroot@sha256:<digest>
WORKDIR /app
COPY --from=builder /app/goapi /app/goapi
USER nonroot:nonroot
EXPOSE 8001 9226
HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD ["/app/goapi", "--healthcheck"]
ENTRYPOINT ["/app/goapi"]
```

```yaml
services:
  gemini-api:
    user: "10001:10001"
    read_only: true
    tmpfs:
      - /tmp
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    mem_limit: 1g
    pids_limit: 256
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8100/health"]
      interval: 30s
      timeout: 3s
      retries: 3
    ports:
      - "127.0.0.1:8100:8100"
      - "127.0.0.1:9226:9226"
```

- `.dockerignore` : ajouter `cookies/`, `*.cact`, `models.json`, `*.db*`.
- Épingler les images par digest, `apk add` avec versions (hadolint DL3018).
- Signer l'image (cosign), générer un SBOM.

---

## F-17 — Incohérence de ports 8001/8100

- **CVSS v3.1 :** `AV:N/AC:H/PR:N/UI:N/S:U/C:N/I:L/A:N` → **3.7 (Low)**
- **Criticité métier :** Faible (mais opérationnellement gênant)
- **CWE :** CWE-1188 (Insecure Default Initialization) · **OWASP :** A05
- **Composant :** `Dockerfile:32`, `docker-compose.yml:8-16`, `main.go:126-129`, `gemini-extension/background.js:8`
- **Statut :** confirmé.

### Description

| Élément | Port |
|---|---|
| `main.go` défaut | `8001` |
| `Dockerfile EXPOSE` | `8001` |
| `docker-compose` mapping + `PORT` | `8100` |
| Extension `SERVER_PORT` | `8100` |
| README (exemples) | `8001` |

Un lancement natif (`go run main.go`) écoute sur 8001, mais l'extension cherche 8100 : la synchronisation échoue. Un `docker run` sans `PORT` écoute 8001, mais l'image déclare 8001 et compose mappe 8100 : confusion.

### Impact

- Échec de connexion de l'extension, synchronisation impossible, diagnostics difficiles.
- Risque de déploiement sur le mauvais port, d'exposition inattendue ou de bypass de reverse proxy.

### Correctif

- Choisir un port unique (ex. `8100`) et le documenter partout.
- Utiliser `PORT` dans le Dockerfile (`ENV PORT=8100`) et dans compose.
- Faire lire le port à l'extension depuis `chrome.storage` (pas de constante codée en dur).
- Ajouter un test de fumée qui vérifie `GET /health` sur le port attendu.

---

## F-18 — Extension : permissions excessives, clipboard, transport en clair

- **CVSS v3.1 :** `AV:A/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N` → **6.5 (Medium)**
- **Criticité métier :** Élevée (sécurité + conformité Chrome Web Store)
- **CWE :** CWE-250, CWE-319 · **OWASP :** A02/A05
- **Composant :** `gemini-extension/manifest.json:6-13`, `background.js`, `popup.js`
- **Statut :** confirmé.

### Description

- `host_permissions` inclut `http://*/*` et `https://*/*` : accès à tous les sites, non nécessaire.
- `clipboardWrite` + bouton « Copy Cookies » copie les cookies de session dans le presse-papiers.
- `chrome.cookies.getAll({})` lit tous les cookies, puis filtre côté code.
- Transport `ws://`/`http://` en clair.
- Collecte de cookies Google globaux (pas seulement Gemini).
- Balayage LAN (F-05).

### Impact

- Un site compromis ou une autre extension ne peut pas lire directement le presse-papiers sans permission, mais toute application locale le peut.
- Les cookies transitent en clair sur le LAN.
- Chrome Web Store : refus probable pour permissions excessives et collecte non minimale.
- Surface d'attaque élargie (permissions quasi universelles).

### Correctif

```json
{
  "permissions": ["cookies", "storage", "alarms"],
  "host_permissions": [
    "https://*.google.com/*",
    "https://*.googleusercontent.com/*",
    "http://127.0.0.1/*"
  ]
}
```

- Supprimer `clipboardWrite` ou le remplacer par un téléchargement de fichier explicitement demandé.
- Utiliser `chrome.cookies.getAll({ domain: ".google.com" })` et ne garder que les noms strictement nécessaires.
- `wss://`/HTTPS + jeton de pairing.
- Écran de consentement + politique de confidentialité.

---

## F-19 — MCP : lecture arbitraire de fichiers locaux et exfiltration

- **CVSS v3.1 :** `AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:N/A:N` → **5.5 (Medium)**
- **Criticité métier :** Élevée (prompt injection)
- **CWE :** CWE-22, CWE-200 · **OWASP :** A01
- **Composant :** `api/mcp.go:308-335`, `postMCPMultipart`
- **Statut :** confirmé par PoC avec backend mock.

### Description

`generate_image` et `generate_video` acceptent `ref_image_path` / `start_image_path`, vérifient seulement `os.Stat` (le fichier existe), puis lisent le fichier et l'envoient au backend en multipart. Aucune restriction de répertoire, aucun rejet des symlinks, aucune limite de taille. Le backend est `GEMINI_API_URL` (par défaut l'API locale).

### Preuve

`poc/mock_backend.py` capture le corps multipart ; `evidence/10_mcp_exfil_body.bin` contient `MCP_SECRET_MARKER_9f3a`, marqueur du fichier `secret.txt` lu par le serveur MCP.

```
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{
  "name":"generate_image",
  "arguments":{"prompt":"audit","ref_image_path":"/tmp/opencode/mcp-root/secret.txt"}}}
```

### Impact

- Un prompt injecté dans un agent (page web, document, issue) peut faire lire `~/.ssh/id_rsa`, `~/.aws/credentials`, `/etc/passwd`, des fichiers de configuration, et les envoyer à Google ou à un backend compromis.
- Le serveur MCP n'a pas d'audit log des fichiers lus.
- `initialize` ignore la version demandée (`2025-06-18` → réponse `2024-11-05`), ce qui peut casser des clients récents.

### Correctif

```go
root, _ := os.OpenRoot(os.Getenv("MCP_MEDIA_ROOT"))
f, err := root.Open(imagePath) // rejette .. et les liens sortants
```

- `filepath.IsLocal`, `os.Lstat` (rejet des symlinks), allowlist d'extensions/MIME, limite de taille (20 Mo).
- `MCP_TOKEN` si transport réseau ; journal d'audit (chemin, taille, hash).
- Répondre avec la version MCP demandée si supportée, sinon erreur explicite.
- Réduire `mcpHttpTimeout` (5 min → 60 s) et les retries.

---

## F-20 — Registre de modèles non authentifié et non validé

- **CVSS v3.1 :** `AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:L/A:L` → **6.5 (Medium)**
- **Criticité métier :** Élevée
- **CWE :** CWE-20 (Improper Input Validation) · **OWASP :** A04/A08
- **Composant :** `api/routes.go:75-130`, `gemini/registry.go`
- **Statut :** confirmé.

### Description

`POST /v1/models` accepte un `ModelConfig` arbitraire :

```json
{"id":"audit-pwn-model","display_name":"Pwn","mode_id":"deadbeefdeadbeef",
 "header_template":"[1,null,null,null,\"%s\",null,null,0,[4],null,null,2]",
 "min_tier":"free","source":"custom"}
```

Aucune validation de `mode_id` (format, longueur), de `header_template` (JSON valide, un seul `%s`), de `min_tier`, ni de `source`. Le modèle est persisté dans `models.json` et rechargé au démarrage.

### Preuve

```bash
curl -s -X POST http://127.0.0.1:18001/v1/models -H 'Content-Type: application/json' -d '{...}'
# {"status":"success","model":{...}}
grep audit-pwn-model models.json
# présent après redémarrage
```

### Impact

- Injection de champs JSON dans la requête vers Google (le template est formaté avec `fmt.Sprintf`).
- Duplication du prompt, requêtes malformées, erreurs 400/500, coût.
- Persistance : un modèle malveillant reste après redémarrage.
- Un attaquant peut faire pointer un alias vers un `mode_id` invalide et bloquer les clients.

### Correctif

- Authentification + rôle admin.
- Valider `id` (`^[a-z0-9][a-z0-9._-]{0,63}$`), `mode_id` (`^[0-9a-f]{16}$`), `min_tier` (`free|subscription|ultra`).
- Parser `header_template` en JSON, vérifier la structure et exactement un `%s`.
- Signer `models.json` ou le stocker en DB avec audit ; refuser les modifications runtime en production.
- Rate limit sur `/v1/models`.

---

## F-21 — Artefacts de debug et secret de prompt dans le dépôt

- **CVSS v3.1 :** `AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:N/A:N` → **3.7 (Low)**
- **Criticité métier :** Moyenne (propriété intellectuelle)
- **CWE :** CWE-538 (Insertion of Sensitive Information into Externally-Accessible File), CWE-312 · **OWASP :** A05
- **Composant :** `data/debug_send_prompt.txt` (57 Ko), `data/debug_system_prompt.txt`, `pr_body.md`, `tuned.cact` (13 Mo)
- **Statut :** confirmé.

### Description

Le dépôt contient des artefacts de debug qui révèlent le prompt système, la structure des requêtes et un binaire `tuned.cact` de 13 Mo (données non documentées). `.gitignore` ne les exclut pas tous ; `tuned.cact` est suivi.

### Impact

- Fuite de propriété intellectuelle (prompt engineering, structure JSPB).
- Gonflement du dépôt et du contexte Docker (13 Mo).
- `tuned.cact` pourrait contenir des données sensibles (l'analyse `strings` n'a pas trouvé de secret, mais le format binaire n'est pas documenté).

### Correctif

- Supprimer les fichiers de debug du dépôt et de l'historique Git (`git filter-repo` si nécessaire).
- Ajouter `data/debug_*`, `*.cact`, `pr_body.md` au `.gitignore`.
- Stocker les prompts système hors dépôt (config chiffrée).
- Documenter `tuned.cact` ou le retirer.

---

## F-22 — API HTTP en clair sur `0.0.0.0`

- **CVSS v3.1 :** `AV:A/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N` → **6.5 (Medium)**
- **Criticité métier :** Élevée
- **CWE :** CWE-319 · **OWASP :** A02
- **Composant :** `main.go:139`, `gemini/monitor.go:356`
- **Statut :** confirmé (`ss -ltnp`).

### Description

`app.Listen(":"+port)` bind `0.0.0.0` et sert du HTTP/1.1 en clair. Le WS bind aussi `0.0.0.0`. Sur un LAN, les prompts, réponses, cookies (via `POST /api/sync-cookies`) et analytics circulent en clair.

### Preuve

```
LISTEN 0 4096 0.0.0.0:18001 0.0.0.0:* users:(("goapi",pid=...))
LISTEN 0 4096       *:19226       *:* users:(("goapi",pid=...))
```

### Impact

- Sniffing passif des conversations et des cookies sur le LAN.
- Attaques de type MITM (ARP spoofing) pour modifier les réponses ou voler les cookies.
- Exposition accidentelle si le port est forwardé sur Internet.

### Correctif

- Bind `127.0.0.1` par défaut (`API_HOST`, `WS_HOST`).
- Si exposition réseau : TLS (reverse proxy Caddy/Traefik), HSTS, mTLS pour l'extension, firewall.
- Ne jamais envoyer de cookies sur HTTP.
- Ajouter un avertissement au démarrage si `API_HOST != 127.0.0.1` sans TLS.

---

## F-23 — Qualité, SAST et couverture

- **CVSS v3.1 :** `AV:N/AC:H/PR:N/UI:N/S:U/C:N/I:L/A:N` → **3.7 (Low)**
- **Criticité métier :** Faible
- **CWE :** CWE-703 (Improper Check/Handling of Exceptional Conditions), CWE-338 (Weak RNG) · **OWASP :** A04/A09
- **Composant :** ensemble du code Go
- **Statut :** confirmé.

### Description

- `gosec` : 84 constats — 47 G104 (erreurs non gérées), 10 G301 (permissions 0755), 8 G304 (fichiers par variable), 6 G306 (écritures 0644), 5 G115 (overflow uint64→int), 4 G706 (log injection), 1 G703 (path traversal), 1 G404 (`math/rand` pour `ReqID`), 1 G112 (Slowloris), 1 G101 (faux positif).
- `staticcheck` : 5 constats — `ST1018` (caractère de contrôle U+009D dans un littéral), `ST1005` ×2 (erreur capitalisée), `U1000` ×2 (code mort : `getConvState`, `executeStreamRequest`).
- Couverture : **28,2 %** global ; `api` 18,6 %, `db` 44,1 %, `gemini` 37,2 % ; `chat_handler`, `export`, `mcp`, `help` à 0 %.
- `go vet` : propre.

### Impact

- Erreurs silencieuses (écritures DB, fichiers, réseau) → comportements imprévisibles.
- `ReqID` prévisible (math/rand) : pas critique, mais peut faciliter la corrélation/le rejeu.
- Code mort et couverture faible → régressions non détectées.
- Log injection : un prompt contenant des retours à la ligne peut falsifier les logs.

### Correctif

- Traiter les erreurs retournées (`if err != nil`) ou les journaliser explicitement.
- Remplacer `math/rand` par `crypto/rand` pour `ReqID` (ou un UUID).
- Supprimer le code mort, corriger `ST1005`/`ST1018`.
- Assainir les logs (`strconv.Quote`, suppression des `\n`).
- Ajouter des tests d'intégration pour `chat_handler`, `export`, `mcp` ; viser 60 %.
- Intégrer `gosec`/`staticcheck` en CI avec seuils.

---

## F-24 — CI/CD sans contrôles de qualité

- **CVSS v3.1 :** `AV:N/AC:H/PR:N/UI:N/S:U/C:L/I:L/A:L` → **5.6 (Medium)**
- **Criticité métier :** Moyenne
- **CWE :** CWE-1357 (Reliance on Insufficiently Trustworthy Component) · **OWASP :** A06/A08
- **Composant :** `.github/workflows/docker-publish.yml`
- **Statut :** confirmé.

### Description

Le workflow publie une image GHCR sur `push` vers `main` et sur release, mais :

- aucun `go test`, `go vet`, `govulncheck`, `gosec`, `staticcheck`, `trivy`, `hadolint` ;
- actions épinglées par tag (`actions/checkout@v4`, `docker/login-action@v3`, etc.), pas par SHA ;
- `permissions: packages: write` au niveau du job (nécessaire pour publier, mais large) ;
- pas de SBOM, pas de signature, pas de provenance ;
- pas de build reproductible (pas de `-trimpath`, pas de version injectée).

### Impact

- Une régression ou une vulnérabilité peut être publiée automatiquement.
- Une action compromise (tag mutable) peut exfiltrer le `GITHUB_TOKEN` ou empoisonner l'image.
- Pas de traçabilité de l'image (qui a construit quoi, à partir de quel commit).

### Correctif

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<SHA>
      - uses: actions/setup-go@<SHA>
        with: { go-version: '1.26' }
      - run: go vet ./...
      - run: go test -race -short ./...
      - run: go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
      - uses: securego/gosec@<SHA>
      - uses: dominikh/staticcheck-action@<SHA>
      - uses: aquasecurity/trivy-action@<SHA>
      - uses: hadolint/hadolint-action@<SHA>
```

- Épingler toutes les actions par SHA.
- Ajouter `cosign sign` + `syft` (SBOM) + `actions/attest-build-provenance`.
- Restreindre `permissions` au strict nécessaire, séparer build et publish.
- Ajouter un job de test avant le job de publication (`needs: test`).

---

## F-25 — Conformité : ToS Google, Chrome Web Store, GDPR, watermark removal

- **CVSS :** N/A (risque business/juridique)
- **Criticité métier :** **Élevée**
- **Composant :** produit, extension, MCP
- **Statut :** analyse.

### Constats

1. **ToS Google** : accès automatisé à l'interface web Gemini, extraction de cookies, farming de comptes, contournement des quotas. Risque de suspension, blocage IP, action juridique.
2. **Watermark removal** : la description MCP de `generate_image` mentionne « auto-cleaned watermarks ». Risque DMCA/ToS et réputationnel.
3. **Chrome Web Store** : permissions excessives (`http://*/*`, `https://*/*`, `clipboardWrite`), collecte de cookies d'authentification globaux, balayage LAN. Refus probable.
4. **GDPR** : conversations et IP stockées sans chiffrement, sans rétention, sans suppression ; exports non authentifiés.
5. **Sécurité produit** : pas de `SECURITY.md`, pas de politique de divulgation, pas de threat model.

### Recommandations

- Migrer vers l'API officielle Gemini/Vertex AI pour tout usage commercial.
- Si le mode reverse-engineered est conservé : compte jetable, consentement, kill-switch, isolation réseau, avis juridique.
- Chrome Web Store : réduire les permissions, supprimer le LAN sweep, ajouter consentement/politique de confidentialité ; envisager native messaging.
- GDPR : rétention 30 jours, `DELETE /history/:id`, chiffrement, DPIA, journal d'accès.
- Ajouter `SECURITY.md`, `PRIVACY.md`, `THREAT_MODEL.md`.

---

## Annexe — Calcul des scores

Les scores ont été calculés avec la formule CVSS v3.1 (base metrics uniquement). Exemples :

| Vecteur | Score |
|---|---:|
| `AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:N/A:N` | 8.6 |
| `AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:N` | 9.1 |
| `AV:N/AC:L/PR:N/UI:R/S:C/C:H/I:H/A:N` | 9.3 |
| `AV:A/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:N` | 9.3 |
| `AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N` | 7.5 |
| `AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:H` | 7.8 |
| `AV:N/AC:H/PR:N/UI:N/S:U/C:N/I:N/A:H` | 5.9 |

La sévérité suit les bandes CVSS v3.1 : Low 0.1–3.9, Medium 4.0–6.9, High 7.0–8.9, Critical 9.0–10.0.
