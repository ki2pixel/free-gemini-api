# Pipeline d'une requête — cycle de vie complet

**Version documentée :** v3.5.7 (post-benchmark P0/P1/P2)
**Code de référence :** `free-gemini-api/api/chat_handler.go`, `api/chat_service.go`,
`api/session_transcripts.go`, `api/observation_masker.go`, `api/doom_loop.go`,
`gemini/arranger.go`, `gemini/client.go`, `gemini/stream_guard.go`

Ce document décrit le trajet exact d'une requête
`POST /v1/chat/completions`, du socket jusqu'au dernier octet SSE. L'ordre des
étapes est significatif : plusieurs garanties dépendent du fait qu'un contrôle
s'exécute **avant** un autre.

---

## 1. Vue d'ensemble

```text
Client OpenAI-compatible
        │
        ▼
┌───────────────────────────────────────────────────────────────┐
│ 1. AuthMiddleware + RateLimit                                 │
│    Authorization: Bearer $API_TOKEN | X-API-Token             │
│    fenêtre fixe 600 req/60 s, clé = IP + hash(jeton)           │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 2. Décodage & validation du corps                             │
│    maxPromptBytes = 1 MiB, validation du schéma OpenAI        │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 3. Résolution du modèle & du mode                             │
│    registre → alias → ThinkingLevel ; Task [40] vs Chat [0]   │
│    Task + tools → requalifié Chat sauf opt-in explicite        │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 4. Sélection du worker (pool multi-comptes)                   │
│    least-busy, ségrégation de palier, cooldown                 │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 5. Masquage des observations anciennes                        │
│    seuil 60 % du budget prompt ; 4 observations récentes      │
│    conservées octet pour octet                                 │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 6. Planification du tour de session                           │
│    identité worker + conversation + préfixe exact              │
│    → delta ancré [Continuation] ou replay complet              │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 7. Coupe-circuit anti-boucle (DoomLoopTracker)                │
│    empreinte canonique des appels, cycles A→B, A→B→C          │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 8. Transport HTTP/3 QUIC + Stall Guard 45 s                   │
│    fingerprint Chrome 152 JA4, jar RFC 6265                   │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 9. Arranger (sortie amont → contrat OpenAI)                   │
│    pensées, tool calls, quarantaine JSON, sanitizer <thought   │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 10. Classifieurs & décision de tour                           │
│     greeting | incomplete | refusal | pseudo_report |          │
│     unbacked_completion | untagged_mutation | schema_invalid   │
│     → accepté, ou retry sur conversation vierge                │
└───────────────────────────────┬───────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────┐
│ 11. Émission SSE & Terminal Guard                             │
│     finish_reason: "stop" ou "error" (jamais "stop" sur échec) │
└───────────────────────────────────────────────────────────────┘
```

---

## 2. Détail des étapes

### 2.1 Authentification et limitation de débit

Deux en-têtes sont acceptés, avec comparaison à temps constant :

```text
Authorization: Bearer $API_TOKEN
X-API-Token: $API_TOKEN
```

`GET /health` est la seule route publique inconditionnelle. Les pré-vols
`OPTIONS` sont exemptés pour que les navigateurs puissent négocier avant
d'envoyer le jeton. Un `API_TOKEN` vide fait répondre `401` sur **toutes** les
routes protégées : le serveur journalise un avertissement au démarrage mais ne
s'ouvre jamais silencieusement.

Le limiteur à fenêtre fixe s'applique à toutes les routes sauf `GET /health` et
`OPTIONS`. La clé combine l'IP et un hachage du jeton, ce qui donne un budget
indépendant à chaque client sans stocker le secret. Valeurs par défaut :
**600 requêtes / 60 s** (`RATE_LIMIT_MAX`, `RATE_LIMIT_WINDOW_SECONDS`).

### 2.2 Décodage et borne de prompt

`maxPromptBytes = 1 MiB` borne le prompt aplati. Au-delà, la requête est
rejetée avant tout appel amont. Le corps est décodé vers les types OpenAI
internes (`OpenAIChatMessage`, `OpenAITool`).

### 2.3 Résolution du modèle et arbitrage de mode

Le registre (`gemini/registry.go`) résout l'alias demandé vers une
`ModelConfig` canonique : identifiant amont, `ModeID` (`[0]` chat, `[40]` task),
`ThinkingLevel` (1 = low, 2 = high, 4 = medium), palier minimal, capacités.

**Règle de routage v3.5.7 (P1) :** une requête qui déclare des outils ne part
jamais vers `AppMode = 40` sans opt-in explicite. Les alias `*-task` sont
requalifiés vers le modèle Chat de la même famille :

```text
hydrogen-task  →  hydrogen
neon-task      →  neon
```

Opt-in par requête : `X-Allow-Task-Mode-For-Tools: true`.
Opt-in serveur : `ALLOW_TASK_MODE_FOR_TOOLS=true`.
Les requêtes **sans** outils conservent le mode Task demandé.

**Escalade de thinking :** après plusieurs tours vides consécutifs, le palier
est escaladé `low → medium → high` en résolvant un **autre nom de modèle**
(`thinkingTierOrder` dans `gemini/thinking.go`). Aucun `ModelConfig` partagé
n'est muté : c'est une propriété importante pour la sûreté concurrente.

### 2.4 Sélection du worker

Le pool multi-comptes choisit un worker selon la stratégie *least-busy*, avec
ségrégation de palier (Free / Pro / Ultra) et protection par cooldown. Une
affinité collante est possible via `X-Agent-ID`, ce qui garantit qu'un même
thread d'agent reste sur le même compte.

Le worker retenu expose deux identifiants critiques pour l'étape 6 :

- `workerID` — l'identité du compte dans le pool ;
- `convID` — l'identifiant de conversation côté amont Gemini.

### 2.5 Masquage des observations anciennes

(`api/observation_masker.go`)

Un harness agentique renvoie l'historique complet à chaque requête. Sur un
horizon long, les résultats d'outils anciens gonflent le prompt jusqu'à
provoquer des refus *canned* ou des réponses vides. Le masquage compacte les
observations anciennes **avant** l'aplatissement.

Trois déclencheurs, évalués indépendamment :

| Condition | Valeur |
|---|---|
| Taille du transcript aplati | > 60 % de `maxPromptBytes` |
| Nombre d'observations d'outils | > 6 |
| Nombre de messages | > 12 |

Seules les observations de plus de **200 octets** sont candidates. Les **4**
observations les plus récentes sont conservées octet pour octet. Pour les
autres, les **3** premières lignes sont gardées, plafonnées à **200 octets**
pour qu'une ligne géante ne puisse pas défaire le masque.

Propriété : l'entrée n'est jamais mutée. Une copie superficielle avec les
contenus d'outils réécrits est renvoyée, ce qui rend la fonction sûre en
concurrence et testable de façon déterministe.

### 2.6 Planification du tour de session

(`api/session_transcripts.go` — `planSessionTurnWithReason`)

C'est l'étape la plus critique pour la fiabilité agentique. Un delta n'est
envoyé que si **quatre** conditions sont réunies :

1. une entrée de session existe et est marquée valide (`convValid`) ;
2. `curWorkerID == entry.lastWorkerID` ;
3. `curConvID == entry.lastConvID` ;
4. le transcript aplati étend exactement le précédent : `flat == lastFlat ++ δ`
   avec `δ ≠ ε`.

Toute divergence force un replay complet sur une conversation vierge. Les
raisons sont exposées pour l'observabilité :

| Raison | Signification |
|---|---|
| `new` | première requête de la session |
| `forced` | conversation invalidée par un tour précédent en échec |
| `worker_mismatch` | un autre compte détient désormais la session |
| `conv_mismatch` | l'identifiant de conversation amont a changé |
| `prefix_mismatch` | l'historique a été réécrit ou compacté par le client |
| `identical` | transcript identique, aucun delta à envoyer |

**Ancre de continuation.** Un delta qui ne contient aucune instruction
utilisateur (uniquement des résultats d'outils) est préfixé par une ancre
éphémère :

```text
[Continuation] Le dernier appel d'outil est terminé. Reprends le plan en cours
au prochain point non réalisé. Ne salue pas, ne redémarre pas et ne produis pas
de synthèse finale tant que des modifications ou vérifications restent à faire.
```

L'ancre n'est **jamais** stockée dans `lastFlat` : elle ne peut donc pas créer
de faux `prefix_mismatch` au tour suivant.

**Budget mémoire.** Deux bornes globales s'appliquent :

- **256 sessions** maximum (`maxTrackedSessions`) ;
- **64 Mio** cumulés de `lastFlat` (`SESSION_MEMORY_BUDGET_MB`, défaut 64).

L'éviction est LRU (`lastAccess`). Elle est toujours sûre : une session évincée
rejoue simplement son transcript complet au tour suivant. Un transcript plus
gros que le budget global n'est pas retenu du tout — le pointeur de
conversation peut rester, mais le tour suivant repart en replay.

### 2.7 Coupe-circuit anti-boucle

(`api/doom_loop.go`)

`DoomLoopTracker` enregistre une empreinte canonique de chaque appel d'outil
(normalisée pour ignorer l'ordre des arguments). Il détecte :

- la répétition identique d'un appel (avertissement puis arrêt) ;
- les cycles `A → B → A` et `A → B → C → A` ;
- les rafales de lectures passives, remises à zéro par une mutation.

Le résultat est une directive correctrice injectée dans le tour suivant, ou un
blocage. Le compteur `gemini_doom_loop_interventions_total` expose l'activité.

### 2.8 Transport et Stall Guard 45 s

(`gemini/client.go`, `gemini/stream_guard.go`)

Le transport utilise HTTP/3 QUIC avec simulation d'empreinte TLS Chrome 152 JA4
et reprise de session 0-RTT. Le jar de cookies est un `cookiejar` RFC 6265 avec
liste de suffixes publics — voir
[cookie_scoping_rfc6265.md](cookie_scoping_rfc6265.md).

**Stall Guard.** `stallGuardReader` enveloppe le corps de la réponse amont. Un
minuteur de `upstreamStreamStallTimeout = 45 * time.Second` est armé à la
création, réarmé à **chaque** lecture produisant des octets. Sans octet reçu
pendant 45 s, le reader annule le contexte de requête et ferme le corps, ce qui
débloque immédiatement un `Read` en attente avec
`ErrUpstreamStreamStalled`.

Pourquoi ce n'est pas redondant avec le timeout HTTP : un flux Gemini bloqué
garde la connexion **ouverte sans émettre un seul octet**. Un timeout de
transport classique ne se déclenche pas, et la session agentique reste gelée
pour tout son budget. C'est la cause du gel de 14 minutes observé en v3.5.0.

`ErrUpstreamStreamStalled` est classé comme erreur de transport : il consomme
`maxTransportRetries`, distinct du budget fonctionnel.

### 2.9 Arranger

(`gemini/arranger.go`)

Transforme la sortie brute amont en contrat OpenAI strict. Trois
responsabilités, dans cet ordre :

1. **Extraction des pensées** — `extractThoughtsAndContent` sépare les blocs
   `<thought>` / `<thinking>` du texte visible. Les balises non fermées,
   imbriquées ou répétées sont gérées par un extracteur unifié (v3.5.4).
2. **Extraction des appels d'outils** — les blocs `<tool_call>` sont parsés,
   réparés si nécessaire (fences Markdown, échappements, caractères de contrôle),
   puis validés contre le schéma JSON `Draft 2020-12` fourni dans `req.Tools`.
3. **Nettoyage du texte visible** — `sanitizeGeminiContent` retire les artefacts
   `<thought` orphelins. Un **sanitizer** dédié (`sanitizeThoughtArtifacts`,
   v3.5.7 P2) supprime les préfixes `<thought` / `<thinking` incomplets et les
   balises fermantes orphelines, **préserve** les citations intentionnelles
   dans les blocs de code délimités par ``` et **ne touche jamais** à
   `ReasoningContent`.

**Quarantaine des JSON non taggés (v3.5.7 P0).** Un objet JSON nu, non entouré
de balises, peut apparaître dans le flux. Deux traitements :

| Cas | Décision |
|---|---|
| Lecture seule, complète, conforme au schéma | Promotion sûre en `tool_call` |
| Mutation, objet tronqué, schéma invalide | Suppression de `CleanText`, marquage `UntaggedMutation`, retry |

Invariant : `CleanText` ne contient aucun objet JSON dont le nom désigne un
outil déclaré. Vérifié par `TestProperty_QuarantineCompleteness` sur des objets
concaténés, *name-last* et tronqués.

### 2.10 Classifieurs et décision de tour

(`api/session_transcripts.go`)

Un tour porteur d'outils est jugé inutilisable si l'une des conditions
suivantes est vraie. L'ordre d'évaluation compte : une mutation tronquée est
rejetée en premier, un `tool_call` validé par l'arranger court-circuite tous les
classifieurs de prose.

| Classe | Détecteur | Effet |
|---|---|---|
| `untagged_mutation` | mutation tronquée en pleine valeur | rejet immédiat |
| `tool_call` | `tool_call` validé par l'arranger | accepté |
| `greeting` | `isConversationalReset` | retry avec ancre |
| `incomplete` | `isIncompleteFinalProse` | retry, puis `error` |
| `refusal` | `isToolRefusalOutput` + erreurs *canned* | retry |
| `pseudo_report` | `isPseudoProgressReport` | retry |
| `unbacked_completion` | `sessionLedger` + lexique S7 | retry |
| `schema_invalid` | validation `Draft 2020-12` | retry |
| `ok` | aucune des conditions ci-dessus | accepté |

**`isConversationalReset`** reconnaît les salutations et offres d'aide
génériques, uniquement sur une requête porteuse d'outils et sans `tool_call`
validé. Un greeting n'est jamais une réponse finale dans une boucle agentique.

**`isIncompleteFinalProse`** intercepte les fragments visibles de **10 à 80
runes**, sans ponctuation finale, sans chemin, code ou URL. C'est la signature
d'un flux amont coupé après le bloc de pensée (cas Argon).

**`sessionLedger`** corrèle les appels assistant et les résultats d'outils par
`tool_call_id`. Il distingue :

- `pendingMutations` — chemins ciblés par `write`, `edit`, `multiedit`, `patch`,
  `apply_patch`, ou par les verbes shell de déplacement/création ;
- `mutationsConfirmed` — chemins dont le résultat ne signale aucune erreur ;
- `pendingValidations` / `validationsConfirmed` — commandes `go test`, `go vet`,
  `go build`, `gofmt`, `audit_check.sh` réellement exécutées avec succès.

Un claim d'achèvement qui cite un chemin non confirmé, ou qui affirme un succès
de test sans validation enregistrée, est classé `unbacked_completion`. Les
chemins cités sont normalisés avant comparaison, ponctuation finale comprise
(`docs/README.md.` → `docs/README.md`).

### 2.11 Budgétisation des retries

Trois budgets indépendants et monotones :

```text
B_fonctionnel = maxToolChatAttempts       = 4
B_vide        = maxEmptyGenerationRetries = 2
B_transport   = maxTransportRetries       = 2
─────────────────────────────────────────────
itérations_total ≤ 4 + 2 + 2              = 8
```

Chaque chemin de retry technique (`vide`, `transport`) exécute `attempt--`
après avoir incrémenté son propre compteur plafonné : une anomalie purement
technique ne peut donc pas affamer les tentatives réservées aux pseudo-rapports,
refus, erreurs de schéma ou troncatures.

### 2.12 Émission SSE et Terminal Guard

(`api/chat_handler.go`)

L'émission respecte le contrat OpenAI SSE. Deux garanties terminales :

1. **Indices séquentiels** — les `tool_calls[].index` sont strictement
   croissants et commencent à 0.
2. **`finish_reason` véridique** — un tour inutilisable épuisé émet
   `finish_reason: "error"`, jamais `"stop"`. Les quatre chemins concernés sont
   l'erreur de flux amont, l'échec de validation de schéma après retries, la
   troncature de mutation mid-value et le blocage `shouldBlockUnusableToolTurn`
   en fin de retries. Le helper `newStreamErrorChunk` fixe structurellement
   cette valeur.

Cette distinction est indispensable : OpenCode 2.4.0 mappe `"error"` vers une
`AIError` et `"stop"` vers un succès (`outcome: succeeded`). Rapporter `"stop"`
faisait passer l'hôte en `idle` sans gestion d'erreur ni retry.

---

## 3. Observabilité d'un tour

Une ligne `📊 [TURN SUMMARY]` est journalisée par requête. Elle contient
uniquement des **longueurs, hashes tronqués et labels normalisés** :

```text
📊 [TURN SUMMARY] session=<hash8> conv=<hash8> prompt_bytes=<n>
   delta_bytes=<n> replay=<reason> visible=<n> tools=<n>
   class=<label> finish=<stop|error>
```

Aucun prompt brut, aucun argument d'outil, aucun cookie n'y figure.

### Métriques Prometheus associées

| Métrique | Labels | Usage |
|---|---|---|
| `gemini_turn_classification_total` | `class` | pourquoi un tour a été rejoué, bloqué ou accepté |
| `gemini_delta_replay_total` | `reason` | coût des replays complets |
| `gemini_quarantine_total` | `kind` | charges quarantinées |
| `gemini_upstream_suspected_stop_total` | `model` | générations interrompues suspectées |
| `gemini_arranger_repairs_total` | `kind` | réparations structurelles |
| `gemini_doom_loop_interventions_total` | `signal` | coupe-circuit anti-boucle |
| `gemini_ground_truth_directives_total` | — | directives de récupération injectées |

Scrape : `GET /metrics`, protégé par `API_TOKEN` ou `ADMIN_TOKEN`.
`PUBLIC_METRICS=true` autorise un scrape sans jeton **uniquement depuis le
loopback**.

---

## 4. Points de défaillance et parades

| Panne | Symptôme | Parade |
|---|---|---|
| Flux amont muet | session gelée indéfiniment | Stall Guard 45 s → `ErrUpstreamStreamStalled` → retry transport |
| Conversation amont perdue | greeting ou réponse hors sujet | identité `worker`/`conv`/préfixe → replay complet |
| Historique réécrit par le client | delta sémantiquement orphelin | `prefix_mismatch` → replay complet |
| JSON d'outil fuité dans le texte | fichier corrompu côté client | quarantaine, jamais de promotion d'une mutation |
| Flux coupé après la pensée | texte de 21 caractères, `stop` | `isIncompleteFinalProse` → retry → `error` |
| Boucle d'outils | consommation infinie | `DoomLoopTracker` |
| Rapport d'achèvement non étayé | agent inactif déclaré terminé | `sessionLedger` + lexique S7 |
| Mémoire non bornée | OOM sur horizon long | budget LRU 64 Mio / 256 sessions |

---

## 5. Voir aussi

- [agentic_resilience.md](agentic_resilience.md) — schémas, quarantaine, ledger en détail
- [cookie_scoping_rfc6265.md](cookie_scoping_rfc6265.md) — jar et isolation des sous-domaines
- [../remediation/remediation_agentic_gemini4_v3.5.7.md](../remediation/remediation_agentic_gemini4_v3.5.7.md) — invariants et preuves formelles
- [../../AGENTS.md](../../AGENTS.md) — runbooks et matrice de tests
- [../README.md](../README.md) — hub de documentation
