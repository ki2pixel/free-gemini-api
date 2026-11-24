# Modèle de menaces — Free Gemini API

**Version :** P2 / v3.5.7, novembre 2026
**Périmètre :** serveur Go (`free-gemini-api/`), extension Chrome
(`gemini-extension/`) et déploiement Docker local.
**Évolution depuis P1 :** chiffrement des cookies reclassé en risque **mitigé**
(A-24), ajout des menaces T-15 à T-18 issues des vagues P0/P1/P2 de 2026-11.
**Posture :** GO local mono-utilisateur (74,25/100) ; NO-GO exposition publique
ou multi-tenant.

## 1. Actifs protégés

| Actif | Sensibilité | Emplacement |
|---|---|---|
| Cookies de session Google | Critique | `cookies/*.json`, mémoire |
| Jetons d'API (`API_TOKEN`, `ADMIN_TOKEN`, `WS_TOKEN`, `MCP_TOKEN`) | Critique | `.env`, mémoire |
| Prompts et réponses | Élevée | SQLite, journaux, exports |
| Métadonnées de médias | Moyenne | SQLite, `output/` |
| Journaux de requêtes (IP, latence) | Moyenne | SQLite |
| Binaires et dépendances | Élevée | image Docker, `go.sum` |

## 2. Acteurs et frontières de confiance

| Acteur | Niveau de confiance | Frontière |
|---|---|---|
| Utilisateur local / client OpenAI | Authentifié par jeton | HTTP loopback |
| Extension Chrome | Authentifiée par jeton WS | WebSocket loopback |
| Processus MCP (stdio) | Local, entrée non fiable | stdin/stdout |
| Google Gemini | Tiers de confiance pour l'inférence | HTTPS sortant |
| Réseau local / Internet | Non fiable | Reverse proxy éventuel |

Le serveur écoute par défaut sur `127.0.0.1`. Toute exposition réseau passe par
un reverse proxy qui doit terminer TLS et transmettre le jeton.

## 3. Menaces et contrôles

| # | Menace | Impact | Contrôles P0/P1/P2 |
|---|---|---|---|
| T-01 | Lecture de fichiers arbitraires via `/output/*` | Critique | `safeOutputPath`, rejet de `..`, chemins absolus et symlinks sortants |
| T-02 | Exfiltration des cookies Google | Critique | permissions `0700/0600`, validation de domaine, pas de commit, rotation documentée |
| T-03 | Appels non authentifiés | Élevée | jeton obligatoire sauf `/health`, comparaison à temps constant |
| T-04 | Abus de ressources (bombes JSON, tokens, exports) | Moyenne | body limit, `maxPromptBytes`, tokenizer borné, rate limiting, exports 5/min |
| T-05 | Injection de commandes via MCP | Élevée | racine `MCP_MEDIA_ROOT`, rejet des chemins absolus/traversée/symlinks, taille max 20 Mio, MIME validé |
| T-06 | Redirection malveillante lors d'un téléchargement | Élevée | allowlist d'hôtes HTTPS, cookie forwarding same-domain, limite 20 Mio |
| T-07 | Vol de session WebSocket | Élevée | `CheckOrigin`, jeton, bind loopback, limite de message |
| T-08 | Injection SQL | Élevée | requêtes paramétrées ; les rares concaténations sont des identifiants internes constants |
| T-09 | Fuite de secrets dans les journaux | Moyenne | pas de cookie/prompt complet dans les logs d'audit, identifiants hachés SHA-256 |
| T-10 | Dépendance compromise | Élevée | `go mod verify`, `govulncheck`, `gosec`, `staticcheck`, `trivy fs` via runner local |
| T-11 | Image Docker compromise | Élevée | non-root, rootfs read-only, `cap_drop: ALL`, scan Trivy image, SBOM et signature cosign |
| T-12 | Conservation excessive de données | Moyenne | purge `RETENTION_DAYS` (30 j), `DELETE /history/:id`, `PRIVACY.md` |
| T-13 | Déni de service par goroutines/DB non bornées | Moyenne | caches LRU/TTL, `HistoryService` borné (3 workers, file 4096), drops comptabilisés |
| T-14 | Fixation de session / identifiants forgés | Moyenne | validation `^[A-Za-z0-9_.-]{1,64}$`, hachage SHA-256 des valeurs invalides |
| **T-15** | **Fuite inter-sous-domaines de cookies** — un cookie `OSID` de `console.cloud.google.com` ou `myaccount.google.com` écrase silencieusement la valeur Gemini | **Critique** | **Jar RFC 6265 + `publicsuffix` ; un seul hôte propriétaire par cookie ; host-only sans attribut `Domain` ; en-tête résolu en direct ; tests `TestLoadCookiesKeepsSubdomainCookiesScoped`, `TestCookieJarScopesByRequestHost`, `TestCookieJar_HostOnlyVsDomainAttribute`** |
| **T-16** | **Déni de service par blocage de flux** — l'amont garde la connexion ouverte sans émettre un octet, donc aucun timeout de transport ne se déclenche (gel de 14 min en v3.5.0) | **Élevée** | **Stall Guard 45 s (`stallGuardReader`, `upstreamStreamStallTimeout`) ; `ErrUpstreamStreamStalled` classé erreur de transport ; budget `maxTransportRetries=2` distinct du budget fonctionnel** |
| **T-17** | **Injection de schémas malformés** — un schéma d'outil compacté avec `required` incohérent ou un tableau sans `items` fait échouer ou vider la validation | **Élevée** | **Validation Draft 2020-12 sur le schéma effectif ; filtrage `required` contre `properties` (A-01) ; synthèse `items: {type: "string"}` (A-02) ; signatures `STRICT PARAMETERS` calculées sur le schéma compacté** |
| **T-18** | **Emballement de boucles d'agents et défaillances de session** — boucle d'outils infinie, rapport de complétion non étayé, JSON de mutation fuité ou tronqué, prose interrompue acceptée comme succès | **Élevée** | **`DoomLoopTracker` (cycles A→B, A→B→C) ; quarantaine des mutations non taggées (`CleanText` sans JSON d'outil) ; `SessionLedger` anti-S7 ; `isConversationalReset`, `isIncompleteFinalProse` ; `finish_reason: "error"` véridique ; budgets plafonnés `4+2+2=8` ; budget LRU 256 sessions / 64 Mio** |

## 4. Risques résiduels

| Risque | Sévérité | Justification |
|---|---|---|
| Cookies non chiffrés au repos | **Faible — mitigé si clé définie** | **Reclassé en P2 / v3.5.7.** Avec `COOKIE_ENCRYPTION_KEY`, les fichiers sont chiffrés en AES-256-GCM (clé de 32 octets directe ou PBKDF2-HMAC-SHA256 100 000 itérations), et `REQUIRE_COOKIE_ENCRYPTION=true` refuse le démarrage sans clé. Le risque résiduel ne subsiste que si l'opérateur laisse la clé vide ; il est alors borné par les permissions `0700`/`0600` et le caractère local du disque. |
| Jeton partagé sans RBAC | Moyenne | déploiement mono-utilisateur ; `ADMIN_TOKEN` sépare les trois mutations administratives (`/v1/models`, `/v1/models/discover`, `/api/sync-cookies`) |
| Absence de mTLS extension ↔ serveur | Moyenne | loopback uniquement ; mTLS/native messaging non implémenté |
| Fournisseur Gemini tiers | Faible | données envoyées à Google selon ses conditions |
| Reverse proxy mal configuré | Élevée | hors périmètre du binaire ; documenté dans `SECURITY.md` |
| Clé de chiffrement en variable d'environnement | Faible | compromission d'une machine en cours d'exécution, pas du disque ; KMS/Vault recommandé pour une exigence forte |
| Budget mémoire transcript épuisé | Faible | éviction LRU, dégradation vers replay complet, jamais vers un delta orphelin |

## 5. Scénarios de test associés

- `api/fuzz_test.go` : `FuzzSafeOutputPath`, `FuzzSafeMCPPath`,
  `FuzzFlattenContent`, `FuzzSanitizeSessionID`.
- `gemini/fuzz_test.go` : `FuzzParseResponse`, `FuzzNextStreamDelta`,
  `FuzzBuildRequestStruct`, `FuzzValidateModelConfig`,
  **`FuzzQuarantineUntaggedToolJSON` (T-17, T-18)**,
  **`FuzzSanitizeThoughtArtifacts`**.
- `api/gdpr_test.go` : purge, suppression idempotente, authentification.
- `api/mcp_test.go` : traversée, symlinks, taille, MIME, négociation de version.
- `gemini/integration_test.go` : chemins HTTP, sessions, upload/download.
- **`gemini/session_cookie_scope_test.go` (T-15)** :
  `TestLoadCookiesKeepsSubdomainCookiesScoped`,
  `TestCookieJarScopesByRequestHost`,
  `TestCookieJar_HostOnlyVsDomainAttribute`,
  `TestScopedCookieHonorsHostOnly`, `TestLegacyCookieStringStillLoads`.
- **`gemini/stream_guard_test.go` (T-16)** :
  `TestStallGuardReaderRejectsSilentStream`, `TestStallGuardReaderRearmsOnBytes`,
  `TestStallGuardReaderCloseIsIdempotent`.
- **`api/benchmark_v357_p0_test.go` (T-18)** :
  `TestPlanSessionTurn_IdentityAndContinuationAnchor`,
  `TestIsConversationalReset_NeonGreeting`,
  `TestQuarantineUntaggedToolJSON_HydrogenLeak`,
  `TestSessionLedger_UnbackedCompletion_HydrogenS7`,
  `TestIsIncompleteFinalProse_ArgonProse`,
  `TestTerminalFinishReason_ErrorOnUnusableOutput`,
  `TestNewStreamErrorChunk_FinishReasonError`.
- **`api/benchmark_v357_p1_test.go` (T-18)** :
  `TestTaskModeRouting_ToolBearingRequest`,
  `TestMetrics_ClassificationAndReplayExposition`,
  `TestSessionLRU_GlobalMemoryBudgetEviction`,
  `TestConfigureSessionMemoryBudget`.
- **`api/benchmark_v357_p2_test.go` (T-17, T-18)** :
  `TestProperty_MidValueMutationRejection`,
  `TestProperty_QuarantineCompleteness`,
  `TestProperty_DeltaIdentitySafety`,
  `TestProperty_MonotonicRetryTermination`.
- **`gemini/schema_validator_test.go` (T-17)** : validation Draft 2020-12,
  compactage `required`/`items`, signatures `STRICT PARAMETERS`.

## 6. Références

- [`SECURITY.md`](SECURITY.md) — contrôles opérationnels, rotation, chiffrement
- [`PRIVACY.md`](PRIVACY.md) — RGPD, rétention, droit à l'effacement
- [`../docs/architecture/pipeline_flow.md`](../docs/architecture/pipeline_flow.md) — position de chaque contrôle dans la requête
- [`../docs/architecture/cookie_scoping_rfc6265.md`](../docs/architecture/cookie_scoping_rfc6265.md) — isolation T-15 en détail
- [`../docs/architecture/agentic_resilience.md`](../docs/architecture/agentic_resilience.md) — contrôles T-17 et T-18 en détail
- [`../audit-2026-11/README.md`](../audit-2026-11/README.md) — audit initial, registre CVSS, PoC
