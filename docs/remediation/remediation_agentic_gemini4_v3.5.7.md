# Remédiation agentique Gemini 4 — v3.5.7

**Date :** 2026-11-24
**Contexte :** clôture du benchmark comportemental « en vol » v3.5.7 et livraison des trois vagues de remédiation arbitrées : P0 (fiabilité immédiate), P1 (mesure et routage) et P2 (cosmétique et durcissement).
**Document de référence :** [`docs/recherches/Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md`](../recherches/Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md)
**Commits amont :** P0 `e5d3629`, P1 `4bf3f6f`. La vague P2 est livrée dans le présent commit.
**Périmètre :** serveur Go `free-gemini-api/`, tests de propriété et de fuzzing, documentation de release.

---

## 0. Synthèse exécutive

Le benchmark « en vol » a produit quatre modes d'échec reproductibles sur les modèles `gemini-4-neon`, `gemini-4-hydrogen`, `gemini-4-hydrogen-task` et `gemini-4-argon`. L'arbitrage a écarté trois attributions causales du rapport initial et retenu sept invariants déterministes, désormais couverts par du code, des tests de non-régression et des tests de propriété.

Les trois vagues ferment les causes identifiées :

| Vague | Objet | Livrables principaux |
|---|---|---|
| **P0** | Fiabilité immédiate | Continuité de session vérifiée, ancre de continuation, classifieur de greeting, quarantaine des JSON non taggés, preuve d'évidence S7, prose courte inachevée, hygiène de prompt *Pink Elephant* |
| **P1** | Mesure et routage | Routage des alias `*-task` vers le mode Chat avec outils, métriques Prometheus par classe, journal structuré sans contenu brut, budget LRU global de 64 Mio, harnais `benchmark_matrix.sh` |
| **P2** | Cosmétique et durcissement | Sanitizer visuel des artefacts `<thought`, extension lexicale bilingue S7, preuve d'exécution des tests/compilations, quatre tests de propriété formelle et deux fuzzers permanents |

Le moteur ne dépend plus d'une clause de prompt supplémentaire pour survivre aux traces observées : chaque sortie dangereuse est soit interceptée, soit rejouée sur une conversation vierge, soit terminée par un `finish_reason: "error"` véridique.

---

## 1. Bilan du benchmark en vol

Le rapport initial est un `n = 1` par modèle. Ses mesures sont exactes, mais il ne permet pas de distinguer la variance du modèle d'un effet de prompt. Les causes retenues après lecture contradictoire du code et des transcripts sont les suivantes.

| Modèle | Symptôme observé | Cause déterministe retenue |
|---|---|---|
| `gemini-4-neon` | `Hello! How can I help you today?` accepté comme `stop` | Identité de conversation non vérifiée avant l'envoi d'un delta, puis greeting non classifié |
| `gemini-4-hydrogen` | JSON `write` de 938 octets fuité dans le texte visible, puis rapport S7 | Objet nu non taggé et tronqué en pleine valeur, non quarantiné ; lexique S7 incomplet |
| `gemini-4-hydrogen-task` | 0 appel d'outil, synthèse finale invoquant un faux quota | Prior paramétrique de fin de budget ; le mode Task n'est pas un mode d'exécution d'outils fiable |
| `gemini-4-argon` | Texte visible de 21 caractères, `finish_reason: "stop"` | Flux amont coupé après le bloc de pensée ; prose courte non classifiée |

### 1.1 Décisions d'arbitrage

| Recommandation | Verdict | Raison courte |
|---|---|---|
| **R1** — Ancrer les deltas courts/vides | **Acceptée, révisée** | La cause première est l'absence d'invariant d'identité, pas seulement la taille du delta |
| **R2** — Ajouter une clause de permission illimitée | **Rejetée telle quelle** | Déjà présente depuis la v3.5.6, échec empirique, risque *Pink Elephant* |
| **R3** — Promouvoir les JSON de tool call fuités | **Rejetée pour les mutations** | La fuite observée est tronquée et sans `path` ; promouvoir écrirait un fichier incomplet |
| **R4** — Rendre `<thought` idempotent comme balise | **Rejetée comme cause ; sanitizer étroit accepté** | `<thought` sans `>` n'est pas une balise ; le vrai correctif Argon est la prose courte |

---

## 2. Vague P0 — Fiabilité immédiate

### 2.1 Continuité de conversation et ancre de continuation

`planSessionTurnWithReason` exige désormais :

1. une entrée de session existante et valide ;
2. le même worker que le dernier tour commité ;
3. le même identifiant de conversation amont ;
4. une extension exacte du transcript commité (`flat == lastFlat ++ δ`, `δ ≠ ε`).

Toute divergence force un replay complet sur une conversation vierge. Un delta sans instruction utilisateur est préfixé par une **ancre de continuation** éphémère, jamais stockée dans `lastFlat` :

```text
[Continuation] Le dernier appel d'outil est terminé. Reprends le plan en cours
au prochain point non réalisé. Ne salue pas, ne redémarre pas et ne produis pas
de synthèse finale tant que des modifications ou vérifications restent à faire.
```

Raisons de replay exposées : `new`, `forced`, `worker_mismatch`, `conv_mismatch`, `identical`, `prefix_mismatch`. Le compteur `gemini_delta_replay_total{reason=...}` mesure leur coût.

### 2.2 Classifieur de réinitialisation conversationnelle

`isConversationalReset` reconnaît les salutations et offres d'aide génériques, uniquement sur une requête porteuse d'outils et sans tool call validé. Un greeting n'est jamais une réponse finale dans une boucle agentique : le tour est rejoué avec l'ancre, puis terminé en `error` après épuisement du budget.

### 2.3 Quarantaine des JSON d'outils non taggés

L'arranger sépare désormais deux traitements :

- **Lecture seule complète et conforme au schéma** : promotion sûre en `tool_call` ;
- **Mutation, objet tronqué ou schéma invalide** : suppression de `CleanText`, marquage `UntaggedMutation`, retry avec directive de troncature, jamais d'exécution.

Invariant : `CleanText` ne contient aucun objet JSON dont le nom désigne un outil déclaré.

### 2.4 Preuve d'évidence S7

Le `sessionLedger` corrèle les appels assistant et les résultats d'outils par `tool_call_id` :

- `pendingMutations` : chemins ciblés par `write`, `edit`, `multiedit`, `patch`, `apply_patch`, ou par les verbes shell de déplacement/création ;
- `mutationsConfirmed` : chemins dont le résultat ne signale aucune erreur ;
- `failed` implicite : tout résultat en erreur ou marqueur d'échec n'est jamais une preuve.

Une affirmation d'achèvement qui cite un chemin non confirmé est classée `unbacked_completion`. Le lexique reste un filet de sécurité pour les claims sans chemin.

### 2.5 Prose courte inachevée et `finish_reason` véridique

`isIncompleteFinalProse` intercepte les fragments visibles de 10 à 80 runes, sans ponctuation finale ni chemin/code/URL. Après épuisement des retries, le chunk terminal porte `finish_reason: "error"` via `newStreamErrorChunk`, jamais `"stop"`.

### 2.6 Hygiène de prompt : suppression de l'amorce *Pink Elephant*

Les chaînes `0 remaining`, `maximum number of tool` et `search calls` ont été retirées du prompt système et des directives de replay. Elles restent uniquement dans les classifieurs de **sortie**, jamais envoyées au modèle. La règle 7 énonce un contrat positif de disponibilité continue des outils.

---

## 3. Vague P1 — Mesure et routage

### 3.1 Routage Task → Chat pour les requêtes avec outils

Une requête qui déclare des outils ne part jamais vers `AppMode = 40` sans opt-in explicite. Les alias `*-task` sont requalifiés vers le modèle Chat de la même famille (`hydrogen-task` → `hydrogen`, `neon-task` → `neon`). Opt-in : en-tête `X-Allow-Task-Mode-For-Tools: true` ou variable `ALLOW_TASK_MODE_FOR_TOOLS=true`.

### 3.2 Observabilité par classe

Quatre familles de métriques et une ligne de résumé par tour :

- `gemini_turn_classification_total{class="tool_call|greeting|incomplete|refusal|pseudo_report|unbacked_completion|untagged_mutation|schema_invalid|ok"}` ;
- `gemini_delta_replay_total{reason="new|conv_mismatch|worker_mismatch|prefix_mismatch|identical|forced"}` ;
- `gemini_quarantine_total{kind="untagged_mutation|schema_invalid|truncated"}` ;
- `gemini_upstream_suspected_stop_total{model=...}`.

La ligne `📊 [TURN SUMMARY]` journalise uniquement des longueurs, des hashes tronqués et des labels normalisés. Aucun prompt brut ni argument d'outil n'est écrit dans les logs.

### 3.3 Budget mémoire global et éviction LRU

`sessionTurnState` suit la somme des `lastFlat` et évince la session la moins récemment utilisée lorsque :

- le nombre de sessions atteint 256 ; ou
- le budget cumulé atteint `SESSION_MEMORY_BUDGET_MB` (64 Mio par défaut).

Un transcript plus gros que le budget global n'est pas retenu : le pointeur de conversation peut rester, mais le tour suivant rejoue tout le transcript. L'éviction est toujours sûre, car elle dégrade vers un replay complet et jamais vers un delta orphelin.

### 3.4 Harnais de benchmark isolé

`scripts/benchmark_matrix.sh` rejoue le plan v3.5.7 sur les quatre modèles, dans des worktrees git propres, avec `k` exécutions par modèle. Il mesure les tool calls, greetings, faux quotas, claims S7, prose courte, `finish_reason` et durée, puis imprime la table `pass^k`.

---

## 4. Vague P2 — Cosmétique et durcissement

### 4.1 Sanitizer visuel des artefacts `<thought`

`gemini/arranger.go` expose `sanitizeThoughtArtifacts(cleanText string) string`. Le sanitizer :

- supprime les préfixes orphelins `<thought` / `<thinking` qui ne forment pas une balise complète ;
- supprime les balises fermantes orphelines `</thought>` / `</thinking>` ;
- ne touche jamais aux mots plus longs (`<thoughtful>` n'est pas une balise) ;
- préserve les citations intentionnelles de balises complètes dans les blocs de code Markdown délimités par ``` ;
- traite les variantes de casse (`<THOUGHT`, `<Thinking`) comme des artefacts.

Intégration :

- `ArrangeGeminiOutput` et `ArrangeGeminiOutputWithTools` nettoient `res.CleanText` via `sanitizeGeminiContent` ;
- `extractThoughtsAndContent` utilise le sanitizer comme filet de sécurité, y compris lorsqu'aucun bloc de pensée complet n'est présent.

**Sanctuarisation de `ReasoningContent` :** le sanitizer n'est jamais appliqué à la trace de raisonnement. Le test Argon vérifie que le token brut `<thought` reste présent dans `ReasoningContent`, que `CleanText` vaut exactement `Le plan d'action pour`, et que `IsToolCall == false`.

### 4.2 Extension lexicale S7 et preuve d'exécution

Deux niveaux de défense sont conservés.

**a. Lexique bilingue élargi** — ajout à `pseudoReportSnippets` et à `completionClaimSnippets` des formes observées et de leurs variantes :

- `tous les objectifs ont été atteints` ;
- `l'ensemble des étapes a été exécuté` / `exécutée` ;
- `les vérifications ont confirmé` ;
- `les modifications ci-dessus ont été appliquées` ;
- `les fichiers ont été créés et organisés` ;
- `l'opération est terminée avec succès` ;
- `the implementation is complete` ;
- `all planned steps have been executed` ;
- `all files have been successfully created` ;
- `the tasks have been completed as requested` ;
- `here is the summary of the completed work`.

**b. Preuve d'exécution** — le `sessionLedger` enregistre désormais les commandes de validation réellement exécutées avec succès :

- `go test`, `go vet`, `go build`, `gofmt`, `audit_check.sh`.

Le cycle est le même que pour les mutations : `pendingValidations[call_id]` à l'émission, puis `validationsConfirmed` uniquement si le résultat ne signale aucune erreur. Une affirmation du type `les tests sont passés`, `audit_check.sh a validé`, `tests unitaires validés avec succès` ou `compilation réussie` est classée `unbacked_completion` tant qu'aucune validation confirmée n'existe dans la session. Un échec de test, un `go build` en erreur ou une simple commande `ls` ne peuvent jamais étayer le claim.

**c. Normalisation des chemins** — la ponctuation finale des chemins cités est retirée avant comparaison : `` `docs/README.md.` `` est normalisé en `docs/README.md`. Un chemin cité reste adossé au ledger des mutations confirmées, et non au simple appel émis.

### 4.3 Tests de propriété formelle

Le fichier `api/benchmark_v357_p2_test.go` formalise les quatre invariants de l'arbitrage.

| Propriété | Test | Contrat vérifié |
|---|---|---|
| **P1 — Anti-mutation tronquée** | `TestProperty_MidValueMutationRejection` | Pour 200 charges générées (`write`, `edit`, `shell`), taguées, non taguées ou fences, une valeur coupée en pleine chaîne n'est jamais promue en `ToolCall` |
| **P2 — Étanchéité de la quarantaine** | `TestProperty_QuarantineCompleteness` | Après arrangement, `CleanText` ne contient aucun objet JSON dont `name`/`tool`/`function` désigne `write` ou `read`, y compris pour les objets concaténés, name-last et tronqués |
| **P3 — Sûreté du delta de session** | `TestProperty_DeltaIdentitySafety` | Tout mismatch worker/conversation force un replay complet ; un préfixe divergent force `prefix_mismatch` ; seul un préfixe exact produit un delta, ancré s'il ne contient pas d'instruction utilisateur |
| **P4 — Terminaison des retries** | `TestProperty_MonotonicRetryTermination` | La borne `maxChatLoopIterations = maxToolChatAttempts + maxTransportRetries + maxEmptyGenerationRetries` vaut 8 pour une requête avec outils, et chaque budget est monotone et plafonné |

Le test de régression `TestArgonSanitizedThoughtArtifact` rejoue la charge Argon exacte. Les tests `TestPseudoReportLexicon_P2CompletionForms`, `TestUnbackedCompletion_ValidationEvidence` et `TestUnbackedCompletion_PathPunctuationNormalization` couvrent l'extension S7.

### 4.4 Fuzzing permanent

Deux fuzzers `testing.F` ont été ajoutés dans `gemini/fuzz_test.go` :

- `FuzzQuarantineUntaggedToolJSON` : JSON aléatoires, imbriqués, tronqués, objets concaténés, contenus de 1 Mio, UTF-8 invalide. Aucun panic, aucune promotion de mutation.
- `FuzzSanitizeThoughtArtifacts` : balises complètes, partielles, imbriquées, fences Markdown et variantes de casse. Le sanitizer ne doit jamais agrandir le texte ni paniquer.

Les corpus graines sont exécutés par `go test` standard ; le fuzzing actif est lancé avec `-fuzztime=5s` dans le runner local.

---

## 5. Invariants, terminaison et sûreté mémoire

```text
I1  delta ⇒ identité worker + conversation + préfixe exact
I2  tools ∧ ¬tool_call ∧ greeting ⇒ retry, jamais stop
I3  CleanText ne contient aucun JSON d'outil déclaré
I4  claim(path) ⇒ path ∈ mutations confirmées par le tool result
I5  finish_reason = stop ⇒ tool_call ∨ réponse finale validée
I6  prompt système sans "0 remaining" / "maximum number of tool" / "search calls"
I7  tout retry est couvert par un budget plafonné et monotone
```

### 5.1 Terminaison de la boucle de génération

```text
B_fonctionnel = maxToolChatAttempts = 4
B_vide        = maxEmptyGenerationRetries = 2
B_transport   = maxTransportRetries = 2
itérations_total ≤ 4 + 2 + 2 = 8
```

Chaque chemin de retry technique (`vide`, `transport`) fait `attempt--` après avoir incrémenté son propre compteur plafonné. Aucun retry n'est illimité ou dépendant d'une condition non monotone.

### 5.2 Complexité par tour

```text
B = taille du transcript aplati, B ≤ 1 Mio
n = taille de la sortie brute du tour, n ≤ B

flatten / masquage      O(B)
planSessionTurn         O(B)     (comparaison de préfixe)
arranger / parse        O(n)     (scans bornés, plafond de 50 candidats)
quarantaine JSON        O(n)
classifieurs            O(n)
ledger                  O(|chemins|), |chemins| ≤ 256
sanitizer `<thought`    O(n), une passe, fences suivies en état local
```

Aucune étape n'est quadratique sur la taille du prompt.

### 5.3 Mémoire et concurrence

- `maxTrackedSessions = 256` ; budget cumulé `lastFlat` de 64 Mio avec éviction LRU.
- `SessionLedger` : 256 chemins et 256 commandes de validation par session, éviction FIFO bornée.
- `sessionTurnMu` protège l'état de continuation ; `sessionLedger.mu` protège chaque ledger ; les snapshots sont copiés avant usage hors verrou.
- L'escalade de thinking se fait par nom de modèle, sans mutation du `ModelConfig` partagé.

---

## 6. Validation

Toutes les commandes ont été exécutées depuis `free-gemini-api/`.

| Contrôle | Commande | Résultat |
|---|---|---|
| Formatage | `test -z "$(gofmt -l .)"` | **OK** |
| Tests ciblés P2 (API) | `go test -v ./api -run "TestProperty_\|TestArgonSanitized\|TestPseudoReportLexicon_P2\|TestUnbackedCompletion_"` | **OK** — 9 tests PASS |
| Tests ciblés sanitizer | `go test -v ./gemini -run "TestSanitizeThought"` | **OK** |
| Suite courte | `go test -short ./...` | **OK** — `api 1.243s`, `db 0.036s`, `gemini 7.010s` |
| Fuzz quarantaine | `go test -short -fuzz=FuzzQuarantineUntaggedToolJSON -fuzztime=5s ./gemini` | **OK** — ~109 000 exécutions, aucun crash |
| Fuzz sanitizer | `go test -short -fuzz=FuzzSanitizeThoughtArtifacts -fuzztime=5s ./gemini` | **OK** — ~68 000 exécutions, aucun crash |
| Data races | `go test -race -short ./...` | **OK** — `api 8.634s`, `db 1.122s`, `gemini 26.893s`, aucune race |
| Audit unifié | `../scripts/audit_check.sh` | **OK** — gofmt, `go vet`, tests courts |

---

## 7. Limites honnêtes et suites

1. **Le benchmark reste un `n = 1` par modèle.** Les tests de propriété prouvent les invariants du proxy, pas la distribution comportementale des modèles. Le banc A/B `pass^k` de `scripts/benchmark_matrix.sh` reste nécessaire pour arbitrer une éventuelle évolution de prompt.
2. **Le sanitizer est cosmétique.** Il nettoie un artefact visible mais ne remplace ni la détection de prose courte (P0-5), ni le `finish_reason` véridique.
3. **Le lexique S7 reste un filet.** L'invariant fort est le ledger de mutations confirmées et, pour les tests, le ledger de validations confirmées. Une formulation inconnue sans chemin et sans marqueur lexical peut encore passer ; elle doit être ajoutée au corpus au prochain benchmark.
4. **La reconnaissance des commandes de validation est volontairement ciblée** (`go test`, `go vet`, `go build`, `gofmt`, `audit_check.sh`). Les autres écosystèmes (`cargo test`, `pytest`, `npm test`) pourront être ajoutés lorsque le harnais les utilisera réellement.
5. **La promotion des lectures non taggées reste étroite.** Seul un objet complet, schéma-valide et non mutateur est promu ; toute mutation non taggée est quarantinée et rejouée.
