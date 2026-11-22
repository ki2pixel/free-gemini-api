# Modèle de menaces — Free Gemini API

**Version :** P1, novembre 2026  
**Périmètre :** serveur Go (`free-gemini-api/`), extension Chrome
(`gemini-extension/`) et déploiement Docker local.

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

| # | Menace | Impact | Contrôles P0/P1 |
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
| T-10 | Dépendance compromise | Élevée | `govulncheck`, `gosec`, `staticcheck`, `trivy fs`, actions CI épinglées par SHA |
| T-11 | Image Docker compromise | Élevée | non-root, rootfs read-only, `cap_drop: ALL`, scan Trivy image, SBOM et signature cosign |
| T-12 | Conservation excessive de données | Moyenne | purge `RETENTION_DAYS` (30 j), `DELETE /history/:id`, `PRIVACY.md` |
| T-13 | Déni de service par goroutines/DB non bornées | Moyenne | caches LRU/TTL, `HistoryService` borné (3 workers, file 4096), drops comptabilisés |
| T-14 | Fixation de session / identifiants forgés | Moyenne | validation `^[A-Za-z0-9_.-]{1,64}$`, hachage SHA-256 des valeurs invalides |

## 4. Risques résiduels

| Risque | Sévérité | Justification |
|---|---|---|
| Cookies non chiffrés au repos | Moyenne | permissions restrictives, disque local ; chiffrement prévu en P2 |
| Jeton partagé sans RBAC | Moyenne | déploiement mono-utilisateur ; `ADMIN_TOKEN` sépare les mutations |
| Absence de mTLS extension ↔ serveur | Moyenne | loopback uniquement ; mTLS/native messaging prévu en P2 |
| Fournisseur Gemini tiers | Faible | données envoyées à Google selon ses conditions |
| Reverse proxy mal configuré | Élevée | hors périmètre du binaire ; documenté dans `SECURITY.md` |

## 5. Scénarios de test associés

- `api/fuzz_test.go` : `FuzzSafeOutputPath`, `FuzzSafeMCPPath`,
  `FuzzFlattenContent`, `FuzzSanitizeSessionID`.
- `gemini/fuzz_test.go` : `FuzzParseResponse`, `FuzzNextStreamDelta`,
  `FuzzBuildRequestStruct`, `FuzzValidateModelConfig`.
- `api/gdpr_test.go` : purge, suppression idempotente, authentification.
- `api/mcp_test.go` : traversée, symlinks, taille, MIME, négociation de version.
- `gemini/integration_test.go` : chemins HTTP, sessions, upload/download.
