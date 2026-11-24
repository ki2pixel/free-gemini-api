# Arbitrage indépendant — Benchmark « en vol » v3.5.7 et remédiations du proxy

> **Objet** : confronter le rapport de benchmark comportemental `Mise-a-jour-et-Reorganisation-des-Documentations-v3.5.7` au code réel de `free-gemini-api`, arbitrer ses quatre recommandations et établir les remédiations qui manquent.
> **Date** : 24 novembre 2026.
> **Sources examinées** : les quatre exports OpenCode du dossier `debug/Mise-a-jour-et-Reorganisation-des-Documentations-v3.5.7/`, la base `free-gemini-api/data/gemini.db`, `api/chat_handler.go`, `api/session_transcripts.go`, `api/observation_masker.go`, `gemini/arranger.go`, `gemini/chat.go`, `gemini/registry.go`, `gemini/schema_validator.go`, ainsi que `docs/recherches/Prompt-Engineering-vs-Middleware-Agentique.md`.
> **Méthode** : lecture contradictoire des traces, vérification des affirmations dans le code, puis tests unitaires temporaires exécutés dans les packages réels (`api`, `gemini`) sur les charges exactes des transcripts. Ces tests ont été supprimés après mesure ; aucune modification de code n'est incluse dans ce document.
> **Limite honnête** : la base `messages` ne conserve que le dernier message `user` et la sortie finale, pas le prompt delta exact envoyé à Google. Les conclusions sur le delta reposent donc sur le code, sur un test direct de `planSessionTurn` et sur la forme des raisonnements amont, pas sur une capture réseau du corps `f.req`.

---

## 0. Synthèse exécutive

1. **Les mesures factuelles du rapport sont exactes** : durées, nombres de tours, nombres d'appels d'outils, textes finaux et `finish_reason: stop` correspondent aux exports. Le rapport a donc une grande valeur d'observabilité : il capture quatre modes d'échec réels et reproductibles.
2. **Trois attributions causales ne résistent pas à l'examen** : la « collision `<thought` » d'Argon n'est pas causée par l'arranger ; la « consigne de quota » n'est pas spécifique au mode Task `[40]` (Argon, en mode Chat `[0]`, hallucine la même consigne) ; le delta vide n'est pas la seule explication de la salutation de Neon : la continuité de conversation n'est jamais vérifiée, ce qui est une cause possible et directement testable.
3. **Deux recommandations sont déjà implémentées** : la clause de permission illimitée au tour 0 existe depuis la v3.5.6 (règle 7 + directive de replay), et la promotion des tool calls fuis existe partiellement (fallback JSON). La première a échoué en conditions réelles ; la seconde est dangereuse sur une mutation tronquée.
4. **Plusieurs angles morts déterministes ne sont pas couverts par le rapport** : le greeting final accepté (Neon), la prose courte tronquée acceptée (Argon), le rapport d'achèvement hors lexique (Hydrogen), le JSON non taggé non quarantiné (Hydrogen), le `finish_reason: stop` mensonger sur un flux amont interrompu, la continuité de conversation non vérifiée, l'observabilité insuffisante et la borne mémoire implicite.
5. **Décision d'arbitrage** : accepter et durcir R1 (ancrage + continuité), rejeter R2 telle quelle (déjà fait, échec empirique, risque *Pink Elephant*), rejeter R3 pour les mutations/tronqués et l'accepter étroitement pour les lectures complètes, rejeter R4 comme cause et n'accepter qu'un sanitizer visuel étroit. Le prochain gain de fiabilité est dans l'état déterministe du proxy, pas dans une clause de prompt supplémentaire.

---

## 1. Vérification factuelle, modèle par modèle

### 1.1 `gemini-4-neon` — la salutation est un symptôme, pas la cause

**Faits observés dans l'export**

- 7 messages, 5 tours utiles, 7 appels d'outils (4 en parallèle au tour 1), durée 28,21 s.
- Tour 5 : raisonnement `The user provided a system prompt with instructions about safety... No actual user task or request is present except the system boilerplate`, puis texte `Hello! How can I help you today?`.
- La base confirme la ligne `assistant` finale `Hello! How can I help you today?` (id 1032), suivie d'aucun tour.

**Vérifications dans le code**

- Le delta envoyé pour un tour de tool result est bien le suffixe du transcript aplati, suivi de `\nAssistant: ` : c'est ce que construit `planSessionTurn` puis `effectiveTurnCue`.
- `planSessionTurn` ne compare **jamais** `e.lastConvID` à `curConvID` : il vérifie seulement que les deux sont non vides. Un test temporaire avec `lastConvID = conv-1` et `curConvID = conv-2` renvoie `reset = false` et envoie le delta. Autrement dit, le proxy peut injecter un delta dans une conversation amont qui n'est pas celle qui détient le plan.
- `isUnusableModelOutput("Hello! How can I help you today?", tools)` renvoie `false`. Aucun classifieur ne reconnaît une réinitialisation conversationnelle.
- `isUnusableModelOutput("Le plan d'action pour", tools)` renvoie également `false` (voir Argon).

**Verdict**

Le symptôme est réel, mais l'explication « le delta est trop maigre » est incomplète. Un delta maigre est normal et voulu ; le problème est qu'il est envoyé **sans preuve d'identité de conversation** et **sans ancrage de continuation**. La salutation est ensuite acceptée comme un `stop` valide, ce qui termine la session côté hôte. Il faut donc deux correctifs, pas un : continuité vérifiée + classifieur de greeting.

### 1.2 `gemini-4-hydrogen` — la fuite est une mutation tronquée, pas un appel à promouvoir

**Faits observés**

- 136,19 s, 5 appels d'outils, 4 tours avec outils.
- Tour 4 : un `write` valide pour `docs/remediation/README.md` et, dans le même message, un texte visible commençant par `{"name": "write", "content": "# 🔄 Pipeline Flow...`.
- Le texte fuité fait **938 octets**, se termine au milieu d'une phrase (`...Flux SSE Brut (avec`) et **ne contient pas de clé `path`**. Ce n'est pas un appel complet : c'est une mutation write coupée en pleine valeur.
- Tour 5 : rapport d'achèvement de 623 tokens qui affirme la réalisation de livrables jamais écrits (S7).

**Vérifications dans le code**

- `ArrangeGeminiOutputWithTools(leak, tools)` renvoie `IsToolCall = false`, `ToolCalls = 0`, `CleanText = 937` octets : le JSON non taggé est laissé dans le texte visible.
- `HasTruncatedMutationToolCall(leak)` renvoie `true` lorsque la charge est testée isolément. La détection existe donc, mais elle n'a pas empêché la fuite de devenir un texte visible dans ce tour. La cause exacte (ordre des spans, plafond de 50 candidats, ou sortie finale déjà consommée par un autre chemin) reste à instrumenter ; le fait vérifiable est que le contrat « aucun JSON d'outil dans `CleanText` » n'est pas garanti.
- `HasInvalidSchemaToolCall` ne valide que les appels **explicitement enveloppés** (`<tool_call>`) ou déjà acceptés par l'arranger ; un objet JSON nu, complet mais invalide (par exemple sans `path`), n'est pas classé : il peut donc être renvoyé comme prose.
- Le rapport final Hydrogen ne contient **aucun** des snippets actuels de `pseudoReportSnippets` : le test de correspondance sur le texte exact ne renvoie aucun marqueur. Les formes observées (`ont été entièrement réalisés`, `Résumé des Livrables Réalisés`) sont hors lexique. Le proxy a donc renvoyé `stop`, et OpenCode a conclu la session.

**Verdict**

La fuite et le S7 sont réels. La recommandation « promouvoir le JSON fuité en tool call » est **dangereuse pour ce cas précis** : promouvoir une mutation tronquée sans `path` écrirait un fichier incomplet ou échouerait de façon non déterministe. Le bon traitement est la quarantaine : classer le tour comme `untagged_mutation` ou `truncation_mid_value`, rejouer, et ne jamais exposer le JSON au client. La promotion ne peut être acceptée que pour un appel **complet, conforme au schéma et strictement lecture seule**.

### 1.3 `gemini-4-hydrogen-task` — la consigne de quota n'est pas propre au mode Task

**Faits observés**

- 41,02 s, 0 appel d'outils, synthèse de 2 448 caractères.
- Raisonnement : `Attendez, la consigne m'indique : "You have exceeded the maximum number of tool and search calls (0 remaining). Synthesize your final response..."`.

**Vérifications**

- Le plan utilisateur complet (16 290 octets dans la base) **ne contient pas** cette phrase ; il ne mentionne que la consommation de quota des tests live.
- Une recherche dans le bundle OpenCode local (`app.asar`, `opencode-cli`) ne trouve pas la phrase. Elle n'est donc pas injectée localement de façon visible par le harnais dans cette installation.
- La règle 7 de `FormatToolsInstruction` (v3.5.6) contient déjà le démenti, et la directive `unlimitedToolExecutionDirective` est déjà préfixée aux replays complets après un tour inutilisable. La recommandation R2 du rapport est donc **déjà appliquée au tour 0**.
- `gemini-4-argon`, qui est un modèle **Chat `[0]`** (`AppMode` par défaut, pas `40`), hallucine exactement la même consigne au tour 1. Le mode Task n'est donc pas nécessaire pour produire le symptôme ; rien dans ces traces ne prouve qu'il serait suffisant.

**Verdict**

La consigne se comporte comme un **prior paramétrique de fin de budget d'outils**, possiblement renforcé par une notice amont invisible, et non comme une propriété du seul mode `[40]`. Le fait qu'elle apparaisse au tour 0, avant tout appel d'outil, exclut un épuisement réel. La clause de permission illimitée existante n'a pas empêché le symptôme en v3.5.7 : ajouter une seconde clause ne peut pas être la réponse principale. Le levier déterministe est la détection/quarantaine de la sortie qui invoque ce faux quota, avec replay sur conversation vierge.

### 1.4 `gemini-4-argon` — le token `<thought` n'a pas cassé l'arranger

**Faits observés**

- 6,95 s, 0 appel d'outils, texte visible de 21 caractères : `Le plan d'action pour`.
- Le raisonnement contient la séquence brute `L'écriture du hub<thought\nL'utilisateur a présenté...` (un `<thought` **sans `>`** au milieu d'un bloc de pensée déjà ouvert).
- `finish` et `rawFinish` valent `stop` dans l'export OpenCode. Ce `stop` est le `finish_reason` **du proxy**, pas une valeur amont : `GeminiResponse` ne porte aucun champ de fin de génération (`models.go`).

**Vérifications**

- Test temporaire sur la charge exacte : `ArrangeGeminiOutput` renvoie `CleanText = "Le plan d'action pour"` et `ReasoningContent = "L'utilisateur présente... (0 restants)."`. L'arranger n'a donc **rien tronqué** : il a extrait le raisonnement et conservé le texte visible tel quel.
- Test sur `<thought>a<thought>b</thought>c</thought>visible` : `CleanText = "visible"`, `ReasoningContent = "abc"`. Les tags ouvrants imbriqués **avec `>`** sont déjà gérés par profondeur.
- Un `<thought` sans `>` n'est pas une balise : il n'est ni ouvrant ni fermant pour `thoughtTagRegex`. Le rendre « idempotent » en le traitant comme une balise ouvrirait la porte à l'absorption de prose légitime qui cite le tag (par exemple la documentation du projet).
- `isUnusableModelOutput("Le plan d'action pour", tools)` renvoie `false` : une phrase inachevée de 21 caractères est acceptée comme `stop` valide.

**Verdict**

La collision de balise n'est pas la cause du crash. La cause observable est un arrêt amont après 21 caractères de prose, que le proxy ne sait ni distinguer d'une fin normale ni classifier comme incomplet. Le correctif n'est pas dans le découpage des pensées : il est dans la détection de prose tronquée et dans la véracité de `finish_reason`.

---

## 2. Arbitrage des quatre recommandations du rapport

| # | Recommandation du rapport | Verdict | Raison courte |
| :-- | :--- | :--- | :--- |
| R1 | Ancrer les deltas courts/vides avec un rappel de contexte | **Accepter, mais réviser** | Le symptôme est réel ; la cause est la continuité non vérifiée, pas seulement la longueur du delta. Ancrer tous les deltas sans instruction utilisateur, pas seulement les vides. |
| R2 | Injecter une clause de permission illimitée au tour 0 | **Rejeter telle quelle** | Déjà implémentée en v3.5.6 ; échec empirique en v3.5.7 ; risque *Pink Elephant* documenté dans `docs/recherches/`. |
| R3 | Détecter et promouvoir les JSON de tool call fuités | **Rejeter pour mutations/tronqués ; accepter étroitement pour lectures** | La fuite observée est tronquée et sans `path` ; la promotion écrirait un fichier incomplet. Quarantaine + retry d'abord. |
| R4 | Rendre le découpage `<thought` idempotent aux ouvertures secondaires | **Rejeter comme cause ; sanitizer visuel étroit** | Les tags imbriqués avec `>` sont déjà gérés ; `<thought` sans `>` n'est pas une balise. Le vrai correctif Argon est la prose tronquée. |

### R1 — Ancrage de delta : accepté avec révision

**Ce que le rapport a raison de dire** : un tour réduit à `[Tool Result for shell]: (no output)` ne contient aucune instruction utilisateur ; si l'amont a perdu le fil, il n'a aucun moyen de le reconstruire.

**Ce qu'il faut corriger** :

1. La cause première n'est pas la taille du delta, mais l'absence d'invariant d'identité. `planSessionTurn` doit exiger `curConvID == lastConvID` **et** le même worker que le dernier tour commité. Si l'un diffère, replay complet. Le commentaire actuel (« any live conversation for this session continues the same thread ») est une hypothèse non vérifiée.
2. L'ancre doit être ajoutée à **tout delta sans instruction utilisateur**, pas seulement aux deltas de moins de N caractères : un tool result de 2 Ko peut tout autant perdre le fil.
3. Il ne faut **pas** affirmer `exit code 0` : `(no output)` ne prouve pas un succès. L'ancre doit dire « le dernier appel d'outil est terminé ; reprends le plan au prochain point » sans inventer de statut.
4. Il faut ajouter le classifieur de greeting : même avec l'ancre, un amont peut répondre `Hello!`. Ce tour ne doit jamais être un `stop` valide.

**Forme d'ancre recommandée** (positive, sans chaîne interdite) :

```text
[Continuation] Le dernier appel d'outil est terminé. Reprends le plan en cours
au prochain point non réalisé. Ne salue pas, ne redémarre pas et ne produis pas
de synthèse finale tant que des modifications ou vérifications restent à faire.
```

**Invariant** :

```text
delta_safe(session, worker, conv, flat)
  ⇔ entry existe ∧ entry.convValid
  ∧ entry.lastWorkerID == worker
  ∧ entry.lastConvID == conv
  ∧ flat == entry.lastFlat ++ δ, δ ≠ ε
  ∧ (δ contient une instruction user ∨ ancre ajoutée)
```

**Terminaison / complexité** : décision O(1) + comparaison de préfixe O(n), n ≤ 1 Mio ; aucun retry supplémentaire n'est introduit par cette règle.

### R2 — Permission illimitée au tour 0 : rejetée telle quelle

**Pourquoi elle est déjà satisfaite** : la règle 7 de `FormatToolsInstruction` dit déjà, depuis la v3.5.6, qu'il n'existe aucune limite d'appels et que toute référence à `0 remaining` est obsolète. `unlimitedToolExecutionDirective` est également préfixée aux replays complets après échec. Ajouter une troisième formulation ne change pas l'état du système.

**Pourquoi elle est probablement contre-productive** : la règle 7 cite **verbatim** la chaîne `0 remaining`. L'analyse `Prompt-Engineering-vs-Middleware-Agentique.md` documente l'effet *Pink Elephant* : nommer un motif dans une instruction négative augmente l'activation des tokens correspondants, surtout après 15 à 20 appels d'outils. Les traces v3.5.7 fournissent un test naturel : la clause est présente au tour 0, et le modèle l'hallucine quand même, y compris dans un modèle Chat `[0]` (Argon). Cela ne prouve pas la causalité du *Pink Elephant*, mais cela prouve que la clause n'est pas un remède suffisant.

**Recommandation** :

- Supprimer la chaîne littérale `0 remaining` (et `maximum number of tool`) du prompt système et des directives de replay.
- Remplacer par un contrat positif : « Tous les outils de `[AVAILABLE TOOLS]` restent actifs à chaque tour ; poursuis jusqu'à ce que les modifications et vérifications prévues soient faites. »
- Conserver la détection déterministe de la sortie qui invoque un faux quota (`isToolRefusalOutput`) et le replay sur conversation vierge.
- Ne pas ajouter de nouvelle clause ; investir dans l'état et les classifieurs.
- Tester l'hypothèse par A/B : ancien prompt vs prompt sans littéral, 3 exécutions par modèle, mesure du nombre de raisonnements contenant `0 remaining`. Si le symptôme persiste sans littéral, il est purement paramétrique/amont et seul le middleware peut le contenir.

**Invariant** : `prompt_system` ne contient aucune des chaînes `0 remaining`, `maximum number of tool`, `search calls` ; le classifieur de sortie, lui, peut les contenir (il n'est jamais envoyé au modèle).

### R3 — Promotion des tool calls fuités : rejetée pour les mutations

**Ce que la trace montre** : le JSON fuité est un `write` de 938 octets, coupé au milieu de `content`, sans `path`. Il n'est pas exécutable de façon sûre. La promotion telle que proposée transformerait une défaillance visible (texte ignoré) en corruption silencieuse (fichier tronqué) ou en erreur de schéma tardive.

**Règle proposée** :

```text
si CleanText contient un objet JSON ressemblant à un appel d'outil :
  si l'appel est complet ∧ schéma valide ∧ outil non mutateur :
      promouvoir en tool_call (lecture seule, sans effet de bord)
  sinon si l'outil est mutateur (write/edit/shell/patch) ou l'objet est tronqué :
      ne jamais promouvoir ; classer untagged_mutation / truncation_mid_value
      → retry avec directive de troncature ; après le budget → finish_reason error
  sinon (outil inconnu) :
      supprimer du texte visible et classer undeclared_tool
```

**Invariant** : pour toute réponse réussie, `CleanText` ne contient aucun objet JSON dont la clé `name`/`tool`/`function` désigne un outil déclaré. C'est testable sur les quatre transcripts et fuzzable.

**Terminaison / complexité** : scan linéaire O(n) avec un plafond de candidats ; le retry consomme le budget fonctionnel existant (4 tentatives) ; aucune boucle non bornée.

### R4 — Idempotence `<thought` : rejetée comme cause, sanitizer étroit accepté

**Ce que le code fait déjà** : `extractThoughtsAndContent` gère les tags ouvrants imbriqués **avec `>`** par profondeur, et coupe une pensée non fermée à l'ouverture d'un `<tool_call>`. Le test temporaire le confirme.

**Ce que la trace contient** : un `<thought` **sans `>`**. Ce n'est pas une balise pour le parseur ; il reste dans `ReasoningContent` et n'affecte pas `CleanText`. Il ne faut pas le traiter comme un ouvrant, sous peine de manger de la prose légitime.

**Correctif minimal** :

- Ajouter un sanitizer de **contenu visible** qui supprime les préfixes orphelins `<thought` / `<thinking` non suivis de `>` s'ils apparaissent hors d'un bloc de pensée. Ne pas toucher au raisonnement, qui est un artefact d'observabilité.
- Ajouter un test de non-régression avec la charge Argon exacte : `CleanText == "Le plan d'action pour"`, `ReasoningContent` contient le token brut, `IsToolCall == false`.
- Le vrai correctif Argon est ailleurs : prose courte inachevée + `finish_reason` véridique (voir A2/A5).

---

## 3. Recommandations déjà satisfaites — ne pas réimplémenter

Le rapport et l'analyse antérieure `Prompt-Engineering-vs-Middleware-Agentique.md` proposent plusieurs mesures qui sont **déjà dans le code v3.5.7**. Les rejouer créerait de la dette et des tests redondants.

| Mesure | État v3.5.7 | Preuve dans le code |
| :--- | :--- | :--- |
| Validation JSON Schema Draft 2020-12 contre `req.Tools` | **Faite** | `ArrangeGeminiOutputWithTools`, `CompiledToolCatalog`, `HasInvalidSchemaToolCall` |
| Rejet de l'auto-fermeture d'une mutation coupée en milieu de valeur | **Faite** | `shouldRejectMidValueRepair`, `HasTruncatedMutationToolCall`, retry `mid_value_truncation` |
| Correction du décalage UTF-8 des apostrophes typographiques | **Faite** | `findUpstreamDisclaimerOffset` utilise `['’]` sur la chaîne originale |
| `index` explicite dans les deltas SSE `tool_calls` | **Faite** | `OpenAIToolCall.Index *int` + boucle d'émission |
| Cadence Read-Parallel / Write-Serial, `path` en premier | **Faite** | Règle 3 et règle 4 de `FormatToolsInstruction` |
| Masquage des observations anciennes | **Faite** | `MaskOldToolObservationsWithOverhead`, seuils 60 %, 6 observations, 12 messages |
| Coupe-circuit anti-boucle | **Faite** | `DoomLoopTracker`, `ActionHalt`, `ActionWarnRepeat`, `ActionWarnPassiveRead` |
| Métriques de réparation/rejet | **Faite** | `RecordArrangerRejection`, `RecordRefusalRetry`, `RecordGroundTruthDirective` |
| Clause de permission illimitée au tour 0 | **Faite (v3.5.6)** | Règle 7 + `unlimitedToolExecutionDirective` |
| Détection des pseudo-rapports | **Partielle** | `pseudoReportSnippets` couvre Challenge 9, mais pas la forme v3.5.7 (`ont été entièrement réalisés`) |
| Promotion des tool calls non taggés | **Partielle et risquée** | Fallback `findFirstJSONToolCall` pour les appels complets ; pas de quarantaine des mutations tronquées |

**Arbitrage avec l'analyse antérieure** : sur les six points de `Prompt-Engineering-vs-Middleware-Agentique.md`, les points 1 à 5 (décalage UTF-8, auto-fermeture mi-valeur, `index` SSE, validation de schéma, cadence Read-Parallel / Write-Serial) sont implémentés en v3.5.7. Le point 6 — retirer le littéral `0 remaining` pour éviter l'effet *Pink Elephant* — reste ouvert et est renforcé par les traces v3.5.7. Le nouveau rapport contredit ce point en proposant une clause de permission supplémentaire ; l'arbitrage retient la position antérieure : **hygiène de prompt + détection déterministe**, pas une clause de plus.

**Conséquence d'arbitrage** : la priorité n'est pas d'ajouter les mesures 1 à 9, mais de fermer les trous de contrat autour d'elles.

---

## 4. Angles morts déterministes non couverts par le rapport

Ces angles morts sont indépendants des quatre recommandations. Ils sont tous vérifiables par un test unitaire sur les transcripts existants.

### A1 — Le greeting final est accepté comme un `stop` valide (Neon)

**Constat** : `isUnusableModelOutput("Hello! How can I help you today?", tools)` renvoie `false`. Le tour est donc commité, `finish_reason: stop` est émis, et OpenCode passe en `idle`.

**Pourquoi c'est grave** : un greeting n'est jamais une réponse finale à une requête qui déclare des outils. Il signale que l'amont a perdu le contexte ou a réinitialisé la conversation. L'accepter transforme une perte de contexte récupérable en fin de session silencieuse.

**Correctif** : classifieur `isConversationalReset` appliqué uniquement aux requêtes porteuses d'outils et sans tool call. Sur détection : replay complet avec ancre de continuation ; après le budget, `finish_reason: error`. Ne pas appliquer aux requêtes sans outils, où un greeting peut être une réponse légitime.

### A2 — La prose courte inachevée est acceptée (Argon)

**Constat** : `Le plan d'action pour` (21 caractères, pas de ponctuation finale) est accepté comme `stop`. `isUnusableModelOutput` ne voit ni refus, ni pseudo-rapport, ni vide.

**Pourquoi c'est grave** : c'est le symptôme d'un flux amont coupé après le bloc de pensée. L'hôte croit que le modèle a terminé et passe en `idle` ; aucune reprise n'est tentée.

**Correctif** : prédicat `isIncompleteFinalProse` (voir P0-5) + journalisation du nombre de snapshots amont et de la longueur visible. Après le budget de retries, `finish_reason: error`, jamais `stop`.

### A3 — Le rapport d'achèvement S7 est hors lexique (Hydrogen)

**Constat** : le texte final contient `ont été entièrement réalisés`, `Résumé des Livrables Réalisés`, `dans le working tree`, `conformément au plan d'action validé`. Aucun snippet actuel de `pseudoReportSnippets` ne correspond. Le proxy renvoie `stop`.

**Pourquoi c'est grave** : le S7 est précisément le mode d'échec que le proxy prétend bloquer depuis la v3.5.5. Un lexique fermé de conjugaisons est structurellement incomplet : il suffit d'une flexion ou d'un synonyme pour passer.

**Correctif** : deux niveaux.
1. **Extension du lexique** avec les formes observées (français et anglais), sans prétendre à l'exhaustivité.
2. **Preuve d'évidence** : un ledger par session des chemins effectivement mutés (write/edit) et des chemins mentionnés dans les commandes shell. Une affirmation d'achèvement qui cite un chemin jamais muté est `unbacked_completion`, même si sa formulation est inconnue. C'est l'invariant fort ; le lexique n'est qu'un filet.

### A4 — Le JSON d'outil non taggé n'est pas quarantiné (Hydrogen)

**Constat** : `HasInvalidSchemaToolCall` ne considère que les appels explicitement enveloppés dans `<tool_call>`. Un objet JSON nu, complet mais invalide, ou tronqué, peut rester dans `CleanText` et être renvoyé comme prose. C'est exactement la fuite observée.

**Pourquoi c'est grave** : l'hôte ignore le JSON, ne l'exécute pas, et le tour se termine avec un travail partiel. Si le JSON était promu sans contrôle, on risquerait une écriture tronquée. Les deux comportements sont mauvais : il faut une troisième voie, la quarantaine avec retry.

**Correctif** : `quarantineUntaggedToolJSON` (voir P0-3). Invariant : aucun objet JSON désignant un outil déclaré ne subsiste dans `CleanText`.

### A5 — `finish_reason: stop` n'est pas véridique sur un flux coupé (Argon, Neon)

**Constat** : `GeminiResponse` ne contient aucun champ de fin de génération. Le proxy déduit `stop` du fait que l'arranger n'a pas trouvé de tool call. Un flux amont interrompu après 21 caractères produit donc un `stop` parfaitement mensonger.

**Pourquoi c'est grave** : le contrat OpenAI SSE promet que `stop` signifie une génération terminée. OpenCode mappe `stop` sur un succès et `error` sur une erreur ; un `stop` mensonger supprime toute reprise côté hôte. Le changelog v3.5.6 a déjà corrigé ce point pour quatre chemins d'erreur, mais pas pour la prose courte ou le greeting.

**Correctif** : élargir la barrière terminale (voir P0-5). Tout tour porteur d'outils qui n'émet ni tool call ni réponse finale validée doit finir en `error` après les retries, jamais en `stop`.

### A6 — La continuité de conversation n'est jamais vérifiée (Neon)

**Constat** : `planSessionTurn` accepte n'importe quel `curConvID` non vide comme continuation du dernier tour commité. Le test temporaire avec `conv-2` renvoie `reset = false` et un delta. Le proxy peut donc envoyer un suffixe dans une conversation amont qui ne contient pas le plan.

**Pourquoi c'est grave** : c'est la classe de bug la plus dangereuse du delta : le prompt envoyé est syntaxiquement valide, mais sémantiquement orphelin. L'amont répond alors avec le système seul, d'où la salutation.

**Correctif** : invariant d'identité stricte (voir P0-1), avec repli complet en cas de doute. Le coût est un replay supplémentaire ; le bénéfice est l'élimination d'une classe entière de pertes de contexte.

### A7 — L'observabilité ne permet pas de distinguer les causes

**Constat** : la table `messages` journalise le dernier message `user` (le plan), pas le prompt envoyé ni le delta ; `GeminiResponse` n'expose pas la cause de fin ; les logs ne disent pas si le tour a été un delta ou un replay complet, ni pourquoi. Le diagnostic Neon/Argon repose sur une inférence.

**Pourquoi c'est grave** : sans ces champs, chaque nouvelle trace demandera la même archéologie. Le rapport a dû reconstruire les causes à partir des raisonnements, ce qui a produit trois attributions erronées.

**Correctif** : journal structuré par tour, sans contenu brut : hash de session, modèle, longueur du prompt, longueur du delta, raison de replay (`new`, `conv_mismatch`, `worker_mismatch`, `prefix_mismatch`, `force_full`), hash de la conversation amont, nombre de snapshots, longueur brute finale, longueur arrangée, nombre de tool calls, classification, `finish_reason`. Ajouter des compteurs Prometheus par classe. C'est un prérequis du banc A/B.

### A8 — La borne mémoire de `lastFlat` n'est pas explicite

**Constat** : `maxTrackedSessions = 256` et `lastFlat` peut atteindre `maxPromptBytes = 1 Mio`. Le pire cas théorique est donc d'environ 256 Mio de transcripts retenus, hors overhead Go.

**Pourquoi c'est grave** : le proxy est un service longue durée ; une borne non explicite devient une fuite mémoire sous charge multi-sessions.

**Correctif** : budget octets global (par exemple 64 Mio) avec éviction LRU ; si l'entrée est évincée, le prochain tour fait un replay complet. Invariant : `Σ |lastFlat| ≤ budget` ; l'éviction est toujours sûre car elle dégrade vers le replay complet, jamais vers un delta incorrect.

---

## 5. Remédiations prioritaires — pseudocode, invariants, terminaison

> Le pseudocode ci-dessous est volontairement non compilable : il fixe les contrats et les invariants, pas l'implémentation Go. Les noms de fonctions existantes sont cités pour le point d'ancrage.

### P0-1 — Continuité de conversation et ancrage des deltas

**Fichiers cibles** : `api/session_transcripts.go`, `api/chat_handler.go`.

```text
// Invariant I1 : un delta n'est envoyé que si l'identité de session est prouvée.
fonction planSessionTurn(session, worker, conv, flat):
    e ← état[session]
    si e absent ou !e.convValid ou e.lastFlat vide:
        retourner (flat, RESET_NEW)
    si e.lastWorkerID ≠ worker:
        retourner (flat, RESET_WORKER)          // le worker a changé : replay complet
    si e.lastConvID ≠ conv:
        retourner (flat, RESET_CONV)            // conversation différente : replay complet
    si flat == e.lastFlat:
        retourner (flat, RESET_IDENTICAL)       // retry client : replay complet
    si flat commence par e.lastFlat:
        δ ← flat[|e.lastFlat|:]
        si δ contient une instruction user:
            retourner (δ, DELTA)
        sinon:
            retourner (ANCRE_CONTINUATION + δ, DELTA_ANCRE)
    retourner (flat, RESET_DIVERGENT)

// Invariant I1b : l'ancre ne fabrique aucun statut.
ANCRE_CONTINUATION =
  "[Continuation] Le dernier appel d'outil est terminé. Reprends le plan en cours
   au prochain point non réalisé. Ne salue pas, ne redémarre pas et ne produis pas
   de synthèse finale tant que des modifications ou vérifications restent à faire."
```

**Points d'attention** :

- `curConvID` provient de `CurrentConversationIDFor(sessionKey, sendModel)` ; il faut aussi transmettre `wWorker.ID` à `planSessionTurn`.
- Le replay complet doit appeler `ResetConversationStateSession` avant d'envoyer `msgFlat`, comme le fait déjà le chemin `needReset`.
- L'ancre doit être ajoutée **avant** le delta, pas après, pour que le modèle la lise comme un cadre de continuation.
- Ne pas inclure l'ancre dans `lastFlat` : elle est éphémère et ne doit pas provoquer un faux `prefix_mismatch` au tour suivant.
- Ajouter un compteur `delta_replay_total{reason=...}` pour mesurer le coût.

**Terminaison** : aucune boucle ; décision O(1), comparaison de préfixe O(n), n ≤ 1 Mio.

**Tests** :

- `planSessionTurn(session, worker-1, conv-2, flat)` → `RESET_CONV`.
- `planSessionTurn(session, worker-2, conv-1, flat)` → `RESET_WORKER`.
- `planSessionTurn(session, worker-1, conv-1, flat + toolResult)` → `DELTA_ANCRE` et le prompt commence par `[Continuation]`.
- Rejouer la séquence Neon : le tour 5 ne doit plus pouvoir partir en delta orphelin.

### P0-2 — Classifieur de réinitialisation conversationnelle (greeting)

**Fichier cible** : `api/session_transcripts.go`.

```text
// Invariant I2 : sur une requête avec outils, un greeting sans tool call
// n'est jamais une réponse finale.
fonction isConversationalReset(clean, tools):
    si tools vide: faux
    si clean vide: faux
    t ← minuscule(clean)
    si |t| > 160: faux
    si t contient "`", "/", "\\", "http", "{" ou "}": faux
    si t correspond à un motif de salutation:
        "hello", "hi ", "hey", "bonjour", "salut", "good morning",
        "how can i help", "comment puis-je vous aider",
        "que puis-je faire pour vous", "what can i do for you"
        → vrai
    si |t| < 80 et t finit par "?" ou "!" et contient "aide"/"help"/"assist":
        → vrai
    faux
```

**Traitement** : si `isConversationalReset` et aucun tool call → classer `conversational_reset`, replay complet avec ancre, consommer le budget fonctionnel ; après épuisement → `finish_reason: error`.

**Terminaison / complexité** : O(n) sur le texte, une seule passe ; aucun retry supplémentaire au-delà du budget existant.

**Tests** : les trois textes `Hello! How can I help you today?`, `Bonjour ! Comment puis-je vous aider ?`, `Le plan d'action pour` doivent être classés respectivement `greeting`, `greeting`, `incomplete` (pas greeting).

### P0-3 — Quarantaine des JSON d'outils non taggés

**Fichiers cibles** : `gemini/arranger.go`, `gemini/schema_validator.go`, `api/chat_handler.go`.

```text
// Invariant I3 : CleanText ne contient aucun objet JSON désignant un outil déclaré.
fonction quarantineUntaggedToolJSON(clean, catalogue):
    pour chaque objet JSON équilibré obj de clean:
        nom ← obj.name | obj.tool | obj.function
        si nom ∉ catalogue: continuer
        si !objetComplet(obj) ou !valideSchema(obj, catalogue):
            retourner (cleanSans(obj), MUTATION_INVALIDE)
        si estMutateur(nom):
            retourner (cleanSans(obj), MUTATION_NON_TAGEE)
        sinon:
            promouvoir(obj)                  // lecture seule, sûre
    retourner (clean, OK)

// Dans le handler, après ArrangeGeminiOutputWithTools :
//   si classification ∈ {MUTATION_INVALIDE, MUTATION_NON_TAGEE, TRONQUÉ}:
//       retry avec directive de troncature/schéma (budget fonctionnel)
//   si après le budget la classification persiste:
//       finish_reason = error ; ne jamais renvoyer le JSON au client.
```

**Pourquoi la promotion est conditionnée** : le fallback existant `findFirstJSONToolCall` peut promouvoir un appel complet et schéma-valide. On conserve ce comportement pour les outils **non mutateurs**. Pour `write`, `edit`, `shell`, `patch`, `multiedit`, `apply_patch`, la promotion est interdite : une mutation non taggée est traitée comme une troncature jusqu'à preuve du contraire.

**Terminaison / complexité** : scan linéaire O(n) avec plafond de candidats ; le retry est borné par `maxToolChatAttempts = 4`.

**Tests** :

- La fuite Hydrogen exacte (938 octets, sans `path`) → `MUTATION_INVALIDE`, `IsToolCall = false`, `CleanText` ne contient plus le JSON.
- Un `read` complet et schéma-valide non taggé → promu en tool call.
- Un `write` complet et schéma-valide non taggé → `MUTATION_NON_TAGEE`, jamais promu.
- Fuzz : aucun panic, aucun objet déclaré dans `CleanText` après quarantaine.

### P0-4 — Preuve d'évidence pour les affirmations d'achèvement (S7)

**Fichiers cibles** : `api/session_transcripts.go`, `api/chat_handler.go`.

```text
// Invariant I4 : une affirmation d'achèvement qui cite un chemin doit être
// adossée à une mutation observée dans la session.
structure SessionLedger:
    mutations: ensemble de chemins normalisés   // mutations CONFIRMÉES
    pending:   map tool_call_id → chemins        // appels émis, résultat non encore vu
    failed:    ensemble de chemins               // résultat en erreur
    lectures:  ensemble de chemins normalisés
    plafond:   256 chemins, éviction LRU

fonction recordToolCall(session, call):
    chemins ← vide
    si call.name ∈ {write, edit, multiedit, patch, apply_patch}:
        chemins.ajouter(call.arguments.path)
    si call.name == shell:
        chemins ← analyserCommande(call.arguments.command)  // git mv, mkdir -p, mv, cp, rm, >
    si call.name == read:
        ledger.lectures.ajouter(call.arguments.path)
    ledger.pending[call.id] ← chemins

fonction recordToolResult(session, callID, result):
    chemins ← ledger.pending[callID]
    si result.succès:
        ledger.mutations.ajouter(chemins)
    sinon:
        ledger.failed.ajouter(chemins)
    ledger.pending.supprimer(callID)

fonction isUnbackedCompletion(clean, tools, ledger):
    si tools vide: faux
    si clean contient un tool call: faux
    si !estClaimAchèvement(clean): faux
    chemins ← extraireCheminsBacktickés(clean)   // *.md, *.go, docs/..., free-gemini-api/...
    si chemins vide:
        retourner correspondanceMarqueurs(clean) // lexique élargi, filet de sécurité
    pour chaque p dans chemins:
        si p ∉ ledger.mutations:
            retourner (vrai, p)
    (faux, vide)

// Traitement : retry avec directive anti-S7 ; après le budget → finish_reason error.
```

**Preuve de mutation confirmée** : le ledger n'enregistre pas seulement l'appel émis, mais la mutation **confirmée par le tool result** (succès explicite, absence d'erreur). Un `write` tenté puis refusé (`oldString` introuvable, schéma invalide, troncature) reste `failed` et ne peut pas étayer un claim d'achèvement. La corrélation se fait par `tool_call_id` entre l'appel assistant et le message `tool` du tour suivant.

**Extension du lexique** (formes observées, sans prétendre à l'exhaustivité) :

```text
"ont été entièrement réalisés", "ont été entièrement réalisées",
"ont été entièrement exécutés", "ont été entièrement exécutées",
"entièrement réalisé", "entièrement réalisée",
"résumé des livrables", "livrables réalisés",
"dans le working tree", "conformément au plan d'action validé",
"tous les fichiers ... ont été", "l'ensemble des ... a été",
"the task is complete", "all files have been updated",
"the plan has been fully executed", "everything is in place"
```

**Garde anti-faux-positif** : ne pas classifier une question, un futur (`sera`, `will be`), une demande (`dois`, `reste à`), ni un texte qui contient un tool call. Le classifieur ne s'applique qu'aux requêtes avec outils et sans tool call, comme les classifieurs existants.

**Terminaison / complexité** : extraction O(n) ; comparaison O(|chemins|) avec un map ; ledger plafonné à 256 entrées ; retry borné.

**Tests** :

- Le texte final Hydrogen exact → `unbacked_completion`, bloqué, `finish_reason error` après le budget.
- Un texte qui cite `docs/README.md` après un `write` réel sur ce chemin → accepté.
- Un texte qui cite `AGENTS.md` sans aucune mutation de `AGENTS.md` → bloqué, même si la formulation est inconnue.
- Un texte sans chemin mais avec `ont été entièrement réalisés` → bloqué par le lexique.
- Un texte futur (`les fichiers seront mis à jour`) → non classé.

### P0-5 — Prose incomplète et `finish_reason` véridique

**Fichiers cibles** : `api/session_transcripts.go`, `api/chat_handler.go`.

```text
// Invariant I5 : sur une requête avec outils, finish_reason = stop
// ⇒ tool call validé ∨ réponse finale validée.
fonction isIncompleteFinalProse(clean, tools):
    si tools vide: faux
    si clean contient un tool call: faux
    r ← runes(clean)
    si |r| < 10 ou |r| > 80: faux
    si clean contient "\n", "`", "/", ".", ":" ou "http": faux
    si dernier rune ∈ {".", "!", "?", "…", "。", "！", "？"}: faux
    vrai

// Variante complémentaire, si l'amont expose un signal de fin :
//   si le flux s'est arrêté juste après "</thought>" avec < 80 runes visibles
//   → suspected_upstream_stop.
```

**Traitement** :

```text
si tools non vide et aucun tool call:
    si isConversationalReset ou isIncompleteFinalProse ou suspected_upstream_stop:
        retry avec ancre de continuation (budget fonctionnel)
    si le budget est épuisé:
        émettre finish_reason = "error" (newStreamErrorChunk), jamais "stop"
```

**Terminaison / complexité** : O(n), un seul prédicat par tour ; le budget est celui de `maxToolChatAttempts`.

**Tests** :

- `Le plan d'action pour` → `incomplete`, retry, puis `error`.
- `Done.` → non classé (longueur < 10).
- `README.md` → non classé (contient `.`).
- `Je vais maintenant lire le fichier` → classé (prose de progression sans appel), ce qui est le comportement anti-S7 souhaité.
- Vérifier que le chunk terminal a bien `finish_reason: "error"`.

### P0-6 — Hygiène de prompt : retirer l'amorce *Pink Elephant*

**Fichiers cibles** : `gemini/arranger.go`, `api/chat_handler.go`, tests.

```text
// Invariant I6 : le prompt système ne contient aucune des chaînes interdites.
interdits = {"0 remaining", "maximum number of tool", "search calls"}
règle 7 positive =
  "7. Tool access is continuous for the whole session. Every tool listed in
   [AVAILABLE TOOLS] is active on every turn. Keep invoking tools until all
   planned file modifications and verifications are complete."
```

- Supprimer le littéral de la règle 7 et de toute directive de replay.
- Conserver les littéraux dans `refusalSnippets` : ce sont des motifs de **sortie**, jamais envoyés au modèle.
- Mettre à jour `TestFormatToolsInstruction_...` pour exiger l'**absence** des chaînes interdites, au lieu de tolérer `0 remaining` dans la clause de démenti.
- Mesurer l'effet par A/B (section 7).

### P1-1 — Routage du mode Task `[40]` pour les charges agentiques

**Constat** : `hydrogen-task` a produit 0 tool call et une synthèse ; le mode Task est un planificateur amont dont le contrat d'outils externe n'est pas maîtrisé. Argon, en mode Chat, a échoué pour une autre raison (faux quota), mais le mode Task n'a pas démontré sa valeur sur cette charge.

**Recommandation** : pour les requêtes qui déclarent des outils, router par défaut `*-task` vers l'alias Chat de la même famille (`hydrogen-task` → `hydrogen`, `neon-task` → `neon`), avec un flag explicite `ALLOW_TASK_MODE_FOR_TOOLS=true` pour revenir au comportement actuel. Documenter le mode Task comme « planification seule » tant qu'un benchmark multi-run n'a pas démontré sa fiabilité en boucle d'outils. Ce n'est pas un correctif de prompt ; c'est une décision de routage produit fondée sur les traces.

**Invariant** : une requête avec outils ne part jamais vers `AppMode = 40` sans opt-in explicite.

**Terminaison / complexité** : O(1) au routage ; aucun impact sur les boucles.

### P1-2 — Observabilité de fin de flux et métriques de classe

**Fichiers cibles** : `gemini/chat.go`, `api/chat_handler.go`, `api/metrics.go`.

- Ajouter à `GeminiResponse` un champ `StopCause` (si le parseur amont peut l'extraire) ou, à défaut, `SnapshotCount`, `VisibleTextLen`, `EndedAfterThought`.
- Journaliser une ligne structurée par tour (voir A7) et exposer :
  - `gemini_turn_classification_total{class="tool_call|greeting|incomplete|refusal|pseudo_report|unbacked_completion|untagged_mutation|ok"}`
  - `gemini_delta_replay_total{reason="new|conv_mismatch|worker_mismatch|prefix_mismatch|identical|forced"}`
  - `gemini_upstream_suspected_stop_total{model=...}`
  - `gemini_quarantine_total{kind="untagged_mutation|schema_invalid|truncated"}`
- Ne jamais journaliser le prompt brut ni les arguments d'outils (données potentiellement sensibles) ; journaliser des longueurs, des hashes et des noms de fichiers normalisés.

### P2-1 — Sanitizer visuel des artefacts `<thought`

**Fichier cible** : `gemini/arranger.go` (nettoyage de `CleanText` uniquement).

```text
fonction sanitizeThoughtArtifacts(clean):
    remplacer "<thought" et "<thinking" non suivis de ">" par ""
    ne jamais toucher à ReasoningContent
```

**Test exact Argon** : `CleanText == "Le plan d'action pour"`, `ReasoningContent` conserve le token brut, `IsToolCall == false`. Ce correctif est cosmétique et ne remplace pas P0-5.

### P2-2 — Banc A/B et pass^k

Le rapport est un **n = 1 par modèle**. Il ne permet pas de distinguer la variance du modèle d'un effet de prompt. Pour arbitrer R2 et P1-1, exécuter :

```text
pour chaque modèle ∈ {neon, hydrogen, hydrogen-task, argon} :
    pour chaque variante ∈ {prompt_actuel, prompt_sans_littéral} :
        pour run ∈ 1..3 :
            worktree propre
            même plan v3.5.7
            mesurer : tool calls, phases complétées, faux quota, greeting,
                      prose courte, claims S7, finish_reason, durée
            calculer pass^3
```

Critères de décision :

- Si le prompt sans littéral réduit significativement les occurrences de `0 remaining` → adopter P0-6.
- Si le taux est identique → le symptôme est paramétrique/amont ; ne pas ajouter de clause, renforcer P0-2/P0-5.
- Si `hydrogen-task` reste à 0 tool call sur 3 runs → appliquer P1-1 par défaut.
- Si `argon` reste bloqué à < 80 runes sur 3 runs → le problème est amont ; P0-5 est la seule protection.

---

## 6. Preuves de terminaison, de complexité et de sûreté mémoire

### 6.1 Terminaison de la boucle de retry

La boucle de génération est bornée par trois budgets indépendants :

```text
B_fonctionnel = maxToolChatAttempts = 4          (requêtes avec outils)
B_vide        = maxEmptyGenerationRetries = 2   (sortie vide / thought-only)
B_transport   = maxTransportRetries = 2         (1097, stall)
```

Les chemins `vide` et `transport` font `attempt--` après avoir incrémenté leur compteur. Chaque compteur est plafonné, donc chaque `attempt--` ne peut se produire qu'un nombre fini de fois :

```text
itérations_total ≤ B_fonctionnel + B_vide + B_transport = 8
```

Chaque itération effectue au plus deux appels amont : `executeWithRetry` tente une fois, puis une seule reprise après auto-guérison de session ; les erreurs 1097 et stall ne sont pas réessayées à ce niveau. Chaque appel est borné par `upstreamRequestTimeout = 120 s` et par le stall guard de 45 s. La terminaison est donc garantie, indépendamment du modèle.

**Règle pour les nouveaux classifieurs** : tout nouveau retry doit soit consommer `B_fonctionnel`, soit introduire un budget dédié plafonné et l'inclure dans la borne ci-dessus. Aucun retry ne doit être illimité ou dépendre d'une condition non monotone.

### 6.2 Complexité par tour

```text
B = taille du transcript aplati, B ≤ maxPromptBytes = 1 Mio
n = taille de la sortie brute du tour, n ≤ B

flatten / masquage          O(B + nombre_de_messages)
planSessionTurn             O(B)      (comparaison de préfixe)
arranger / parse            O(n)      (scans bornés, plafond de candidats)
quarantaine JSON            O(n)      (scan linéaire, plafond)
classifieurs                O(n)      (quelques passes)
ledger                      O(|chemins|) avec map, |chemins| ≤ 256
```

Aucune étape n'est quadratique sur la taille du prompt. Les seuls scans répétés sont bornés par des constantes (50 candidats JSON, 4 tentatives, 256 sessions).

### 6.3 Sûreté mémoire

- `maxTrackedSessions = 256` ; `lastFlat` ≤ 1 Mio ⇒ pire cas ~256 Mio. Ajouter un budget global (P1-2/A8) et évincer LRU ; l'éviction force un replay complet, jamais un delta incorrect.
- `SessionLedger` : 256 chemins par session, 256 sessions ⇒ borne explicite ; éviction LRU.
- Les structures partagées (`ModelConfig`, registre) ne sont jamais mutées par requête : l'escalade de thinking se fait par **nom de modèle** (`EscalateThinkingModel`), pas par écriture dans l'entrée partagée. C'est conforme à la règle projet « zéro data race, zéro mutation de pointeur global ».
- `sessionTurnMu` protège la map ; le ledger doit être protégé par le même mutex ou par un mutex par entrée. Les lectures concurrentes doivent copier le snapshot nécessaire, pas exposer la map.

### 6.4 Invariants globaux

```text
I1  delta ⇒ identité worker + conversation + préfixe exact
I2  tools ∧ ¬tool_call ∧ greeting ⇒ retry, jamais stop
I3  CleanText ne contient aucun JSON d'outil déclaré
I4  claim(path) ⇒ path ∈ mutations confirmées par le tool result
I5  finish_reason = stop ⇒ tool_call ∨ réponse finale validée
I6  prompt système sans "0 remaining" / "maximum number of tool" / "search calls"
I7  tout retry est couvert par un budget plafonné et monotone
```

---

## 7. Plan de tests de non-régression

### 7.1 Tests unitaires sur les transcripts exacts

| Test | Entrée | Attendu |
| :--- | :--- | :--- |
| `TestDeltaRejectsForeignConversation` | `lastConv=conv-1`, `curConv=conv-2`, préfixe valide | `RESET_CONV`, prompt complet |
| `TestDeltaRejectsForeignWorker` | worker différent | `RESET_WORKER` |
| `TestDeltaAnchorsToolOnlyTurn` | delta `[Tool Result for shell]: (no output)` | prompt commence par `[Continuation]` |
| `TestNeonGreetingIsUnusable` | `Hello! How can I help you today?` + tools | `conversational_reset`, retry |
| `TestArgonShortProseIsIncomplete` | `Le plan d'action pour` + tools | `incomplete`, retry, puis `error` |
| `TestArgonThoughtArtifact` | charge brute Argon | `CleanText` = 21 caractères, `ReasoningContent` contient `<thought`, `IsToolCall=false` |
| `TestHydrogenLeakQuarantine` | fuite 938 octets sans `path` | `MUTATION_INVALIDE`, pas de tool call, `CleanText` sans JSON |
| `TestHydrogenCompletionUnbacked` | rapport final exact | `unbacked_completion`, bloqué |
| `TestHydrogenCompletionBacked` | même rapport + writes correspondants dans le ledger | accepté |
| `TestQuotaPhraseIsDetected` | sortie citant `0 remaining` | refus classé, replay |
| `TestPromptHasNoPinkElephant` | `FormatToolsInstruction` | aucune chaîne interdite |
| `TestFinishReasonErrorOnUnusableToolTurn` | prose courte + tools | chunk terminal `error` |

### 7.2 Tests de propriété et fuzz

- **Propriété 1** : pour toute sortie brute, `ArrangeGeminiOutputWithTools` ne renvoie jamais un `ToolCall` mutateur auto-réparé depuis une valeur coupée (`shouldRejectMidValueRepair`).
- **Propriété 2** : après quarantaine, `CleanText` ne contient aucun objet dont `name` ∈ catalogue.
- **Propriété 3** : `planSessionTurn` ne renvoie `DELTA` que si le préfixe exact et l'identité sont vérifiés ; sinon `RESET`.
- **Propriété 4** : la boucle de retry termine en ≤ 8 itérations pour toute combinaison de budgets.
- **Fuzz** : JSON tronqué, balises imbriquées, apostrophes typographiques, contenu avec `<tool_call>` en prose, objets concaténés, `content` de 1 Mio. Aucun panic, aucune écriture promue.

### 7.3 Course et concurrence

- `go test -race -short ./...` avec N requêtes concurrentes sur la même `sessionKey` et des `sessionKey` distinctes.
- Vérifier que le ledger et `sessionTurnState` sont protégés ; aucun accès non synchronisé.
- Vérifier que l'escalade de thinking ne mute pas le `ModelConfig` partagé (test existant de non-mutation du registre).

### 7.4 Golden prompts et tests de contrat

- Le prompt système contient le contrat positif, la cadence Read-Parallel/Write-Serial, `path` en premier, et **pas** les chaînes interdites.
- Le chunk terminal d'un tour inutilisable porte `finish_reason: "error"`, pas `"stop"`.
- Les deltas SSE `tool_calls` portent `index` et jamais `tool_calls: []`.

### 7.5 Validation de bout en bout

```text
cd free-gemini-api
test -z "$(gofmt -l .)"
go vet ./...
go test -short ./...
go test -race -short ./...
./scripts/audit_check.sh
./scripts/live_middleware_smoke.sh
```

Puis rejouer les quatre sessions v3.5.7 avec le même plan et mesurer :

- nombre de tool calls par modèle ;
- nombre de greetings, de proses courtes et de claims S7 ;
- nombre de faux quotas dans le raisonnement ;
- `finish_reason` final ;
- pass^3 sur 3 runs.

---

## 8. Décision finale et séquencement

### 8.1 Tableau de décision

| Recommandation | Décision | Justification |
| :--- | :--- | :--- |
| R1 — ancrage des deltas | **Accepter avec révision** | Utile, mais insuffisant seul : ajouter l'invariant d'identité worker/conversation et ancrer tous les deltas sans instruction utilisateur. |
| R2 — clause de permission illimitée | **Rejeter telle quelle** | Déjà implémentée, échec empirique v3.5.7, risque *Pink Elephant*. Remplacer par l'hygiène de prompt + détection déterministe + routage Task. |
| R3 — promotion des tool calls fuités | **Rejeter pour mutations/tronqués ; accepter pour lectures complètes** | La fuite observée est tronquée et sans `path` ; la promotion corromprait. Quarantaine + retry d'abord. |
| R4 — idempotence `<thought` | **Rejeter comme cause ; sanitizer visuel étroit** | Les tags imbriqués avec `>` sont déjà gérés ; `<thought` sans `>` n'est pas une balise. Le vrai correctif est la prose courte. |
| A1 — greeting | **Ajouter P0** | Termine silencieusement la session Neon. |
| A2 — prose courte | **Ajouter P0** | Termine silencieusement la session Argon. |
| A3 — S7 hors lexique | **Ajouter P0** | Termine silencieusement la session Hydrogen. |
| A4 — JSON non taggé | **Ajouter P0** | Fuite visible et travail partiel. |
| A5 — `finish_reason` mensonger | **Ajouter P0** | Empêche l'hôte de reprendre. |
| A6 — continuité de conversation | **Ajouter P0** | Cause racine probable de Neon. |
| A7 — observabilité | **Ajouter P1** | Prérequis du diagnostic et du banc A/B. |
| A8 — borne mémoire | **Ajouter P1** | Évite une fuite sous charge multi-sessions. |

### 8.2 Séquencement proposé

**Vague P0 — fiabilité immédiate, faible risque produit**

1. P0-1 continuité + ancrage.
2. P0-2 greeting.
3. P0-3 quarantaine JSON non taggé.
4. P0-4 ledger + preuve d'achèvement.
5. P0-5 prose incomplète + `finish_reason` error.
6. P0-6 hygiène de prompt.

Chaque correctif est indépendant et testable sur les transcripts existants. Les tests P0 doivent être ajoutés **avant** le correctif (TDD), conformément au mode de travail du dépôt.

**Vague P1 — mesure et routage**

7. A7 observabilité structurée + métriques.
8. A8 budget mémoire.
9. P1-1 routage Task → Chat pour les outils.
10. Banc A/B et pass^3.

**Vague P2 — cosmétique et durcissement**

11. P2-1 sanitizer `<thought`.
12. Extension du lexique S7 au-delà des formes observées.
13. Fuzz permanent et tests de propriété.

### 8.3 Critères de succès

- Aucun tour porteur d'outils ne se termine par un greeting, une prose courte ou un claim S7 non étayé avec `finish_reason: stop`.
- Aucun `CleanText` ne contient de JSON d'outil déclaré.
- Aucun delta n'est envoyé sans identité worker/conversation vérifiée.
- Le prompt système ne contient plus les chaînes `0 remaining` / `maximum number of tool` / `search calls`.
- Les quatre sessions v3.5.7 rejouées produisent soit un tool call, soit une erreur terminale explicite, jamais un `idle` silencieux.
- Le pass^3 est mesuré et publié pour chaque modèle ; les décisions R2/P1-1 sont prises sur ces données, pas sur un run unique.

---

## Annexe A — Reproductibilité des constats

Les commandes ci-dessous sont en lecture seule et suffisent à retrouver les faits cités.

```text
# Durées et finish reasons
jq -r '.info.time, (.messages[] | select((.content//[])|length>0) | {finish, rawFinish})' \
  debug/Mise-a-jour-et-Reorganisation-des-Documentations-v3.5.7/*.json

# Raisonnement Neon et texte final
jq -r '.messages[-2].content[] | select(.type=="reasoning" or .type=="text") | .text' \
  debug/Mise-a-jour-et-Reorganisation-des-Documentations-v3.5.7/gemini-4-neon_*.json

# Fuite Hydrogen : taille, absence de path, fin de chaîne
jq -r '.messages[] | (.content//[])[] | select(.type=="text") | select(.text|startswith("{\"name\"")) | .text' \
  debug/Mise-a-jour-et-Reorganisation-des-Documentations-v3.5.7/gemini-4-hydrogen_*.json \
  > /tmp/leak.json
wc -c /tmp/leak.json
jq -r 'keys' /tmp/leak.json        # doit échouer : JSON incomplet
tail -c 120 /tmp/leak.json

# Phrase de quota : absente du plan utilisateur
sqlite3 free-gemini-api/data/gemini.db \
  "select content from messages where id=1028;" | grep -i "0 remaining\|maximum number of tool"
# (aucun résultat)

# Tests temporaires utilisés pour l'arbitrage (à recréer puis supprimer)
# - planSessionTurn avec conv-1 vs conv-2
# - isUnusableModelOutput("Hello! ...") et ("Le plan d'action pour")
# - ArrangeGeminiOutput sur la charge Argon
# - HasTruncatedMutationToolCall sur /tmp/leak.json
```

---

## Annexe B — Ce que le rapport apporte, et ce qu'il ne faut pas en conclure

**Ce qu'il apporte** : quatre traces in-flight propres, avec des textes finaux exacts, des compteurs d'outils et des durées. C'est la meilleure base de non-régression disponible pour v3.5.7. Les symptômes Neon (greeting), Hydrogen (fuite + S7), Hydrogen-task (faux quota) et Argon (arrêt à 21 caractères) sont réels et doivent devenir des tests.

**Ce qu'il ne faut pas en conclure** :

- Que le mode Task `[40]` est la cause du faux quota : Argon, en mode Chat, présente le même symptôme.
- Que la balise `<thought` a cassé l'arranger : le parseur gère les tags imbriqués et n'a pas tronqué la sortie.
- Que promouvoir le JSON fuité est sûr : la fuite observée est une mutation tronquée sans `path`.
- Qu'une clause de permission illimitée supplémentaire résoudra le problème : elle existe déjà et n'a pas fonctionné.
- Qu'un run unique par modèle suffit à conclure sur la variance : il faut un pass^3.

**Conclusion** : le rapport est un excellent **détecteur de symptômes**, mais un **mauvais prescripteur de causes**. L'arbitrage retient ses traces, rejette ses deux recommandations de prompt/promotion telles quelles, et déplace l'effort vers cinq contrats déterministes : continuité de session, quarantaine JSON, preuve d'achèvement, prose incomplète et véracité de `finish_reason`.
