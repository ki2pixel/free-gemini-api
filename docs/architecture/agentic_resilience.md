# Résilience agentique — schémas, quarantaine et ledger

**Version documentée :** v3.5.7 (vagues P0, P1, P2)
**Code de référence :** `api/session_transcripts.go`, `api/chat_handler.go`,
`api/doom_loop.go`, `api/observation_masker.go`, `gemini/arranger.go`,
`gemini/schema_validator.go`, `gemini/thinking.go`
**Tests de référence :** `api/benchmark_v357_p0_test.go`,
`api/benchmark_v357_p1_test.go`, `api/benchmark_v357_p2_test.go`,
`gemini/thought_sanitizer_test.go`, `gemini/fuzz_test.go`

Ce document décrit les mécanismes qui empêchent un modèle probabiliste de
corrompre un état déterministe. La thèse de fond est développée dans
[../recherches/Prompt-Engineering-vs-Middleware-Agentique.md](../recherches/Prompt-Engineering-vs-Middleware-Agentique.md) :
le prompt réduit la variance, le middleware garantit les invariants.

---

## 1. Les huit invariants

```text
I1  delta ⇒ identité worker + conversation + préfixe exact
I2  tools ∧ ¬tool_call ∧ greeting ⇒ retry, jamais stop
I3  CleanText ne contient aucun JSON d'outil déclaré
I4  claim(path) ⇒ path ∈ mutations confirmées par le tool result
I5  finish_reason = stop ⇒ tool_call ∨ réponse finale validée
I6  prompt système sans "0 remaining" / "maximum number of tool" / "search calls"
I7  tout retry est couvert par un budget plafonné et monotone
I8  prompt ⇒ mémoire bornée (256 sessions, 64 Mio cumulés)
```

Chaque invariant possède au moins un test de non-régression ou de propriété.
Les quatre propriétés formelles de `benchmark_v357_p2_test.go` couvrent I1, I3,
I4 et I7 sur des charges générées.

---

## 2. Validation des schémas d'outils

(`gemini/schema_validator.go`)

Les schémas fournis dans `req.Tools` sont validés contre **JSON Schema Draft
2020-12**. La validation s'applique aux appels **explicites** : un `tool_call`
extrait et réparé par l'arranger doit satisfaire le schéma déclaré pour être
promu.

### 2.1 Compactage des paramètres

Avant envoi au modèle, les schémas sont compactés pour tenir dans le budget de
prompt. Deux corrections issues de la vague 1 de fiabilisation :

| Correctif | Règle |
|---|---|
| **A-01** | `required` est filtré contre `properties` : un champ requis absent des propriétés est retiré |
| **A-02** | Un tableau sans `items` reçoit la synthèse `items: {type: "string"}` |

Sans A-01, Google rejette le schéma entier. Sans A-02, certains tableaux sont
refusés ou produisent des appels impossibles à valider.

### 2.2 Signatures `STRICT PARAMETERS`

La signature attachée à chaque outil est calculée **sur le schéma effectif**
(après compactage), pas sur une chaîne statique. Si le schéma et la signature
divergentent, le modèle reçoit une consigne incohérente et produit des appels
invalides.

### 2.3 Rejet et retry

Un appel dont le schéma est invalide est :

1. retiré de la sortie structurée ;
2. compté `gemini_quarantine_total{kind="schema_invalid"}` ;
3. classé `schema_invalid` dans `gemini_turn_classification_total` ;
4. rejoué avec une directive correctrice.

Si un appel valide coexiste avec un appel invalide, **seul l'invalide est
retiré** : `TestArrangeGeminiOutputWithTools_InvalidCallDroppedButValidKept`
verrouille ce comportement.

---

## 3. Quarantaine des JSON d'outils non taggés

C'est le correctif central de la vague P0. Le modèle peut émettre un objet JSON
nu — sans balise `<tool_call>` — au milieu du texte visible. Sur un horizon
long, cet objet peut être **tronqué en pleine valeur**, par exemple au milieu
d'un contenu de fichier de 938 octets.

### 3.1 Deux traitements, jamais un seul

| Condition | Décision | Raison |
|---|---|---|
| Lecture seule **et** complète **et** schéma-valide | Promotion en `tool_call` | Aucun effet de bord possible |
| Mutation (même complète) | Quarantaine | Une promotion contournerait le contrôle de schéma |
| Tronquée en pleine valeur | Quarantaine + `UntaggedMutation` | Écrire un fichier partiel est pire que retenter |
| Schéma invalide | Quarantaine | Le modèle a mal compris le contrat |

### 3.2 Invariant

```text
I3 : CleanText ne contient aucun objet JSON dont name/tool/function
     désigne un outil déclaré dans req.Tools.
```

`TestProperty_QuarantineCompleteness` vérifie I3 sur **200 charges générées** :
objets concaténés, *name-last*, tronqués, avec fences Markdown. La formulation
*name-last* est importante : un détecteur naïf qui cherche `"name"` avant
`"function"` rate les objets où la clé arrive en dernier.

### 3.3 Compteurs

```text
gemini_quarantine_total{kind="untagged_mutation"}
gemini_quarantine_total{kind="schema_invalid"}
gemini_quarantine_total{kind="truncated"}
```

### 3.4 Fuzzing

`FuzzQuarantineUntaggedToolJSON` génère du JSON aléatoire : imbriqué, tronqué,
concaténé, jusqu'à 1 Mio, avec UTF-8 invalide et fences Markdown. Contrat :
**aucun panic, aucune promotion de mutation**. La campagne de validation v3.5.7
a exécuté ~109 000 cas sans crash.

---

## 4. Write-Serial : une mutation par tour

Le middleware n'exécute pas les outils : le client le fait. Mais il impose une
cadence. Un tour ne doit pas contenir plus d'une **mutation** (`edit`, `write`,
`patch`, `apply_patch`, `multiedit`).

Justification empirique : deux mutations dans le même tour sur des chemins
différents créent une fenêtre où le second appel s'appuie sur un état disque que
le premier n'a pas encore produit. Sur un horizon long, ces courses se
composent.

Vérification live (`scripts/live_middleware_smoke.sh`, contrôle 6/6) :

- au plus une mutation par tour ;
- `tool_calls[].index` strictement séquentiels à partir de 0.

Réparation associée (**A-04**) : après `ThrottleMutatingToolCalls`, les appels
orphelins sont réconciliés et marqués `[recovered]` pour que le client reçoive
un identifiant de résultat cohérent.

---

## 5. Continuité de session

### 5.1 Identité stricte

`planSessionTurnWithReason` exige les quatre conditions décrites dans
[pipeline_flow.md §2.6](pipeline_flow.md). La conséquence pratique : un delta
n'est **jamais** injecté dans une conversation étrangère ni après un changement
de worker.

### 5.2 Ancre de continuation

Un delta composé uniquement de résultats d'outils n'a aucune instruction
utilisateur. Un modèle qui a perdu le fil répond alors par une salutation ou une
synthèse fraîche. L'ancre `[Continuation]` restate le contexte :

```text
[Continuation] Le dernier appel d'outil est terminé. Reprends le plan en cours
au prochain point non réalisé. Ne salue pas, ne redémarre pas et ne produis pas
de synthèse finale tant que des modifications ou vérifications restent à faire.
```

Propriétés :

- **éphémère** : jamais stockée dans `lastFlat` ;
- **non observable** par le client ;
- **non causale** lors du calcul de préfixe du tour suivant.

### 5.3 Budget mémoire et éviction LRU

| Borne | Valeur | Configurable |
|---|---|---|
| Nombre de sessions | 256 | non (`maxTrackedSessions`) |
| Mémoire cumulée `lastFlat` | 64 Mio | oui, `SESSION_MEMORY_BUDGET_MB` |

L'éviction rafraîchit `lastAccess` à chaque accès et retire la session la moins
récemment utilisée. **L'éviction dégrade toujours vers un replay complet**,
jamais vers un delta orphelin. Un transcript plus gros que le budget global
n'est pas retenu du tout.

`TestProperty_DeltaIdentitySafety`,
`TestSessionLRU_GlobalMemoryBudgetEviction` et
`TestConfigureSessionMemoryBudget` couvrent ces garanties.

---

## 6. `SessionLedger` — preuve d'achèvement

### 6.1 Le problème S7

Un agent peut affirmer « tous les fichiers ont été mis à jour et les tests
passent » sans avoir rien écrit ni rien exécuté. C'est l'effet **S7** : la
pression de conclure produit un rapport de complétion non étayé.

### 6.2 Le ledger

`sessionLedger` corrèle les appels assistant et les résultats d'outils par
`tool_call_id` :

| Ensemble | Contenu | Alimentation |
|---|---|---|
| `pendingMutations` | chemins ciblés par `write`, `edit`, `multiedit`, `patch`, `apply_patch`, verbes shell de déplacement/création | à l'émission de l'appel |
| `mutationsConfirmed` | chemins dont le résultat ne signale **aucune** erreur | au résultat |
| `pendingValidations` | commandes de validation émises | à l'émission |
| `validationsConfirmed` | commandes réellement exécutées avec succès | au résultat |

Commandes de validation reconnues : `go test`, `go vet`, `go build`, `gofmt`,
`audit_check.sh`. La reconnaissance est **volontairement ciblée** : d'autres
écosystèmes (`cargo test`, `pytest`, `npm test`) pourront être ajoutés quand le
harnais les utilisera réellement.

### 6.3 Classification

Un claim d'achèvement est classé `unbacked_completion` si :

- il cite un chemin absent de `mutationsConfirmed` ; **ou**
- il affirme un succès de test/build/audit sans validation confirmée ; **ou**
- il correspond à une forme du lexique S7 sans aucun chemin vérifiable.

Un résultat en erreur ou un marqueur d'échec **n'est jamais** une preuve. Une
simple commande `ls` ne peut pas étayer un claim.

### 6.4 Normalisation des chemins

Avant comparaison, la ponctuation finale des chemins cités est retirée :

```text
`docs/README.md.`   →   docs/README.md
```

Sans cette normalisation, un chemin parfaitement correct suivi d'un point de
phrase serait classé non étayé.
`TestUnbackedCompletion_PathPunctuationNormalization` verrouille ce point.

### 6.5 Lexique bilingue

`pseudoReportSnippets` et `completionClaimSnippets` couvrent les formes
observées, françaises et anglaises :

```text
tous les objectifs ont été atteints
l'ensemble des étapes a été exécuté / exécutée
les vérifications ont confirmé
les modifications ci-dessus ont été appliquées
les fichiers ont été créés et organisés
l'opération est terminée avec succès
the implementation is complete
all planned steps have been executed
all files have been successfully created
the tasks have been completed as requested
here is the summary of the completed work
```

Le lexique est un **filet**, pas l'invariant fort. L'invariant fort est le
ledger. Une formulation inconnue et sans chemin peut encore passer ; elle doit
alors être ajoutée au corpus au benchmark suivant.

### 6.6 Bornes

`SessionLedger` : **256** chemins et **256** commandes de validation par
session, éviction FIFO. Complexité du ledger : `O(|chemins|)`, avec
`|chemins| ≤ 256`.

---

## 7. Classifieurs de sortie

Ordre d'évaluation (l'ordre compte) :

```text
1. mutation tronquée mid-value          → rejet immédiat
2. tool_call validé par l'arranger      → accepté (court-circuite tout le reste)
3. sortie pensée-seule sans contenu     → retry (budget vide)
4. erreur canned / refus amont          → retry (budget fonctionnel)
5. pseudo-rapport                       → retry
6. completion non étayée                → retry
7. greeting                             → retry avec ancre
8. prose courte inachevée               → retry
9. schéma invalide                      → retry
10. sinon                               → accepté
```

### 7.1 `isConversationalReset`

Reconnaît les salutations et offres d'aide génériques (`Hello! How can I help
you today?`, `Bonjour, comment puis-je vous aider ?`, …), **uniquement** sur
une requête porteuse d'outils et **sans** `tool_call` validé. Un greeting n'est
jamais une réponse finale dans une boucle agentique.

### 7.2 `isIncompleteFinalProse`

Intercepte les fragments visibles de **10 à 80 runes** qui :

- n'ont pas de ponctuation finale (`.!?…。！？`) ;
- ne contiennent ni saut de ligne, ni backtick, ni slash, ni deux-points ;
- ne commencent pas par `http`.

C'est la signature d'un flux amont coupé après le bloc de pensée (cas Argon :
21 caractères, `finish_reason: "stop"`).

### 7.3 Erreurs *canned*

Google peut renvoyer sa propre prose d'échec :

```text
i encountered an error doing what you asked
could you try again?
sorry, something went wrong
something went wrong. please try your request again
please try your request again
```

Sur une requête avec outils, ces sorties sont inutilisables : la session est
invalidée et le tour rejoué sur une conversation vierge. **Sans** outils, la
même prose reste une réponse légitime.

---

## 8. Terminal Guard — `finish_reason` véridique

Quatre chemins SSE terminaient par `"stop"` alors que le tour avait échoué :

1. erreur de flux amont (`streamErr`) ;
2. échec de validation de schéma après retries ;
3. troncature de mutation mid-value ;
4. blocage `shouldBlockUnusableToolTurn` en fin de retries.

Ils émettent désormais `finish_reason: "error"` via `newStreamErrorChunk`, dont
la valeur est **structurellement fixée**.

Pourquoi c'est indispensable : OpenCode 2.4.0
(`packages/ai/src/protocols/openai-chat.ts`, `mapFinishReason`) mappe `"error"`
vers une `AIError`, tandis que `"stop"` est accepté comme réponse complète
(`outcome: succeeded`). Rapporter `"stop"` faisait passer l'hôte en `idle` sans
gestion d'erreur ni retry — l'agent s'arrêtait silencieusement.

---

## 9. Terminaison et complexité

### 9.1 Budgétisation

```text
B_fonctionnel = maxToolChatAttempts       = 4
B_vide        = maxEmptyGenerationRetries = 2
B_transport   = maxTransportRetries       = 2
─────────────────────────────────────────────
maxChatLoopIterations                     = 8
```

Chaque budget est monotone et plafonné. Les chemins techniques (`vide`,
`transport`) font `attempt--` après avoir incrémenté leur propre compteur : ils
ne peuvent pas affamer les tentatives fonctionnelles.
`TestProperty_MonotonicRetryTermination` vérifie que la borne vaut exactement 8
et que chaque budget est croissant puis plafonné.

### 9.2 Complexité par tour

```text
B = taille du transcript aplati, B ≤ 1 Mio
n = taille de la sortie brute du tour, n ≤ B

flatten / masquage      O(B)
planSessionTurn         O(B)     comparaison de préfixe
arranger / parse        O(n)     scans bornés, plafond de 50 candidats
quarantaine JSON        O(n)
classifieurs            O(n)
ledger                  O(|chemins|), |chemins| ≤ 256
sanitizer <thought      O(n), une passe, fences suivies en état local
```

Aucune étape n'est quadratique sur la taille du prompt.

### 9.3 Concurrence

- `sessionTurnMu` protège l'état de continuation ;
- `sessionLedger.mu` protège chaque ledger ;
- les snapshots sont copiés avant usage hors verrou ;
- **l'escalade de thinking se fait par nom de modèle**, sans mutation du
  `ModelConfig` partagé (`thinkingTierOrder`).

Ce dernier point est une exigence de conception : zéro data race, zéro mutation
de pointeur global. Une requête qui a besoin d'un palier supérieur résout un
**autre alias** au lieu de modifier la configuration partagée.
`go test -race -short ./...` passe sur les trois packages.

---

## 10. Hygiène de prompt — le *Pink Elephant*

Les littéraux suivants ont été retirés du prompt système et des directives de
replay :

```text
0 remaining
maximum number of tool
search calls
```

Ils restent **uniquement** dans les classifieurs de **sortie**, jamais envoyés
au modèle. La règle 7 énonce désormais un contrat positif : chaque outil de
`[AVAILABLE TOOLS]` est actif à chaque tour jusqu'à ce que toutes les
modifications et vérifications soient terminées.

Raison : nommer une limite dans le prompt augmente la probabilité que le modèle
la reproduise. La v3.5.6 avait déjà ajouté une clause de permission illimitée ;
le benchmark v3.5.7 a montré que la clause elle-même entretenait l'idée de
quota. L'arbitrage R2 a donc rejeté l'ajout d'une clause supplémentaire.

---

## 11. Routage produit Task → Chat

Une requête qui déclare des outils ne part jamais vers `AppMode = 40` sans
opt-in explicite :

```text
hydrogen-task  →  hydrogen
neon-task      →  neon
```

Opt-in par requête : `X-Allow-Task-Mode-For-Tools: true`.
Opt-in serveur : `ALLOW_TASK_MODE_FOR_TOOLS=true`.
Les requêtes **sans** outils conservent le mode Task demandé.

Justification empirique : dans le benchmark v3.5.7,
`gemini-4-hydrogen-task` a produit **0 appel d'outil** et une synthèse finale
invoquant un faux quota. Le planificateur autonome de Task n'exécute pas la
boucle d'outils externe ; il la remplace par une narration.

---

## 12. Tableau de correspondance invariant → preuve

| Invariant | Test de non-régression | Test de propriété | Fuzzer |
|---|---|---|---|
| I1 identité de session | `TestPlanSessionTurn_IdentityAndContinuationAnchor` | `TestProperty_DeltaIdentitySafety` | — |
| I2 greeting ⇒ retry | `TestIsConversationalReset_NeonGreeting` | — | — |
| I3 quarantaine | `TestQuarantineUntaggedToolJSON_HydrogenLeak` | `TestProperty_QuarantineCompleteness` | `FuzzQuarantineUntaggedToolJSON` |
| I4 ledger S7 | `TestSessionLedger_UnbackedCompletion_HydrogenS7`, `TestUnbackedCompletion_ValidationEvidence` | — | — |
| I5 finish_reason | `TestNewStreamErrorChunk_FinishReasonError`, `TestTerminalFinishReason_ErrorOnUnusableOutput` | — | — |
| I6 hygiène prompt | `TestPromptHasNoPinkElephant` | — | — |
| I7 terminaison | `TestPlanSessionTurn_ReplayReasons` | `TestProperty_MonotonicRetryTermination` | — |
| I8 mémoire bornée | `TestSessionLRU_GlobalMemoryBudgetEviction`, `TestConfigureSessionMemoryBudget` | — | — |
| Sanitizer `<thought` | `TestArgonSanitizedThoughtArtifact` | — | `FuzzSanitizeThoughtArtifacts` |
| Troncature mid-value | — | `TestProperty_MidValueMutationRejection` | `FuzzQuarantineUntaggedToolJSON` |

---

## 13. Limites honnêtes

1. **Le benchmark reste un `n = 1` par modèle.** Les propriétés prouvent les
   invariants du proxy, pas la distribution comportementale des modèles. Le
   banc `pass^k` de `scripts/benchmark_matrix.sh` reste nécessaire pour arbitrer
   une évolution de prompt.
2. **Le sanitizer `<thought` est cosmétique.** Il nettoie un artefact visible ;
   il ne remplace ni la détection de prose courte, ni le `finish_reason`
   véridique.
3. **Le lexique S7 est un filet.** Une formulation inconnue, sans chemin et
   sans marqueur lexical, peut encore passer. Elle doit être ajoutée au corpus.
4. **La reconnaissance des validations est ciblée** sur `go test`, `go vet`,
   `go build`, `gofmt`, `audit_check.sh`.
5. **La promotion des lectures non taggées reste étroite** : seul un objet
   complet, schéma-valide et non mutateur est promu.

---

## 14. Voir aussi

- [pipeline_flow.md](pipeline_flow.md) — position de chaque mécanisme dans le cycle de requête
- [cookie_scoping_rfc6265.md](cookie_scoping_rfc6265.md) — isolation des cookies
- [../remediation/remediation_agentic_gemini4_v3.5.7.md](../remediation/remediation_agentic_gemini4_v3.5.7.md) — rapport de la vague P0-P2
- [../recherches/Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md](../recherches/Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md) — décisions d'arbitrage R1-R4
- [../../AGENTS.md](../../AGENTS.md) — commandes de test ciblées
