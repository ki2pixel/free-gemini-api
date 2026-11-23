# Brief d'audit — opencode-antigravity-auth 2.1.2 → fiabilisation Gemini 4 (free-gemini-api)

**Date :** 2026-11-23  
**Contexte :** brief de mission destiné à un agent d'analyse indépendant (DeepSeek). Ce document est un **cahier des charges**, pas le rapport d'audit. Il fixe le périmètre, les ancrages vérifiés, les verdicts déjà établis côté Go et les critères d'acceptation du livrable.  
**Source :** `/home/kidpixel/Téléchargements/opencode-antigravity-auth-2.1.2/` (version 2.1.2, **lecture seule**)  
**Cible :** `/home/kidpixel/free-gemini-api-main/free-gemini-api` (module Go `goapi`, version projet 3.5.6)  
**Livrable attendu :** `docs/recherches/audit-opencode-antigravity-auth-gemini-fiabilisation.md`

---

## 1. Mission, périmètre, exclusions

Produire un audit comparatif en **six axes** évaluant comment les mécanismes de résilience de `opencode-antigravity-auth` 2.1.2 peuvent être transposés dans le proxy Go `free-gemini-api` pour éradiquer les écueils résiduels de la famille **Gemini 4**.

### 1.1 Cibles réelles (IDs du registre Go — à utiliser tels quels)

| Modèle | `ModeID` | Particularité | Ancre |
|---|---|---|---|
| `gemini-4-hydrogen` | `56fdd199312815e2` | Défaut, rapide, sensible aux hallucinations de paramètres | `gemini/registry.go:175-186` |
| `gemini-4-hydrogen-task` | `56fdd199312815e2` | `AppMode: 40` → en-tête `[40]` | `gemini/registry.go:187-199` |
| `gemini-4-neon` | `e6fa609c3fa255c0` | Raisonnement frontière, exigeant sur `thinking` | `gemini/registry.go:200-224` |
| `gemini-4-argon` | `95a4c0abcba24364` | Multimodal / analyse lourde, Low par défaut (niveau 1) | `gemini/registry.go:379-390` |
| `gemini-4-argon-thinking-high` | `95a4c0abcba24364` | Alias `argon-high` | `gemini/registry.go:392-404` |
| `gemini-4-argon-thinking-medium` | `95a4c0abcba24364` | Alias `argon-medium` | `gemini/registry.go:406-418` |
| `gemini-4-argon-thinking-low` | `95a4c0abcba24364` | Alias `argon-low` | `gemini/registry.go:420-432` |

> **Attention nomenclature — divergence amont/registre.** `gemini-4-argon-high` **existe** : c'est l'identifiant **annoncé par l'amont Antigravity** (`docs/ANTIGRAVITY_API_SPEC.md:93`, `src/plugin/transform/model-resolver.ts:70`). Le registre Go local, lui, nomme ce modèle `gemini-4-argon-thinking-high` (alias `argon-high`). Les deux sont donc légitimes mais désignent des couches différentes. Utiliser les IDs canoniques Go dans les recommandations d'implémentation, et signaler explicitement l'écart de mapping dans l'Axe 6 (question 3c).

### 1.2 Exclusions strictes

Hors périmètre, sauf lorsqu'ils révèlent le contrat amont Google :

- Toute la machinerie de plugin OpenCode : TUI (`src/tui.ts`, `src/plugin/ui/`), hooks, wrappers CLI `opencode auth`.
- Implémentation OAuth 2.0 locale (`src/antigravity/oauth.ts`, `src/plugin/token.ts`, `src/plugin/auth.ts`).
- Multi-comptes (`accounts.ts`, `account-management.ts`, `account-rpc.ts`), quota et rotation (`quota.ts`, `rotation.ts`, `refresh-queue.ts`).
- `auto-update-checker/`, `image-saver.ts`, `debug.ts`, `logger.ts`, `storage.ts`, `recovery/`.

Restent **dans** le périmètre : les en-têtes canoniques, l'enveloppe JSON, les endpoints, la matrice de support JSON-Schema, les formats de charge utile — dans la mesure où ils constituent le contrat de l'API Antigravity.

### 1.3 Interdits

- Ne pas lancer `npm install`, `npm test`, `npx` sur le dépôt source (`node_modules` absent).
- Ne pas modifier le dépôt source.
- Ne **pas** produire de code Go complet : pseudocode algorithmique uniquement (signatures, structures, invariants, étapes).
- Ne pas proposer de patch, de diff ou de PR.

---

## 2. Prérequis d'accès et garde-fous

Le dépôt source est **hors du workspace Git** de la cible (`/home/kidpixel/Téléchargements/…`). Avant toute analyse :

1. Vérifier l'accessibilité effective du chemin absolu (lecture d'un fichier témoin, par exemple `src/plugin/request-helpers.ts`).
2. Si l'accès est refusé par le bac à sable : **demander explicitement** l'ajout du répertoire aux chemins autorisés, ou une copie de travail locale. Ne jamais poursuivre en aveugle.
3. **Aucune référence de ligne ne doit être inventée.** Toute affirmation porte un ancrage `fichier:ligne-ligne` lu dans cette session. Si une plage n'a pas été lue, l'écrire `non vérifié` plutôt que de la deviner.
4. Les plages fournies en §4 ont été vérifiées le 2026-11-23 sur la version 2.1.2. Elles sont fiables mais **doivent être re-confirmées** : les fichiers évoluent.

---

## 3. Table de verdicts côté Go — section anti-redite

> **Règle impérative.** Ne propose **aucun** correctif pour une zone marquée `ALREADY PRESENT`. Confirme le verdict, ou démontre factuellement sa faiblesse par un contre-exemple ancré. Une recommandation qui duplique un mécanisme existant est un défaut bloquant du livrable.

| # | Zone | Verdict | Preuve |
|---|---|---|---|
| 1 | Compaction de schéma d'outils | **PARTIAL** | `gemini/arranger.go:165-196` — `required` émis sans filtrage contre `properties` (`:173-176`) ; `items` jamais synthétisé si absent (`:314-315`) ; `$ref/$defs/allOf` préservés (`:183-194`) |
| 2 | Validation de schéma à l'exécution | **ALREADY PRESENT** | `gemini/schema_validator.go` (`santhosh-tekuri/jsonschema/v6`, LRU 64, `ValidateCall:30`, `HasInvalidSchemaToolCall:271`) ; retry + reset `api/chat_handler.go:50-56, 916-925, 970-981, 1281-1290, 1332-1340` |
| 3 | Instructions d'outils (`FormatToolsInstruction`) | **ALREADY PRESENT** | 11 règles `gemini/arranger.go:100-114` ; unique appelant `api/chat_handler.go:443` ; injection `:496-499` |
| 4 | Signatures par paramètre injectées (`STRICT PARAMETERS`, `ARRAY_OF_OBJECTS`) | **ABSENT** | Aucun match dans tout le dépôt Go |
| 5 | Points d'injection de directive système | **ALREADY PRESENT** | 6 injecteurs : `api/chat_handler.go:50, 62, 72, 79` ; `api/doom_loop.go:341` ; `api/session_transcripts.go:479` — plus `FormatToolsInstruction` lui-même |
| 6 | Sérialisation historique → amont | **PROSE, pas de `contents[]`** | `api/chat_handler.go:512-523` (`Assistant called tools:`, `[Tool Result for %s]:`) ; aucun `functionCall`/`functionResponse` dans le dépôt |
| 7 | Réconciliation des appels orphelins | **ABSENT** | `ToolCallID` jamais sérialisé ; `ThrottleMutatingToolCalls` (`gemini/arranger.go:803-815`) supprime délibérément des appels sans synthèse compensatoire |
| 8 | Détection de boucle agentique | **ALREADY PRESENT** | `api/doom_loop.go:25-38` (répétition 3/5, cycle 2/3 de longueur 2-3, >4 lectures passives) ; halt = HTTP 429 |
| 9 | Mode tâche `[40]` | **PARTIAL (en-tête seulement)** | `gemini/registry.go:107-114` (`BuildModeHeaderValue`) ; aucune promptification spécifique, budget d'essais identique |
| 10 | Extraction des pensées | **ALREADY PRESENT** | `gemini/arranger.go:1528-1608` (`extractThoughtsAndContent`), `:1612`, `:1620` ; SSE `delta.reasoning_content` |
| 11 | `thoughtSignature` / `thinkingMetadata` | **ABSENT** | Zéro match dans le dépôt Go |
| 12 | Paliers de thinking | **ALREADY PRESENT (registre)** | `gemini/registry.go:120-157` — index 15, **Low=1, Medium=4, High=2** ; templates `:180, :230, :397` |
| 13 | Niveau de thinking côté requête (`reasoning_effort`) | **ABSENT** | `api/types.go:67-74` sans champ ; `tool_choice` parsé puis ignoré (`:71`) |
| 14 | Récupération après tour de pensée tronqué | **PARTIAL** | `api/chat_handler.go:888-900`, `:1257-1269` (`maxEmptyGenerationRetries=2`) ; aucune escalade de palier |
| 15 | Garde de flux (stall) | **ALREADY PRESENT** | `gemini/stream_guard.go:19-100` ; 45 s (`gemini/client.go:29`) ; `ErrUpstreamStreamStalled` |
| 16 | Déduplication de texte de pensée | **PARTIAL** | `gemini/chat.go:306-321` (`nextStreamDelta`, projection par préfixe seulement) |
| 17 | Émission SSE | **ALREADY PRESENT** | `api/chat_handler.go:690-708` (heartbeat), `:787-845` (deltas), `:1090-1119` (tool_calls), `:1143-1156` (`finish_reason`) |
| 18 | Grounding / Google Search | **ABSENT** | Zéro match `googleSearch`/`grounding` ; `gemini/models.go:15-24` sans champ citations ; `gemini/chat.go:576` sans emplacement |
| 19 | Transport amont | **ALREADY PRESENT** | `gemini/transport.go:41-44` (URLs), `gemini/chat.go:225, 515, 599`, `gemini/session.go:241-243` (`SNlM0e`/`FdrFJe`/`cfb2h`), `gemini/client.go:143` (Chrome 152) |
| 20 | Abstraction de transport interchangeable | **ABSENT** | Aucune déclaration `interface {` dans tout le dépôt ; `gemini/client.go:33-34` types concrets (`tls_client.HttpClient`, `*stdhttp.Client`) |
| 21 | Structuration du raisonnement en SSE | **ALREADY PRESENT** | Deltas par rune (`api/chat_handler.go:787-812`), flush terminal `:1043-1067` |

**Ce qui est réellement manquant et donc légitimement proposable** : filtrage `required` + synthèse `items` (1) ; hints de type par paramètre et directive de durcissement (4) ; réconciliation orphelins au niveau du transcript aplati et sérialisation de `ToolCallID` (7) ; niveau de thinking côté requête et aller-retour de signature (11, 13) ; dédoublonnage par suffixe/hachage (16) ; grounding complet et jointure `Transport` (18, 20).

---

## 4. Les six axes — cadrage corrigé

Pour chaque axe : fichiers sources avec plages exactes, questions d'audit, livrable pseudocode, et **confrontation obligatoire** à la table §3.

### Axe 1 — Nettoyage de schéma JSON (moteur Protobuf Gemini 4)

**Sources**
- `src/plugin/request-helpers.ts:659-686` — `cleanJSONSchemaForAntigravity`, **exporté**, pipeline en 11 étapes séquentielles (chaque étape est indépendamment récursive sur tout l'arbre : la récursion a lieu N fois, pas une).
- `:25-38` — `UNSUPPORTED_CONSTRAINTS` (`minLength, maxLength, exclusiveMinimum, exclusiveMaximum, pattern, minItems, maxItems, format, default, examples`) et `UNSUPPORTED_KEYWORDS` (les précédents + `$schema, $defs, definitions, const, $ref, additionalProperties, propertyNames, title, $id, $comment`).
- `:43-50` — `appendDescriptionHint` : forme d'annotation **suffixe parenthésé**, jamais de préfixe (`"<description existante> (<hint>)"`).
- Internes : `convertRefsToHints:56`, `convertConstToEnum:87`, `addEnumHints:111`, `addAdditionalPropertiesHints:142`, `moveConstraintsToDescription:171`, `mergeAllOf:204`, `scoreSchemaOption:278`, `tryMergeEnumFromUnion:313`, `flattenAnyOfOneOf:369`, `flattenTypeArrays:460`, `removeUnsupportedKeywords:540`, `cleanupRequiredFields:575`, `addEmptySchemaPlaceholder:612`.
- `src/plugin/transform/gemini.ts:52-124` — `toGeminiSchema` ; l'injection d'`items` manquants est **ici** (`:117-121`, `{ type: "STRING" }`), pas dans le nettoyeur Claude.
- `:476-579` — `wrapToolsAsFunctionDeclarations` ; incompatibilité `googleSearch` + `functionDeclarations` (`:510, 561-571`).
- Tests : `request-helpers.test.ts:1408-1560`, `transform/gemini.test.ts:667, 1244, 1279`.

**Questions d'audit**
1. Comment chaque contrainte non supportée est-elle convertie en annotation textuelle plutôt que supprimée ? Quel est l'impact mesuré sur le respect des contraintes ?
2. `cleanupRequiredFields:575` filtre `required` contre `properties` — décrire exactement la règle et le devenir d'un `required` vide.
3. Cas limites imbriqués : tableaux sans `items`, `const` → `enum: [v]`, `$ref`/`$defs`/`allOf`, tableaux de types (`type: ["string","null"]`), schéma objet vide (`_placeholder`).
4. `flattenTypeArrays` enregistre les propriétés nulles dans une `Map` keyée par chemin, mais seule la clé racine (`""`) est relue au moment du filtrage : quel est l'effet réel sur les `required` imbriqués ?

**Livrable**
- Pseudocode de `CleanSchemaForGemini4` : arbre de décision complet, ordre des passes, transformation récursive, invariants (`required ⊆ properties`, tout `array` possède `items`, aucun `$ref` non résolu), complexité temps/mémoire.
- Jeu de cas limites : schéma MCP complexe, outil d'édition de fichiers (`edit`/`write` avec `old_string`), schéma `anyOf` d'un outil de fetch web.
- **Confrontation §3** : les deux trous Go (lignes 1) sont-ils comblés par ce pipeline ? Lequel est déjà couvert ?

---

### Axe 2 — Prévention active des hallucinations d'arguments

**Sources**
- `src/plugin/request-helpers.ts:2452-2505` — `formatTypeHint`. Table d'émission : `ENUM[…]` (≤5 valeurs, littérales) / `ENUM[N options]` (>5), `CONST=`, `ARRAY_OF_OBJECTS[N: type, …]`, `ARRAY_OF_<TYPE>`, `ARRAY`, `object{…}`, suffixe `, REQUIRED`.
- `:2519-2559` — `injectParameterSignatures(tools, promptTemplate = "\n\n⚠️ STRICT PARAMETERS: {params}.")`. Garde anti-doublon sur la sous-chaîne `"STRICT PARAMETERS:"`. Ignore les outils sans `properties`.
- `:2568-2608` — `injectToolHardeningInstruction`. Garde anti-doublon sur `"CRITICAL TOOL USAGE INSTRUCTIONS"`. Trois cas d'injection : `parts[]` existant (unshift), `systemInstruction` chaîne (remplacée), absent (créé).
- `src/constants.ts:188-199` — `CLAUDE_TOOL_SYSTEM_INSTRUCTION` : 6 règles impératives (« DO NOT use your internal training data to guess tool parameters », « Parameter names in schemas are EXACT », etc.). Texte court : à citer verbatim dans le rapport.
- `src/constants.ts:205` — `CLAUDE_DESCRIPTION_PROMPT = "\n\n⚠️ STRICT PARAMETERS: {params}."`.
- Tests : `request-helpers.test.ts:1230-1400`.

**Questions d'audit**
1. Pourquoi cette redondance descriptive compense-t-elle la tendance de Gemini 4 (Hydrogen en particulier) à privilégier ses a priori d'entraînement plutôt que le schéma formel ?
2. Où injecter la directive de durcissement sans interférer avec le mode `[40]` ni polluer les consignes utilisateur ?
3. **Recouvrement avec l'existant** : `formatSchemaRetryDirective` (`api/chat_handler.go:50-56`) intervient **après** échec de validation. Les hints préventifs et la directive curative sont-ils redondants ou complémentaires ?

**Livrable**
- Pseudocode de la refonte de `FormatToolsInstruction` intégrant le double verrouillage : signatures injectées dans les descriptions + clause de durcissement amont, avec le point d'insertion exact parmi les règles 1-11 (`arranger.go:100-114`).
- Évaluation qualitative du gain attendu sur le taux d'échec de `schema_validator.go` (et donc sur la consommation du budget `maxToolChatAttempts = 4`).
- **Confrontation §3** : verdicts 3, 4, 5.

---

### Axe 3 — Orphelins d'outils et cohérence du Task Mode `[40]` *(cadrage reformulé)*

**Sources**
- `src/plugin/request-helpers.ts:2044-2216` — `fixToolResponseGrouping`. Passe de collecte puis cascade à 4 niveaux : ID exact → correspondance par **nom de fonction** → orphelin `"unknown_function"` → premier disponible → **placeholder synthétique** avec `recovered: true` et le littéral `"Tool response was lost during context processing. This is a recovered placeholder."`.
- `:2622-2656` — `assignToolIdsToContents` (génère `tool-call-${n}`, file FIFO par nom).
- `:2666-2696` — `matchResponseIdsToContents` (appariement FIFO idempotent).
- `:2710-2751` — `applyToolPairingFixes` (orchestrateur, réservé au chemin Claude).
- `:2224-2260` — `detectToolIdMismatches` : **export mort** (aucun appelant en production, aucun test).
- `src/plugin/errors.ts` — `ToolIdMismatchError`.
- Aucun test dédié à `fixToolResponseGrouping` ni aux passes d'appariement.

**⚠️ Reformulation obligatoire.** Le proxy Go ne manipule **ni** `contents[]`, **ni** `functionCall`/`functionResponse` : l'historique est aplati en prose (`api/chat_handler.go:512-523`). Transposer littéralement la réconciliation JSPB n'a aucun sens ici. Le livrable doit **reformuler** le mécanisme au niveau du transcript OpenAI aplati :

- Détecter un `assistant` porteur de `tool_calls` sans message `role:"tool"` correspondant.
- Sérialiser `ToolCallID` (aujourd'hui ignoré au profit de `m.Name`, ce qui produit `[Tool Result for ]:` quand seul `tool_call_id` est renseigné).
- Synthétiser un message `role:"tool"` factice plutôt que muter un tableau JSPB.
- Traiter le cas d'école : `ThrottleMutatingToolCalls` (`gemini/arranger.go:803-815`) supprime délibérément les appels au-delà de la première mutation ⇒ orphelins créés **par construction**.

**Questions d'audit**
1. L'invariance d'alternance exigée par Gemini (tout appel doit avoir sa réponse avant une nouvelle prise de parole) est-elle structurellement respectée par un transcript en prose ? Quels sont les modes de rupture ?
2. Les 4 passes de la cascade sont-elles ordonnées par coût de confiance ? La passe « premier disponible » n'introduit-elle pas des appariements faux ?
3. Quel doit être le contrat du placeholder synthétique côté Go : texte, marqueur de récupération, visibilité pour le modèle ?

**Livrable**
- Pseudocode de réconciliation multi-tours adapté à `free-gemini-api` : structures d'entrée (liste de messages OpenAI), invariants (bijection appel ↔ résultat, ou placeholder explicite), algorithme pas à pas, **preuve de terminaison**, complexité.
- **Confrontation §3** : verdicts 6, 7, 8, 9.

---

### Axe 4 — Résilience du raisonnement sur Neon & Argon

**Sources**
- `src/plugin/thinking-recovery.ts:125-179` — `analyzeConversationState` (deux passes ; `inToolLoop` ssi le dernier message est un résultat d'outil ; `turnHasThinking` lu sur le **premier** message du tour, `lastModel*` en dernier-gagne).
- `:264-294` — `closeToolLoopForThinking`. Non mutant. Littéraux exacts : `"[Processing previous context.]"` (0 résultat), `"[Tool execution completed.]"` (1), `"[N tool executions completed.]"` (>1), puis message utilisateur `"[Continue]"`.
- `:305-307` — `needsThinkingRecovery` = `inToolLoop && !turnHasThinking` (deux conditions, aucun seuil).
- `:330-372` — `looksLikeCompactedThinkingTurn` : trois heuristiques en ET (au moins un `functionCall` ; aucun `thought`/`thinking`/`redacted_thinking` ; aucun texte non-pensée **avant** le premier `functionCall`).
- `:381-395` — `hasPossibleCompactedThinking` : **export mort**, et n'accepte que `role === "model"` là où `analyzeConversationState` accepte `"model" || "assistant"` ⇒ angles morts Anthropic.
- `src/plugin/transform/cross-model-sanitizer.ts:40-79` — `stripGeminiThinkingMetadata` (`thoughtSignature`, `thinkingMetadata`, `metadata.google`).
- `:200-260` — `deepSanitizeCrossModelMetadata` : assainissement **unidirectionnel** (cible `claude` ⇒ retire les champs Gemini ; cible `gemini` ⇒ l'inverse ; même famille ⇒ no-op). Un rejeu Gemini→Gemini conserve donc sa signature.
- Tests : `thinking-recovery.test.ts` (172-321), `cross-model-sanitizer.test.ts`.

**⚠️ Correction obligatoire.** `docs/MODEL-VARIANTS.md` (182 lignes) ne documente **que** Claude et Gemini 2.5/3 (`thinkingConfig.thinkingBudget`, `thinkingLevel`, mapping déprécié ≤8192→low / ≤16384→medium / >16384→high). **Gemini 4 n'y figure pas.** La « matrice Thinking Gemini 4 » demandée n'existe pas dans le source : elle doit être **dérivée** du registre Go réel (`gemini/registry.go:120-157` — index 15, Low=1 / Medium=4 / High=2 ; templates `:332, :345, :358` pour Hydrogen, `:230, :243, :256` pour Neon, `:397, :411, :425` pour Argon) et des IDs opaques `model-resolver.ts:67-76` / `ANTIGRAVITY_API_SPEC.md:93-94`. **Le dire explicitement dans le rapport** au lieu de prétendre citer une documentation.

**Questions d'audit**
1. Pourquoi « réparer » un bloc de pensée tronqué échoue-t-il systématiquement, et pourquoi la clôture synthétique (`[Tool execution completed.]` + `[Continue]`) force-t-elle une régénération propre ?
2. Les trois heuristiques de compactage suffisent-elles à distinguer un tour tronqué d'un modèle sans thinking ? Quel contre-exemple les invalide ?
3. Comment transposer au pipeline Go, qui n'a **aucun** champ de signature de pensée (verdict 11) mais possède déjà l'extraction (`arranger.go:1528`) et le budget de tour vide (`chat_handler.go:888-900`) ?

**Livrable**
- Pseudocode du détecteur de corruption de réflexion + du mécanisme de réinitialisation propre du tour, adapté aux invariants Go (message aplati, pas de `parts[]`).
- Matrice de configuration Thinking **Gemini 4** explicitement reconstruite depuis le registre Go, sourcée ligne par ligne.
- **Confrontation §3** : verdicts 10, 11, 12, 13, 14.

---

### Axe 5 — Streaming SSE et dédoublonnage du raisonnement

**Sources**
- `src/plugin/core/streaming/transformer.ts:13-19` — `hashString`, DJB2 (graine 5381, multiplicateur 33, `>>> 0`, hex non padé).
- `:21-28` — `createThoughtBuffer` (simple `Map<number, string>`).
- `:30-56` — `transformStreamingPayload` (ne conserve que les lignes `data:`, ré-émet `{response}`, **perd les clés d'enveloppe**).
- `:58-173` — `deduplicateThinkingText` : deux branches parallèles (Gemini `p.thought`, Claude `b.type === 'thinking'`), même cœur : si `fullText.startsWith(sentText)` → `delta = fullText.slice(sentText.length)` ; sinon renvoie la partie entière. Déduplication par hachage optionnelle, activée **uniquement** pour Gemini 3 (`request.ts:1690`).
- `:329-361` — `flush` : décode le reste, injecte un `usageMetadata` synthétique si absent.
- `src/plugin/request.ts:79` — `sessionDisplayedThinkingHashes`, `Set` module-level.

**⚠️ Correction obligatoire.** Le mécanisme de « fermeture propre des balises `<thought>` en cas d'interruption réseau » **n'existe pas** dans la version 2.1.2 : aucun match pour `antml`, `</thinking`, `closeThought`, `premature`, `unterminated`. `flush` se limite à l'injection d'un `usageMetadata` synthétique. Le rapport doit **statuer l'absence** et réorienter l'analyse vers les deux trous réels du proxy Go :

1. `nextStreamDelta` (`gemini/chat.go:306-321`) accepte tout instantané qui **étend** le préfixe : `"A" → "A\nA"` passe le test et duplique le paragraphe.
2. Dans `api/chat_handler.go:787-812` et `:1043-1067`, le compteur `emittedThoughtRunes` est monotone : une pensée **réouverte** plus courte que le high-water mark est intégralement perdue.

**Questions d'audit**
1. Le mode cumulatif vs delta : décrire l'algorithme de tampon glissant et pourquoi il suffit pour les snapshots croissants.
2. Le hachage DJB2 : que protège-t-il exactement ? Quels sont ses modes de défaillance (espace 32 bits, set non borné, non purgé) ?
3. Comment exprimer un dédoublonnage correct sans réintroduire l'état global non borné du source ?

**Livrable**
- Pseudocode du dédoublonneur SSE temps réel adapté au pipe Go, avec invariant explicite (« aucun suffixe émis deux fois »), gestion du cas divergence/ré-émission, et complexité mémoire **bornée**.
- **Confrontation §3** : verdicts 15, 16, 17, 21.

---

### Axe 6 — Grounding découplé et passerelle Antigravity

**Sources**
- `src/plugin/search.ts:223-313` — `executeSearch` : construit `tools = [{googleSearch:{}}]` (+ `{urlContext:{}}` si URLs), `generationConfig {temperature: 0, topP: 1}`, endpoint **non-streaming** `/v1internal:generateContent`, timeout 60 s.
- `:147-211` — `parseSearchResponse` : `groundingMetadata.webSearchQueries`, `groundingChunks[].web.{uri,title}` (les deux requis), `urlContextMetadata.url_metadata[]`.
- `:113-145` — `formatSearchResult` : gabarit Markdown exact (`## Search Results`, `### Sources`, `### URLs Retrieved` avec ✓/✗, `### Search Queries Used`).
- Outil virtuel `google_search` : `src/v2.ts:602-640` (schéma `query` requis, `urls`, `thinking`), `src/plugin.ts:1354-1401`.
- `docs/ANTIGRAVITY_API_SPEC.md` — endpoints `:24-41`, en-têtes `:64-81` (`User-Agent: antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; cl=1000335682)`), enveloppe requête `:109-123`, enveloppe réponse et trames SSE `:364-399`, matrice de support JSON-Schema `:319-354` (`const`/`$ref`/`$defs`/`$schema`/`default`/`examples` **non supportés** ⇒ HTTP 400), rôle `assistant` rejeté `:148-150`, `systemInstruction` objet obligatoire `:181-197`.

**Côté Go, à confronter**
- `gemini/transport.go:41-44` (URLs), `gemini/chat.go:225, 515, 599` (`StreamGenerate`, `batchexecute`), `gemini/session.go:241-243` (`SNlM0e`/`FdrFJe`/`cfb2h`), `gemini/transport.go:81-90` (en-têtes JSPB), `gemini/client.go:139-185` (Chrome 152, QUIC).
- Verdicts 18 et 20 : grounding absent, aucune abstraction de transport.

**Questions d'audit**
1. Pourquoi l'API native refuse-t-elle de combiner `{googleSearch:{}}` et `{functionDeclarations:[...]}` ? Décortiquer le pattern d'interception (outil virtuel → requête dédiée → parsing `groundingMetadata` → réinjection Markdown).
2. Comparer `cloudcode-pa.googleapis.com` / `daily-cloudcode-pa.googleapis.com` au scraping web actuel : fiabilité, latence, contraintes.
3. Trancher explicitement les trois prérequis souvent omis :
   - (a) modèle d'authentification **OAuth + `project`** vs cookies `SNlM0e` — sont-ils substituables ?
   - (b) absence totale d'abstraction `Transport` côté Go : quel refactor de jointure minimal ?
   - (c) mapping des IDs opaques (`gemini-4-argon-high` côté amont vs `gemini-4-argon-thinking-high` côté registre Go) contre le schéma d'alias du registre.

**Livrable**
- Pseudocode du handler virtuel `google_search` (entrée, sortie, chemin d'erreur, formatage, budget).
- Tableau comparatif d'opportunité : scraping web actuel vs connecteur Antigravity Gateway (colonnes : latence, fiabilité, coût de refactor, risque opérationnel, prérequis).
- **Confrontation §3** : verdicts 18, 19, 20.

---

## 5. Ce qu'il ne faut pas copier

Section à part entière du rapport (alimente directement la SWOT). Chaque point est ancré :

| # | Défaut du source | Ancre | Conséquence |
|---|---|---|---|
| D1 | `sessionDisplayedThinkingHashes` = `Set` module-level **non borné, jamais purgé**, clé = DJB2 32 bits | `request.ts:79`, `transformer.ts:13-19` | Collisions ⇒ suppression silencieuse de blocs de pensée ; croissance mémoire non bornée |
| D2 | **Deux assainisseurs de schéma disjoints** : `cleanJSONSchemaForAntigravity` (minuscules, sans `items`) vs `toGeminiSchema` (MAJUSCULES, avec `items`) | `request-helpers.ts:659`, `transform/gemini.ts:52` | Le chemin Claude peut émettre `{type:"array"}` sans `items` |
| D3 | `fixToolResponseGrouping` **mute en place** les objets orphelins collectés | `request-helpers.ts:2168-2171` | Fuite d'ID réécrit dans le tableau de l'appelant en cas de retry |
| D4 | Paramètre `thinking` de `executeSearch` **mort** (destructuré, jamais placé dans la charge utile) ; `SEARCH_THINKING_BUDGET_*` déclarés, inutilisés | `search.ts:229`, `constants.ts:247, 252` | Faux paramétrage, illusion de configurabilité |
| D5 | `formatTypeHint` : incohérence de casse (`object{…}` minuscule vs `ARRAY_OF_*` majuscule) | `request-helpers.ts:2482-2500` | Bruit dans le prompt |
| D6 | `hasPossibleCompactedThinking` n'accepte que `role === "model"` alors que `analyzeConversationState` accepte `"model" \|\| "assistant"` | `thinking-recovery.ts:381` vs `:125` | Angles morts Anthropic |
| D7 | `displayedThinkingHashes` passé **uniquement** pour Gemini 3 | `request.ts:1690` | Incohérence de couverture entre familles |
| D8 | `detectToolIdMismatches` et `hasPossibleCompactedThinking` : exports morts | `request-helpers.ts:2224`, `thinking-recovery.ts:381` | Dette de maintenance, faux sentiment de couverture |

Chaque défaut doit être explicitement écarté dans la proposition de transposition, ou corrigé.

---

## 6. Gabarit du rapport attendu

Structure numérotée, en **français** (convention du dépôt : `docs/` et `audit-2026-11/` sont en français ; seuls `README.md` et `CHANGELOG.md` sont en anglais).

```markdown
# Audit opencode-antigravity-auth 2.1.2 → fiabilisation Gemini 4

**Date :** <date>
**Contexte :** <mission, version source, commit de référence de free-gemini-api>
**Sources :** <dépôt source, version audités>
---

## 1. Résumé exécutif & matrice SWOT
## 2. Axe 1 — Nettoyage JSON Schema (Protobuf)
## 3. Axe 2 — Prévention des hallucinations d'outils
## 4. Axe 3 — Orphelins & cohérence Task Mode [40]
## 5. Axe 4 — Résilience du Thinking (Neon & Argon)
## 6. Axe 5 — Optimisations du Streaming SSE
## 7. Axe 6 — Grounding découplé & passerelle Antigravity
## 8. Défauts du source à ne pas transposer
## 9. Feuille de route d'implémentation priorisée (P0, P1, P2)
## 10. Sources des citations
```

**Feuille de route (§9)** : reprendre le schéma de `audit-2026-11/PLAN_REMEDIATION.md` — colonnes `ID | Action | Priorité | Effort | Impact attendu Gemini 4 | Réf. source`. IDs au format `A-01`, `A-02`… Priorités P0/P1/P2 avec justification.

**SWOT (§1)** : forces (mécanismes directement transposables), faiblesses (défauts §5), opportunités (trous Go identifiés §3), menaces (risques opérationnels des changements).

---

## 7. Critères d'acceptation

Le livrable est **rejeté** si l'un de ces critères manque :

1. **Rigueur des références** — chaque analyse cite `fichier:ligne-ligne` pour les deux dépôts. Aucune plage inventée. Les plages §4 re-confirmées ou corrigées avec justification.
2. **Clarté algorithmique** — chaque pseudocode explicite : structures d'entrée/sortie, invariants, conditions aux limites, complexité temps **et** mémoire, preuve de terminaison pour les boucles de réconciliation.
3. **Orientation pratique** — chaque axe se conclut par une confrontation explicite à la table §3, avec l'un des trois verdicts : *transposable*, *déjà couvert*, *non pertinent (divergence d'architecture)*.
4. **Traitement de l'errata** — les sept points de §8 sont traités explicitement. Un mécanisme absent du source doit être déclaré absent, jamais halluciné.
5. **Non-duplication** — aucune recommandation ne cible une zone `ALREADY PRESENT` sans démonstration factuelle de faiblesse.
6. **Conventions** — français, en-tête `**Date :**` / `**Contexte :**`, tableaux avec ligne d'alignement, pseudocode en blocs ```text``` ou ```go``` (pas de Go compilable).

---

## 8. Annexe errata — corrections du cadrage initial

| # | Prémisse initiale | Réalité vérifiée |
|---|---|---|
| E1 | `cleanJSONSchemaForAntigravity` lignes 20-680 | `request-helpers.ts:659-686` (export) ; internes éparpillés `:43-612` |
| E2 | Fermeture propre des balises `<thought>` en SSE | **Mécanisme inexistant** en 2.1.2 ; `flush` injecte seulement un `usageMetadata` synthétique (`transformer.ts:348-360`) |
| E3 | `docs/MODEL-VARIANTS.md` couvre les paliers Gemini 4 | Ne couvre que Claude + Gemini 2.5/3 ; Gemini 4 = IDs opaques (`model-resolver.ts:67-76`, `ANTIGRAVITY_API_SPEC.md:93-94`) |
| E4 | `gemini-4-argon-high` = nom du modèle dans le registre Go | Existe côté **amont** Antigravity (`ANTIGRAVITY_API_SPEC.md:93`, `model-resolver.ts:70`) ; le registre Go le nomme `gemini-4-argon-thinking-high` (alias `argon-high`). Divergence de couche, pas d'erreur — à cartographier en Axe 6.3c (`gemini/registry.go:392-432`) |
| E5 | Le nettoyeur injecte `{type:"STRING"}` pour les `items` manquants | Cette injection est dans `transform/gemini.ts:117-121`, **pas** dans `cleanJSONSchemaForAntigravity` |
| E6 | Réconciliation sur `contents[]`/`functionCall` côté Go | Aucune sérialisation structurée : historique aplati en prose (`api/chat_handler.go:512-523`) |
| E7 | `detectToolIdMismatches` et `hasPossibleCompactedThinking` sont actifs | Exports morts, aucun appelant en production |

---

## 9. Rappel des contraintes de rendu

- Pseudocode algorithmique uniquement — pas de code Go complet, pas de patch, pas de diff.
- Ne pas modifier le dépôt source, ne pas lancer ses tests.
- **Ne pas committer** : le rapport est déposé dans `docs/recherches/` et laissé dans le working tree pour inspection.
