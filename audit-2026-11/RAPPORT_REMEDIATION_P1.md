# Rapport de remédiation — P1 (A-16 → A-23)

**Date :** 21 novembre 2026  
**Périmètre :** `free-gemini-api/` et workflows CI/CD  
**Référence :** `audit-2026-11/PLAN_REMEDIATION.md` §3.1–3.7

## 1. Statut des actions P1

| Action | Description | Statut | Preuves principales |
|---|---|---|---|
| A-16 | Exports atomiques + sérialisés | ✅ | `api/export.go` : `AnalyticsService`, worker coalescent (canal 1, debounce 5 s), écritures atomiques, endpoints en mémoire, limiteur 5/min |
| A-17 | Caches LRU/TTL (`UserSessions`, `affinityMap`) | ✅ | `api/cache.go`, `api/session.go`, `api/queue.go` : LRU génériques bornés (1024/4096) avec TTL 30 min et hook d'éviction |
| A-18 | Refactor `client.go` / `chat_handler.go` | ✅ | `gemini/{client,transport,session,chat,media}.go` ; `api/chat_service.go`, `api/history_service.go` ; handlers allégés |
| A-19 | Tests d'intégration + fuzz + couverture 60 % | ✅ | `api` 62,8 %, `gemini` 67,7 %, `db` 78,6 % ; 8 cibles de fuzz |
| A-20 | CI : tests, SAST, scans, actions par SHA | ✅ | `.github/workflows/ci.yml`, `.github/workflows/docker-publish.yml` |
| A-21 | Cookie forwarding same-domain + egress allowlist | ✅ | `gemini/media.go` : `isAllowedDownloadHost`, `sameCookieDomain`, `c.rawCookies()`, limite 20 Mio |
| A-22 | MCP : racine média, symlinks, taille, audit | ✅ | `api/mcp.go` : `safeMCPPath`, `validateMCPMediaType`, négociation de version, audit SHA-256, timeouts/retries |
| A-23 | Rétention GDPR + endpoint de suppression | ✅ | `db/db.go` : `PurgeExpiredData`, `DeleteConversation`, worker 24 h ; `DELETE /history/:conversation_id` ; `PRIVACY.md` |

## 2. Changements structurants

### 2.1 Caches bornés

- `LRUCache[K,V]` natif (aucune dépendance externe) avec TTL, janitor et
  callback d'éviction.
- `UserSessions` : 1024 sessions, TTL 30 min ; l'éviction ferme le client Gemini.
- Affinité worker : 4096 entrées, TTL 30 min, initialisation paresseuse
  (`affinityOnce`) pour rester compatible avec les pools construits en test.
- Identifiants de session/utilisateur validés par
  `^[A-Za-z0-9_.-]{1,64}$` ; les valeurs invalides sont hachées en SHA-256
  tronqué, avec repli sur l'IP puis `anonymous`.

### 2.2 Découpage de `gemini/client.go`

| Fichier | Responsabilité |
|---|---|
| `client.go` | type client, construction, cookies bruts, helpers |
| `transport.go` | TLS/QUIC, profils Chrome, headers, fallback HTTP/2 |
| `session.go` | `InitSession`, `ReloadSession`, `ensureInit`, `Close` |
| `chat.go` | envoi de prompts, streaming, parsing JSPB, RPC |
| `media.go` | upload, download, redirections, MIME, allowlist |

`secureReqID()` utilise désormais `crypto/rand` (suppression du G404).

### 2.3 Services API

- `ChatService` : décodage unifié JSON/multipart, résolution de modèle,
  identifiant de session, annulation, timeout de stream.
- `HistoryService` : 3 workers, file de 4096, abandon contrôlé si saturation,
  `FlushAndClose` au shutdown. Plus aucune goroutine DB par requête.
- `AnalyticsService` : génération/export JSON et Excel, worker coalescent,
  écriture atomique, limiteur dédié aux exports.

### 2.4 MCP durci

- Racine `MCP_MEDIA_ROOT` (défaut `./output`), rejet des chemins absolus, de la
  traversée, des symlinks sortants et des fichiers > 20 Mio.
- Validation MIME des images de référence.
- Négociation explicite de version (`2024-11-05`, `2025-03-26`, `2025-06-18`).
- `MCP_TOKEN` prioritaire sur `API_TOKEN`, timeouts 60 s, 2 retries.
- Journal d'audit sans secret (identifiants hachés SHA-256 tronqué).

### 2.5 GDPR

- `RETENTION_DAYS` (défaut 30, `0` désactive) ; purge au démarrage puis toutes
  les 24 h des messages, métadonnées de médias et journaux de requêtes.
- `DELETE /history/:conversation_id` authentifié, idempotent, journalisé.
- Migration `media_generations.conversation_id` avec index créé après l'`ALTER
  TABLE` (compatibilité avec les bases existantes).
- Documentation : `PRIVACY.md`, `SECURITY.md`, `THREAT_MODEL.md`.

## 3. Vérifications exécutées

Environnement : Go 1.27.4 (`/tmp/opencode/audit/go/bin`), outils
`gosec`, `staticcheck` 2026.2.2, `govulncheck` 1.10.0.

```bash
cd free-gemini-api
export PATH=/tmp/opencode/audit/go/bin:/tmp/opencode/audit/bin:$PATH

gofmt -l .                 # aucun fichier
go build ./...             # OK
go vet ./...               # OK
go test -race -short -cover ./...
# goapi/api    63,1 %
# goapi/db     78,6 %
# goapi/gemini 67,7 %

gosec -quiet -severity high ./...   # exit 0 (aucun HIGH)
staticcheck ./...                    # exit 0
govulncheck ./...                    # 0 vulnérabilité (code, paquets et modules)
```

La dépendance indirecte `golang.org/x/crypto` a été mise à jour de v0.58.0 vers
v0.59.0 (GO-2026-5932), avec `x/sys` et `x/text` alignés par `go mod tidy`.

Fuzzers exécutés 5 s chacun, sans crash :

- `FuzzFlattenContent`, `FuzzSafeOutputPath`, `FuzzSafeMCPPath`,
  `FuzzSanitizeSessionID` (`./api`) ;
- `FuzzParseResponse`, `FuzzNextStreamDelta`, `FuzzBuildRequestStruct`,
  `FuzzValidateModelConfig` (`./gemini`).

Tests de concurrence exécutés sous `-race` : `TestConcurrentExports` (10
exports Excel + 10 exports JSON en parallèle), `TestWorkerPoolAffinityAndStreaming`,
`TestLRUCacheConcurrentAccess` et les tests de course de `gemini/race_test.go`.

## 4. CI/CD

`ci.yml` :

- `gofmt`, `go vet`, `go test -race -short -cover` ;
- `gosec` limité à `HIGH` (les findings MEDIUM/LOW restent visibles en local) ;
- `staticcheck`, `govulncheck` ;
- `trivy fs` (CRITICAL/HIGH, `ignore-unfixed`) ;
- `hadolint` sur le Dockerfile ;
- actions épinglées par SHA, `permissions: contents: read`.

`docker-publish.yml` :

- build multi-arch avec `sbom: true` et `provenance: mode=max` ;
- scan `trivy image` sur le digest publié ;
- signature keyless `cosign` ;
- attestation de provenance `actions/attest-build-provenance` ;
- permissions minimales (`packages: write`, `id-token: write`,
  `attestations: write`).

## 5. Risques résiduels (P2)

- Chiffrement des cookies au repos (A-24).
- RBAC multi-utilisateur (A-25).
- mTLS ou native messaging pour l'extension (A-26).
- Observabilité OTel, métriques et alertes (A-27).
- SBOM/cosign/provenance déjà amorcés côté image ; généralisation supply chain
  (A-28).
- Threat model formel et pentest externe (A-30) ; `THREAT_MODEL.md` fournit la
  base interne.
