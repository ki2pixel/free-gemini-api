# AGENTS.md — Free Gemini API

High-signal instructions and operational context for AI agents working in this repository.

**Target version: v3.5.9** — corrélation benchmark ↔ télémétrie, observabilité granulaire, *LoopRoute*, masquage thinking-aware et suite de tâches étendue. Sécurité : voir
[`free-gemini-api/SECURITY.md`](free-gemini-api/SECURITY.md). Hub documentaire :
[`docs/README.md`](docs/README.md).

---

## 1. Project Overview & Architecture

Open-source, drop-in OpenAI-compatible proxy engine in pure Go unlocking Google's Gemini 4 lineup (Hydrogen, Neon, Argon, Carbon), thinking depth controls, and Agentic Task Mode (`[40]`).

### Directory & Package Boundaries
- **`free-gemini-api/`**: Main Go server root (all build/test commands run here).
  - `api/`: REST & SSE streaming handlers, OpenAI schema conversions, auth middleware, rate-limiting, SQLite metrics/analytics, MCP and queue. Contient l'état de session (`session_transcripts.go`), le coupe-circuit (`doom_loop.go`), le masquage d'observations (`observation_masker.go`) et les métriques Prometheus (`metrics.go`).
  - `gemini/`: Gemini web/client driver, cookies/session managers, output arranger (tool calls, thinking extraction, JSON repair), model registry, `schema_validator.go`, `stream_guard.go`, `thinking.go`, `crypto.go`.
  - `db/`: SQLite access layer (`sqlite` WAL, `_pragma=foreign_keys(1)` sur chaque connexion du pool), retention worker, exports. Contient `telemetry.go` : `agent_sessions` (clé = SHA-256 de la session), `agent_turns` (métriques de tour sans contenu, colonnes générées, `ON DELETE CASCADE`), `tool_call_spans` (tentatives d'appel, `roundtrip_ms` généré), `turn_anomalies` (journal catégoriel d'incidents), `benchmark_trials` (`ON DELETE SET NULL`), et les vues `v_session_telemetry_summary` (recréée par `DROP`+`CREATE` à chaque démarrage), `v_benchmark_trial_telemetry`, `v_benchmark_cost_of_pass`.
  - `data/`: Local SQLite database (`gemini.db` + WAL/SHM). Runtime only, never commit.
  - `cookies/`: Cookie jars (`account_*.json`, `session_*.json`), `0700`/`0600`. Never commit.
  - `output/`: Generated media served by `GET /output/*`. Never commit.
- **`gemini-extension/`**: Companion Chrome extension (MV3) for automated browser session & cookie syncing via WebSocket (`ws://127.0.0.1:9226`).
- **`scripts/`**: Local quality and live-test tooling (`audit_check.sh`, `live_middleware_smoke.sh`, `benchmark_matrix.sh`).
- **`docs/`**: Documentation hub. `architecture/` (living specs), `remediation/` (v3.4.8 → v3.5.7 archives), `recherches/` (theoretical papers and comparative audits).
- **`audit-2026-11/`**: Security audit reports, CVSS registry, proof-of-concept exploits, and remediation documentation.

> Les commandes Go s'exécutent **dans** `free-gemini-api/`. Les scripts s'exécutent depuis la **racine** du dépôt.

---

## 2. Key Developer Commands & Runbooks

### Build & Run
```bash
cd free-gemini-api
cp -n .env.example .env   # Configure API_TOKEN in .env if not already set
go run main.go
```

### Tests & Quality Checks

**Runner officiel (recommandé) — depuis la racine :**
```bash
./scripts/audit_check.sh
```
Trois barrières, dans cet ordre : `gofmt -l .` (échec si un fichier est mal
formaté), `go vet ./...` via Docker `golang:1.26-alpine`, puis
`go test -short -cover ./...` via le même conteneur. Toute tâche déclarée
terminée doit avoir franchi ces trois barrières.

**Suite locale (si le toolchain Go ≥ 1.26 est installé) :**
```bash
cd free-gemini-api

# Run test suite (short / race)
go test -short ./...
go test -race -short ./...

# Run a specific unit test
go test -v ./gemini -run TestArrangeGeminiOutput
go test -v ./api -run TestIsToolRefusalOutput

# Fuzzing actif (5 s par cible)
go test -short -fuzz=FuzzQuarantineUntaggedToolJSON -fuzztime=5s ./gemini
go test -short -fuzz=FuzzSanitizeThoughtArtifacts -fuzztime=5s ./gemini

# Linting & Formatting verification
test -z "$(gofmt -l .)"
go vet ./...
```
> **Note on Go Toolchain**: `go.mod` targets Go 1.26. If the local host has an earlier version (e.g. Go 1.18–1.24), run tests inside Docker:
> ```bash
> docker run --rm -v $(pwd)/free-gemini-api:/app -w /app golang:1.26-alpine go test -short ./...
> ```

**Tests de fumée live (consomme du quota Google) :**
```bash
API_TOKEN=... ./scripts/live_middleware_smoke.sh
MODEL=gemini-4-neon ./scripts/live_middleware_smoke.sh
```
Six contrôles : compteurs agentiques exposés, sanitizer de balises fermantes
orphelines, directive de récupération ground-truth, compaction des notices
d'instructions, classifieur de refus amont, Write-Serial (au plus une mutation
par tour + `tool_calls[].index` séquentiels). Requiert un worker sain et une
extension connectée. **Ne pas lancer en boucle** : chaque exécution consomme du
quota réel. Volontairement exclu de `audit_check.sh`.

**Banc de benchmark `pass^k` :**
```bash
./scripts/benchmark_matrix.sh
BENCHMARK_RUNS=3 BENCHMARK_MODELS="gemini-4-neon gemini-4-hydrogen" ./scripts/benchmark_matrix.sh
BENCHMARK_RUNS=5 BENCHMARK_K=3 ./scripts/benchmark_matrix.sh
BENCHMARK_STRICT=1 ./scripts/benchmark_matrix.sh   # exit non-zéro si un modèle échoue
./scripts/test_benchmark_matrix.sh                 # suite hors-ligne, sans quota
```
Rejoue le plan v3.5.7 ou une suite de tâches via OpenCode pour chaque modèle,
`k` fois, dans des git worktrees isolés créés depuis un instantané du working
tree courant. Détecte les tool calls, greetings, faux quotas (`0 remaining`),
claims S7, prose courte inachevée et `finish_reason`. Écrit `results.tsv`,
`summary.txt` et les transcripts dans `debug/benchmark_matrix_<ts>/`. Le working
tree principal n'est **jamais** réinitialisé ni nettoyé. Prérequis : `opencode`,
`python3`, `jq` (facultatif), proxy démarré et authentifié.

La suite embarquée `scripts/benchmark_tasks/` contient six tâches stratifiées
(`2 S1`, `2 S2`, `2 S3`). `S2_long_horizon_chain` force la lecture de dix nœuds
verbeux avant la correction de deux nœuds non contigus, ce qui déclenche
réellement `MaskOldToolObservationsForTier`. `scripts/benchmark_report.py`
(`--tsv`, `--db`, `--suite`) produit un rapport lecture seule : taux brut vs
conditionnel hors couche A, différence appariée `AB/BA` par `pair_id` avec
erreur-type par grappe, Cost-of-Pass, `roundtrip_ms` et `SUM(masked_obs_bytes_saved)`.

Le résumé rapporte les estimateurs combinatoires sans biais de Yao et al. —
`pass^1 = c/n`, `pass^k = C(c,k)/C(n,k)`, `pass@k = 1 - C(n-c,k)/C(n,k)` — et non
la formule naïve `(c/n)^k`, fortement biaisée à petit `n`. Réglages :

| Variable | Défaut | Effet |
|---|---|---|
| `BENCHMARK_K` | `BENCHMARK_RUNS` | `k` des estimateurs ; doit vérifier `1 <= k <= RUNS`. |
| `BENCHMARK_MIN_PASS_K` | `1.0` | Seuil de `pass^k` pour un verdict `PASS` et pour que `BENCHMARK_STRICT=1` sorte en 0. |
| `BENCHMARK_ALLOW_TASK_MODE_FOR_TOOLS` | `auto` | Envoie `X-Allow-Task-Mode-For-Tools: true` pour tout modèle dont le nom canonique finit par `-task`. Sans cet en-tête, le proxy requalifie la requête porteuse d'outils de `[40]` vers `[0]` et le bras Task mesure en réalité le modèle Chat (`api/chat_service.go`). |
| `BENCHMARK_STANDALONE` | `auto` | Ajoute `--standalone` exactement quand un overlay est injecté : `OPENCODE_CONFIG_CONTENT` n'atteint que le serveur propriétaire de la config. |
| `BENCHMARK_PROVIDER_ID` | `free-gemini` | Clé provider ciblée quand la référence modèle n'a pas de préfixe `provider/`. |
| `BENCHMARK_SUITE_ID` | `legacy-v357` | Nom de suite enregistré dans `benchmark_trials` (ou nom du dossier `BENCHMARK_TASKS_DIR`). |
| `BENCHMARK_TASKS_DIR` | vide | Répertoire de tâches `<task>/prompt.md` (+ `meta.env`, `setup.sh`, `verify.sh`). Vide = mode mono-plan historique. |
| `BENCHMARK_TASK_FILTER` | vide | Expression `grep -E` appliquée au `TASK_ID` déclaré ou au nom du dossier sous `BENCHMARK_TASKS_DIR`. Exige `BENCHMARK_TASKS_DIR` ; sans correspondance ou expression invalide, le harness échoue avant le premier tir. |
| `BENCHMARK_VERIFY_CMD` | vide | Oracle déterministe exécuté dans le worktree quand la tâche n'a pas de `verify.sh`. |
| `BENCHMARK_ORACLE_MODE` | `auto` | `auto|hybrid|verify_only|transcript_only`. `auto` = hybride dès qu'un oracle existe. |
| `BENCHMARK_PAIRING` | `sequential` | `abba` apparie Chat et Task sur la même tâche, en alternant l'ordre par essai. |
| `BENCHMARK_PAIR_COOLDOWN_SEC` | `10` | Attente entre les deux bras d'une paire. |
| `BENCHMARK_EMIT_TRIAL_ID` | `true` | Envoie `X-Benchmark-Trial-ID`. L'overlay devenant non vide, `BENCHMARK_STANDALONE=auto` ajoute alors `--standalone` à chaque run (nécessaire pour que l'en-tête atteigne le proxy). |
| `BENCHMARK_RECORD_TRIALS_URL` | `<proxy>/v1/benchmark/trials` | Endpoint de verdict ; `none` désactive. |
| `API_TOKEN` | — | Jeton Bearer pour le verdict et `/reset`. |

### Docker Deployment
```bash
cd free-gemini-api
docker compose up -d --build
curl -s http://127.0.0.1:8100/health
```

---

### Matrice des tests de non-régression v3.5.7

Exécution ciblée depuis `free-gemini-api/` :

| Domaine | Test | Commande |
|---|---|---|
| Continuité + ancre | `TestPlanSessionTurn_IdentityAndContinuationAnchor` | `go test -v ./api -run TestPlanSessionTurn_IdentityAndContinuationAnchor` |
| Raisons de replay | `TestPlanSessionTurn_ReplayReasons` | `go test -v ./api -run TestPlanSessionTurn_ReplayReasons` |
| Greeting Neon | `TestIsConversationalReset_NeonGreeting` | `go test -v ./api -run TestIsConversationalReset_NeonGreeting` |
| Fuite JSON Hydrogen | `TestQuarantineUntaggedToolJSON_HydrogenLeak` | `go test -v ./api -run TestQuarantineUntaggedToolJSON_HydrogenLeak` |
| S7 non étayé | `TestSessionLedger_UnbackedCompletion_HydrogenS7` | `go test -v ./api -run TestSessionLedger_UnbackedCompletion_HydrogenS7` |
| Preuve de validation S7 | `TestUnbackedCompletion_ValidationEvidence` | `go test -v ./api -run TestUnbackedCompletion_ValidationEvidence` |
| Prose Argon | `TestIsIncompleteFinalProse_ArgonProse` | `go test -v ./api -run TestIsIncompleteFinalProse_ArgonProse` |
| Routage Task → Chat | `TestTaskModeRouting_ToolBearingRequest` | `go test -v ./api -run TestTaskModeRouting` |
| Métriques par classe | `TestMetrics_ClassificationAndReplayExposition` | `go test -v ./api -run TestMetrics_ClassificationAndReplayExposition` |
| Budget LRU | `TestSessionLRU_GlobalMemoryBudgetEviction` | `go test -v ./api -run TestSessionLRU_GlobalMemoryBudgetEviction` |
| Budget configurable | `TestConfigureSessionMemoryBudget` | `go test -v ./api -run TestConfigureSessionMemoryBudget` |
| `finish_reason` terminal | `TestTerminalFinishReason_ErrorOnUnusableOutput` | `go test -v ./api -run TestTerminalFinishReason_ErrorOnUnusableOutput` |
| Chunk d'erreur SSE | `TestNewStreamErrorChunk_FinishReasonError` | `go test -v ./api -run TestNewStreamErrorChunk_FinishReasonError` |
| Absence de *Pink Elephant* | `TestPromptHasNoPinkElephant` | `go test -v ./gemini -run TestPromptHasNoPinkElephant` |
| Sanitizer `<thought` | `TestSanitizeThoughtArtifacts` | `go test -v ./gemini -run TestSanitizeThoughtArtifacts` |
| Artefact Argon exact | `TestArgonSanitizedThoughtArtifact` | `go test -v ./gemini -run TestArgonSanitizedThoughtArtifact` |
| Fenced citations préservées | `TestArrangeGeminiOutput_PreservesFencedThoughtCitations` | `go test -v ./gemini -run TestArrangeGeminiOutput_PreservesFencedThoughtCitations` |
| Propriété P1 (mutation tronquée) | `TestProperty_MidValueMutationRejection` | `go test -v ./api -run TestProperty_MidValueMutationRejection` |
| Propriété P2 (quarantaine) | `TestProperty_QuarantineCompleteness` | `go test -v ./api -run TestProperty_QuarantineCompleteness` |
| Propriété P3 (identité delta) | `TestProperty_DeltaIdentitySafety` | `go test -v ./api -run TestProperty_DeltaIdentitySafety` |
| Propriété P4 (terminaison) | `TestProperty_MonotonicRetryTermination` | `go test -v ./api -run TestProperty_MonotonicRetryTermination` |
| Scope cookies | `TestLoadCookiesKeepsSubdomainCookiesScoped` | `go test -v ./gemini -run TestLoadCookiesKeepsSubdomainCookiesScoped` |
| Jar par hôte | `TestCookieJarScopesByRequestHost` | `go test -v ./gemini -run TestCookieJarScopesByRequestHost` |
| Host-only vs Domain | `TestCookieJar_HostOnlyVsDomainAttribute` | `go test -v ./gemini -run TestCookieJar_HostOnlyVsDomainAttribute` |
| Stall Guard | `TestStallGuardReaderRejectsSilentStream` | `go test -v ./gemini -run TestStallGuardReader` |

Lancer l'ensemble des propriétés et du sanitizer d'un coup :
```bash
cd free-gemini-api
go test -v ./api -run "TestProperty_|TestArgonSanitized|TestPseudoReportLexicon_P2|TestUnbackedCompletion_"
go test -v ./gemini -run "TestSanitizeThought"
```

---

## 3. Tech Stack & Invariants v3.5.7 (post-benchmark)

- **Web Framework**: Fiber v3 (`github.com/gofiber/fiber/v3`) with HTTP/3 & QUIC support (`github.com/quic-go/quic-go`).
- **TLS Fingerprinting**: Bogdan Finn's `tls-client` and `fhttp` simulating Chrome 152 JA4 fingerprints.
- **Database & Metrics**: Pure Go SQLite (`modernc.org/sqlite`) in WAL mode tracking tokens, latency, cost savings, with dual export to JSON and Excel (`github.com/xuri/excelize/v2`).
- **Streaming & Arranger**: Real-time SSE parser separating live `<thought>` tags and auto-repairing unescaped control characters in JSON `<tool_call>` arguments.

### Invariants P0 / P1 / P2

```text
I1  delta ⇒ identité worker + conversation + préfixe exact
I2  tools ∧ ¬tool_call ∧ greeting ⇒ retry, jamais stop
I3  CleanText ne contient aucun JSON d'outil déclaré
I4  claim(path) ⇒ path ∈ mutations confirmées par le tool result
I5  finish_reason = stop ⇒ tool_call ∨ réponse finale validée
I6  prompt système sans "0 remaining" / "maximum number of tool" / "search calls"
I7  tout retry est couvert par un budget plafonné et monotone
I8  mémoire des sessions bornée (256 sessions, 64 Mio cumulés)
```

**Cookie jar RFC 6265.** `tls_client.NewCookieJar()` est volontairement évité :
il regroupe par eTLD+1 et déduplique sur le nom seul. Le client utilise
`fhttp/cookiejar` avec `golang.org/x/net/publicsuffix`. `loadCookies` enregistre
chaque cookie contre **l'hôte unique qui le possède** ; un cookie host-only est
stocké **sans** attribut `Domain`. `rawCookies` est rendu depuis le jar pour
`gemini.google.com`. L'en-tête `Cookie` est résolu **en direct** à chaque
requête, jamais depuis un instantané de chargement.

**Stall Guard 45 s.** `stallGuardReader` (`gemini/stream_guard.go`) arme un
minuteur de `upstreamStreamStallTimeout = 45s`, réarmé à chaque lecture
produisant des octets. Sans octet pendant 45 s, il annule le contexte et ferme
le corps, ce qui débloque un `Read` en attente avec
`ErrUpstreamStreamStalled`. Indispensable : un flux amont bloqué garde la
connexion ouverte sans émettre un octet, donc un timeout de transport classique
ne se déclenche jamais (cause du gel de 14 min en v3.5.0).

**Validation Draft 2020-12.** Les appels explicites sont validés contre le
schéma de `req.Tools`. Les schémas compactés filtrent `required` contre
`properties` (A-01) et synthétisent `items: {type: "string"}` pour les tableaux
sans `items` (A-02). Les signatures `STRICT PARAMETERS` sont calculées sur le
schéma effectif.

**Quarantaine des mutations non taggées.** Un objet JSON nu n'est promu en
`tool_call` que s'il est complet, schéma-valide et **non mutateur**. Toute
mutation non taggée est retirée de `CleanText`, marquée `UntaggedMutation` et
rejouée. `CleanText` ne contient jamais un JSON d'outil déclaré.

**Continuité de session stricte.** Un delta exige
`curConvID == lastConvID`, le même worker et un préfixe de transcript exact.
Sinon : replay complet sur conversation vierge. Un delta sans instruction
utilisateur reçoit l'ancre éphémère `[Continuation]`, jamais stockée dans
`lastFlat`.

**Salutations et prose inachevée.** `isConversationalReset` classe les greetings
comme inutilisables sur une requête avec outils. `isIncompleteFinalProse`
intercepte les fragments de 10 à 80 runes sans ponctuation finale.

**`finish_reason` véridique.** Un tour inutilisable épuisé émet
`finish_reason: "error"` via `newStreamErrorChunk`, jamais `"stop"`. OpenCode
2.4.0 mappe `"error"` vers une `AIError` et `"stop"` vers un succès : rapporter
`"stop"` faisait passer l'hôte en `idle` sans retry.

**Hygiène cognitive.** Les littéraux `0 remaining`, `maximum number of tool` et
`search calls` sont absents du prompt système et des directives de replay. Ils
restent uniquement dans les classifieurs de sortie.

**Routage produit Task → Chat.** Une requête déclarant des outils est
requalifiée de `[40]` vers `[0]` (`hydrogen-task` → `hydrogen`,
`neon-task` → `neon`), sauf opt-in `X-Allow-Task-Mode-For-Tools: true` ou
`ALLOW_TASK_MODE_FOR_TOOLS=true`.

**Budget mémoire LRU.** 256 sessions maximum, 64 Mio cumulés de `lastFlat`
(`SESSION_MEMORY_BUDGET_MB`). L'éviction dégrade toujours vers un replay
complet, jamais vers un delta orphelin.

**Budgets de retry (terminaison garantie).**
```text
maxToolChatAttempts      = 4
maxEmptyGenerationRetries = 2
maxTransportRetries       = 2
maxChatLoopIterations     = 8
```

**LoopRoute (coupe-circuit sur erreur d'outil).** `DoomLoopTracker` reçoit le
résultat de l'appel précédent (`RecordToolOutcome(callID, isError)`, scan à
rebours de l'historique borné). Un appel identique dont les occurrences ont
échoué avertit dès **2** répétitions et coupe à **4** échecs identiques
consécutifs ; sans résultat connu, les seuils historiques **3 / 5** restent
inchangés. Un nouveau tour utilisateur (`Reset`) efface cette preuve.

**Masquage thinking/error-aware.** `MaskOldToolObservationsForTier` porte la
fenêtre à **8** pour les paliers `medium`/`high` (4 sinon, ou
`OBSERVATION_MASK_THINKING_KEEP_RECENT`) et préserve les **2** erreurs d'outils
les plus récentes hors fenêtre, tant que le prompt projeté reste sous
`maxPromptBytes`. Les trois entrées historiques (`MaskOldToolObservations`,
`...WithOverhead`, `...WithStats`) gardent leur comportement exact.

**Métriques Prometheus.** `gemini_turn_classification_total{class}`,
`gemini_delta_replay_total{reason}`, `gemini_quarantine_total{kind}`,
`gemini_upstream_suspected_stop_total{model}`, plus
`gemini_arranger_repairs_total{kind}`, `gemini_doom_loop_interventions_total` et
`gemini_ground_truth_directives_total`. Les tokens de complétion sont exposés en
deux familles **disjointes**, étiquetées par palier de réflexion
(`none|low|medium|high`) : `gemini_thought_tokens_total{tier}` et
`gemini_answer_tokens_total{tier}` (cette dernière incluant les appels d'outils,
donc non nulle pour un tour purement agentique).

---

## 4. Security & Operational Gotchas

- **Authentication (P0 Hardening)**: Every route except `GET /health` strictly requires authentication:
  - Header: `Authorization: Bearer $API_TOKEN` or `X-API-Token: $API_TOKEN`.
  - Comparaison à temps constant. `API_TOKEN` vide ⇒ `401` partout, jamais d'ouverture silencieuse.
- **Admin hierarchy**:
  - `ADMIN_TOKEN` non vide ⇒ il est le **seul** jeton accepté sur `POST /v1/models`, `POST /v1/models/discover` et `POST /api/sync-cookies`.
  - `ADMIN_TOKEN` vide ⇒ repli sur `API_TOKEN`.
  - Les pré-vols `OPTIONS` sont exemptés. `PUBLIC_HELP=true` rend `GET /` et `GET /help` publics.
- **Cookie encryption at rest**:
  - `COOKIE_ENCRYPTION_KEY` active AES-256-GCM sur `cookies/account_*.json` et les caches de session.
  - 64 caractères hexadécimaux = clé directe ; toute autre valeur passe par PBKDF2-HMAC-SHA256 (100 000 itérations).
  - `REQUIRE_COOKIE_ENCRYPTION=true` transforme l'absence de clé en erreur fatale.
  - Générer : `openssl rand -hex 32`.
- **Critical environment variables**:
  - `ALLOW_TASK_MODE_FOR_TOOLS` (défaut `false`) — conserve Task `[40]` sur requêtes avec outils.
  - `SESSION_MEMORY_BUDGET_MB` (défaut `64`) — budget cumulé des transcripts.
  - `PUBLIC_METRICS` (défaut `false`) — scrape `/metrics` sans jeton, **loopback uniquement**.
  - `RETENTION_DAYS` (défaut `30`, `0` désactive) — purge RGPD.
  - `RATE_LIMIT_MAX` / `RATE_LIMIT_WINDOW_SECONDS` (défaut `600` / `60`).
  - `WS_TOKEN`, `MCP_TOKEN` — repli sur `API_TOKEN` s'ils sont vides.
- **Default Ports**:
  - REST & SSE API: `http://127.0.0.1:8100`
  - Cookie Bridge WebSocket: `ws://127.0.0.1:9226`
- **Supported Model Aliases**:
  - `gemini-4-hydrogen` : Fast, lightweight, default free tier.
  - `gemini-4-neon` : Pro tier, reasoning frontier model.
  - `gemini-4-hydrogen-task` / `[40]` : Dedicated Agentic Task mode.
  - Thinking depth tiers : `neon-high`, `neon-medium`, `neon-low`.
- **Never commit**: `cookies/`, `data/`, `output/`, `.env`, `*.db*`.
- **Never** edit `.commandcode/taste/` or `~/.commandcode/taste/` if present.

### API routes — source de vérité

`free-gemini-api/api/routes.go` est la référence. Les routes fréquemment
oubliées dans la documentation :

| Method | Endpoint | Auth |
|---|---|---|
| `GET` | `/metrics` | API/ADMIN (loopback si `PUBLIC_METRICS=true`) |
| `GET` | `/api/pool` | API — alias de `/v1/workers` |
| `POST` | `/api/sync-cookies` | **Admin** |
| `POST` | `/v1/models` | **Admin** |
| `POST` | `/v1/models/discover` | **Admin** |
| `POST` | `/reset` | API |
| `GET` | `/status` | API |
| `GET` | `/history` | API |
| `DELETE` | `/history/:conversation_id` | API — RGPD art. 17, idempotent |
| `GET` | `/history/search` | API |
| `GET` | `/media` | API |
| `GET` | `/export/json` | API — 5 req/min |
| `POST` | `/chat`, `/music` | API |
| `POST` | `/v1/benchmark/trials` | API — verdict d'oracle, corrèle `trial_id` avec `agent_turns` |
| `GET` | `/v1/benchmark/trials` | API — `?suite_id=&task_id=&limit=`, renvoie les essais et le Cost-of-Pass |
| `GET` | `/v1/telemetry/sessions` | API — `?session_id=&trial_id=&limit=` ; un `trial_id` inconnu renvoie une liste vide, jamais toutes les sessions |
| `GET` | `/output/*` | API — confinement `safeOutputPath` |

---

## 5. Documentation Map

| Document | Usage |
|---|---|
| [`docs/README.md`](docs/README.md) | Hub : parcours par profil + matrice v3.4.8 → v3.5.7 |
| [`docs/architecture/pipeline_flow.md`](docs/architecture/pipeline_flow.md) | Cycle de requête complet, étape par étape |
| [`docs/architecture/cookie_scoping_rfc6265.md`](docs/architecture/cookie_scoping_rfc6265.md) | Jar, host-only, sous-domaines, chiffrement |
| [`docs/architecture/agentic_resilience.md`](docs/architecture/agentic_resilience.md) | Schémas, quarantaine, ledger, budgets, preuves |
| [`docs/remediation/README.md`](docs/remediation/README.md) | Index chronologique des remédiations |
| [`docs/recherches/README.md`](docs/recherches/README.md) | Arbitrages et papiers théoriques |
| [`CHANGELOG.md`](CHANGELOG.md) | Vérité sémantique par version |
| [`free-gemini-api/SECURITY.md`](free-gemini-api/SECURITY.md) | Rotation, chiffrement, pipeline qualité |
| [`free-gemini-api/THREAT_MODEL.md`](free-gemini-api/THREAT_MODEL.md) | STRIDE, T-01 → T-18 |
| [`free-gemini-api/PRIVACY.md`](free-gemini-api/PRIVACY.md) | RGPD, rétention, effacement |

---

## 6. Working Rules for Agents

1. **Do not commit.** Leave changes in the working tree unless explicitly asked.
2. **Run `./scripts/audit_check.sh`** before declaring a task complete.
3. **Never claim** a test, build or audit passed without executing it — this is
   exactly the rule the proxy's `SessionLedger` enforces on models.
4. **Never write secrets** (`API_TOKEN`, `ADMIN_TOKEN`, cookies,
   `COOKIE_ENCRYPTION_KEY`) into a tracked file.
5. **Preserve the invariants.** Any change to `arranger.go`, `session_transcripts.go`,
   `chat_handler.go` or the cookie path must keep I1–I8 true and the relevant
   regression tests green.
6. **Cite the source.** Documentation claims must be verifiable in
   `free-gemini-api/`. Do not invent routes, env vars, metrics or test names.
