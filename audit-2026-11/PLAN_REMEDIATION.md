# Plan de remédiation — Free Gemini API

**Date :** 21/11/2026  
**Objectif :** passer d'un indice de maturité de **38/100** à **58/100 en 48 h** (P0), **74/100 en 2 semaines** (P1), **85/100 en 2 mois** (P2).  
**Principe :** d'abord réduire la surface d'attaque (auth, bind loopback, traversal, CORS/WS), ensuite corriger la robustesse (races, exports, caches), enfin durcir l'exploitation (Docker, CI, conformité).

---

## 1. Matrice de priorisation

| Priorité | Délai | Objectif | Effort | Impact |
|---|---|---|---|---|
| **P0** | 0–48 h | Bloquer les exploitations triviales, protéger les cookies, stabiliser | 2–3 j·h | Critique |
| **P1** | 1–2 semaines | Corriger les bugs de concurrence, refactoriser les points chauds, durcir Docker/CI | 8–12 j·h | Élevé |
| **P2** | 1–2 mois | Architecture multi-utilisateur, chiffrement, conformité, observabilité | 20–30 j·h | Moyen/Élevé |

### Backlog priorisé

| ID | Action | Priorité | Effort | Réf. |
|---|---|---|---|---|
| A-01 | Bind API/WS sur `127.0.0.1` par défaut | P0 | 0,5 h | F-22 |
| A-02 | Auth `API_TOKEN` sur toutes les routes | P0 | 3 h | F-02 |
| A-03 | Correction traversal `/output/*` | P0 | 2 h | F-01 |
| A-04 | CORS allowlist + vérification `Origin` | P0 | 1 h | F-04 |
| A-05 | WS : `CheckOrigin`, jeton, bind loopback, limites | P0 | 3 h | F-03 |
| A-06 | Rotation des cookies Google + permissions 0600/0700 | P0 | 1 h | F-05/F-06 |
| A-07 | Suppression de `AutoExportAnalytics` du chemin de requête | P0 | 3 h | F-10 |
| A-08 | Limite tokenizer + body limit + `EstimateTokens` | P0 | 2 h | F-11 |
| A-09 | Timeouts + rate limiting | P0 | 2 h | F-12 |
| A-10 | Correction des data races | P0 | 4 h | F-08 |
| A-11 | Temp dir par téléchargement | P0 | 1 h | F-09 |
| A-12 | Validation du registre de modèles | P0 | 2 h | F-20 |
| A-13 | Docker non-root + `.dockerignore` + healthcheck | P0 | 2 h | F-16 |
| A-14 | Mise à jour des dépendances reachables | P0 | 2 h | F-15 |
| A-15 | Extension : suppression du LAN sweep + URL fixe | P0 | 3 h | F-05/F-18 |
| A-16 | Exports atomiques + sérialisés | P1 | 3 h | F-14 |
| A-17 | Caches LRU/TTL (`UserSessions`, `affinityMap`) | P1 | 4 h | F-13 |
| A-18 | Refactor `client.go` / `chat_handler.go` | P1 | 3 j | F-23 |
| A-19 | Tests d'intégration + fuzz + couverture 60 % | P1 | 4 j | F-23 |
| A-20 | CI : tests, SAST, scans, actions par SHA | P1 | 1 j | F-24 |
| A-21 | Cookie forwarding same-domain + egress allowlist | P1 | 2 h | F-07 |
| A-22 | MCP : racine média, symlinks, taille, audit | P1 | 3 h | F-19 |
| A-23 | Rétention GDPR + endpoint de suppression | P1 | 1 j | F-25 |
| A-24 | Chiffrement des cookies au repos | P2 | 2 j | F-06 |
| A-25 | Auth multi-utilisateur / RBAC | P2 | 5 j | F-02 |
| A-26 | mTLS ou native messaging extension | P2 | 3 j | F-05 |
| A-27 | Observabilité (OTel, métriques, alertes) | P2 | 3 j | F-23 |
| A-28 | SBOM, cosign, provenance SLSA | P2 | 2 j | F-24 |
| A-29 | Migration API officielle / isolement | P2 | 10 j+ | F-25 |
| A-30 | Threat model, pentest, bug bounty | P2 | 5 j | F-25 |

---

## 2. P0 — Quick wins (0–48 h)

### 2.1 Corriger la traversée de répertoire (F-01)

**Fichier :** `api/routes.go`

```go
import (
    "os"
    "path/filepath"
)

func safeOutputPath(name string) (string, bool) {
    clean := filepath.Clean(name)
    if clean == "." || !filepath.IsLocal(clean) {
        return "", false
    }
    full := filepath.Join("./output", clean)
    rel, err := filepath.Rel("./output", full)
    if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
        return "", false
    }
    return full, true
}

app.Get("/output/*", func(c fiber.Ctx) error {
    name := c.Params("*")
    full, ok := safeOutputPath(name)
    if !ok {
        return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid path"})
    }
    if _, err := os.Stat(full); err != nil {
        return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
    }
    return c.SendFile(full)
})
```

Variante Go 1.24+ recommandée (`os.Root` bloque aussi les symlinks sortants) :

```go
root, err := os.OpenRoot("./output")
if err != nil { return c.Status(500).JSON(...) }
defer root.Close()

f, err := root.Open(c.Params("*"))
if err != nil { return c.Status(404).JSON(...) }
defer f.Close()
return c.SendStream(f)
```

**Vérification :**

```bash
curl -s -o /dev/null -w '%{http_code}\n' \
  'http://127.0.0.1:18001/output/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd'
# attendu : 400 ou 404, jamais 200
```

### 2.2 Authentification (F-02)

**Fichier :** `main.go` (ou `api/auth.go`)

```go
import (
    "crypto/subtle"
    "os"
    "strings"
)

func authMiddleware(token string) fiber.Handler {
    return func(c fiber.Ctx) error {
        if c.Path() == "/health" {
            return c.Next()
        }
        got := strings.TrimPrefix(c.Get("Authorization"), "Bearer ")
        if got == "" {
            got = c.Get("X-API-Token")
        }
        if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
            return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
        }
        return c.Next()
    }
}

token := os.Getenv("API_TOKEN")
if token == "" {
    log.Fatal("API_TOKEN is required (or set LOCAL_ONLY=true and bind 127.0.0.1)")
}
app.Use(authMiddleware(token))
```

- Ajouter `API_TOKEN` dans `.env.example` et la documentation.
- Séparer les routes admin : `adminGroup := app.Group("/v1", authMiddleware(adminToken))` pour `/models`, `/models/discover`, `/api/sync-cookies`, `/export/*`.
- Journaliser les échecs d'auth (IP, route, horodatage) avec rate limit.

**Vérification :**

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18001/history
# 401
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $API_TOKEN" \
  http://127.0.0.1:18001/history
# 200
```

### 2.3 CORS (F-04)

```go
app.Use(cors.New(cors.Config{
    AllowOrigins: []string{
        "chrome-extension://<EXTENSION_ID>",
        "http://127.0.0.1:8100",
        "http://localhost:8100",
    },
    AllowMethods:     []string{"GET", "POST"},
    AllowHeaders:     []string{"Content-Type", "Authorization", "X-API-Token"},
    AllowCredentials: false,
    MaxAge:           600,
}))
```

- Ajouter un middleware qui rejette les mutations si `Origin` n'est pas dans l'allowlist.
- Ne pas utiliser `AllowOrigins: []string{"*"}`.
- Ajouter `Vary: Origin`.

**Vérification :**

```bash
curl -s -D - -o /dev/null -X OPTIONS http://127.0.0.1:18001/v1/models \
  -H 'Origin: https://evil.example' \
  -H 'Access-Control-Request-Method: POST' | grep -i access-control
# attendu : pas de Access-Control-Allow-Origin: *
```

### 2.4 Registre de modèles (F-20)

```go
var (
    idRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
    modeIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func validateModel(cfg *gemini.ModelConfig) error {
    if !idRe.MatchString(cfg.ID) { return errors.New("invalid id") }
    if cfg.ModeID != "" && !modeIDRe.MatchString(cfg.ModeID) { return errors.New("invalid mode_id") }
    if cfg.MinTier != "" && cfg.MinTier != "free" && cfg.MinTier != "subscription" && cfg.MinTier != "ultra" {
        return errors.New("invalid min_tier")
    }
    if cfg.HeaderTemplate != "" {
        if strings.Count(cfg.HeaderTemplate, "%s") != 1 { return errors.New("template must contain exactly one %s") }
        if !json.Valid([]byte(cfg.HeaderTemplate)) { return errors.New("template must be valid JSON") }
    }
    return nil
}
```

- Protéger la route par `authMiddleware` + rôle admin.
- Refuser `source` inconnu ; limiter la taille du payload.
- Écrire `models.json` en `0600` via temp + rename.
- En production, désactiver l'enregistrement runtime ou le réserver à un fichier signé.

### 2.5 WebSocket (F-03)

```go
var upgrader = websocket.Upgrader{
    ReadBufferSize:  1024,
    WriteBufferSize: 1024,
    CheckOrigin: func(r *http.Request) bool {
        switch r.Header.Get("Origin") {
        case "chrome-extension://<EXTENSION_ID>",
             "http://127.0.0.1:8100",
             "http://localhost:8100":
            return true
        }
        return false
    },
}

func handleWS(w http.ResponseWriter, r *http.Request) {
    if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(os.Getenv("WS_TOKEN"))) != 1 {
        http.Error(w, "unauthorized", http.StatusUnauthorized)
        return
    }
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil { return }
    defer conn.Close()
    conn.SetReadLimit(1 << 20) // 1 Mo
    // ... lecture, validation, rate limit
}

server := &http.Server{
    Addr:              "127.0.0.1:9226",
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout:       10 * time.Second,
    IdleTimeout:       60 * time.Second,
}
```

- Remplacer `addr := "0.0.0.0:..."` par `127.0.0.1:...` (ou `WS_HOST`).
- Valider les cookies : domaines Google uniquement, noms allowlistés, valeur non vide, taille max.
- Debounce `OnCookiesUpdated` (2 s) et `singleflight` sur `InitSession`.

### 2.6 Extension : supprimer le LAN sweep (F-05)

**`gemini-extension/background.js`**

```js
// Remplacer discoverServer() par :
async function getServerUrl() {
  const { serverUrl } = await chrome.storage.local.get(['serverUrl']);
  return serverUrl || 'http://127.0.0.1:8100';
}

async function connectToBackend() {
  const base = await getServerUrl();
  const wsUrl = base.replace(/^http/, 'ws') + '/ws';
  // ...
}
```

- Supprimer `probeServer`, `discoverServer`, les listes d'IP et le subnet sweep.
- Ajouter un écran de configuration dans `popup.html` (URL du serveur + jeton de pairing).
- Avant tout envoi, faire un `POST /pair` avec un code affiché par le serveur, et stocker le jeton.
- `wss://`/HTTPS si l'URL n'est pas loopback.
- `host_permissions` minimal (voir 2.17).

**`gemini-extension/manifest.json`**

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

### 2.7 Cookie forwarding (F-07)

```go
func sameCookieDomain(from, to *url.URL) bool {
    if from.Scheme != "https" || to.Scheme != "https" {
        return false
    }
    f, t := from.Hostname(), to.Hostname()
    return t == f || strings.HasSuffix(t, "."+f)
}

// Dans la boucle de redirection :
next, err := url.Parse(location)
if err != nil { return err }
if sameCookieDomain(current, next) {
    req.Header.Set("Cookie", cookieStr)
}
currentURL = next.String()
```

- Allowlist d'egress : `*.google.com`, `*.googleusercontent.com`, `*.gstatic.com`.
- Passer les cookies du worker courant, pas `CachedImageCookies`.
- `io.LimitReader(resp.Body, maxMediaBytes)`.

### 2.8 Data races (F-08)

```go
type GeminiClient struct {
    initialized atomic.Bool
    initMu      sync.Mutex
    accountPrefix string // calculé dans NewClient, plus d'écriture paresseuse
    // ...
}

func (c *GeminiClient) ensureInit() error {
    if c.initialized.Load() { return nil }
    c.initMu.Lock()
    defer c.initMu.Unlock()
    if c.initialized.Load() { return nil }
    if err := c.InitSession(); err != nil { return err }
    c.initialized.Store(true)
    return nil
}

func (c *GeminiClient) ReloadSession() error {
    c.initMu.Lock()
    defer c.initMu.Unlock()
    c.initialized.Store(false)
    return c.loadCookies()
}
```

Pour `CachedImageCookies` :

```go
var cachedImageCookies atomic.Value // string

func setCachedImageCookies(s string) { cachedImageCookies.Store(s) }
func getCachedImageCookies() string {
    v, _ := cachedImageCookies.Load().(string)
    return v
}
```

Mieux : supprimer la globale et passer `cookies string` à `DownloadFile`.

### 2.9 Répertoire temporaire (F-09)

```go
func DownloadAndClean(client *gemini.GeminiClient, urlStr, filename, fileType string) error {
    base := filepath.Base(filename)
    if base == "." || base == string(filepath.Separator) || !filepath.IsLocal(base) {
        return fmt.Errorf("invalid filename")
    }
    if err := os.MkdirAll("./output", 0o755); err != nil { return err }

    tmpDir, err := os.MkdirTemp("./output", ".dl-*")
    if err != nil { return err }
    defer os.RemoveAll(tmpDir)

    tmpPath := filepath.Join(tmpDir, base)
    if err := client.DownloadFile(urlStr, tmpPath); err != nil { return err }
    return os.Rename(tmpPath, filepath.Join("./output", base))
}
```

### 2.10 Exports (F-10, F-14)

**Supprimer les appels `AutoExportAnalytics()`** de `chat_handler.go` et `media_handler.go`. Les remplacer par un worker coalescent :

```go
var exportSignal = make(chan struct{}, 1)

func ScheduleAnalyticsExport() {
    select {
    case exportSignal <- struct{}{}:
    default: // un export est déjà en attente
    }
}

func startAnalyticsWorker() {
    go func() {
        for range exportSignal {
            time.Sleep(5 * time.Second) // debounce
            if err := exportAnalyticsAtomic(); err != nil {
                log.Printf("analytics export: %v", err)
            }
        }
    }()
}

func exportAnalyticsAtomic() error {
    tmp, err := os.CreateTemp("./output", ".analytics-*.json")
    if err != nil { return err }
    name := tmp.Name()
    defer os.Remove(name)
    // ... écrire le JSON dans tmp, puis :
    tmp.Close()
    return os.Rename(name, "./output/gemini_analytics.json")
}
```

**Pour `/export/excel`**, écrire dans un buffer et l'envoyer directement :

```go
func HandleExcelExport(c fiber.Ctx) error {
    f := excelize.NewFile()
    defer f.Close()
    // ... remplir f ...
    var buf bytes.Buffer
    if err := f.Write(&buf); err != nil { return err }
    c.Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
    c.Set("Content-Disposition", `attachment; filename="gemini_analytics.xlsx"`)
    return c.Send(buf.Bytes())
}
```

- Ajouter un mutex global pour les exports lourds, ou un worker dédié.
- Rate limit sur `/export/*`.

### 2.11 Tokenizer et body limit (F-11)

```go
const maxTokenizeBytes = 256 * 1024

func SafeCountTokens(s string) int {
    if len(s) > maxTokenizeBytes {
        return EstimateTokens(s)
    }
    return CountTokens(s)
}
```

- `BodyLimit: 10 * 1024 * 1024` (10 Mo), et valider `len(prompt) <= 1 Mo`.
- Sémaphore global :

```go
var tokenSem = make(chan struct{}, 2)

func CountTokensLimited(s string) int {
    tokenSem <- struct{}{}
    defer func() { <-tokenSem }()
    return SafeCountTokens(s)
}
```

- Remplacer les appels `CountTokens` par `CountTokensLimited` dans `chat_handler.go`.
- Pour les réponses, utiliser `EstimateTokens` si `len(text) > 256 Ko`.

### 2.12 Timeouts et rate limiting (F-12)

```go
app := fiber.New(fiber.Config{
    AppName:      "Gemini Go API",
    BodyLimit:    10 * 1024 * 1024,
    ReadTimeout:  10 * time.Second,
    WriteTimeout: 0, // SSE
    IdleTimeout:  60 * time.Second,
})

app.Use(limiter.New(limiter.Config{
    Max:        60,
    Expiration: time.Minute,
    KeyGenerator: func(c fiber.Ctx) string {
        return c.IP() + ":" + c.Get("X-API-Token")
    },
}))
```

- `limiter` Fiber v3 : `github.com/gofiber/fiber/v3/middleware/limiter`.
- Pour le WS : `ReadHeaderTimeout`, `ReadTimeout`, `IdleTimeout` sur `http.Server`.
- En production, ajouter un reverse proxy (Caddy/Traefik) avec limite de connexions.

### 2.13 MCP (F-19)

```go
func safeMCPPath(rootDir, name string) (string, error) {
    if !filepath.IsLocal(name) {
        return "", fmt.Errorf("path must be relative to MCP_MEDIA_ROOT")
    }
    root, err := os.OpenRoot(rootDir)
    if err != nil { return "", err }
    defer root.Close()
    f, err := root.Open(name)
    if err != nil { return "", err }
    defer f.Close()
    info, err := f.Stat()
    if err != nil { return "", err }
    if info.Size() > 20<<20 { return "", fmt.Errorf("file too large") }
    return filepath.Join(rootDir, name), nil
}
```

- `MCP_MEDIA_ROOT` par défaut `./output` ; refuser les chemins absolus.
- `os.Lstat` + rejet des symlinks.
- Valider le MIME (image/png, image/jpeg, image/webp, video/mp4).
- Journaliser chaque lecture (chemin, taille, hash).
- Répondre à `initialize` avec la version demandée si supportée ; sinon erreur `-32602`.
- `mcpHttpTimeout` 60 s, retries 2.

### 2.14 Docker (F-16)

**`.dockerignore`**

```
data/
output/
cookies/
scripts/
goapi
*.log
*.db
*.db-wal
*.db-shm
models.json
*.cact
.git
```

**Dockerfile**

```dockerfile
# syntax=docker/dockerfile:1.7
FROM golang:1.26-alpine@sha256:<digest> AS builder
WORKDIR /app
RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o goapi .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:<digest>
WORKDIR /app
COPY --from=builder /app/goapi /app/goapi
USER nonroot:nonroot
EXPOSE 8100 9226
HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD ["/app/goapi", "--healthcheck"]
ENTRYPOINT ["/app/goapi"]
```

**`docker-compose.yml`**

```yaml
services:
  gemini-api:
    build: .
    user: "10001:10001"
    read_only: true
    tmpfs:
      - /tmp
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    mem_limit: 1g
    pids_limit: 256
    environment:
      - PORT=8100
      - API_TOKEN=${API_TOKEN}
      - WS_TOKEN=${WS_TOKEN}
      - TZ=Europe/Paris
    ports:
      - "127.0.0.1:8100:8100"
      - "127.0.0.1:9226:9226"
    volumes:
      - ./cookies:/app/cookies:rw
      - ./data:/app/data:rw
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8100/health"]
      interval: 30s
      timeout: 3s
      retries: 3
    restart: unless-stopped
```

- Créer l'utilisateur `10001` dans l'image ou utiliser `nonroot`.
- S'assurer que `cookies/` et `data/` appartiennent à `10001` sur l'hôte (`chown -R 10001:10001`).
- Épingler les images par digest ; mettre à jour Alpine/distroless.

### 2.15 Dépendances (F-15)

```bash
cd free-gemini-api
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

- Vérifier les breaking changes (Fiber, tls-client, quic-go) avant de mettre à jour ces modules.
- Mettre à jour l'image Alpine (3.22+) ou passer à distroless.
- Ajouter `govulncheck`/`trivy` en CI, bloquants sur HIGH reachable.

### 2.16 Caches bornés (F-13)

```go
import "github.com/hashicorp/golang-lru/v2/expirable"

var sessions = expirable.NewLRU[string, *gemini.GeminiClient](1024, func(_ string, c *gemini.GeminiClient) {
    c.Close()
}, 30*time.Minute)
```

- Remplacer `UserSessions sync.Map` par ce cache.
- Borner `affinityMap` (LRU 4096, TTL 30 min) ou la stocker dans le worker.
- Valider `user_id` : `^[A-Za-z0-9_-]{1,64}$`.
- Fermer les clients évincés.

### 2.17 Permissions de l'extension (F-18)

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

- Supprimer `clipboardWrite` (ou le remplacer par un téléchargement de fichier).
- Filtrer les cookies côté `chrome.cookies.getAll({ domain: ".google.com" })`.
- Ne collecter que les noms nécessaires ; documenter chaque nom.
- Ajouter un écran de consentement et une politique de confidentialité.
- Supprimer les IP codées en dur et le subnet sweep.

### 2.18 Rotation des cookies et permissions (F-05/F-06)

1. **Considérer les cookies comme compromis** si l'instance a été exposée sur un réseau ou si `/output` était accessible.
2. Se connecter au compte Google concerné → `https://myaccount.google.com/security` → « Vos appareils » → déconnecter les sessions ; changer le mot de passe si doute.
3. Vérifier l'activité récente (Gmail, Drive, Gemini).
4. `chmod 700 cookies data && chmod 600 cookies/*.json data/*.db*`.
5. Relancer la synchronisation avec l'extension après rotation.
6. Ne jamais committer `cookies/` ; vérifier `.gitignore` et l'historique Git (`git log --all -- cookies`).

---

## 3. P1 — Refactor (1–2 semaines)

### 3.1 Découpler les exports

- `AnalyticsService` avec un worker dédié, un canal de taille 1, un debounce de 5 s, une écriture atomique.
- `/export/excel` et `/export/json` génèrent à la demande, en mémoire, avec un rate limit (ex. 2/min).
- Ne plus écrire dans `output/` pour les analytics ; si un fichier est nécessaire, le servir depuis un cache signé.

### 3.2 Refactoriser `gemini/client.go`

Découper en :

| Fichier | Responsabilité |
|---|---|
| `transport.go` | TLS/QUIC, profils Chrome, headers |
| `session.go` | `InitSession`, `ReloadSession`, `ensureInit`, cookies |
| `chat.go` | `SendPrompt`, streaming, parsing JSPB |
| `media.go` | upload, download, redirects, MIME |
| `registry.go` | modèles, templates, validation |

- Injecter un `SessionStore` et un `HTTPClient` pour les tests.
- Remplacer les globales par des champs de struct.
- Ajouter des interfaces pour les tests.

### 3.3 Refactoriser `api/chat_handler.go`

- Extraire `ChatService` (prompt, streaming, médias, tokens).
- Extraire `HistoryService` (DB, exports).
- Utiliser `context.Context` de bout en bout ; annuler les goroutines à la déconnexion SSE.
- Détacher la journalisation DB dans un worker borné (pas une goroutine par requête).

### 3.4 Tests

- Tests d'intégration : auth, traversal, CORS, WS, exports, MCP.
- Tests de concurrence : `-race`, 100 requêtes parallèles, exports.
- Fuzz : parsing JSON, `header_template`, cookies, chemins.
- Couverture cible : `api` 60 %, `gemini` 60 %, `db` 70 %.
- Ajouter un test de non-régression pour chaque PoC (P-01 à P-18).

### 3.5 CI/CD

Voir le modèle dans `RAPPORT_AUDIT.md` §F-24. Ajouter :

- `go vet`, `go test -race -short`, `govulncheck`, `gosec`, `staticcheck`.
- `trivy fs` + `trivy image`, `hadolint`.
- Actions épinglées par SHA, `permissions` minimales.
- SBOM (`syft`), signature (`cosign`), provenance (`actions/attest-build-provenance`).
- Blocage sur HIGH/CRITICAL reachable.

### 3.6 Observabilité et audit

- Logs structurés JSON (`slog`), redaction des cookies/prompts.
- Métriques : requêtes, latence, erreurs, taille DB, workers, exports, tokenizer.
- Alertes : taux d'erreur > 5 %, `429` Google, cookies expirés, disque > 80 %.
- Journal d'audit : auth, admin, sync cookies, modèles, exports.

### 3.7 GDPR / rétention

- Rétention par défaut 30 jours (`DELETE FROM messages WHERE created_at < ...`).
- Endpoint `DELETE /history/:conversation_id` (auth + confirmation).
- Chiffrement au repos (SQLCipher ou chiffrement applicatif).
- Documenter la base légale, la finalité, les destinataires.
- `PRIVACY.md`, `SECURITY.md`, `THREAT_MODEL.md`.

---

## 4. P2 — Hardening et architecture (1–2 mois)

### 4.1 Authentification multi-utilisateur

- OIDC (Google, GitHub) ou clés API par utilisateur.
- RBAC : `user` (chat), `admin` (modèles, cookies, exports), `auditor` (lecture logs).
- Quotas par utilisateur (requêtes/min, tokens/jour).
- Sessions signées, rotation, révocation.

### 4.2 Chiffrement

- Cookies et tokens chiffrés au repos (AES-GCM, clé dans un KMS/Vault).
- TLS partout (reverse proxy, mTLS extension).
- Secrets via fichiers Docker secrets ou Vault, pas d'env en clair.

### 4.3 Isolation réseau

- Egress allowlist : seuls `gemini.google.com`, `*.googleusercontent.com`, `*.gstatic.com`.
- Proxy sortant avec inspection TLS (si autorisé) ou firewall.
- Interdire les redirections vers des hôtes hors allowlist.
- Réseau Docker dédié, pas de `network_mode: host`.

### 4.4 Extension

- Remplacer le WebSocket par **native messaging** (IPC OS) ou mTLS.
- Pairing avec code éphémère, stockage du jeton dans `chrome.storage.session`.
- Consentement granulaire, politique de confidentialité, écran de révocation.
- Distribution hors Chrome Web Store si nécessaire (entreprise).

### 4.5 Supply chain

- SBOM CycloneDX, signature cosign, provenance SLSA.
- Renovate/Dependabot, revue des mises à jour.
- `go mod verify`, vendor ou proxy interne.
- Scanner les secrets (gitleaks) dans la CI et l'historique.

### 4.6 Conformité

- Avis juridique sur les ToS Google et le watermark removal.
- Évaluer la migration vers l'API officielle Gemini/Vertex AI.
- DPIA, registre des traitements, politique de rétention.
- Chrome Web Store : justification des permissions, politique de confidentialité, test de soumission.

### 4.7 Tests avancés

- Pentest externe (réseau, API, extension, MCP).
- Test de charge 50–100 req/s (local d'abord, puis compte jetable).
- Chaos engineering : coupure Google, expiration cookies, disque plein, DB locked.
- Bug bounty / divulgation responsable.

---

## 5. Plan de vérification

### 5.1 Tests de sécurité (à automatiser)

| Test | Commande | Attendu |
|---|---|---|
| Traversal | `curl ... /output/%2e%2e%2f...` | 400/404 |
| Auth | `curl /history` sans jeton | 401 |
| Auth admin | `curl -X POST /v1/models` sans jeton | 401 |
| CORS | préflight `Origin: evil` | pas de `ACAO: *` |
| WS origine | handshake `Origin: evil` | 403 |
| WS jeton | handshake sans token | 401 |
| Cookies perms | `stat -c %a cookies/*.json` | 600 |
| Docker user | `docker run ... id` | non-root |
| Healthcheck | `docker inspect` | défini |
| Exports | 30 parallèles | 30/30 valides |
| Races | `go test -race` | PASS |
| Deps | `govulncheck ./...` | 0 reachable |
| Image | `trivy image` | 0 HIGH/CRITICAL |
| Tokenizer | prompt 50 Mo | 413 ou estimation rapide |

### 5.2 Script de fumée

```bash
#!/usr/bin/env bash
set -euo pipefail
API="${API:-http://127.0.0.1:8100}"
TOKEN="${API_TOKEN:?}"

code=$(curl -s -o /dev/null -w '%{http_code}' "$API/history")
[ "$code" = "401" ] || { echo "auth KO: $code"; exit 1; }

code=$(curl -s -o /dev/null -w '%{http_code}' \
  "$API/output/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd")
[ "$code" = "400" ] || [ "$code" = "404" ] || { echo "traversal KO: $code"; exit 1; }

code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$API/history")
[ "$code" = "200" ] || { echo "auth token KO: $code"; exit 1; }

echo "smoke OK"
```

### 5.3 Test de charge local

```bash
python3 - <<'PY'
import concurrent.futures, urllib.request
B='http://127.0.0.1:8100'
def get(_):
    with urllib.request.urlopen(B+'/health', timeout=5) as r:
        return r.status
with concurrent.futures.ThreadPoolExecutor(max_workers=100) as ex:
    print(sum(1 for s in ex.map(get, range(100)) if s == 200), '/100')
PY
```

---

## 6. Runbook de déploiement

### 6.1 Local mono-utilisateur (recommandé)

```bash
# .env
API_TOKEN=$(openssl rand -hex 32)
WS_TOKEN=$(openssl rand -hex 32)
PORT=8100
API_HOST=127.0.0.1
WS_HOST=127.0.0.1

# Démarrage
set -a; . ./.env; set +a
./goapi
```

- L'extension est configurée sur `http://127.0.0.1:8100` avec le `WS_TOKEN`.
- Pas de port forward, pas d'exposition LAN.
- Sauvegardes chiffrées de `data/` ; cookies dans un volume `0700`.

### 6.2 Docker

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f --tail=100
curl -s http://127.0.0.1:8100/health
```

- Vérifier `docker inspect` : `User=nonroot`, `Healthcheck` défini, `ReadOnlyRootfs=true`.
- Vérifier que les ports sont mappés sur `127.0.0.1` uniquement.

### 6.3 Reverse proxy (si exposition réseau)

```caddyfile
gemini.example.com {
    reverse_proxy 127.0.0.1:8100
    tls internal
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        X-Content-Type-Options "nosniff"
        X-Frame-Options "DENY"
        Referrer-Policy "no-referrer"
    }
    @api {
        path /v1/* /chat /history* /stats /export/* /api/*
    }
    basicauth @api {
        admin $2a$14$...
    }
}
```

- Ajouter un WAF (Coraza/ModSecurity) si exposition publique.
- Rate limit au niveau du proxy (ex. 60 req/min/IP).
- Ne jamais exposer le WS (9226) publiquement.

---

## 7. Risques résiduels après P0

| Risque | Statut | Mitigation |
|---|---|---|
| ToS Google / bannissement | Non résolu | Migration API officielle (P2) |
| Chrome Web Store | Non résolu | Permissions minimales + consentement (P1) |
| Watermark removal | Non résolu | Retirer la fonctionnalité ou avis juridique |
| Chiffrement au repos | Non résolu | AES-GCM/KMS (P2) |
| Multi-utilisateur | Non résolu | OIDC/RBAC (P2) |
| Supply chain | Partiel | CI + SBOM + cosign (P1/P2) |
| Détection TLS/QUIC | Non résolu | Veille + fallback + tests |
| Couverture tests | Partiel | Objectif 60 % (P1) |

---

## 8. Checklist P0

**Statut : appliqué et vérifié le 21/11/2026** (`go test -race`, `govulncheck`,
smoke tests HTTP/WS, conteneur durci).

- [x] `API_TOKEN` obligatoire, middleware sur toutes les routes sauf `/health`.
- [x] `WS_TOKEN` + `CheckOrigin` allowlist + bind `127.0.0.1`.
- [x] `/output/*` : `filepath.IsLocal`/`os.OpenRoot`, tests de traversal.
- [x] CORS allowlist, pas de `*`.
- [x] Cookies `0600`, répertoires `0700`, rotation des sessions Google.
- [x] `AutoExportAnalytics` retiré du chemin de requête.
- [x] `BodyLimit` 10 Mo, `CountTokens` limité/estimé, sémaphore.
- [x] `ReadTimeout`/`IdleTimeout`, rate limiter.
- [x] Data races corrigées (`atomic`, `initMu`, `atomic.Value`).
- [x] `DownloadAndClean` : `os.MkdirTemp` + `filepath.Base`.
- [x] Registre de modèles validé + auth admin.
- [x] Docker non-root, `.dockerignore` complet, healthcheck, ports loopback.
- [x] Dépendances reachables mises à jour, `govulncheck` OK.
- [x] Extension : LAN sweep supprimé, URL fixe, permissions minimales.
- [x] Tests de fumée + `go test -race` + stress 100 req.
- [x] Documentation mise à jour (`README`, `.env.example`, `SECURITY.md`).

---

## 9. Annexes

### 9.1 Exemple de middleware de sécurité

```go
app.Use(func(c fiber.Ctx) error {
    c.Set("X-Content-Type-Options", "nosniff")
    c.Set("X-Frame-Options", "DENY")
    c.Set("Referrer-Policy", "no-referrer")
    c.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
    return c.Next()
})
```

### 9.2 Exemple de log d'audit

```go
log.Printf("audit action=%s user=%s ip=%s route=%s status=%d",
    action, userID, c.IP(), c.Path(), status)
```

Ne jamais logger les cookies, les prompts complets ou les tokens.

### 9.3 Exemple de test de non-régression traversal

```go
func TestOutputTraversalRejected(t *testing.T) {
    app := fiber.New()
    RegisterRoutes(app)
    req := httptest.NewRequest("GET",
        "/output/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd", nil)
    resp, err := app.Test(req)
    if err != nil { t.Fatal(err) }
    if resp.StatusCode == 200 {
        t.Fatalf("traversal still exploitable")
    }
}
```

### 9.4 Exemple de test d'auth

```go
func TestHistoryRequiresAuth(t *testing.T) {
    app := fiber.New()
    RegisterRoutes(app)
    resp, _ := app.Test(httptest.NewRequest("GET", "/history", nil))
    if resp.StatusCode != 401 {
        t.Fatalf("expected 401, got %d", resp.StatusCode)
    }
}
```

### 9.5 Références

- OWASP ASVS 4.0 : V1 (auth), V5 (validation), V12 (fichiers), V14 (config).
- OWASP Top 10 2021.
- CWE-22, CWE-306, CWE-319, CWE-362, CWE-400, CWE-942.
- CVSS v3.1 specification.
- Chrome Web Store Program Policies (minimum permissions, limited use).
- Google APIs Terms of Service.
- GDPR articles 5, 6, 17, 32.
