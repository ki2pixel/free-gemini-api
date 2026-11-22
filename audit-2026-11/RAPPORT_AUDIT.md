# Audit de sécurité, de robustesse et de maturité — Free Gemini API

**Date :** 21 novembre 2026  
**Périmètre :** `free-gemini-api/` (moteur Go), `gemini-extension/` (extension Chrome MV3), `Dockerfile`, `docker-compose.yml`, CI GitHub Actions, base SQLite, serveur MCP.  
**Version auditée :** arbre de travail au 21/11/2026 (pas de tag Git ; extension et MCP `2.2.0`, Fiber `3.4.0`, Go `1.26.0`).  
**Type d'audit :** boîte grise — revue de code, SAST, DAST local, tests de concurrence, benchmarks, analyse de la chaîne CI/CD et de la conformité.  
**Statut :** audit terminé. Aucune modification du code de production n'a été effectuée. Tous les tests dynamiques ont été exécutés sur une copie isolée dans `/tmp/opencode/audit-src` et une instance locale sur les ports `18001`/`19226`, avec des cookies factices. Les cookies Google réels n'ont jamais été utilisés, copiés ni exfiltrés.

---

## 1. Synthèse exécutive

### 1.1 Verdict

> **Indice global de maturité : 38 / 100 — niveau 2/5 (« ad hoc / reproductible »).**  
> **Décision : NO-GO pour une exposition réseau, un usage multi-utilisateur ou une publication Chrome Web Store en l'état.**  
> **GO conditionnel pour un usage local mono-utilisateur**, uniquement après application des quick wins P0 (authentification, bind localhost, correction de la traversée de répertoire, rotation des cookies).

Le moteur est techniquement ambitieux — pool de comptes avec affinité, circuit breaker, streaming SSE, SQLite WAL, simulation TLS/QUIC Chrome 152, découverte de modèles — mais la sécurité périphérique est presque absente : **aucune authentification**, **traversée de répertoire**, **CORS `*`**, **WebSocket sans contrôle d'origine**, **cookies Google mondiaux stockés en clair**, et **l'extension Chrome envoie ces cookies à tout hôte du LAN qui répond à `/health`**.

### 1.2 Top 5 des risques

| # | Risque | Preuve | Impact |
|---|---|---|---|
| 1 | **Vol de session Google via traversée `/output/*`** | `GET /output/%2e%2e%2f...%2fcookies%2faccount_*.json` → `200` + JSON des cookies ; lecture de `/etc/passwd` et du WAL SQLite | Prise de contrôle du compte Google (Gmail, Drive, YouTube…), exfiltration de tout l'historique de chat |
| 2 | **API sans authentification, exposée sur `0.0.0.0`** | `/history`, `/stats`, `/v1/workers`, `/v1/analytics`, `/export/excel`, `POST /v1/models`, `POST /api/sync-cookies` → `200` sans en-tête `Authorization` | Lecture de tous les chats, injection de modèles, empoisonnement du pool de comptes |
| 3 | **Extension : exfiltration des cookies vers le LAN** | `discoverServer()` scanne `192.168.1.1-254`, `probeServer()` accepte tout JSON contenant `engine`, puis `ws://host:9226` ou `http://host:8100/api/sync-cookies` | Un attaquant sur le même réseau récupère les cookies d'authentification Google complets |
| 4 | **CORS `*` + WebSocket `CheckOrigin=true`** | Préflight `204` avec `Access-Control-Allow-Origin: *` et méthodes `GET, POST, PUT, DELETE, PATCH` ; handshake WS accepté depuis `Origin: https://evil.example` | Un site web ou une extension peut lire l'API locale et injecter des cookies/modèles (drive-by), et tout hôte du LAN peut attaquer l'API directement |
| 5 | **Déni de service non authentifié** | `AutoExportAnalytics()` à chaque requête (0,53 s CPU/IO pour 5 000 messages, sérialisé) ; `CountTokens` 45 ms et 12 Mo d'allocations pour 100 000 caractères, body limit 50 Mo | Un seul client peut saturer le CPU, la mémoire et les goroutines ; OOM possible |

### 1.3 Top 5 des actions immédiates (P0, 0–48 h)

1. **Corriger la traversée de répertoire** : `filepath.Base` + `filepath.IsLocal` ou `os.OpenRoot("./output")`, et ne servir que des fichiers enregistrés en base.
2. **Ajouter une authentification** par jeton (`API_TOKEN`) sur toutes les routes sauf `/health`, avec comparaison en temps constant ; **bind `127.0.0.1` par défaut** pour l'API et le WS.
3. **Verrouiller le WebSocket** : `CheckOrigin` allowlist (extension + localhost), jeton de pairing, bind loopback.
4. **Restreindre CORS** à l'origine de l'extension et à `http://127.0.0.1:*`, jamais `*`.
5. **Rotation des cookies Google exposés** : révoquer les sessions Google du fichier `cookies/account_ga000FAlQD.json`, puis passer les fichiers en `0600` et les répertoires en `0700`.

### 1.4 Ce qui a été testé

- Revue complète des paquets `main`, `api`, `gemini`, `db`, de l'extension et des fichiers de déploiement.
- Build et `go vet` avec Go 1.26.0 ; `go test -short ./...`, `go test -race`, `go test -shuffle=on` : **tous verts** sur le code existant.
- SAST : `gosec` (84 constats), `staticcheck` (5 constats), `govulncheck` (10 vulnérabilités, 2 reachables), `trivy` (28 vulnérabilités Go, 2 Alpine), `hadolint` (3 avertissements).
- DAST local : traversée de répertoire, endpoints non authentifiés, CORS, WebSocket, injection de cookies, empoisonnement de modèles, Slowloris, stress concurrent, corruption d'exports, MCP.
- Tests de concurrence dédiés : data races, répertoire temporaire partagé, téléchargement avec redirection, écritures SQLite concurrentes, benchmarks tokenizer/export.

### 1.5 Ce qui n'a pas été testé (et pourquoi)

| Test | Raison | Protocole prêt |
|---|---|---|
| Flux Gemini réels (guest, chat, images) | Consentement utilisateur requis ; risque de bannissement du compte | Oui — voir §8.1 |
| Stress 50–100 requêtes parallèles **vers Google** | Risque de rate-limit, de blocage de compte et de bannissement IP | Oui — voir §8.2 ; le stress local 100 requêtes a été exécuté |
| Soumission Chrome Web Store | Nécessite un compte développeur et une build signée | Analyse statique de conformité MV3 fournie (§6.8) |
| Test d'intrusion réseau externe | Périmètre local uniquement | Non applicable |

---

## 2. Périmètre, méthodologie et environnement

### 2.1 Composants audités

```
free-gemini-api-main/
├── free-gemini-api/            # moteur Go (module goapi)
│   ├── main.go                 # bootstrap, Fiber, CORS, recover, graceful shutdown
│   ├── api/                    # routes, chat SSE, worker pool, exports, MCP, sessions
│   ├── gemini/                 # client TLS/QUIC Chrome 152, cookies, registre, monitor WS
│   ├── db/                     # SQLite WAL, schéma, requêtes
│   ├── Dockerfile
│   └── docker-compose.yml
├── gemini-extension/           # extension Chrome MV3 (manifest, background, popup)
└── .github/workflows/          # publication GHCR
```

### 2.2 Environnement d'audit

- Copie isolée : `/tmp/opencode/audit-src` (cookies, `data/`, `output/`, `tuned.cact` exclus).
- Runtime : `/tmp/opencode/audit-run`, ports `18001` (HTTP) et `19226` (WS), cookies factices `AUDIT_*`.
- Go 1.27.4 pour le build initial, puis `GOTOOLCHAIN=go1.26.0` pour les outils et les tests (le `go.mod` déclare `go 1.26.0` ; le Go système 1.18.1 est trop ancien).
- Docker 29.1.3, image `audit-fga:latest` construite localement (55,8 Mo), `trivy` et `hadolint` via conteneurs.
- Aucune donnée de production n'a été lue par les PoC : les cookies réels sont restés hors de la copie d'audit ; les fichiers lus par traversée dans l'instance locale étaient des fixtures.

### 2.3 Outils et versions

| Outil | Version / mode | Résultat principal |
|---|---|---|
| `go build`, `go vet` | Go 1.26.0 | Build et vet OK |
| `go test -short`, `-race`, `-shuffle` | Go 1.26.0 | Tous verts (code existant) |
| `gosec` | v2.x, `-fmt=text` | 84 constats : 8 HIGH, 25 MEDIUM, 51 LOW |
| `staticcheck` | 2025.1.1 (0.6.1) | 5 constats de style / code mort |
| `govulncheck` | v1.10.0 | 10 vulnérabilités, 2 reachables (brotli, klauspost/compress) |
| `trivy` | latest, `fs` + `image` | 28 vulnérabilités Go (13 HIGH), 2 HIGH Alpine (musl) |
| `hadolint` | latest | 3 avertissements (DL3018 ×2, DL3059) |
| Benchmarks Go | `-benchtime=1x -benchmem` | Voir §6.4 |
| Stress local | Python `ThreadPoolExecutor` | 100 req. `/health` et `/history`, 30 exports concurrents |

### 2.4 Limites

- L'audit est statique et dynamique **local** ; il ne remplace pas un pentest externe, un test de charge réel ni une revue juridique.
- Les scores CVSS sont des scores de base, sans métriques temporelles ni environnementales ; la criticité métier est indiquée séparément.
- Les PoC sont exécutés sur une copie isolée ; les commandes du cahier doivent être adaptées à l'environnement de l'utilisateur.
- Les tests Google live n'ont pas été exécutés faute de consentement explicite (compte jetable) et pour éviter tout risque de bannissement.

---

## 3. Architecture et flux

### 3.1 Vue d'ensemble

```
┌─────────────────────┐        ws://127.0.0.1:9226 / http://:8100
│  Extension Chrome   │ ─────────────────────────────────────────┐
│  (cookies Google)   │   cookies_payload / api/sync-cookies      │
└─────────────────────┘                                          ▼
                                                     ┌──────────────────────┐
┌─────────────────────┐   HTTP/SSE   :8001 (défaut)  │  Free Gemini API     │
│ Clients OpenAI-like │ ───────────────────────────► │  Fiber v3 + WorkerPool│
│ (Cursor, Cline…)    │                              │  SQLite WAL          │
└─────────────────────┘                              └──────────┬───────────┘
                                                                │ TLS/QUIC Chrome 152
                                                                ▼
                                                     ┌──────────────────────┐
                                                     │ gemini.google.com    │
                                                     │ (endpoints non doc.) │
                                                     └──────────────────────┘
```

### 3.2 Flux principaux

1. **Synchronisation des cookies** : l'extension lit `chrome.cookies.getAll({})`, filtre les domaines Google et une allowlist de noms (`SID`, `HSID`, `SSID`, `APISID`, `SAPISID`, `__Secure-1PSID`, `__Secure-3PSID`, `NID`…), puis pousse le JSON via WebSocket (`cookies_payload`) ou HTTP (`POST /api/sync-cookies`). Le serveur écrit `cookies/account_<id>.json` et recharge le pool.
2. **Sélection de compte** : `WorkerPool` applique une affinité de session (`affinityMap`), un tier (`free`/`subscription`/`ultra`), un least-busy et une concurrence max de 3 par worker. Un circuit breaker met les workers en cooldown après erreurs.
3. **Chat** : `HandleUnifiedChat` ou `HandleOpenAIChatCompletions` aplatit l'historique, planifie un delta de conversation (`session_transcripts.go`), exécute la requête via le worker, streame la réponse SSE, télécharge les médias, puis journalise dans SQLite.
4. **Analytics** : chaque requête de chat/média déclenche `AutoExportAnalytics()`, qui régénère en arrière-plan `output/gemini_analytics.xlsx` et `.json` (sous mutex).
5. **MCP** : `goapi --mcp` expose des outils stdio (`generate_image`, `generate_video`, `generate_music`, `chat`, `reset_session`, `check_health`) qui proxifient vers l'API HTTP (`GEMINI_API_URL`).

### 3.3 Points forts

- Build statique pur Go (SQLite ModernC), déploiement simple, image finale de 55,8 Mo.
- WAL SQLite, `busy_timeout=5000`, `synchronous=NORMAL`, 25 connexions : bon compromis lecture/écriture.
- Pool de workers avec affinité, cooldown et reprise ; `go test -race` passe sur le code existant.
- Arrêt gracieux SIGINT/SIGTERM fonctionnel (testé : `app.ShutdownWithTimeout(5s)`).
- Commentaires de code riches, notamment sur les pièges de la reverse engineering (rotation `__Secure-1PSID`, doublons de workers, etc.).
- Séparation claire `api` / `gemini` / `db`, tests unitaires présents.

---

## 4. Indice de maturité

### 4.1 Rubrique

| Niveau | Libellé | Description |
|---|---|---|
| 1 | Initial | Ad hoc, pas de contrôle, secrets exposés |
| 2 | Reproductible | Build/test reproductibles, mais sécurité et exploitation non maîtrisées |
| 3 | Défini | Processus et architecture documentés, contrôles partiels |
| 4 | Géré | Contrôles mesurés, tests automatisés, sécurité intégrée |
| 5 | Optimisé | Amélioration continue, résilience prouvée, conformité auditée |

### 4.2 Notes par axe

| Axe | Poids | Note /100 | Niveau | Justification synthétique |
|---|---:|---:|---|---|
| 1. Sécurité applicative, secrets, API | 25 % | **22** | 2 | Traversée, aucune auth, CORS `*`, WS ouvert, cookies 0644, races |
| 2. Architecture et qualité du code | 15 % | **55** | 3 | Packages clairs, commentaires, tests ; monolithes `client.go`/`chat_handler.go`, globales, code mort, couverture 28 % |
| 3. Concurrence et tolérance aux pannes | 15 % | **42** | 3 | Pool, retries, WAL, shutdown ; data races, temp partagé, caches non bornés, exports concurrents |
| 4. Performance et SQLite | 15 % | **50** | 3 | 807 écritures/s, WAL ; export par requête, tokenizer CPU, body 50 Mo, pas de rate limit |
| 5. DevOps, Docker, build | 15 % | **38** | 2 | Multi-stage, compose, CI publish ; root, pas de healthcheck/limits, images non épinglées, CI sans tests/SAST, ports incohérents |
| 6. Reverse engineering et intégration Google | 15 % | **30** | 2 | TLS/QUIC Chrome 152 sophistiqué ; ToS, bannissement, watermark removal, extension non conforme, pas d'API officielle |
| **Global pondéré** | **100 %** | **38** | **2** | **Non production-ready** |

### 4.3 Trajectoire cible

| Étape | Note projetée | Condition |
|---|---:|---|
| État actuel | 38 | — |
| Après P0 (0–48 h) | **58** | Auth, bind local, traversal, CORS, WS, rotation cookies, limites |
| Après P1 (1–2 semaines) | **74** | Refactor sessions/exports, tests d'intégration, Docker durci, deps à jour, CI sécurité |
| Après P2 (1–2 mois) | **85** | Architecture multi-utilisateur, chiffrement au repos, egress allowlist, conformité, observabilité |

---

## 5. Registre des constats (synthèse CVSS v3.1)

Le détail de chaque fiche est dans [`REGISTRE_CVSS.md`](REGISTRE_CVSS.md). Le fichier [`REGISTRE_CVSS.csv`](REGISTRE_CVSS.csv) est exploitable dans un tableur ou un ticketing.

| ID | Constat | CVSS | Sévérité | Statut |
|---|---|---:|---|---|
| F-01 | Traversée de répertoire `/output/*` — lecture arbitraire de fichiers | 8.6 | High (critique métier) | Confirmé |
| F-02 | Absence d'authentification sur l'API et les endpoints d'administration | 9.1 | Critical | Confirmé |
| F-03 | WebSocket cookies sans auth, `CheckOrigin=true`, bind `0.0.0.0` | 9.1 | Critical | Confirmé |
| F-04 | CORS `*` — lecture/écriture cross-origin | 9.3 | Critical | Confirmé |
| F-05 | Extension : cookies Google envoyés à tout hôte du LAN répondant `/health` | 9.3 | Critical | Confirmé (statique) |
| F-06 | Cookies Google en `0644` + conteneur root | 5.5 | Medium | Confirmé |
| F-07 | `DownloadFile` forward le cookie sur toutes les redirections | 5.9 | Medium | Confirmé (test) |
| F-08 | Data races (`CachedImageCookies`, `IsInitialized`, `AccountPrefix`) | 5.6 | Medium | Confirmé (`-race`) |
| F-09 | Répertoire temporaire partagé `./output/.temp` | 5.3 | Medium | Confirmé (test) |
| F-10 | `AutoExportAnalytics` à chaque requête — amplification I/O | 7.5 | High | Confirmé (code + bench) |
| F-11 | `CountTokens` sur prompt 50 Mo — CPU/mémoire | 7.5 | High | Confirmé (bench) |
| F-12 | Pas de rate limiting ni de timeouts (Slowloris) | 5.3 | Medium | Confirmé |
| F-13 | Caches `UserSessions`/`affinityMap` non bornés | 5.3 | Medium | Confirmé (code) |
| F-14 | Exports concurrents vers le même fichier — corruption | 5.3 | Medium | Confirmé (stress) |
| F-15 | Dépendances vulnérables (brotli, klauspost, excelize, x/crypto…) | 5.9 | Medium | Confirmé |
| F-16 | Conteneur root, pas de healthcheck/limits, `COPY . .` inclut `cookies/` | 7.8 | High | Confirmé |
| F-17 | Incohérence de ports 8001/8100 | 3.7 | Low | Confirmé |
| F-18 | Extension : permissions excessives, clipboard, ws/http en clair | 6.5 | Medium | Confirmé |
| F-19 | MCP : lecture arbitraire de fichiers + exfil vers le backend | 5.5 | Medium | Confirmé (PoC) |
| F-20 | Registre de modèles non authentifié (`header_template`/`mode_id`) | 6.5 | Medium | Confirmé |
| F-21 | Artefacts de debug et secret de prompt dans le dépôt | 3.7 | Low | Confirmé |
| F-22 | API HTTP clair sur `0.0.0.0` — sniffing LAN | 6.5 | Medium | Confirmé |
| F-23 | Qualité/SAST : erreurs non gérées, RNG faible, code mort, couverture 28 % | 3.7 | Low | Confirmé |
| F-24 | CI/CD : pas de tests/SAST, actions non épinglées | 5.6 | Medium | Confirmé |
| F-25 | Conformité : ToS Google, Chrome Web Store, GDPR, watermark removal | N/A | High (business) | Analyse |

**Répartition :** 4 Critical, 4 High CVSS + 1 High métier (F-25, sans score CVSS), 13 Medium, 3 Low.

---

## 6. Analyse par axe

### 6.1 Sécurité applicative, secrets et API

#### 6.1.1 Traversée de répertoire (F-01)

Le handler est minimal :

```go
// api/routes.go:267-269
app.Get("/output/*", func(c fiber.Ctx) error {
    return c.SendFile("./output/" + c.Params("*"))
})
```

`c.Params("*")` est décodé avant d'être concaténé, et `SendFile` ne normalise pas le chemin. Une requête encodée une fois (`%2e%2e%2f`) remonte l'arborescence ; une requête brute (`../`) est normalisée par le routeur et renvoie 404 ; le double encodage échoue car le fichier littéral `%2e%2e` n'existe pas.

**Impact confirmé :** lecture de `/etc/passwd`, des fichiers de cookies (`cookies/account_*.json`), de la base SQLite et de son WAL (`data/gemini.db-wal`), donc de tout l'historique de chat. Les cookies Google permettent une prise de contrôle du compte (Gmail, Drive, YouTube, etc.). C'est le constat le plus grave en impact métier.

**Correctif :** `os.OpenRoot("./output")` (Go 1.24+) ou `filepath.IsLocal` + `filepath.Clean` + vérification de préfixe ; idéalement, ne servir que les fichiers dont le nom est enregistré dans `media_generations`.

#### 6.1.2 Absence d'authentification (F-02)

Aucune route ne vérifie d'identité. Les routes sensibles sont :

- `GET /history`, `/history/search`, `/media`, `/stats`, `/v1/workers`, `/v1/analytics`, `/export/json`, `/export/excel`, `/status` ;
- `POST /v1/models`, `/v1/models/discover`, `/api/sync-cookies` ;
- `POST /chat`, `/v1/chat/completions`, `/music` (consomment les comptes Google) ;
- `POST /reset` (supprime une session arbitraire).

Le README documente `Authorization: Bearer any-key` : le client envoie un jeton, mais le serveur ne le lit jamais. C'est une fausse impression de sécurité.

**Impact :** tout utilisateur réseau peut lire les conversations, les analytics, les métriques de comptes, exporter les données, enregistrer un modèle malveillant, injecter des cookies et consommer le quota Google. Combiné à F-01, l'attaquant n'a même pas besoin d'un compte.

**Correctif :** middleware d'authentification global, jeton en variable d'environnement (`API_TOKEN`), `crypto/subtle.ConstantTimeCompare`, rotation, rate limiting par jeton. Pour un usage local, bind `127.0.0.1` **et** jeton (défense en profondeur).

#### 6.1.3 WebSocket (F-03)

```go
// gemini/monitor.go:186-191
var wsUpgrader = websocket.Upgrader{
    CheckOrigin: func(r *http.Request) bool { return true },
}
// gemini/monitor.go:356
addr := fmt.Sprintf("0.0.0.0:%d", port)
```

Le bridge accepte n'importe quelle origine et n'importe quel client. Un site web peut ouvrir `ws://127.0.0.1:9226` (si le navigateur l'autorise) et envoyer `{"type":"cookies_payload","cookies":[...]}`. Un attaquant LAN peut s'y connecter directement. Le serveur écrit les cookies, recharge le pool et peut router des prompts vers le compte de l'attaquant.

**Impact :** empoisonnement du pool, lecture indirecte de prompts, déni de service (fichiers de cookies en masse, `InitSession` à chaque payload, 5 s de sleep), et vecteur de persistence (cookies écrits sur disque).

**Correctif :** `CheckOrigin` allowlist (`chrome-extension://<id>`, `http://127.0.0.1:*`), jeton de pairing, bind `127.0.0.1`, limite de taille et de fréquence des messages, validation stricte du schéma des cookies.

#### 6.1.4 CORS (F-04)

`app.Use(cors.New())` avec les valeurs par défaut de Fiber v3 : `AllowOrigins: ["*"]`, `AllowMethods: GET, POST, HEAD, PUT, DELETE, PATCH, QUERY`. Le préflight renvoie `204` avec `Access-Control-Allow-Origin: *`.

**Impact :** n'importe quel site web peut lire `/history`, `/v1/analytics`, `/stats` depuis le navigateur de la victime, et envoyer des `POST` (modèles, cookies, chat). Le navigateur n'envoie pas de credentials, mais l'API n'en demande pas. C'est une chaîne drive-by complète.

> **Nuance navigateur :** les Chrome récents peuvent appliquer Private Network Access (PNA) et bloquer certaines requêtes public→localhost. Cela n'atténue ni l'accès direct au service sur le LAN (`0.0.0.0`), ni les requêtes depuis une extension, ni les navigateurs/versions qui n'appliquent pas PNA. Le score CVSS reste fondé sur l'exposition réseau et l'absence d'authentification.

**Correctif :** allowlist d'origines, pas de `*`, pas de credentials, vérification `Origin` sur les mutations, jeton CSRF si cookies de session.

#### 6.1.5 Secrets et cookies (F-05, F-06, F-07, F-22)

- Les cookies sont écrits en `0644` (`os.WriteFile(..., 0644)` dans `monitor.go:277`) et les répertoires en `0755`. Le fichier réel `cookies/account_ga000FAlQD.json` est `640 root:root` dans l'installation observée, mais le processus tourne en root dans Docker : tout processus du conteneur peut le lire.
- `DownloadFile` injecte `Cookie` **à chaque hop** de redirection, sans vérifier l'hôte :

```go
// gemini/client.go:1627, 1660-1662
cookieStr := CachedImageCookies
...
if cookieStr != "" {
    req.Header.Set("Cookie", cookieStr)
}
```

Le test `TestAuditDownloadFileForwardsCookiesAcrossRedirect` confirme qu'un serveur de redirection reçoit le cookie Google. Si Google redirige vers un CDN tiers ou si un attaquant contrôle une URL média, la session fuit.
- L'API écoute sur `0.0.0.0` (`app.Listen(":"+port)`), en HTTP clair. Sur un LAN, un attaquant peut sniffer les chats et les cookies (si le WS est utilisé à distance).
- L'extension collecte les cookies **Google globaux** (pas seulement Gemini) : `SID`, `HSID`, `SSID`, `APISID`, `SAPISID`, `__Secure-1PSID`, `__Secure-3PSID`, `NID`… Ces cookies donnent accès à l'ensemble des services Google.

**Correctif :** `0600`/`0700`, chiffrement au repos (AES-GCM avec clé hors dépôt), ne jamais forward les cookies cross-host, allowlist d'egress (`*.google.com`, `*.googleusercontent.com`, `*.gstatic.com`), TLS local ou bind loopback, et ne collecter que les cookies strictement nécessaires à Gemini avec consentement explicite.

#### 6.1.6 Modèles et MCP (F-19, F-20)

`POST /v1/models` accepte un `ModelConfig` arbitraire : `id`, `mode_id`, `header_template`, `min_tier`, `source`. Le template est ensuite utilisé dans `fmt.Sprintf` pour construire la requête vers Google. Un attaquant peut enregistrer un modèle avec un template qui duplique le prompt, injecte des champs JSON, ou casse la requête. Le modèle est persisté dans `models.json` et survit au redémarrage.

Le serveur MCP lit `ref_image_path` / `start_image_path` sans restriction de répertoire et envoie le fichier au backend via multipart. Un prompt injecté dans un agent peut donc lui faire lire `/etc/passwd`, `~/.ssh/id_rsa`, etc., et les transmettre à Google (ou à un backend compromis). Le PoC `10_mcp_exfil_body.bin` contient le marqueur du fichier lu.

**Correctif :** authentification, validation JSON stricte, allowlist de `mode_id` (16 hex), template avec exactement un `%s`, suppression de l'enregistrement runtime ou signature des configurations ; pour MCP, racine média allowlistée (`MCP_MEDIA_ROOT`), rejet des symlinks, taille max, journalisation.

### 6.2 Architecture et qualité du code

**Points forts :**

- Séparation `api` / `gemini` / `db`, noms explicites, commentaires utiles.
- `session_transcripts.go` est correctement verrouillé (`sync.Mutex`) et documenté.
- `WorkerPool` implémente affinité, tiers, cooldown, circuit breaker et least-busy.
- Arrêt gracieux, `recover()` middleware, `go vet` propre.
- Tests existants : `api`, `db`, `gemini` ; `go test -race` passe.

**Faiblesses :**

- `gemini/client.go` (~1 700 lignes) mélange transport TLS/QUIC, sessions, cookies, upload, download, parsing et découverte ; `api/chat_handler.go` (~1 000 lignes) mélange HTTP, SSE, pool, DB, médias et analytics.
- État global : `DB`, `dbMu`, `GlobalWorkerPool`, `UserSessions`, `CachedImageCookies`, `sessionTurnState`, `OnCookiesUpdated`. Difficile à tester et à faire évoluer.
- Code mort : `getConvState`, `executeStreamRequest` (staticcheck U1000).
- Couverture globale **28,2 %** ; `chat_handler`, `export`, `mcp`, `help` à 0 %.
- Erreurs ignorées : 47 constats G104 ; certaines écritures DB ou fichiers échouent silencieusement.
- Pas de migrations DB versionnées ; `schema.sql` exécuté au démarrage.
- Pas de `context` propagation uniforme ; certains goroutines de log sont détachés.

**Recommandation :** extraire des services (`SessionManager`, `MediaService`, `AnalyticsService`, `ModelRegistry`), injecter les dépendances, remplacer les globales par des structs, ajouter des tests d'intégration et des fuzz tests. Cible : couverture > 60 % sur `api` et `gemini`.

### 6.3 Concurrence et tolérance aux pannes

**Points forts :**

- WAL SQLite, `busy_timeout=5000`, 25 connexions ; test de 1 600 écritures concurrentes : **807 écritures/s, 0 échec**.
- Pool de workers avec `InFlight` atomique, cooldown, reload, affinité.
- Arrêt gracieux testé.
- Retries et `executeWithRetry` pour les appels Google.

**Faiblesses confirmées :**

1. **Data race `CachedImageCookies`** : écriture dans `monitor.go:287` (goroutine WS) et lecture dans `client.go:1627` (goroutine de requête), sans verrou. `go test -race` le prouve.
2. **Data race `IsInitialized`** : `ensureInit()` lit le booléen sans verrou, `InitSession()` l'écrit, `executeWithRetry()` le remet à `false`. Deux requêtes simultanées peuvent lancer deux `InitSession`, deux broadcasts de cookies, deux sleeps de 5 s, et corrompre l'état de session.
3. **Data race `AccountPrefix`** : `getAccountPrefix()` écrit le champ après lecture, sans verrou ; deux goroutines écrivent la même valeur (bénin en apparence, mais formellement une race).
4. **Répertoire temporaire partagé** : `DownloadAndClean` utilise `./output/.temp` pour tous les téléchargements, puis `os.RemoveAll(tempDir)` à la fin. Le test `TestAuditDownloadAndCleanSharedTempCollision` montre qu'un téléchargement concurrent échoue avec `stat output/.temp/slow.png: no such file or directory`.
5. **Exports concurrents** : `HandleExcelExport` écrit dans `output/gemini_analytics.xlsx` puis `c.Download(filePath)`. 30 requêtes parallèles : **26 OK, 4 connexions coupées, 4 XLSX invalides**. `HandleJSONExport` écrit le même fichier JSON à chaque requête, sans verrou (les clients reçoivent la copie mémoire, mais le fichier servi par `/output` peut être corrompu).
6. **Caches non bornés** : `UserSessions` n'a pas d'éviction globale ; `affinityMap` (`sync.Map`) n'est jamais purgé sauf en cas d'échec. Un client peut envoyer des `user_id` uniques pour créer des milliers de clients et d'entrées.
7. **`ProcessAndSaveCookies`** : écrit le fichier, met à jour `CachedImageCookies`, puis appelle `OnCookiesUpdated()` qui recharge **toutes** les sessions et le pool, en bloquant potentiellement 5 s par compte. Un payload WS répété suffit à saturer le serveur.

**Correctif :** verrous/atomiques, `singleflight` pour `InitSession`, `os.MkdirTemp` par téléchargement, export atomique (temp + `os.Rename`) et sérialisé, caches LRU/TTL bornés, debounce/coalescence des reloads de cookies.

### 6.4 Performance et SQLite

**Mesures :**

| Scénario | Résultat | Commentaire |
|---|---:|---|
| SQLite, 32 goroutines × 50 écritures | **807 écritures/s**, 0 échec, 1,98 s | Bon pour un usage mono-nœud |
| Export JSON, 5 000 messages | **130,9 ms**, 28,4 Mo alloués, 325 028 allocs | Coût élevé par requête |
| Export Excel, 5 000 messages | **407,3 ms**, 78,0 Mo alloués, 820 967 allocs | Coût très élevé par requête |
| `CountTokens`, 100 000 caractères | **45,4 ms**, 12,0 Mo alloués, 133 364 allocs | Linéaire ; 50 Mo → ~22 s CPU et ~6 Go d'allocations |
| `EstimateTokens`, 100 000 caractères | **0,42 ms**, 327 Ko, 1 alloc | Alternative pour les gros prompts |
| `/health`, 100 requêtes parallèles | 100/100, p95 **11 ms** | API légère |
| `/history`, 100 requêtes parallèles | 100/100, p95 **11 ms** | OK |
| `/v1/analytics`, 30 requêtes parallèles | 30/30, p95 **6 ms** | Le fichier disque peut être corrompu |
| `/export/excel`, 30 requêtes parallèles | 26/30 OK, 4 erreurs, 4 XLSX invalides | Bug de concurrence |

**Problèmes :**

- `AutoExportAnalytics()` est appelé à chaque requête de chat/média (`chat_handler.go:242, 330, 856, 1011`, `media_handler.go:85`). Le mutex sérialise les exports : à 5 000 messages, chaque requête coûte ~0,53 s de CPU/IO en arrière-plan. À 10 req/s, la file de goroutines croît sans limite.
- `CountTokens` est appelé sur le prompt et la réponse (`chat_handler.go:230, 234, 315, 316, 845, 846, 941, 942`). Le body limit de 50 Mo permet un prompt de 50 Mo → ~22 s de CPU et ~6 Go d'allocations par requête. Avec quelques requêtes parallèles, OOM.
- Pas de rate limiting, pas de `ReadTimeout`/`IdleTimeout`, body limit 50 Mo, pas de limite de taille par champ.
- `DownloadFile` ne limite pas la taille téléchargée (`io.Copy` sans `LimitReader`), ce qui permet un disque plein via une URL média géante.
- Les exports écrivent l'intégralité de l'historique et des messages dans des fichiers servis par `/output/*` (non authentifié).

**Correctif :** export découplé (worker dédié, debounce 30 s, snapshot mémoire, écriture atomique), `CountTokens` limité à 256 Ko ou remplacé par `EstimateTokens` au-delà, body limit 2–10 Mo, `io.LimitReader` sur les téléchargements, rate limiting et timeouts.

### 6.5 DevOps, Docker et build

**Points forts :**

- Build multi-stage, binaire statique, image finale 55,8 Mo.
- `docker-compose.yml` simple, volumes `cookies`/`data`, `restart: unless-stopped`.
- CI de publication GHCR multi-arch (`linux/amd64`, `linux/arm64`), cache GHA.
- `.dockerignore` exclut `data/`, `output/`, `scripts/`, `.git`, `goapi`, `*.log`.

**Faiblesses :**

- **Conteneur root** : aucun `USER` dans le Dockerfile ; `docker run ... id` renvoie `uid=0(root)`. Volumes `cookies`/`data` montés en écriture, pas de `read_only`, pas de `cap_drop`, pas de `no-new-privileges`, pas de `mem_limit`/`pids_limit`.
- **Pas de healthcheck** : ni Dockerfile ni compose.
- **Images non épinglées** : `golang:alpine`, `alpine:3.20` (tag mobile, pas de digest) ; `apk add` sans versions (hadolint DL3018).
- **`COPY . .` dans le builder** : `.dockerignore` n'exclut pas `cookies/` ni `tuned.cact`. Le contexte de build envoie donc les cookies Google réels et 13 Mo de binaire au démon Docker (et au builder distant en CI). L'image finale ne les copie pas, mais les couches/caches du builder peuvent les retenir.
- **Ports incohérents** : Dockerfile `EXPOSE 8001`, compose `8100:8100` + `PORT=8100`, serveur par défaut `8001`, extension `SERVER_PORT = 8100`. Un lancement natif sur 8001 casse l'extension ; un `docker run` sans `PORT` expose 8001 mais mappe 8100.
- **CI sans qualité** : aucun `go test`, `go vet`, `govulncheck`, `gosec`, `staticcheck`, `trivy`, `hadolint` ; actions épinglées par tag (`@v4`, `@v3`) et non par SHA ; publication automatique sur `main`.
- **Pas de SBOM, pas de signature, pas de provenance**.
- **`ENV TZ=Asia/Kolkata`** dans l'image, écrasé par `Europe/Paris` dans compose (incohérence mineure).

**Correctif :** utilisateur non-root, `read_only`, `tmpfs`, `cap_drop: [ALL]`, `security_opt`, healthcheck, limites de ressources, images par digest, `.dockerignore` complet (`cookies/`, `*.cact`, `models.json`), CI avec tests/SAST/scans, actions par SHA, SBOM + cosign.

### 6.6 Reverse engineering et intégration Google

**Points forts :**

- Simulation TLS/QUIC Chrome 152 (`tls-client`, `quic-go`, `fhttp`), headers ordonnés, `sec-ch-ua`, `User-Agent` à jour.
- Découverte de modèles (`/v1/models/discover`), gestion des `mode_id` et des templates JSPB.
- Gestion de la rotation `__Secure-1PSID` et des doublons de workers (commentaires détaillés).
- Fallback HTTP/2, cache de session, retries.

**Risques :**

- **ToS Google** : accès automatisé à l'interface web Gemini, extraction de cookies, farming de comptes, contournement des quotas. Risque élevé de suspension de compte, de blocage IP et d'action juridique.
- **Watermark removal** : la description de l'outil MCP `generate_image` mentionne « auto-cleaned watermarks ». C'est un risque ToS/DMCA et un risque réputationnel.
- **Détection** : les empreintes TLS/QUIC évoluent ; le code est fragile aux changements d'UI/endpoints (`SNlM0e`, `mode_id`). Le fallback est un point de rupture.
- **Pas d'API officielle** : aucune utilisation de l'API Gemini/Vertex AI, donc pas de SLA, pas de quotas légitimes, pas de support.
- **Comptes multiples** : le pool multiplie les requêtes par compte (3 en parallèle), ce qui augmente le risque de lockout ; le code le reconnaît lui-même.
- **HTTP/3 revendiqué côté serveur** : le log et le nom de l'app disent « HTTP/3 QUIC enabled », mais `app.Listen` sert du HTTP/1.1 (curl renvoie `HTTP/1.1 200`). Le HTTP/3 n'est utilisé que côté client vers Google (`quicClient`). C'est trompeur pour l'utilisateur et pour l'auditeur.

**Recommandation :** pour tout usage sérieux, migrer vers l'API officielle Gemini (clé API, quotas, facturation) ou Vertex AI. Si le mode reverse-engineered est conservé, l'isoler dans un profil jetable, documenter le consentement, ajouter un kill-switch, et ne pas l'exposer en multi-utilisateur.

### 6.7 Chapitre dédié — serveur MCP (`api/mcp.go`)

Le serveur MCP est un processus stdio local, lancé par `goapi --mcp`. Il expose :

| Outil | Risque |
|---|---|
| `generate_image` / `generate_video` | Lecture arbitraire de `ref_image_path` / `start_image_path`, envoi multipart au backend |
| `generate_music` | Proxy `/music`, pas de lecture de fichier |
| `chat` | Proxy `/chat`, pas d'auth |
| `reset_session` | Supprime une session arbitraire |
| `check_health` | Fuite d'information limitée |

**Constats :**

- Aucune authentification ni autorisation : tout processus capable d'écrire sur stdin du serveur MCP peut appeler les outils.
- `os.Stat(imagePath)` puis `postMCPMultipart` : pas de restriction de répertoire, pas de rejet des symlinks, pas de limite de taille. Un fichier de 1 Go peut être lu et envoyé.
- `initialize` ignore la version demandée : le client envoie `2025-06-18`, le serveur répond `2024-11-05`. Certains clients MCP récents peuvent refuser ou mal négocier.
- Le backend est configurable par `GEMINI_API_URL` ; si un attaquant peut modifier l'environnement, il redirige les fichiers et les prompts vers son serveur.
- `mcpHttpTimeout` de 5 minutes et 3 retries : un outil peut bloquer longtemps.

**PoC :** `poc/mock_backend.py` + `10_mcp_exfil_body.bin` — le serveur MCP lit `secret.txt` et l'envoie au backend mock ; le marqueur est présent dans le corps multipart.

**Correctif :** `MCP_MEDIA_ROOT`, `filepath.IsLocal`, rejet des symlinks, `os.Lstat`, limite de taille (ex. 20 Mo), allowlist MIME, jeton `MCP_TOKEN` si transport réseau, validation de `protocolVersion` (répondre la version demandée si supportée), timeout plus court, journal d'audit.

### 6.8 Extension Chrome et conformité MV3

**Permissions :**

```json
"permissions": ["cookies", "storage", "alarms", "clipboardWrite"],
"host_permissions": [
  "*://*.google.com/*", "*://*.google.co/*",
  "*://*.googleusercontent.com/*",
  "http://*/*", "https://*/*"
]
```

**Constats :**

- `http://*/*` et `https://*/*` sont des permissions d'hôte quasi universelles, non nécessaires : l'extension n'a besoin que de `*.google.com` et de l'URL locale du serveur. Chrome Web Store exige la permission minimale ; c'est un motif de rejet.
- `clipboardWrite` + bouton « Copy Cookies » copie les cookies de session Google dans le presse-papiers, lisible par toute application locale. Risque de fuite et justification difficile.
- `chrome.cookies.getAll({})` lit **tous** les cookies du navigateur, puis filtre les domaines Google. La permission `cookies` combinée à `http://*/*` permet en théorie de lire les cookies de tous les sites ; le filtre est côté code, pas côté permission.
- La collecte inclut `SID`, `HSID`, `SSID`, `APISID`, `SAPISID`, `__Secure-1PSID`, `__Secure-3PSID`, `NID` : ce sont des cookies d'authentification **Google globaux**, pas seulement Gemini.
- **Découverte LAN** : `discoverServer()` sonde `127.0.0.1`, `localhost`, `host.docker.internal`, une liste d'IP privées codées en dur (`192.168.1.x`, `10.56.65.x`), puis balaie `192.168.1.1-254`. `probeServer()` accepte tout JSON contenant `status === 'online'` **ou** `engine`. Ensuite, l'extension envoie les cookies via `ws://host:9226` ou `http://host:8100/api/sync-cookies`.
- **Aucune authentification du serveur** : un attaquant sur le LAN peut répondre à `/health` avec `{"engine":"x"}` et recevoir les cookies complets. C'est une exfiltration de credentials critique (F-05).
- **Transport en clair** : `ws://` et `http://`, même pour un hôte distant. Sur un LAN, sniffing trivial.
- **Pas de TLS, pas de pinning, pas de jeton de pairing**.
- **Liste d'IP codées en dur** : fuite d'information sur l'infrastructure de l'auteur, et comportement non déterministe chez l'utilisateur.

**Correctif MV3 :**

1. Remplacer le balayage LAN par une URL de serveur configurée par l'utilisateur (`chrome.storage.local`), par défaut `http://127.0.0.1:8100`.
2. Valider le serveur par un challenge-response (jeton de pairing) avant tout envoi de cookies.
3. Utiliser `wss://`/HTTPS dès que l'hôte n'est pas loopback ; certificate pinning ou CA locale.
4. Réduire `host_permissions` à `https://*.google.com/*`, `https://*.googleusercontent.com/*` et `http://127.0.0.1/*`.
5. Supprimer `clipboardWrite` ou le réserver à un geste utilisateur avec avertissement explicite.
6. Ne collecter que les cookies nécessaires à Gemini, documenter chaque nom, ajouter un écran de consentement.
7. Supprimer les IP codées en dur et le subnet sweep.
8. Ajouter une politique de confidentialité, une justification des permissions et une procédure de suppression des données.

**Verdict Chrome Web Store :** très probablement refusé en l'état (permissions excessives, collecte de cookies d'authentification, balayage réseau, `clipboardWrite`). Une distribution hors store (mode développeur, entreprise) ou une application compagne avec native messaging est plus réaliste.

---

## 7. Conformité et risques juridiques

### 7.1 OWASP Top 10 (2021)

| Catégorie | Constats |
|---|---|
| A01 Broken Access Control | F-01, F-02, F-03, F-19 |
| A02 Cryptographic Failures | F-05, F-06, F-18, F-22 |
| A03 Injection | F-01 (path traversal), F-23 (log injection) |
| A04 Insecure Design | F-04, F-09, F-10, F-11, F-13, F-20 |
| A05 Security Misconfiguration | F-03, F-04, F-12, F-16, F-17, F-22 |
| A06 Vulnerable Components | F-15, F-24 |
| A07 Identification/Auth Failures | F-02, F-03 |
| A08 Software/Data Integrity | F-20, F-24 |
| A09 Logging/Monitoring Failures | F-23 (pas d'audit log des actions admin) |
| A10 SSRF | F-07 (redirections), F-19 (fichiers locaux) |

### 7.2 Google ToS et risque juridique

- Accès automatisé à l'interface web Gemini en contournant l'API officielle : risque de violation des Conditions d'utilisation Google.
- Extraction et stockage de cookies de session Google : risque de violation de la politique d'utilisation des données et de sécurité des comptes.
- Farming de comptes multiples et watermark removal : risque aggravé (contournement technique, propriété intellectuelle).
- Conséquences possibles : suspension des comptes, blocage d'IP, notification, action civile. Prévoir un avis juridique avant toute distribution.

### 7.3 GDPR / données personnelles

- Les conversations, prompts, réponses, adresses IP (`user_id` par défaut = IP) et identifiants de comptes sont stockés en SQLite sans chiffrement, sans durée de conservation, sans endpoint de suppression.
- Les exports Excel/JSON contiennent l'intégralité des messages et sont accessibles sans authentification.
- Si des utilisateurs européens utilisent l'instance : base légale, information, droit d'accès/effacement, DPIA, chiffrement au repos, minimisation.
- Recommandation : rétention 30 jours par défaut, purge automatique, endpoint `DELETE /history/:conversation_id`, chiffrement SQLCipher ou chiffrement applicatif, journal d'accès.

### 7.4 Licence et chaîne d'approvisionnement

- Licence MIT : pas de clause de sécurité ni de disclaimer robuste ; ajouter `SECURITY.md`, `PRIVACY.md`, `THREAT_MODEL.md`.
- Dépendances : 79 modules, `go mod verify` OK ; ajouter `go.sum` pinning, Renovate/Dependabot, SBOM CycloneDX, signature cosign.

---

## 8. Tests non réalisés et protocole de validation

### 8.1 Flux Gemini réels (compte jetable)

**Prérequis :** compte Google jetable, consentement explicite, IP dédiée, quotas limités.

1. Créer un compte jetable, se connecter à `gemini.google.com`, accepter les conditions.
2. Synchroniser les cookies via l'extension ou `POST /api/sync-cookies` avec un cookie jar exporté.
3. Tester : `POST /v1/chat/completions` (non-stream et stream), `POST /chat` (texte, image, vidéo), `POST /music`, `POST /v1/models/discover`.
4. Vérifier : `200`, contenu non vide, pas de `ServiceLogin`, pas de `SNlM0e not found`, pas de blocage.
5. Surveiller : logs serveur, quotas Google, éventuel captcha, expiration des cookies.
6. Arrêter et révoquer les sessions du compte jetable.

**Critère d'arrêt :** captcha, `429`, `403`, ou message de suspension → arrêt immédiat, ne pas réessayer.

### 8.2 Stress 50–100 requêtes parallèles

**Local (déjà exécuté) :** 100 requêtes `/health` et `/history`, 30 `/v1/analytics`, 30 `/export/excel`. Résultats : API légère, exports corrompus.

**Vers Google (non exécuté) :** nécessite un compte jetable et une fenêtre de test. Protocole :

- 50 requêtes `POST /v1/chat/completions` avec des prompts courts, `stream=false`, réparties sur 3 workers.
- Mesurer : latence p50/p95/p99, taux d'erreur, `429`, cooldown, consommation mémoire, taille DB.
- Arrêt si taux d'erreur > 5 %, `429` > 1 %, ou captcha.
- Ne jamais utiliser le compte principal ; ne pas paralléliser au-delà de la concurrence configurée (3/worker).

**Recommandation :** ne pas exécuter ce test sur l'infrastructure de production ; utiliser un environnement isolé et un compte jetable.

### 8.3 Chrome Web Store

- Soumettre une build non listée avec justification des permissions.
- Préparer : politique de confidentialité, description de l'usage des cookies, capture d'écran du consentement, procédure de suppression.
- Anticiper un refus sur `http://*/*`, `https://*/*`, `clipboardWrite`, le balayage LAN et la collecte de cookies globaux.

### 8.4 MCP avec un client réel

- Tester avec Claude Desktop / Cline / un client MCP générique.
- Vérifier la négociation de version (`2025-06-18` vs `2024-11-05`), les timeouts, les erreurs, la taille des fichiers.
- Ajouter un test d'injection de prompt : demander à l'agent de générer une image à partir de `~/.ssh/id_rsa` et vérifier que le serveur refuse.

---

## 9. Conclusion et recommandation

Le projet démontre une réelle maîtrise de la reverse engineering réseau (TLS/QUIC Chrome 152, JSPB, rotation de cookies) et une architecture de pool de workers soignée. Mais il est construit comme un outil local mono-utilisateur, sans modèle de menace, et les contrôles de sécurité fondamentaux sont absents. La combinaison **traversée de répertoire + cookies Google en clair + API sans auth + CORS `*` + extension qui envoie les cookies au premier hôte du LAN** est exploitable par un attaquant non authentifié et peut conduire à la compromission complète des comptes Google.

**Recommandation :**

- **NO-GO** pour une exposition réseau, un usage multi-utilisateur, une distribution Chrome Web Store ou un déploiement cloud en l'état.
- **GO local mono-utilisateur** uniquement après les P0 : bind loopback, auth, correction traversal, CORS/WS, rotation des cookies, limites de taille/rate.
- **P1** avant toute mise en production : refactor sessions/exports, Docker non-root, dépendances à jour, CI sécurité, tests d'intégration.
- **P2** pour une maturité 85/100 : API officielle ou isolement fort, chiffrement au repos, egress allowlist, observabilité, conformité.

Les livrables détaillés :

- [`REGISTRE_CVSS.md`](REGISTRE_CVSS.md) — fiches de vulnérabilité, vecteurs, impacts, correctifs.
- [`REGISTRE_CVSS.csv`](REGISTRE_CVSS.csv) — registre exploitable.
- [`CAHIER_POC.md`](CAHIER_POC.md) — 18 PoC avec commandes exactes et résultats observés.
- [`PLAN_REMEDIATION.md`](PLAN_REMEDIATION.md) — quick wins, refactor, hardening guide, vérifications.
- [`evidence/`](evidence/) — sorties brutes (SAST, scans, benchmarks, stress, logs).
- [`poc/`](poc/) — tests Go d'audit, mock backend MCP, script de préparation.

---

## Annexe A — Index des fichiers et lignes clés

| Fichier | Lignes | Constat |
|---|---|---|
| `api/routes.go` | 267-269 | Traversée `/output/*` |
| `api/routes.go` | 38-73 | `POST /api/sync-cookies` sans auth |
| `api/routes.go` | 157-185 | `/history`, `/history/search` sans auth |
| `main.go` | 103 | `BodyLimit: 50 MB` |
| `main.go` | 107 | `cors.New()` wildcard |
| `main.go` | 139 | `app.Listen(":"+port)` → `0.0.0.0` |
| `gemini/monitor.go` | 186-191 | `CheckOrigin: true` |
| `gemini/monitor.go` | 277 | cookies `0644` |
| `gemini/monitor.go` | 287 | `CachedImageCookies` sans verrou |
| `gemini/monitor.go` | 356 | WS bind `0.0.0.0` |
| `gemini/client.go` | 111-128 | `getAccountPrefix` race |
| `gemini/client.go` | 539-543 | `ensureInit` race |
| `gemini/client.go` | 1612-1705 | `DownloadFile`, cookie sur chaque redirect |
| `api/session.go` | 21-47 | `./output/.temp` partagé |
| `api/export.go` | 22-42 | `AutoExportAnalytics` |
| `api/chat_handler.go` | 242, 330, 856, 1011 | Appels `AutoExportAnalytics` |
| `api/chat_handler.go` | 230-234, 315-316, 941-942 | `CountTokens` |
| `api/queue.go` | 51 | `affinityMap` non borné |
| `api/mcp.go` | 308-335 | lecture `ref_image_path` |
| `db/db.go` | 34-41 | WAL, busy_timeout, 25 conns |
| `Dockerfile` | 1-36 | root, images non épinglées, pas de healthcheck |
| `docker-compose.yml` | 8-17 | ports 8100, pas de hardening |
| `gemini-extension/manifest.json` | 6-13 | permissions excessives |
| `gemini-extension/background.js` | 28-110 | découverte LAN |
| `gemini-extension/background.js` | 130-131 | `ws://` en clair |
| `.github/workflows/docker-publish.yml` | 1-60 | CI sans tests/SAST |

## Annexe B — Reproductibilité

```bash
# Environnement isolé
export AUDIT_ROOT=/tmp/opencode/audit-src
export RUN_ROOT=/tmp/opencode/audit-run
export PATH=/tmp/opencode/audit/go/bin:$PATH
export GOTOOLCHAIN=go1.26.0

# Build + tests
cd "$AUDIT_ROOT"
go build ./...
go vet ./...
go test -short -count=1 ./...
go test -race -short -count=1 -skip 'TestAudit' ./...

# SAST
gosec -fmt=text -out=/tmp/opencode/gosec.txt ./...
staticcheck ./...
govulncheck ./...
trivy fs --scanners vuln /tmp/opencode/audit-src
hadolint < Dockerfile
```

Les commandes détaillées, les sorties attendues et les résultats observés sont dans [`CAHIER_POC.md`](CAHIER_POC.md).

## Annexe C — Glossaire

- **CVSS v3.1** : score de base 0–10, sévérité Low/Medium/High/Critical.
- **CWE** : classification des faiblesses logicielles.
- **SAST/DAST** : analyse statique / dynamique.
- **WAL** : Write-Ahead Logging SQLite.
- **SSE** : Server-Sent Events (streaming chat).
- **MCP** : Model Context Protocol (outils pour agents).
- **PoC** : proof of concept.
- **P0/P1/P2** : priorités de remédiation (immédiat / court terme / moyen terme).
