# 📚 Documentation — Free Gemini API

Hub central de la documentation du proxy **Free Gemini API v3.5.7** (Gemini 4
Hydrogen, Neon, Argon, Carbon ; modes Chat `[0]` et Task `[40]`).

Ce dossier regroupe trois familles de documents :

- **`architecture/`** — spécifications vivantes du système tel qu'il est codé aujourd'hui.
- **`remediation/`** — archives chronologiques des vagues de durcissement (v3.4.8 → v3.5.7).
- **`recherches/`** — audits comparatifs, arbitrages et papiers théoriques.

Le code fait foi : chaque affirmation de ces documents peut être vérifiée dans
`free-gemini-api/` (packages `api/`, `gemini/`, `db/`).

---

## 🧭 Parcours par profil

### 👤 Utilisateur API & Intégrateur

Vous branchez un client OpenAI-compatible (OpenCode, Cursor, Cline, OpenWebUI,
SDK Python/TypeScript) sur le proxy local.

| Sujet | Où aller |
|---|---|
| Quickstart, exemples Python/TypeScript/cURL | [README.md racine](../README.md) |
| Matrice des modèles et alias | [README.md — Supported Models](../README.md#-supported-models--thinking-matrix) |
| Profondeur de réflexion `neon-high` / `neon-medium` / `neon-low` | [README.md — What's New](../README.md#-whats-new-in-gemini-4) |
| Endpoints et badges `[Admin]` | [README.md — API Reference](../README.md#-api-reference--endpoints) |
| Référence exhaustive des routes (source de vérité) | `free-gemini-api/api/routes.go` |
| Manuel CLI intégré | `GET /help` (ou `GET /?format=json`) |

**Points de vigilance v3.5.7 :**

- Toutes les routes sauf `GET /health` exigent `Authorization: Bearer $API_TOKEN`
  ou `X-API-Token: $API_TOKEN`.
- Les requêtes **avec outils** visant un alias `*-task` sont **automatiquement
  requalifiées vers le mode Chat `[0]`** de la même famille. Pour conserver le
  mode Task `[40]`, envoyer `X-Allow-Task-Mode-For-Tools: true` ou configurer
  `ALLOW_TASK_MODE_FOR_TOOLS=true` côté serveur.
- Le paramètre standard `reasoning_effort` est accepté et mappé sur la
  profondeur de pensée ; les alias explicites (`neon-high`, etc.) restent
  prioritaires.
- Un tour inutilisable épuisé (salutation, prose tronquée, JSON non taggé,
  rapport prématuré) se termine par `finish_reason: "error"`, jamais `"stop"` :
  les clients doivent traiter `"error"` comme une réponse à rejouer.

### 🛠️ Développeur & Contributeur

Vous modifiez le proxy, le déboguez ou mesurez ses performances.

| Sujet | Où aller |
|---|---|
| Cycle complet d'une requête | [architecture/pipeline_flow.md](architecture/pipeline_flow.md) |
| Résilience agentique (schémas, quarantaine, ledger) | [architecture/agentic_resilience.md](architecture/agentic_resilience.md) |
| Scoping des cookies RFC 6265 | [architecture/cookie_scoping_rfc6265.md](architecture/cookie_scoping_rfc6265.md) |
| Runbooks, commandes de test, invariants | [AGENTS.md](../AGENTS.md) |
| Historique sémantique détaillé | [CHANGELOG.md](../CHANGELOG.md) |
| Rapports de remédiation d'une vague | [remediation/README.md](remediation/README.md) |

**Commandes de référence** (depuis `free-gemini-api/`) :

```bash
go run main.go                                    # serveur local (8100 + WS 9226)
go test -short ./...                              # suite courte
go test -race -short ./...                        # détection de data races
test -z "$(gofmt -l .)" && go vet ./...           # hygiène statique
```

**Scripts d'assurance qualité** (depuis la racine du dépôt) :

```bash
./scripts/audit_check.sh                 # gofmt + go vet + tests courts (Docker)
API_TOKEN=... ./scripts/live_middleware_smoke.sh   # fumée live (consomme du quota)
./scripts/benchmark_matrix.sh            # benchmark pass^k en worktrees isolés
```

**Points de vigilance v3.5.7 :**

- Le cache de transcripts de session est borné globalement à **64 Mio**
  (`SESSION_MEMORY_BUDGET_MB`) et à **256 sessions** ; l'éviction LRU dégrade
  toujours vers un replay complet, jamais vers un delta orphelin.
- Un delta de session n'est envoyé que si `curConvID == lastConvID`, que le
  worker est identique et que le préfixe du transcript est exact.
- Toute modification de l'arranger doit conserver l'invariant : `CleanText` ne
  contient **aucun** objet JSON dont le nom désigne un outil déclaré.

### 🔒 Auditeur Sécurité & Conformité

Vous évaluez le risque, la conformité RGPD ou l'exposition réseau.

| Sujet | Où aller |
|---|---|
| Politique de sécurité, rotation, chiffrement | [free-gemini-api/SECURITY.md](../free-gemini-api/SECURITY.md) |
| Modèle de menaces STRIDE (T-01 → T-18) | [free-gemini-api/THREAT_MODEL.md](../free-gemini-api/THREAT_MODEL.md) |
| RGPD, rétention 30 j, droit à l'effacement | [free-gemini-api/PRIVACY.md](../free-gemini-api/PRIVACY.md) |
| Audit de novembre 2026 et scorecard | [audit-2026-11/README.md](../audit-2026-11/README.md) |
| Registre CVSS (25 constats) | [audit-2026-11/REGISTRE_CVSS.md](../audit-2026-11/REGISTRE_CVSS.md) |
| PoC reproductibles (18) | [audit-2026-11/CAHIER_POC.md](../audit-2026-11/CAHIER_POC.md) |

**Posture actuelle (22/11/2026) :** indice de maturité **74,25/100**, GO local
mono-utilisateur validé ; **NO-GO** maintenu pour une exposition publique ou
multi-tenant sans durcissement complémentaire. Les 25 constats de l'audit sont
traités ou documentés dans
[RAPPORT_POST_AUDIT.md](../audit-2026-11/RAPPORT_POST_AUDIT.md).

**Contrôles clés à vérifier lors d'un audit :**

- `ADMIN_TOKEN` (s'il est défini) est le **seul** jeton accepté sur
  `POST /v1/models`, `POST /v1/models/discover` et `POST /api/sync-cookies`.
- `COOKIE_ENCRYPTION_KEY` active AES-256-GCM au repos ; sans clé, les fichiers
  sont en clair et un avertissement est journalisé au démarrage.
- `REQUIRE_COOKIE_ENCRYPTION=true` transforme l'absence de clé en erreur fatale.
- `PUBLIC_METRICS=true` ne relâche l'authentification de `/metrics` que pour le
  loopback.
- `GET /metrics` expose `gemini_turn_classification_total`,
  `gemini_delta_replay_total`, `gemini_quarantine_total` et
  `gemini_upstream_suspected_stop_total` : ces compteurs permettent de détecter
  une dérive comportementale des modèles.

### 🤖 Agent IA & Outil Autonome

Vous êtes un agent de codage qui intervient dans ce dépôt.

| Sujet | Où aller |
|---|---|
| Manuel opérationnel (obligatoire) | [AGENTS.md](../AGENTS.md) |
| Invariants P0/P1/P2 et preuves | [remediation/remediation_agentic_gemini4_v3.5.7.md](remediation/remediation_agentic_gemini4_v3.5.7.md) |
| Frontière prompt-engineering vs middleware | [recherches/Prompt-Engineering-vs-Middleware-Agentique.md](recherches/Prompt-Engineering-vs-Middleware-Agentique.md) |
| Arbitrage du benchmark en vol | [recherches/Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md](recherches/Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md) |

**Règles non négociables :**

1. Lancer `./scripts/audit_check.sh` avant de déclarer une tâche terminée.
2. Ne pas commiter : les modifications restent dans le working tree, sauf
   demande explicite.
3. Ne pas affirmer qu'un test, un build ou un audit passe sans l'avoir exécuté :
   le `SessionLedger` du proxy applique exactement cette règle aux modèles.
4. Ne jamais écrire de secret (`API_TOKEN`, cookies, `COOKIE_ENCRYPTION_KEY`)
   dans un fichier suivi par Git.

---

## 🗂️ Matrice chronologique des remédiations

| Version | Date | Objet principal | Journal détaillé |
|---|---|---|---|
| **v3.4.8** | 2026-11-23 | Challenge & validation initiale : filtrage `required`/`properties`, `items` synthétiques, sérialisation des `tool_call_id`, réconciliation des orphelins, high-water marks par bloc | [remediation v3.4.8](remediation/remediation_agentic_gemini4_v3.4.8.md) |
| **v3.4.9** | 2026-11-23 | Arrêts prématurés après réinjection de la notice `AGENTS.md` par OpenCode | [remediation v3.4.9](remediation/remediation_agentic_gemini4_v3.4.9.md) |
| **v3.5.0** | 2026-11-23 | Gel de 14 min au tour 21, faux refus de sécurité Task, rapport de complétion prématuré | [remediation v3.5.0](remediation/remediation_agentic_gemini4_v3.5.0.md) |
| **v3.5.1** | 2026-11-23 | Quatrième passe de durcissement post-challenge v3.5.0 | [remediation v3.5.1](remediation/remediation_agentic_gemini4_v3.5.1.md) |
| **v3.5.2** | 2026-11-23 | Cinquième passe : challenge d'édition multi-fichiers | [remediation v3.5.2](remediation/remediation_agentic_gemini4_v3.5.2.md) |
| **v3.5.3** | 2026-11-23 | Sixième passe : analyse du code source OpenCode 2.4.0 | [remediation v3.5.3](remediation/remediation_agentic_gemini4_v3.5.3.md) |
| **v3.5.4** | 2026-11-23 | Septième passe : alignement documentaire v3.5.3 | [remediation v3.5.4](remediation/remediation_agentic_gemini4_v3.5.4.md) |
| **v3.5.5** | 2026-11-23 | Huitième passe : marqueurs de pseudo-rapport (Challenge 8), budget de retry vide dédié | [remediation v3.5.5](remediation/remediation_agentic_gemini4_v3.5.5.md) |
| **v3.5.6** | 2026-11-23 | Neuvième passe : erreurs amont *canned*, pseudo-rapports Challenge 9, `finish_reason: "error"` sur les quatre chemins SSE | [remediation v3.5.6](remediation/remediation_agentic_gemini4_v3.5.6.md) |
| **v3.5.7** | 2026-11-24 | **Clôture du benchmark en vol** : vagues **P0** (continuité de session, ancre `[Continuation]`, quarantaine JSON, `SessionLedger`, prose tronquée), **P1** (routage Task→Chat, 4 familles de métriques, budget LRU 64 Mio, `benchmark_matrix.sh`), **P2** (sanitizer `<thought`, lexique S7 bilingue, tests de propriété, fuzzing) | [remediation v3.5.7](remediation/remediation_agentic_gemini4_v3.5.7.md) |

Le détail sémantique par version, avec les entrées `Added` / `Changed` /
`Fixed` / `Regression Tests` / `Validation`, reste dans
[CHANGELOG.md](../CHANGELOG.md).

---

## 🔗 Index complet du dossier

```text
docs/
├── README.md                                   ← ce hub
├── architecture/
│   ├── pipeline_flow.md                        cycle de vie d'une requête
│   ├── cookie_scoping_rfc6265.md               jar RFC 6265 et sous-domaines
│   └── agentic_resilience.md                   schémas, quarantaine, ledger
├── remediation/
│   ├── README.md                               index chronologique
│   ├── walkthrough_fiabilisation_v3.5.7.md     journal des vagues 1-3
│   └── remediation_agentic_gemini4_v3.4.8.md … v3.5.7.md
└── recherches/
    ├── README.md                               index des travaux théoriques
    ├── Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md
    ├── audit-opencode-antigravity-auth-gemini-fiabilisation.md
    ├── BRIEF-audit-opencode-antigravity-auth-gemini-fiabilisation.md
    └── Prompt-Engineering-vs-Middleware-Agentique.md
```

## 🧷 Traçabilité et historique Git

Les rapports de remédiation historiques ainsi que le journal de bord `walkthrough.md`
ont été réorganisés dans `docs/remediation/` via `git mv`.
L'historique complet des commits et des révisions est intégralement préservé par Git :
`git log --follow docs/remediation/remediation_agentic_gemini4_v3.5.7.md`
ou `git log --follow docs/remediation/walkthrough_fiabilisation_v3.5.7.md`.
