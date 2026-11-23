# Audit opencode-antigravity-auth 2.1.2 → fiabilisation Gemini 4

**Date :** 2026-11-23
**Contexte :** audit comparatif demandé par le brief `docs/recherches/BRIEF-audit-opencode-antigravity-auth-gemini-fiabilisation.md`, portant sur la transposition des mécanismes de résilience du plugin `opencode-antigravity-auth` 2.1.2 vers le proxy Go `free-gemini-api` (module `goapi`, version 3.5.6, commit de référence `54a9a9a4cb35d1d8e751908bead6f2b6d813b3f8`, 2026-11-23).
**Sources :** dépôt `/home/kidpixel/Téléchargements/opencode-antigravity-auth-2.1.2/` (version 2.1.2, lecture seule) et `/home/kidpixel/free-gemini-api-main/free-gemini-api/`. Toutes les plages citées ont été relues pendant cette session ; les rares plages non relues sont explicitement marquées `non vérifié`.

---

## 1. Résumé exécutif & matrice SWOT

### 1.1 Verdict d'ensemble

L'audit conduit sur les six axes produit un constat unique et contre-intuitif : **le dépôt amont n'est pas une source de vérité architecturale pour Gemini 4**, il est une source de *patrons de robustesse* dont la valeur est conditionnée à une reformulation complète. Trois faits structurent cette conclusion.

1. **Le code amont qui intéresse réellement la fiabilité d'outils est réservé au chemin Claude.** La normalisation de schéma (`cleanJSONSchemaForAntigravity`, `src/plugin/request-helpers.ts:659-686`) n'est appelée qu'à l'intérieur du bloc `if (isClaude)` (`src/plugin/request.ts:1157` → `:1179`). Les signatures de paramètres et la clause de durcissement ne sont injectées que sous la même condition, doublée d'un drapeau de configuration (`src/plugin/request.ts:1305-1318`). La réconciliation d'orphelins `fixToolResponseGrouping` est, elle aussi, confinée à ce bloc (`src/plugin/request.ts:1327-1422`, appel `:1421`). Autrement dit : la chaîne « nettoyage → hints → réconciliation » n'est **jamais** exercée sur une requête Gemini de bout en bout dans l'amont. Elle reste néanmoins transposable, car ses algorithmes sont indépendants du protocole.
2. **Le proxy Go possède déjà une couche de résilience agentique supérieure à l'amont sur plusieurs plans.** Validation de schéma à l'exécution avec catalogue compilé et LRU (`gemini/schema_validator.go:30-62`, `:271-291`), budget d'essais séparant incidents de transport, tours vides et échecs fonctionnels (`api/chat_handler.go:110-124`), garde de flux à 45 s (`gemini/stream_guard.go:19-100`, `gemini/client.go:27-30`), disjoncteur anti-boucle à trois signaux (`api/doom_loop.go:20-38`, `:113-140`). Ces zones ne doivent recevoir **aucune** recommandation de duplication.
3. **Les trous réels sont étroits et localisés.** Le schéma compacté n'applique ni filtre `required ⊆ properties` ni synthèse d'`items` (`gemini/arranger.go:165-196`, `:299-333`) ; `ToolCallID` n'est jamais sérialisé dans le transcript aplati (`api/chat_handler.go:501-530`) ; aucun champ de signature de pensée n'existe (zéro occurrence de `thoughtSignature`/`thinkingMetadata`) ; le niveau de thinking n'est pas pilotable par requête (`api/types.go:67-74`) ; le dédoublonnage de pensée est un simple test de préfixe (`gemini/chat.go:306-321`) doublé d'un compteur monotone (`api/chat_handler.go:787-812`, `:1043-1067`) ; le grounding et l'abstraction de transport sont absents (zéro occurrence de `googleSearch`/`grounding`, zéro déclaration `interface {`).

### 1.2 Matrice SWOT

| Quadrant | Contenu | Ancrage |
|---|---|---|
| **Forces** (directement transposables) | Pipeline de nettoyage en 11 passes indépendamment récursives ; annotation par suffixe parenthésé qui préserve la sémantique des contraintes ; cascade de réconciliation à quatre niveaux avec placeholder explicite ; clôture synthétique de tour (`[Tool execution completed.]` + `[Continue]`) non mutante ; signatures de paramètres redondantes avec le schéma formel | `request-helpers.ts:659-686`, `:43-50`, `:2044-2216`, `thinking-recovery.ts:264-294`, `:2519-2559` |
| **Faiblesses** (défauts à ne pas copier) | Set de hachages global non borné ; deux assainisseurs de schéma disjoints et incompatibles ; mutation en place des orphelins ; paramètre `thinking` mort ; incohérence de casse dans les hints ; export mort `hasPossibleCompactedThinking` limité au rôle `model` ; couverture de dédoublonnage limitée à Gemini 3 ; exports morts | `request.ts:79`, `request-helpers.ts:659` vs `transform/gemini.ts:52`, `request-helpers.ts:2168-2171`, `search.ts:229`, `request-helpers.ts:2482-2500`, `thinking-recovery.ts:381-395`, `request.ts:1690`, `request-helpers.ts:2224` |
| **Opportunités** (trous Go identifiés) | `required` non filtré et `items` non synthétisé ; hints de type par paramètre absents ; `ToolCallID` jamais rendu ; aucune escalade de palier thinking ; dédoublonnage par préfixe seulement ; grounding et transport non abstraits | `arranger.go:173-176`, `:314-315`, `chat_handler.go:512-523`, `chat.go:306-321`, `models.go:15-24`, `client.go:33-34` |
| **Menaces** (risques opérationnels) | Toute injection supplémentaire en tête de prompt déplace le préfixe caché et peut dégrader la continuité de session ; un filtrage `required` trop agressif rejette des appels légitimes de schémas sans `properties` ; un dédoublonnage par suffixe peut supprimer une réouverture légitime de pensée ; une jointure `Transport` touche les quatre chemins QUIC/HTTP2 × stream/non-stream simultanément | `chat_handler.go:490-530`, `:754-776`, `arranger.go:276-297`, `transport.go:68-246` |

### 1.3 Lecture de la table de verdicts

Les 21 verdicts du brief sont confirmés dans leur intégralité, à une réserve factuelle près, documentée en §5.3 : l'entrée `gemini-4-hydrogen` est déclarée **deux fois** dans le registre (`gemini/registry.go:175-186` puis `:312-326`), et l'enregistrement par clé fait gagner la seconde définition (`:554-568`). La conséquence est analysée dans l'axe 4 ; elle ne change aucun verdict du brief.

---

## 2. Axe 1 — Nettoyage JSON Schema (Protobuf)

### 2.1 Ce que fait réellement le pipeline amont

`cleanJSONSchemaForAntigravity` est un orchestrateur de onze passes (`src/plugin/request-helpers.ts:659-686`) :

| Ordre | Passe | Rôle | Ancre |
|---|---|---|---|
| 1 | `convertRefsToHints` | `$ref` → `{type:"object", description:"See: <nom>"}` | `:56-81` |
| 2 | `convertConstToEnum` | `const: v` → `enum:[v]`, seulement si `enum` absent | `:87-105` |
| 3 | `addEnumHints` | ajoute `(Allowed: a, b, …)` si `2 ≤ len(enum) ≤ 10` | `:111-136` |
| 4 | `addAdditionalPropertiesHints` | `additionalProperties:false` → `(...)` | `:142-165` |
| 5 | `moveConstraintsToDescription` | recopie `minLength`, `pattern`, `default`, etc. | `:171-197` |
| 6 | `mergeAllOf` | fusionne propriétés et `required` | `:204-272` |
| 7 | `flattenAnyOfOneOf` | sélectionne la meilleure option, ou fusionne en `enum` | `:313-454` |
| 8 | `flattenTypeArrays` | `type:["string","null"]` → `type:"string"` + hints | `:460-534` |
| 9 | `removeUnsupportedKeywords` | supprime les mots-clés après extraction | `:540-570` |
| 10 | `cleanupRequiredFields` | filtre `required` contre `properties` | `:575-606` |
| 11 | `addEmptySchemaPlaceholder` | injecte `_placeholder:boolean` requis | `:612-651` |

Trois propriétés de conception méritent d'être soulignées, car elles sont la vraie valeur du module.

**a) La récursion est globale et répétée, pas locale.** Chaque passe est indépendamment récursive sur l'arbre entier (voir par exemple `:129-133` pour `addEnumHints`, `:157-160` pour `addAdditionalPropertiesHints`, `:189-194` pour `moveConstraintsToDescription`). Un schéma de profondeur *d* est donc parcouru onze fois, soit une complexité temps en `Θ(11 · n)` où *n* est le nombre total de nœuds — et non `O(n)` avec un facteur constant dissimulé. C'est un choix coûteux mais qui rend chaque passe triviale à raisonner et à tester. La réserve critique : à chaque passe, un objet est reconstruit (`{...schema}` ou `result = {}`), si bien que le coût mémoire est également en `Θ(11 · n)` allocations transitoires.

**b) L'annotation est un suffixe parenthésé, jamais un préfixe.** `appendDescriptionHint` (`:43-50`) produit `"<description existante> (<hint>)"` ou `<hint>` si la description est vide. Cette discipline est structurante : elle garantit qu'un hint ajouté en passe 3 ne « vole » pas le début de la description, et que deux hints successifs sur le même nœud restent lisibles et ordonnés chronologiquement. C'est directement transposable.

**c) Les contraintes sont dégradées, pas perdues.** `moveConstraintsToDescription` (`:171-197`) ne supprime pas `pattern` : il le recopie dans la description, puis la passe 9 le retire du schéma. La sémantique survit donc sous forme de prose pour le modèle, tandis que le validateur protobuf ne voit plus de mot-clé non supporté. La matrice de support du contrat amont confirme l'enjeu : `const`, `$ref`, `$defs`, `$schema`, `$id`, `default`, `examples` sont explicitement rejetés en HTTP 400 (`docs/ANTIGRAVITY_API_SPEC.md:319-354`). C'est un mécanisme de **dégradation contrôlée**, pas un nettoyage.

### 2.2 Le trou de `flattenTypeArrays` (question 4 du brief)

`flattenTypeArrays` reçoit une `Map` keyée par chemin (`:460-470`). Pour chaque propriété nullable rencontrée, elle enregistre le nom de la propriété sous la clé du **parent** (`:501-509`) :

```text
objectPath = currentPath || ""            // :505
localNullableFields[objectPath].push(propKey)
```

Mais la relecture au moment du filtrage n'interroge **que** la clé racine (`:515-524`) :

```text
if Array.isArray(result.required) && !nullableFields:
    nullableAtRoot = localNullableFields.get("")     // :517
```

Deux conséquences exactes, et une seule est un vrai défaut :

- **Au niveau racine**, la règle fonctionne : une propriété `x` déclarée nullable et présente dans `required` racine est retirée de `required`, et `required` est supprimé s'il devient vide (`:519-522`). C'est correct.
- **Aux niveaux imbriqués**, la clé enregistrée est `"properties.foo"` (ou `"…properties.foo"`), jamais `""`. La lecture `get("")` ne la voit donc pas. Les `required` imbriqués **conservent** des propriétés nullables.

Est-ce grave ? Non, et il faut le dire clairement : les passes 9-11 s'exécutent après, et la passe 10 (`cleanupRequiredFields`, `:575-606`) filtre `required` contre `properties` — mais elle ne filtre **que l'existence**, pas la nullabilité. Une propriété nullable restée requise est donc toujours exigée du modèle. Le comportement est cohérent avec le contrat (une propriété requise et nullable doit tout de même être fournie, éventuellement à `null`), mais il est **contraire à l'intention exprimée par le code**. C'est une dette de lisibilité, pas un bug de correction. En revanche, si un futur mainteneur « corrige » la clé pour lire le chemin réel, il changera silencieusement la sémantique de tous les schémas imbriqués. Ce point doit être documenté comme invariant, pas corrigé à l'aveugle.

### 2.3 Le trou de l'injection d'`items` (errata E5)

La correction du brief est confirmée sans ambiguïté : `cleanJSONSchemaForAntigravity` **n'injecte jamais** d'`items`. L'injection vit dans `toGeminiSchema` (`src/plugin/transform/gemini.ts:117-121`) :

```text
if (result.type === "ARRAY" && !result.items) {
  result.items = { type: "STRING" };
}
```

Or `toGeminiSchema` est un **second** assainisseur, disjoint du premier, avec des conventions inverses : types en MAJUSCULES (`:75-77`) et suppression d'une liste de champs différente (`:27-50`). C'est le défaut D2 du §8. `cleanJSONSchemaForAntigravity` laisse les types en minuscules et n'ajoute jamais d'`items`. Les deux fonctions ne sont jamais composées : `cleanJSONSchemaForAntigravity` est appliqué au chemin Claude (`request.ts:1179`), `toGeminiSchema` au chemin Gemini (`transform/gemini.ts:279`). Un schéma passant par le premier chemin peut donc sortir avec `type:"array"` sans `items`.

C'est exactement le trou Go n°1 (`gemini/arranger.go:314-315` : `items` n'est émis que s'il existe déjà). La transposition doit donc **unifier** les deux passes dans une seule fonction, en retenant la convention de casse du proxy Go (minuscules, car `compactToolDefinitions` émet du JSON consommé comme description textuelle, pas comme protobuf).

### 2.4 Pseudocode de `CleanSchemaForGemini4`

Le pseudocode suivant fusionne les deux assainisseurs, corrige D2, et comble les deux trous Go. Il est volontairement exprimé en pseudo-langage : aucune signature Go compilable.

```text
FONCTION CleanSchemaForGemini4(racine) -> arbre
  PRÉCONDITION   racine est un objet JSON quelconque (schéma partiel autorisé)
  POSTCONDITION  Invariant I1 : tout nœud `required` est un sous-ensemble de ses `properties`
                 Invariant I2 : tout nœud `type = "array"` possède un `items` non vide
                 Invariant I3 : aucun nœud ne contient $ref, $defs, definitions, $schema, $id, $comment
                 Invariant I4 : aucun nœud ne contient const (converti en enum à valeur unique)
                 Invariant I5 : toute contrainte retirée (passes 5 et 9) apparaît dans `description`
                 Invariant I6 : les types restent en minuscules (convention proxy Go)
  SI racine n'est pas un objet  ALORS RENDRE racine
  arbre ← CopierProfond(racine)

  POUR CHAQUE passe DANS [RefsVersHints, ConstVersEnum, HintsEnum,
                          HintsAdditionalProperties, ContraintesVersDescription,
                          FusionAllOf, AplatirAnyOfOneOf, AplatirTableauxDeTypes,
                          RetirerMotsClesNonSupportes, NettoyerRequired,
                          PlaceholderObjetVide] FAIRE
      arbre ← passe(arbre)            // chaque passe est totale et récursive
  FIN POUR

  // I2 est réparé APRÈS la passe de nettoyage : un items supprimé par
  // RetirerMotsClesNonSupportes doit être reconstruit, pas laissé absent.
  arbre ← SynthetiserItemsManquants(arbre, {type:"string"})
  RENDRE arbre
FIN FONCTION

FONCTION NettoyerRequired(noeud) -> noeud
  SI noeud n'est pas un objet ALORS RENDRE noeud
  résultat ← Copier(noeud)
  SI résultat.required est un tableau ET résultat.properties est un objet ALORS
      valides ← [ r POUR r DANS résultat.required SI r ∈ clés(résultat.properties) ]
      SI longueur(valides) = 0 ALORS supprimer résultat.required
      SINON SI longueur(valides) ≠ longueur(résultat.required) ALORS
          résultat.required ← valides        // préserve l'ordre d'origine
      FIN SI
  FIN SI
  POUR CHAQUE (clé, valeur) DANS résultat FAIRE        // profondeur d'abord
      SI valeur est un objet ALORS résultat[clé] ← NettoyerRequired(valeur)
  FIN POUR
  RENDRE résultat
FIN FONCTION

FONCTION SynthetiserItemsManquants(noeud, defaut) -> noeud
  SI noeud n'est pas un objet ALORS RENDRE noeud
  résultat ← Copier(noeud)
  SI résultat.type = "array" ET (résultat.items est absent OU vide) ALORS
      résultat.items ← defaut                    // comble le trou arranger.go:314-315
  FIN SI
  POUR CHAQUE (clé, valeur) DANS résultat FAIRE
      SI valeur est un objet ALORS résultat[clé] ← SynthetiserItemsManquants(valeur, defaut)
  FIN POUR
  RENDRE résultat
FIN FONCTION
```

**Preuve des invariants.** I1 découle de `NettoyerRequired`, qui est appliqué à *tous* les nœuds par récursion exhaustive ; l'ordre `required` est préservé par construction du filtre. I2 découle de `SynthetiserItemsManquants`, exécuté après la passe de retrait — l'ordre est essentiel et c'est précisément l'erreur que commettrait une transposition naïve plaçant l'injection avant le nettoyage. I3 et I4 découlent des passes 1, 2 et 9. I5 découle de l'ordre : les passes 3-5 s'exécutent **avant** la passe 9, donc chaque contrainte est recopiée avant d'être retirée. I6 est une contrainte de non-transformation, vérifiable par absence de `ToUpper` dans le pipeline.

**Terminaison.** Chaque passe est définie par récursion structurelle sur un arbre fini ; la hauteur décroît strictement à chaque appel récursif. Les onze passes sont composées séquentiellement, chacune terminant avant la suivante. La fonction est donc totale sur tout JSON fini.

**Complexité.** Temps `Θ(11 · n)` pour *n* nœuds, plus `Θ(n)` pour la synthèse d'`items`, soit `Θ(n)` à facteur constant 12. Mémoire `Θ(11 · n)` en allocations transitoires si les passes ne mutent pas, réductible à `Θ(n)` en mutant en place — recommandation explicite : **muter en place** côté Go, contrairement à l'amont, pour éviter onze copies d'un catalogue MCP volumineux.

### 2.5 Jeu de cas limites

| Cas | Entrée | Sortie attendue | Justification |
|---|---|---|---|
| Tableau sans `items` | `{"type":"array"}` | `{"type":"array","items":{"type":"string"}}` | I2 ; comble `arranger.go:314-315` |
| `const` seul | `{"const":"foo"}` | `{"enum":["foo"]}` | Passe 2 ; contrat `ANTIGRAVITY_API_SPEC.md:333` |
| `$ref` local | `{"$ref":"#/$defs/Addr"}` | `{"type":"object","description":"See: Addr"}` | Passe 1 ; `:66-73` |
| `required` fantôme | `{"type":"object","properties":{"a":{}},"required":["a","b"]}` | `required:["a"]` | Passe 10 ; `:587-596` |
| `required` entièrement fantôme | `{"type":"object","properties":{"a":{}},"required":["b"]}` | `required` supprimé | `:591-592` |
| Objet sans propriétés | `{"type":"object"}` | `properties:{_placeholder:boolean}`, `required:["_placeholder"]` | Passe 11 ; `:626-641` |
| Union de types | `{"type":["string","null"]}` | `{"type":"string","description":"(nullable)"}` | Passe 8 ; `:473-491` |
| `anyOf` d'énumérations | `{"anyOf":[{"const":"text"},{"const":"markdown"}]}` | `{"type":"string","enum":["text","markdown"]}` | `:388-402` ; cas réel WebFetch |
| Outil d'édition (`edit`) | `old_string`/`new_string` en `string` requis | inchangé hormis annotations | Aucune contrainte non supportée en jeu ; vérifier la non-régression |
| Schéma MCP volumineux | arbre de profondeur > 6 | troncature gérée par `maxCompactSchemaDepth` | `arranger.go:40`, `:292-297` |

### 2.6 Confrontation à la table de verdicts

**Verdict : transposable, avec un périmètre partiel.**

- Verdict n°1 (**PARTIAL**) : les deux trous annoncés sont réels et confirmés — `required` émis sans filtrage (`gemini/arranger.go:173-176`, où `stringList(params["required"])` alimente `pathFirst` sans jamais consulter `params["properties"]`) et `items` jamais synthétisé (`:314-315`). Le pipeline de l'axe 1 les comble tous les deux. La préservation de `$ref`/`$defs`/`allOf` par le proxy (`:183-194`) est en revanche un choix **délibéré et correct** (voir §5.1) : la passe 1 de l'amont ne doit **pas** être transposée à l'identique, car le proxy Go n'est pas contraint par un validateur protobuf rejetant `$ref`.
- Verdict n°2 (**ALREADY PRESENT**) : aucune recommandation. La validation d'exécution (`schema_validator.go`) est plus stricte que tout ce que propose l'amont.
- Verdict n°3 (**ALREADY PRESENT**) : `FormatToolsInstruction` existe et est invoqué (`gemini/arranger.go:80`, `api/chat_handler.go:443`). Seule la *teneur* des 11 règles doit évoluer (axe 2), pas le mécanisme.

---

## 3. Axe 2 — Prévention des hallucinations d'outils

### 3.1 Le mécanisme : double verrouillage descriptif

L'amont applique deux injections complémentaires, toutes deux réservées au chemin Claude (`src/plugin/request.ts:1305-1318`, sous `if (enableToolHardening && isClaude && …)`).

**Verrou 1 — signatures de paramètres dans chaque description d'outil.** `formatTypeHint` (`src/plugin/request-helpers.ts:2452-2505`) produit une chaîne typée à partir du schéma du paramètre, puis `injectParameterSignatures` (`:2519-2559`) concatène `"\n\n⚠️ STRICT PARAMETERS: {params}."` à la description de l'outil. Table d'émission exacte :

| Forme du paramètre | Hint émis | Ancre |
|---|---|---|
| `enum` de 1 à 5 valeurs littérales | `string ENUM["a", "b"]` | `:2456-2460` |
| `enum` de plus de 5 valeurs | `string ENUM[N options]` | `:2461` |
| `const` | `string CONST=<valeur JSON>` | `:2464-2467` |
| `array` d'`object` avec propriétés (profondeur < 1) | `ARRAY_OF_OBJECTS[n: type REQUIRED, …]` | `:2473-2483` |
| `array` d'`object` sans propriétés | `ARRAY_OF_OBJECTS` | `:2484` |
| `array` d'un autre type | `ARRAY_OF_<TYPE>` | `:2486` |
| `array` sans `items` | `ARRAY` | `:2488` |
| `object` avec propriétés (profondeur < 1) | `object{n: type REQUIRED, …}` | `:2491-2501` |
| autre | type brut (`string`, `number`, …) | `:2504` |

La liste des paramètres est ensuite composée en `"<nom> (<hint>[, REQUIRED])"` (`:2543-2547`), et la signature n'est ajoutée que si `properties` est non vide (`:2541`) et si la description ne contient pas déjà `"STRICT PARAMETERS:"` (`:2531`). Cette garde anti-doublon est essentielle : sans elle, un retry ré-injecterait la signature dans une description déjà enrichie.

**Verrou 2 — clause de durcissement au niveau système.** `injectToolHardeningInstruction` (`:2568-2608`) place `CLAUDE_TOOL_SYSTEM_INSTRUCTION` en **première** position du `systemInstruction` : `parts.unshift(instructionPart)` si `parts[]` existe (`:2588-2590`), reconstruction `[instruction, {text: ancien}]` si c'est une chaîne (`:2591-2595`), création sinon (`:2596-2607`). La garde anti-doublon porte sur `"CRITICAL TOOL USAGE INSTRUCTIONS"` (`:2578`). Le texte verbatim (`src/constants.ts:188-199`) est le suivant :

```text
CRITICAL TOOL USAGE INSTRUCTIONS:
You are operating in a custom environment where tool definitions differ from your training data.
You MUST follow these rules strictly:

1. DO NOT use your internal training data to guess tool parameters
2. ONLY use the exact parameter structure defined in the tool schema
3. Parameter names in schemas are EXACT - do not substitute with similar names from your training
4. Array parameters have specific item types - check the schema's 'items' field for the exact structure
5. When you see "STRICT PARAMETERS" in a tool description, those type definitions override any assumptions
6. Tool use in agentic workflows is REQUIRED - you must call tools with the exact parameters specified

If you are unsure about a tool's parameters, YOU MUST read the schema definition carefully.
```

Le gabarit de signature est `CLAUDE_DESCRIPTION_PROMPT = "\n\n⚠️ STRICT PARAMETERS: {params}."` (`src/constants.ts:205`).

### 3.2 Pourquoi la redondance fonctionne (question 1)

L'argument de l'amont est explicite dans ses commentaires : la description d'outil est le seul champ que le modèle lit **à côté** du nom de l'outil au moment de choisir son action, alors que le schéma formel est un objet structuré qui, pour Gemini 4, est aplati en JSON textuel dans un bloc `[AVAILABLE TOOLS]`. Trois raisons expliquent l'efficacité de la redondance :

1. **Elle aligne deux représentations concurrentes.** Le modèle a mémorisé, pour des outils célèbres (`read`, `edit`, `write`, `bash`), une signature d'entraînement. Quand le schéma réel diffère (par exemple `oldString` au lieu de `old_str`), le modèle doit arbitrer. Une signature en prose, adjacente au nom, réduit la distance entre le point de décision et la contrainte.
2. **Elle rend explicite l'implicite.** Un schéma `{"type":"string"}` sans hint ne dit rien sur la nature de la valeur. `path (string, REQUIRED)` dit à la fois le type et la nécessité. Pour un modèle sensible aux hallucinations de paramètres, cette redondance coûte quelques dizaines de tokens et évite un tour complet.
3. **Elle survit à l'aplatissement.** C'est le point décisif pour `free-gemini-api` : le proxy ne transmet pas de protobuf, il **sérialise le catalogue en JSON dans une chaîne de prompt** (`gemini/arranger.go:93-98`, `:131-163`). Une contrainte qui vit dans `description` survit donc exactement comme une contrainte qui vit dans `properties` — mais elle est lue en langage naturel, à l'endroit où le modèle prend sa décision.

**Limite honnête.** Aucune mesure d'impact n'est fournie par l'amont : les tests (`src/plugin/request-helpers.test.ts:1230-1400`) vérifient la forme des chaînes, pas le taux de réussite des appels. L'affirmation « cela réduit les hallucinations » est donc une hypothèse d'ingénierie, cohérente avec la littérature sur le prompt, mais **non mesurée** dans le dépôt source. Le rapport ne peut pas prétendre le contraire.

### 3.3 Où injecter sans polluer (question 2)

Le proxy Go possède déjà six points d'injection distincts, tous documentés :

| Injecteur | Emplacement | Portée |
|---|---|---|
| `formatSchemaRetryDirective` | `api/chat_handler.go:50-56` | curative, après échec de validation |
| `formatTruncationRetryDirective` | `:62-64` | curative, mutation tronquée |
| `formatPseudoReportRetryDirective` | `:72-74` | curative, pseudo-rapport |
| `unlimitedToolExecutionDirective` | `:79` | préventive, préfixe tout rejeu complet |
| `formatDoomLoopDirective` | `api/doom_loop.go:341-357` | recence maximale, après transcript |
| `formatRecentToolErrorDirective` | `api/session_transcripts.go:479` | recence maximale, erreur d'outil |
| `FormatToolsInstruction` | `gemini/arranger.go:80-129` | préventive, en tête de prompt |

Le bon emplacement pour les signatures est **`FormatToolsInstruction`**, et pour la clause de durcissement **la règle n°1 existante**, pas un nouveau bloc. Raisons :

- `FormatToolsInstruction` est déjà le bloc `[AVAILABLE TOOLS]` + `[TOOL CALL INSTRUCTIONS]`, injecté une seule fois en tête (`api/chat_handler.go:441-444`, `:496-499`). Ajouter les signatures dans les descriptions d'outils **ne déplace pas le préfixe du prompt** : les descriptions sont à l'intérieur du bloc déjà présent. C'est le choix à coût de cache nul.
- Un bloc `CRITICAL TOOL USAGE INSTRUCTIONS` séparé en tête déplacerait le préfixe et invaliderait la continuité de session (`api/chat_handler.go:754-776` montre que le prompt de continuation ne renvoie qu'un delta). Il faut donc **fusionner** la clause dans les règles existantes 1-11 (`gemini/arranger.go:100-114`) plutôt que l'empiler.
- Le mode `[40]` ne change qu'un en-tête JSPB (`gemini/registry.go:109-114`) : aucune interaction avec le texte du prompt. La clause de durcissement peut donc être commune aux deux modes sans risque.

### 3.4 Préventif vs curatif (question 3)

Les deux dispositifs ne sont **pas** redondants, et la démonstration est structurelle :

| | Hints préventifs | Directive curative |
|---|---|---|
| Moment | avant la génération | après un échec de validation |
| Mécanisme | réduit la probabilité d'un appel non conforme | corrige un appel déjà non conforme |
| Coût | tokens constants par requête | un tour de génération consommé sur `maxToolChatAttempts = 4` |
| Ancrage | `arranger.go:80-129` | `chat_handler.go:50-56`, `:916-925` |

Le budget d'outils est de quatre tentatives (`api/chat_handler.go:111`). Chaque échec de schéma en consomme une et invalide la session (`:916-925` : `invalidateSessionTurn` puis `forceFull`). Les hints préventifs attaquent donc **le numérateur du taux d'échec**, la directive curative **le dénominateur restant**. Complémentarité, pas redondance.

### 3.5 Pseudocode de la refonte de `FormatToolsInstruction`

```text
FONCTION FormatToolsInstructionV2(outils) -> texte
  PRÉCONDITION   outils est une liste de déclarations OpenAI (function.parameters ou function.inputSchema)
  POSTCONDITION  Invariant S1 : toute description non vide contient au plus UNE signature
                 Invariant S2 : la signature liste exactement les propriétés du schéma, dans l'ordre
                                path-first puis required puis alphabétique (ordre de orderedPropertyKeys)
                 Invariant S3 : chaque paramètre requis porte le suffixe ", REQUIRED"
                 Invariant S4 : aucun outil sans `properties` ne reçoit de signature
                 Invariant S5 : les règles 1-11 existantes restent inchangées, la clause de
                                durcissement est fusionnée dans la règle 4 (arguments)
  SI outils est vide ALORS RENDRE ""
  catalogue ← CompacterDéfinitions(outils)          // comportement actuel conservé

  POUR CHAQUE outil DANS catalogue FAIRE
      propriétés ← outil.parameters.properties OU ∅
      SI propriétés est vide ALORS CONTINUER          // S4
      requis ← Ensemble(outil.parameters.required OU ∅)
      POUR CHAQUE (nom, schéma) DANS propriétés FAIRE
          hint ← HintDeType(schéma, profondeur=0)
          fragment ← nom + " (" + hint
          SI nom ∈ requis ALORS fragment ← fragment + ", REQUIRED"
          fragment ← fragment + ")"
          accumuler fragment
      FIN POUR
      SI outil.description contient déjà "STRICT PARAMETERS:" ALORS CONTINUER   // S1
      outil.description ← outil.description + "\n\n⚠️ STRICT PARAMETERS: " + joindre(", ") + "."
  FIN POUR

  écrire "[AVAILABLE TOOLS]" + JSON(catalogue)
  écrire "[TOOL CALL INSTRUCTIONS]"
  écrire règles 1..3
  écrire règle 4 enrichie :
      "…placer `path` en premier… Exact-anchor… Les noms de paramètres du schéma sont
       EXACTS : n'utilise pas ton corpus d'entraînement pour deviner un paramètre.
       Quand une description contient « STRICT PARAMETERS », ces définitions de type
       priment sur toute supposition."
  écrire règles 5..11
  RENDRE texte
FIN FONCTION

FONCTION HintDeType(schéma, profondeur) -> chaîne
  SI schéma.enum est une liste ALORS
      SI longueur ≤ 5 ALORS RENDRE "string ENUM[" + littéraux joints + "]"
      SINON RENDRE "string ENUM[" + longueur + " options]"
  SI schéma.const existe ALORS RENDRE "string CONST=" + JSON(schéma.const)
  SI schéma.type = "array" ALORS
      items ← schéma.items
      SI items est un objet ALORS
          SI items.type = "object" ET items.properties non vide ET profondeur < 1 ALORS
              RENDRE "ARRAY_OF_OBJECTS[" + liste(items.properties, items.required) + "]"
          SI items.type = "object" ALORS RENDRE "ARRAY_OF_OBJECTS"
          RENDRE "ARRAY_OF_" + majuscules(items.type)
      RENDRE "ARRAY"
  SI schéma.type = "object" ET schéma.properties non vide ET profondeur < 1 ALORS
      RENDRE "object{" + liste(schéma.properties, schéma.required) + "}"
  RENDRE schéma.type OU "unknown"
FIN FONCTION
```

**Preuve des invariants.** S1 : la garde d'idempotence est évaluée **avant** toute concaténation et porte sur la sous-chaîne exacte `STRICT PARAMETERS:` — un second appel sur une sortie du premier est donc un point fixe. S2 : l'itération réutilise l'ordre de `orderedPropertyKeys` (`gemini/arranger.go:207-235`) et non l'ordre d'insertion de la map Go, ce qui rend la sortie déterministe — condition nécessaire pour que S1 soit testable. S3 : la présence dans `required` est testée par appartenance ensembliste, en `O(1)` amorti. S4 : le `CONTINUER` sur `properties` vide reproduit exactement la garde amont (`request-helpers.ts:2541`). S5 : la fusion dans la règle 4 plutôt que l'ajout d'une règle 12 évite le déplacement du préfixe.

**Terminaison.** La boucle externe parcourt un tableau fini ; la boucle interne parcourt une map finie ; `HintDeType` décroît `profondeur` et s'arrête à 1. Composition de trois boucles finies, donc terminaison immédiate.

**Complexité.** Temps `Θ(Σ |properties|)` sur l'ensemble des outils, plus `Θ(1)` par hint ; la sérialisation JSON est `Θ(taille du catalogue)`. Mémoire `Θ(taille du prompt)` pour la sortie, plus `O(|properties|)` transitoire par outil. Aucune récursion non bornée : la profondeur est plafonnée à 1 par construction, ce qui borne aussi la taille d'un hint.

### 3.6 Gain attendu sur `schema_validator.go`

Évaluation qualitative, faute de mesure amont exploitable :

- La validation d'exécution (`gemini/schema_validator.go:30-62`) rejette un appel si la propriété est absente, mal nommée, mal typée, ou si `path` est vide (`:69-77`). Les quatre causes dominantes d'échec — nom de paramètre issu du corpus, type d'`items` erroné, `required` omis, `old_string` vide — sont précisément celles que la signature et la règle enrichie adressent nommément.
- Le budget `maxToolChatAttempts = 4` (`api/chat_handler.go:111`) étant partagé entre schéma, troncature, pseudo-rapport et refus, chaque échec de schéma évité rend une tentative disponible aux trois autres catégories. Le gain se mesure donc en **tentatives préservées**, pas seulement en erreurs évitées.
- Le risque de sur-injection existe : les descriptions sont tronquées à 180 runes (`gemini/arranger.go:38`, `:147`) et les descriptions de propriétés à 120 (`:39`, `:311`). Une signature ajoutée après troncature n'est pas elle-même tronquée, mais elle peut porter une description déjà amputée. Il faut donc **calculer la signature sur le schéma, jamais sur la description tronquée**, ce que le pseudocode respecte.

### 3.7 Confrontation à la table de verdicts

**Verdict : transposable, sur un seul des trois verdicts concernés.**

- Verdict n°3 (**ALREADY PRESENT**) : `FormatToolsInstruction` existe (`arranger.go:80`) et a un appelant unique confirmé (`api/chat_handler.go:443`). La recommandation ne crée donc **pas** un mécanisme, elle enrichit un mécanisme existant. Conforme à la règle anti-redite.
- Verdict n°4 (**ABSENT**) : confirmé par recherche exhaustive — zéro occurrence de `STRICT PARAMETERS` et d'`ARRAY_OF_OBJECTS` dans tout le dépôt Go. La proposition est donc légitime.
- Verdict n°5 (**ALREADY PRESENT**) : six injecteurs recensés (`chat_handler.go:50`, `:62`, `:72`, `:79`, `doom_loop.go:341`, `session_transcripts.go:479`) plus `FormatToolsInstruction`. Aucun septième injecteur n'est proposé : la clause de durcissement est **fusionnée** dans un injecteur existant.

---

## 4. Axe 3 — Orphelins & cohérence Task Mode [40]

### 4.1 Reformulation obligatoire du problème

Le brief impose une reformulation, et l'audit confirme qu'elle est **indispensable**, non par prudence mais par nécessité factuelle. Le proxy Go ne manipule aucune structure `contents[]`, aucun `functionCall`, aucun `functionResponse` : recherche exhaustive, zéro occurrence dans tout le dépôt. L'historique est aplati en prose dans un unique `strings.Builder` (`api/chat_handler.go:490-530`) :

| Rôle OpenAI | Sérialisation amont | Ancre |
|---|---|---|
| `system` initial | `[System]: <contenu>` | `:491-495` |
| `assistant` avec `tool_calls` | `Assistant called tools: <JSON des tool_calls>` | `:511-516` |
| `assistant` sans `tool_calls` | `Assistant: <contenu>` | `:517-521` |
| `tool` | `[Tool Result for %s]: <contenu>` avec `m.Name` | `:522-523` |
| `system` tardif | `[System Notice]: …` | `:524-528` |

Trois défauts en découlent, tous vérifiés :

1. **`ToolCallID` n'est jamais utilisé.** Le champ existe (`api/types.go:28`) et il est compté dans l'estimation de taille (`api/observation_masker.go:113`), mais il n'apparaît nulle part dans la sérialisation. Le seul identifiant transmis est `m.Name`. Conséquence exacte : si un harnais renseigne `tool_call_id` sans `name`, la ligne produite est `[Tool Result for ]: …`. C'est un cas de rupture silencieux, pas théorique — le brief le cite explicitement.
2. **Le JSON des `tool_calls` est sérialisé en bloc**, avec `id`, `type`, `function.name` et `function.arguments`. Le modèle voit donc bien l'appel et son identifiant, mais le résultat ne porte jamais le même identifiant. La bijection appel ↔ résultat n'est pas exprimée : elle est **inférée** par le modèle à partir du nom et de l'ordre.
3. **`ThrottleMutatingToolCalls` crée des orphelins par construction.** La fonction conserve tous les appels de lecture jusqu'à la première mutation incluse, puis **abandonne** tout le reste (`gemini/arranger.go:803-815`). L'appel est appliqué deux fois : en streaming (`api/chat_handler.go:1003`) et en non-streaming (`:1358`). Les appels supprimés ne reçoivent jamais de résultat — et le modèle, au tour suivant, voit un `Assistant called tools: [...]` partiel sans aucune compensation. C'est une divergence assumée avec le contrat OpenAI, et elle n'est compensée par rien.

### 4.2 La cascade amont, et pourquoi elle est ordonnée par confiance

`fixToolResponseGrouping` (`src/plugin/request-helpers.ts:2044-2216`) opère en deux temps : collecte des réponses par identifiant (premier gagne, doublons ignorés — `:2068-2075`), puis satisfaction des groupes d'appels en attente de la fin vers le début (`:2078-2091`), un seul groupe par message de réponse. Les groupes non satisfaits sont traités après la boucle, triés par index décroissant pour ne pas décaler les insertions (`:2122`), puis chaque identifiant manquant descend une cascade à quatre niveaux (`:2127-2204`) :

| Niveau | Règle | Coût de confiance | Ancre |
|---|---|---|---|
| 0 | correspondance exacte par `id` | nul — c'est l'appariement voulu | `:2131-2134` |
| 1 | correspondance par **nom de fonction** | faible — le nom est stable, l'identifiant ne l'est pas | `:2139-2146` |
| 2 | orphelin nommé `unknown_function` | moyen — réponse dégradée mais rattachable | `:2148-2156` |
| 3 | premier disponible (`keys().next()`) | **élevé** — aucune garantie sémantique | `:2158-2161` |
| 4 | placeholder synthétique | nul par construction — marqué `recovered: true` | `:2182-2202` |

La réponse à la question 2 du brief est donc nuancée. La cascade **est** ordonnée par coût de confiance, mais elle contient un niveau intrinsèquement dangereux : le niveau 3. Quand plusieurs réponses orphelines coexistent et qu'aucune ne correspond ni par identifiant ni par nom, « premier disponible » apparie un résultat d'outil A à un appel d'outil B. L'appariement est alors **faux**, et pire, il est *silencieusement* faux : le code réécrit ensuite l'identifiant et le nom de l'orphelin pour les faire correspondre à l'attendu (`:2168-2171`), ce qui **détruit la trace** de la substitution. Le modèle reçoit un résultat cohérent en apparence pour un appel qu'il n'a pas fait dans cet ordre.

Le placeholder (`:2183-2195`) est, lui, correctement conçu : texte littéral explicite, marqueur `recovered: true`, nom de repli `unknown_function`. C'est le seul niveau qui indique au modèle que la réponse est synthétique.

### 4.3 Le défaut D3 : mutation en place

La correction du brief est confirmée. Le niveau 1 de la cascade mute l'objet collecté (`:2168-2169`), et cet objet est une **référence** vers un élément du tableau `contents[]` d'origine, puisque la collecte n'a jamais cloné (`:2073` : `collectedResponses.set(respId, resp)` sur le `resp` extrait du `parts` d'entrée). Un retry du même payload après échec réutilise donc des objets dont l'identifiant et le nom ont été réécrits par la passe précédente. L'effet est cumulatif et non idempotent : deux exécutions successives ne produisent pas le même résultat. **Toute transposition Go doit cloner avant de réécrire.**

### 4.4 Réponses aux questions d'audit

**Question 1 — l'invariance d'alternance est-elle respectée par un transcript en prose ?** Non, et elle ne peut pas l'être structurellement. Gemini exige qu'un appel soit suivi de sa réponse avant toute nouvelle prise de parole. Un transcript en prose n'exprime cette contrainte que par l'ordre des lignes, sans marqueur de complétude. Trois modes de rupture sont identifiables dans le code actuel :

- **Rupture par suppression** : `ThrottleMutatingToolCalls` (`arranger.go:803-815`) retire les appels tardifs ; aucune ligne `[Tool Result for …]` ne leur correspondra jamais.
- **Rupture par nom vide** : `[Tool Result for ]` (`chat_handler.go:523`) quand seul `tool_call_id` est fourni.
- **Rupture par masquage** : `MaskOldToolObservationsWithOverhead` remplace le corps d'anciennes observations par `... [compacted: N bytes omitted; re-read if needed]` (`api/observation_masker.go:152`). Le message reste présent, mais son contenu devient un marqueur : le modèle doit réémettre une lecture. Ce n'est pas une rupture de bijection, c'est une perte d'information explicite et correctement signalée — à distinguer des deux précédentes.

**Question 2 — la cascade est-elle ordonnée par confiance ?** Oui, sauf le niveau 3, comme démontré en §4.2. La recommandation est donc de **conserver l'ordre et de supprimer le niveau 3** dans la transposition.

**Question 3 — contrat du placeholder côté Go ?** Trois exigences :

- **Texte** : reprendre la formulation amont, qui est bonne — `Tool response was lost during context processing. This is a recovered placeholder.` — et l'adapter à la prose Go : `[Tool Result for <nom>]: [recovered] Tool response was lost during context processing. Do not assume this tool produced output.` Le second membre de phrase est un ajout : en prose, un placeholder ressemble à un vrai résultat, il faut donc **interdire explicitement l'inférence**.
- **Marqueur** : préfixe stable `[recovered]` cherchable, pour que le modèle puisse le reconnaître et que les tests puissent l'asserter.
- **Visibilité** : pleinement visible du modèle. Un placeholder caché laisserait le modèle croire à une réponse réelle absente, ce qui est pire que l'absence elle-même.

### 4.5 Pseudocode de réconciliation adapté à `free-gemini-api`

```text
FONCTION RéconcilierTranscript(messages) -> messages'
  PRÉCONDITION   messages est la liste OpenAI d'origine, non modifiée par l'appelant
  POSTCONDITION  Invariant R1 : pour tout message assistant portant des tool_calls,
                     chaque appel non throttlé possède exactement un message role:"tool"
                     correspondant, soit réel, soit synthétique marqué [recovered]
                 Invariant R2 : aucun message synthétique n'est produit si un résultat
                     réel de même identifiant existe déjà (idempotence)
                 Invariant R3 : aucun message d'entrée n'est muté (clonage systématique)
                 Invariant R4 : les messages réels conservent leur ordre relatif
                 Invariant R5 : jamais deux résultats pour un même identifiant d'appel
  copie ← CopierSurface(messages)                  // R3
  appelsEnAttente ← FileVide()                     // FIFO globale, ordre d'émission
  résultatsVus ← EnsembleVide()

  POUR CHAQUE message DANS copie FAIRE
      SI message.role = "assistant" ET message.tool_calls non vide ALORS
          POUR CHAQUE appel DANS message.tool_calls FAIRE
              SI appel.id non vide ALORS Enfiler(appelsEnAttente, appel)
          FIN POUR
      SINON SI message.role = "tool" ALORS
          clé ← message.tool_call_id
          SI clé est vide ALORS clé ← message.name      // repli explicite, pas silencieux
          SI clé est vide ALORS marquer message comme non appariable
          SINON
              SI clé ∈ résultatsVus ALORS marquer message comme doublon   // R5
              SINON ajouter résultatsVus ← clé ; RetirerDeFile(appelsEnAttente, clé)
          FIN SI
      FIN SI
  FIN POUR

  // Phase de synthèse : un seul point d'insertion, juste après le message
  // assistant qui a émis l'appel resté sans réponse.
  POUR CHAQUE appel DANS appelsEnAttente FAIRE     // ordre d'émission préservé
      SI appel.id ∈ résultatsVus ALORS CONTINUER   // R2
      message ← NouveauMessage(role="tool", tool_call_id=appel.id,
                               name=appel.function.name)
      message.content ← "[recovered] Tool response was lost during context" +
                        " processing. Do not assume this tool produced output."
      insérer message immédiatement après le message assistant émetteur
      ajouter résultatsVus ← appel.id
  FIN POUR
  RENDRE copie
FIN FONCTION
```

**Preuve de terminaison.** La première boucle parcourt un tableau fini de taille *m* ; la boucle imbriquée parcourt la liste finie des `tool_calls` d'un message ; la phase de synthèse parcourt la file, qui ne peut contenir plus d'appels qu'il n'en existe dans l'entrée. Aucune boucle ne réenfiler d'élément dans sa propre source (le `CONTINUER` sur `résultatsVus` empêche tout réenfilage). Terminaison en `O(m + a)` où *a* est le nombre d'appels.

**Complexité.** Temps `Θ(m + a)` avec un ensemble et une file à accès `O(1)` amorti. Mémoire `Θ(m + a)` pour la copie de surface et la file, soit une empreinte linéaire bornée par la taille du transcript — ce qui répond directement à la question 3 du brief sur le caractère borné de l'état.

**Preuve de R1.** Tout appel est soit retiré de la file par un résultat réel dont la clé correspond (cas nominal), soit présent en fin de parcours et matériellement synthétisé. Les appels sans identifiant ne sont pas mis en file : c'est une **limite assumée**, car un appel sans identifiant n'est pas appariable de façon fiable. La file doit donc enregistrer cette catégorie à part et la signaler, plutôt que de la laisser silencieusement non réconciliée.

**Preuve de R2.** La condition `appel.id ∈ résultatsVus` est évaluée immédiatement avant la synthèse, sur un ensemble mis à jour à chaque insertion réelle **et** synthétique. Un second appel de la fonction sur une sortie du premier est donc un point fixe : tous les identifiants sont dans `résultatsVus`.

**Preuve de R4.** La copie est faite par surface et les insertions se font à un offset calculé après coup ; pour éviter tout décalage, l'implémentation doit construire une **nouvelle** liste en parcourant la copie et en émettant les synthétiques au bon endroit, plutôt que d'insérer dans un tableau en cours d'itération. C'est la seule subtilité d'implémentation à ne pas rater.

### 4.6 Confrontation à la table de verdicts

**Verdict : transposable sous forme reformulée ; un verdict déjà couvert.**

- Verdict n°6 (**PROSE, pas de `contents[]`**) : c'est le fait fondateur de cet axe. Il interdit toute transposition littérale de `fixToolResponseGrouping`, et impose la reformulation ci-dessus. La recommandation porte sur `api/chat_handler.go:501-530`, pas sur une structure inexistante.
- Verdict n°7 (**ABSENT**) : confirmé. `ToolCallID` n'est jamais sérialisé (seul `m.Name` l'est, `:523`), et `ThrottleMutatingToolCalls` supprime des appels sans compensation (`arranger.go:803-815`). Les deux moitiés de la recommandation sont donc légitimes.
- Verdict n°8 (**ALREADY PRESENT**) : la détection de boucle agentique existe et est plus riche que tout ce que propose l'amont — trois signaux, seuils 3/5, cycles 2/3, lectures passives > 4, coupure en HTTP 429 (`api/doom_loop.go:20-38`, `:113-140`, `api/chat_handler.go:600-611`). **Aucune recommandation de détection.** La réconciliation d'orphelins ne doit pas être présentée comme une détection de boucle : ce sont deux mécanismes disjoints.
- Verdict n°9 (**PARTIAL, en-tête seulement**) : confirmé. `BuildModeHeaderValue` n'émet que `[40]` (`gemini/registry.go:109-114`) ; `ResolveModel` force `AppMode = 40` sur en-tête, paramètre de requête ou suffixe `-task` (`api/chat_service.go:152-162`). Aucune promptification spécifique, aucun budget d'essais distinct. La question de la promptification du mode tâche est légitime, mais elle est **hors du périmètre utile de cet axe** : l'amont n'a aucun équivalent de promptification `[40]`, donc il n'y a rien à transposer. Le seul apport transposable est l'invariant R1, qui bénéficie identiquement aux deux modes.

---

## 5. Axe 4 — Résilience du Thinking (Neon & Argon)

### 5.1 La clôture synthétique de tour (question 1)

`closeToolLoopForThinking` (`src/plugin/thinking-recovery.ts:264-294`) est **non mutant** : il construit une nouvelle liste par concaténation (`:293`). Il opère en trois temps.

1. **Retrait de tout bloc de pensée** (`:266` → `stripAllThinkingBlocks`, `:189-219`). La fonction traite les deux familles : `parts[]` filtrés sur `isThinkingPart` (`:194-203`) et `content[]` filtré sur `type !== "thinking" && "redacted_thinking"` (`:206-215`). Garde importante : si le filtrage vide entièrement un message, le message d'origine est **conservé** (`:199-201`, `:211-213`) — on ne crée jamais de message vide.
2. **Comptage des résultats d'outils en fin de conversation** (`:269` → `countTrailingToolResults`, `:224-…`, fin de plage `non vérifié`). Le comptage remonte depuis la fin et s'arrête à la première vraie intervention utilisateur.
3. **Injection de deux messages synthétiques** : un `role:"model"` dont le texte dépend du compte, puis un `role:"user"` valant `[Continue]` (`:282-291`). Les littéraux exacts (`:273-279`) sont `[Processing previous context.]` pour zéro résultat, `[Tool execution completed.]` pour un, `[N tool executions completed.]` au-delà.

**Pourquoi « réparer » échoue systématiquement.** Un bloc de pensée tronqué est, du point de vue du fournisseur, un bloc **invalide** : il porte une signature ou une métadonnée qui ne correspond plus à son contenu. Toute tentative de compléter le texte produit un bloc dont la signature ne sera pas vérifiable — et pour Gemini comme pour Claude, le rejet est structurel, pas probabiliste. Il n'existe aucune réparation possible côté client : soit le bloc est intact, soit il est fautif. La seule stratégie correcte est donc de **le retirer** et de fermer le tour proprement.

**Pourquoi la clôture force une régénération propre.** L'invariance d'alternance exige qu'un tour se termine par une prise de parole du modèle. Si le tour est interrompu après des résultats d'outils, le fournisseur voit un tour « en cours ». En injectant un `model` synthétique (qui clôt le tour) puis un `user` synthétique (qui en ouvre un nouveau), le client force le fournisseur à traiter la suite comme un **nouveau tour** — donc à régénérer une pensée fraîche, sans hériter du contexte de pensée corrompu. C'est le « let it crash and start again » que le commentaire de `src/plugin/request.ts:1431-1435` revendique explicitement.

**La contrepartie, qui n'est pas négociable.** On perd toute la pensée du tour interrompu, et on ajoute deux messages qui ne viennent pas de l'utilisateur. Si le mécanisme se déclenche à tort, il dégrade réellement la qualité (perte de raisonnement utile) tout en augmentant le coût. C'est pourquoi le déclencheur doit être conservateur — voir §5.3.

### 5.2 Les trois heuristiques de compactage (question 2)

`looksLikeCompactedThinkingTurn` (`:330-372`) applique trois conditions en ET :

| # | Condition | Ancre |
|---|---|---|
| 1 | au moins un `functionCall` | `:337-341` |
| 2 | aucun `thought`/`thinking`/`redacted_thinking` | `:344-351` |
| 3 | aucun texte non-pensée **avant** le premier `functionCall` | `:354-371` |

**Elles ne suffisent pas, et un contre-exemple simple l'invalide.** Un modèle sans mode pensée qui émet un appel d'outil directement, sans texte préalable, satisfait les trois conditions : il a un `functionCall`, aucun bloc de pensée (parce qu'il n'en produit jamais), et aucun texte avant l'appel. L'heuristique le classera « compacté », alors qu'il n'a jamais eu de pensée à perdre. Le coût de cette erreur est double : on retire des blocs inexistants (sans effet) et on injecte la clôture synthétique (effet réel : deux messages parasites, perte du contexte de tour).

Le second contre-exemple est plus grave et concerne précisément le proxy Go : **le transcript en prose ne porte aucun `parts[]`**. Les trois heuristiques sont donc structurellement inapplicables telles quelles. Il faut les réexprimer sur les lignes de prose — et là, la signature d'un tour compacté devient beaucoup plus difficile à établir de façon fiable.

Le troisième contre-exemple est le défaut D6 : `hasPossibleCompactedThinking` (`:381-395`) n'accepte que `role === "model"` (`:389`), alors que `analyzeConversationState` accepte `"model" || "assistant"` (`:153`). Sur un transcript Anthropic, la détection ne voit rien. La fonction est de surcroît un **export mort** : le seul appelant trouvé est le fichier de test (`src/plugin/thinking-recovery.test.ts:5`, `:286-319`). Le brief est confirmé sur E7.

### 5.3 Matrice de configuration Thinking Gemini 4 — reconstruite depuis le registre Go

Cette matrice **n'existe pas** dans le dépôt amont. Vérification faite : `docs/MODEL-VARIANTS.md` (182 lignes) ne mentionne aucune des chaînes `gemini-4`, `argon`, `neon`, `hydrogen` ; ses seules occurrences de niveaux concernent Claude (`thinkingConfig.thinkingBudget`, `:28-29`, `:43`), Gemini 3 (`thinkingLevel`, `:44`, `:71-90`) et Gemini 2.5 (`thinkingBudget`, `:45`, `:116-132`), avec le mapping déprécié `≤8192 → low`, `≤16384 → medium`, `>16384 → high` (`:141-145`). L'errata E3 est donc confirmé, et la matrice est reconstruite ci-dessous **exclusivement** depuis le registre Go.

Le niveau de thinking occupe l'**index 15** du tableau JSPB (`gemini/registry.go:116-157`, commentaire `:118-128`), avec l'encodage **Low = 1, Medium = 4, High = 2** (`:124`). Le gabarit complet est `[1,null,null,null,"<modeID>",null,null,null,[4,5,6,8,16,19,20],null,null,null,null,null,8,<niveau>,null,null,null,null]` (`:153`, `:180`, `:230`, `:320`, `:332`, `:345`, `:358`, `:397`, `:411`, `:425`).

| Modèle (ID canonique Go) | `ModeID` | Palier par défaut | Gabarit | Ancre |
|---|---|---|---|---|
| `gemini-4-hydrogen` (2ᵉ définition, effective) | `56fdd199312815e2` | **1** (Low figé) | `%s` seul, niveau littéral `1` | `registry.go:313-326` |
| `gemini-4-hydrogen-task` | `56fdd199312815e2` | **0** (non-thinking) | `[1,null,null,null,"%s",null,null,0,[4],null,null,2]` + `AppMode = 40` | `:187-199` |
| `gemini-4-hydrogen-thinking-low` | `56fdd199312815e2` | 1 | `%d` | `:354-365` |
| `gemini-4-hydrogen-thinking-medium` | `56fdd199312815e2` | 4 | `%d` | `:328-339` |
| `gemini-4-hydrogen-thinking-high` | `56fdd199312815e2` | 2 | `%d` | `:341-352` |
| `gemini-4-neon` | `e6fa609c3fa255c0` | **0** (non-thinking) | `…,0,[4],null,null,2]` | `:200-211` |
| `gemini-4-neon-thinking-low` | `e6fa609c3fa255c0` | 1 | `%d` | `:252-263` |
| `gemini-4-neon-thinking-medium` | `e6fa609c3fa255c0` | 4 | `%d` | `:239-250` |
| `gemini-4-neon-thinking-high` | `e6fa609c3fa255c0` | 2 | `%d` | `:226-237` |
| `gemini-4-argon` | `95a4c0abcba24364` | **1** (Low littéral, commentaire `:383`) | `%s` seul | `:379-390` |
| `gemini-4-argon-thinking-low` (alias `argon-low`) | `95a4c0abcba24364` | 1 | `%d` | `:420-432` |
| `gemini-4-argon-thinking-medium` (alias `argon-medium`) | `95a4c0abcba24364` | 4 | `%d` | `:406-418` |
| `gemini-4-argon-thinking-high` (alias `argon-high`) | `95a4c0abcba24364` | 2 | `%d` | `:392-404` |

**Deux observations factuelles importantes, non présentes dans le brief.**

**a) Duplication de `gemini-4-hydrogen`.** L'identifiant est déclaré une première fois avec un gabarit non-thinking `…,0,[4],…` (`:175-186`) — c'est la définition citée par le brief — puis une seconde fois avec un gabarit thinking-low littéral `…,8,1,…` et un commentaire de capture live daté du 2026-11-17 (`:312-326`). Or `registerInternal` écrit dans une map indexée par identifiant (`:554-556`) : la seconde définition **écrase** la première, et la map d'alias est également réécrite (`:558-562`). Le comportement effectif du proxy pour `gemini-4-hydrogen` est donc **thinking Low par défaut**, et non non-thinking comme la première définition le suggère. Ce n'est pas un bug fonctionnel (le choix semble délibéré, daté et commenté), mais c'est un piège de lecture : toute analyse qui cite `:175-186` sans lire `:312-326` conclut à l'inverse du comportement réel. Cette réserve n'invalide aucun verdict du brief ; elle précise l'état réel.

**b) `neon` et `hydrogen-task` sont non-thinking par défaut**, tandis qu'`argon` est Low par défaut. La conséquence pour l'axe 4 est directe : « escalader le palier de thinking » n'a pas le même sens selon le modèle de départ. Sur `argon`, on escalade de 1 vers 4 ou 2. Sur `neon`, on passe de l'absence totale de palier (`0`) à un palier explicite — ce qui est un **changement de forme de gabarit**, pas un simple changement de valeur.

### 5.4 Le trou Go, et pourquoi la signature est le vrai verrou (question 3)

Le proxy Go possède déjà l'extraction de pensée (`gemini/arranger.go:1528-1608`, avec terminaison sur `</thought>`, sur `<tool_call>` ou sur fin de flux, et retrait des balises partielles `:1592`) et un budget de tour vide dédié (`api/chat_handler.go:119-124`, `:888-900`, `:1257-1269`, `maxEmptyGenerationRetries = 2`). Ce qui manque est double :

1. **Aucun champ de signature de pensée.** Zéro occurrence de `thoughtSignature`/`thinkingMetadata` dans le dépôt. Or sans signature, un tour de pensée tronqué n'est pas *détectable* comme invalide : il est simplement du texte. Le proxy ne peut donc pas savoir qu'il doit clôturer — il peut seulement constater qu'un tour est vide.
2. **Aucune escalade de palier.** `maxEmptyGenerationRetries` rejoue le même modèle au même palier (`:888-900` : `ResetConversationStateSession` puis `forceFull`, sans changement de modèle). Un modèle dont le thinking est insuffisant pour produire une sortie échouera deux fois exactement de la même manière.

La transposition correcte n'est donc **pas** de copier `closeToolLoopForThinking` — qui opère sur `parts[]` — mais de reproduire son *effet* : détecter un tour improductif, retirer l'état corrompu, clôturer, et rouvrir. Côté Go, cela se traduit par l'ajout d'une ligne de clôture dans le transcript aplati, et par une escalade de palier.

### 5.5 Pseudocode du détecteur et de la réinitialisation propre

```text
FONCTION DétecterTourImpropre(messages, réponseBrute) -> (impropre, raison)
  PRÉCONDITION   messages est le transcript OpenAI ; réponseBrute est la sortie amont
  POSTCONDITION  Invariant T1 : un tour impropre ne contient NI appel d'outil exploitable
                     NI contenu visible non-pensée
                 Invariant T2 : la détection est monotone — un tour contenant un appel
                     d'outil valide n'est jamais déclaré impropre
                 Invariant T3 : la détection ne dépend d'aucun champ de signature
  (contenu, pensée) ← ExtraireThoughtsEtContenu(réponseBrute)   // arranger.go:1528
  SI contenu non vide après Trim ALORS RENDRE (faux, "")
  SI le transcript contient un outil accepté par le catalogue ALORS RENDRE (faux, "")   // T2
  SI pensée est vide ALORS RENDRE (vrai, "tour vide sans pensée")
  // La pensée existe mais rien n'en est sorti : tour tronqué ou bloqué
  RENDRE (vrai, "pensée présente, sortie absente")
FIN FONCTION

FONCTION ClôturerEtRelancer(messages, dernierNombreDeRésultats) -> messages'
  PRÉCONDITION   DétecterTourImpropre a rendu vrai
  POSTCONDITION  Invariant C1 : aucun bloc de pensée n'est conservé du tour clos
                 Invariant C2 : exactement deux lignes synthétiques sont ajoutées,
                     dans l'ordre modèle puis utilisateur
                 Invariant C3 : la fonction est non mutante sur l'entrée
                 Invariant C4 : le texte de clôture dépend du nombre de résultats
                     de la fin de tour, jamais du contenu de la pensée
  copie ← Copier(messages)                                    // C3
  // C1 : dans un transcript en prose, le retrait consiste à ne PAS réinjecter
  // la ligne de pensée. Le texte brut de la réponse n'est jamais rejoué.
  SI dernierNombreDeRésultats = 0 ALORS texteModèle ← "[Processing previous context.]"
  SINON SI dernierNombreDeRésultats = 1 ALORS texteModèle ← "[Tool execution completed.]"
  SINON texteModèle ← "[" + dernierNombreDeRésultats + " tool executions completed.]"
  FIN SI
  ajouter(copie, {role:"assistant", content: texteModèle})     // C2, C4
  ajouter(copie, {role:"user", content: "[Continue]"})         // C2
  RENDRE copie
FIN FONCTION

FONCTION EscaladerPalier(modèleCourant) -> modèleSuivant
  PRÉCONDITION   modèleCourant est un ID canonique du registre
  POSTCONDITION  Invariant E1 : le successeur existe dans le registre, sinon identité
                 Invariant E2 : le successeur a un palier STRICTEMENT supérieur
                                (0 → 1 → 4 → 2, ordre de capacité croissante documentée)
                 Invariant E3 : la fonction est idempotente au sommet — un modèle
                                déjà au palier maximal se rend lui-même
  palier ← registre[modèleCourant].thinking_level
  SI palier = 0 ALORS RENDRE varianteLow(modèleCourant)        // changement de gabarit
  SI palier = 1 ALORS RENDRE varianteMedium(modèleCourant)
  SI palier = 4 ALORS RENDRE varianteHigh(modèleCourant)
  RENDRE modèleCourant                                          // palier 2 = sommet
FIN FONCTION
```

**Preuve de terminaison.** Les trois fonctions sont des compositions de tests et d'ajouts d'éléments sur une liste finie ; aucune ne boucle. `EscaladerPalier` est une échelle à quatre échelons avec point fixe explicite au sommet, donc sa composition itérée `EscaladerPalier^k` termine trivialement en au plus 3 itérations avant fixation.

**Complexité.** `DétecterTourImpropre` est `Θ(taille de la réponse)` pour l'extraction (linéaire, `arranger.go:1528` est un parcours unique) plus `Θ(nombre d'appels)` pour la vérification par catalogue. `ClôturerEtRelancer` est `Θ(taille du transcript)` pour la copie. `EscaladerPalier` est `Θ(1)` si l'index de variantes est précalculé. Mémoire `Θ(taille du transcript)`, sans état persistant — c'est la différence essentielle avec le `Set` non borné du défaut D1.

**Preuve de T2 (sûreté).** La condition « un outil accepté par le catalogue » est évaluée **avant** toute autre, et elle est univoque : si elle est vraie, la fonction rend `faux`. Aucun tour productif ne peut donc être clos. C'est la garantie qui protège contre le contre-exemple de §5.2.

### 5.6 Confrontation à la table de verdicts

**Verdict : partiellement transposable ; deux verdicts déjà couverts.**

- Verdict n°10 (**ALREADY PRESENT**) : extraction des pensées confirmée (`arranger.go:1528-1608`, `:1612-1614`, `:1617-1623`) et diffusion SSE `delta.reasoning_content` (`api/chat_handler.go:804`, `:1059`). Aucun mécanisme d'extraction n'est proposé ; la recommandation se limite à **consommer** la sortie existante.
- Verdict n°11 (**ABSENT**) : confirmé (zéro occurrence). La recommandation est légitime, mais elle doit être **honnête sur sa limite** : ajouter un champ de signature côté Go n'a de sens que si le proxy sérialise des blocs de pensée vers un amont qui les vérifie. Or l'amont Gemini web reçoit une **prose** (`chat_handler.go:501-530`). La signature est donc utile comme *détecteur local de corruption*, pas comme jeton d'aller-retour. C'est une nuance majeure : la recommandation doit viser la détection, pas la transmission.
- Verdict n°12 (**ALREADY PRESENT (registre)**) : matrice reconstruite en §5.3. Aucune recommandation de configuration : elle existe. La recommandation ne porte que sur l'**escalade**, qui est absente.
- Verdict n°13 (**ABSENT**) : confirmé. `OpenAIChatCompletionRequest` n'a pas de champ de niveau de raisonnement (`api/types.go:67-74`) et `tool_choice` est parsé puis jamais lu (seule occurrence : `api/types.go:71`). La recommandation est légitime.
- Verdict n°14 (**PARTIAL**) : confirmé. Le budget de tour vide existe (`:119-124`, `:888-900`, `:1257-1269`) mais sans escalade de palier. La recommandation comble exactement l'écart annoncé.

---

## 6. Axe 5 — Optimisations du Streaming SSE

### 6.1 Le mécanisme amont, et le mécanisme inexistant (errata E2)

Le pipeline de streaming amont tient en quatre fonctions :

| Fonction | Rôle | Ancre |
|---|---|---|
| `hashString` | DJB2 : graine 5381, `hash * 33 + c`, `>>> 0`, hex non padé | `transformer.ts:13-19` |
| `createThoughtBuffer` | `Map<number, string>` indexée par candidat | `:21-28` |
| `transformStreamingPayload` | ne conserve que les lignes `data:`, ré-émet `{response}` | `:30-56` |
| `deduplicateThinkingText` | deux branches parallèles, même cœur de préfixe | `:58-173` |
| `flush` | décode le reste, injecte un `usageMetadata` synthétique | `:329-361` |

Le cœur de `deduplicateThinkingText` est identique dans les deux branches (Gemini `p.thought === true`, `:90-116` ; Claude `b.type === 'thinking'`, `:135-163`) : si `fullText.startsWith(sentText)`, on émet `fullText.slice(sentText.length)` et on mémorise l'instantané complet ; sinon, on émet le bloc entier et on remplace le tampon (`:114-115`, `:161-162`). Le hachage n'intervient qu'en amont de ce calcul (`:93-100`, `:138-146`) et **seulement si** `displayedThinkingHashes` est fourni.

**L'errata E2 est confirmé de façon catégorique.** Recherche exhaustive des chaînes `closeThought`, `antml`, `unterminated`, `premature` dans tout `src/` : zéro occurrence. Le mécanisme de « fermeture propre des balises `<thought>` en cas d'interruption réseau » **n'existe pas** en 2.1.2. `flush` (`:329-361`) se limite à deux actions : traiter la ligne résiduelle du tampon décodé (`:332-346`) et, si aucune ligne n'a porté `usageMetadata` (`:302`, `:313-315`, `:333-335`), injecter un objet synthétique à compteurs nuls (`:349-360`). Toute analyse qui décrirait une fermeture de balise serait une hallucination de mécanisme.

**Une seconde précision, non demandée par le brief mais nécessaire.** `transformStreamingPayload` (`:30-56`) ré-émet `data: {"response": …}` en **perdant les clés d'enveloppe** voisines de `response` — c'est exactement ce que le brief signale. Cette fonction, cependant, n'est pas celle utilisée par le transformateur principal (`createStreamingTransformer`, `:291-362`), qui appelle `transformSseLine` (`:175-224`). Les deux coexistent : `transformStreamingPayload` est un utilitaire autonome, `transformSseLine` est le chemin réel. Il faut donc éviter de confondre les deux lors d'une transposition.

### 6.2 Les deux trous réels du proxy Go

Le brief impose de réorienter l'analyse vers ces deux trous, et l'audit les confirme tous les deux.

**Trou 1 — `nextStreamDelta` accepte toute extension de préfixe** (`gemini/chat.go:311-321`) :

```text
if snapshot == "" || snapshot == sent      → ("", sent)
if strings.HasPrefix(snapshot, sent)       → (snapshot[len(sent):], snapshot)
sinon                                      → ("", sent)
```

Le cas `"A"` puis `"A\nA"` **passe** le test de préfixe : `"A\nA"` commence bien par `"A"`. Le delta émis est `"\nA"` et le paragraphe est dupliqué. Ce n'est pas un cas d'école : Gemini émet, en fin de flux, des ré-affichages complets du texte déjà produit, et un ré-affichage peut légitimement commencer par le texte déjà envoyé puis en répéter une partie. Le test de préfixe est nécessaire mais **non suffisant**.

**Trou 2 — le compteur de pensées est monotone** (`api/chat_handler.go:787-812` et `:1043-1067`) :

```text
thoughtRunes   := []rune(allThoughts)
if len(thoughtRunes) > emittedThoughtRunes {
    deltaThought      := string(thoughtRunes[emittedThoughtRunes:])
    emittedThoughtRunes = len(thoughtRunes)
}
```

`emittedThoughtRunes` n'est jamais décrémenté. Si une pensée **réouverte** est plus courte que le high-water mark — cas typique d'un second bloc de pensée après un appel d'outil, ou d'un modèle qui redémarre son raisonnement — alors `len(thoughtRunes) > emittedThoughtRunes` est faux, et **la totalité du nouveau raisonnement est perdue**. Le compteur global est réinitialisé à chaque tentative (`:734-735`), mais pas à l'intérieur d'une même réponse. La perte est silencieuse et totale.

Un troisième défaut, adjacent, mérite d'être signalé parce qu'il partage la même racine : le compteur `emittedContentRunes` (`:721`, `:823-825`) présente exactement le même biais sur le contenu visible. Il n'est pas listé dans le brief, mais toute correction du dédoublonnage de pensée qui ignorerait le contenu laisserait le défaut en place.

### 6.3 Réponses aux questions d'audit

**Question 1 — cumulatif vs delta.** Gemini `StreamGenerate` émet des **instantanés croissants** : chaque trame porte le texte complet produit jusqu'ici, pas un fragment. Un tampon glissant qui conserve le dernier instantané émis et ne transmet que le suffixe est donc suffisant **pour autant que les instantanés soient effectivement croissants**. Le mode delta, lui, n'a besoin d'aucun tampon : chaque trame est autonome. Le proxy Go reçoit du cumulatif (`gemini/chat.go:327-360` : `onChunk(delta)` est appelé depuis un instantané) et **réémet du delta** vers le client OpenAI. C'est cette conversion qui doit être rendue sûre.

La règle correcte n'est pas « le nouvel instantané commence par l'ancien », mais « le nouvel instantané **moins** l'ancien est un suffixe plausible, et le recouvrement avec le texte déjà émis est borné ». Dire cela proprement exige de distinguer trois cas :

| Cas | `snapshot` vs `sent` | Action correcte |
|---|---|---|
| extension stricte | `snapshot = sent + s`, et `s` n'est pas une répétition de `sent` | émettre `s` |
| ré-affichage | `snapshot = sent + suffixe` où le suffixe rejoue une partie de `sent` | émettre le suffixe **sans** la partie rejouée |
| divergence | `snapshot` ne commence pas par `sent` | émettre l'intégralité, sauf si c'est un doublon déjà vu |

Le cas « ré-affichage » est précisément celui que le code actuel traite comme une extension.

**Question 2 — que protège DJB2 ?** Le hachage (`:13-19`) protège contre la **ré-émission d'un bloc de pensée identique** dans une session longue, où les trames répètent parfois le même texte intégral. Ses modes de défaillance sont au nombre de quatre, et le brief en cite trois :

- **Espace de 32 bits** : `>>> 0` borne le domaine à 2³² valeurs. Avec quelques milliers de blocs, la probabilité de collision devient non négligeable (paradoxe des anniversaires : ~50 % vers 77 000 entrées). Une collision **supprime un bloc de pensée distinct** (`:95-98` renvoie `null`), c'est-à-dire exactement le contraire du but recherché.
- **Set non borné** : `sessionDisplayedThinkingHashes` est déclaré au niveau module (`src/plugin/request.ts:79`) et n'est jamais purgé. Il croît pendant toute la vie du processus.
- **Non purgé par session** : la portée est le **processus**, pas la session. Deux conversations distinctes partagent le même ensemble, donc un même texte de pensée répété dans deux sessions voit sa seconde occurrence supprimée.
- **Hex non padé** (quatrième défaut, non cité par le brief) : `toString(16)` produit `"1f"` et `"abc"`, mais aussi `"7"` pour une valeur basse. Deux hachages différents ne peuvent pas produire la même chaîne, donc ce n'est pas un bug de correction — seulement une inélégance qui rend le format instable entre valeurs.

**Question 3 — comment exprimer un dédoublonnage correct sans état global non borné ?** La réponse tient en une phrase : **lier l'état à la réponse en cours, et le borner par construction**. Le dédoublonnage n'a de sens qu'à l'échelle d'une réponse SSE ; il n'y a aucune raison de mémoriser des hachages au-delà. Un état local à la requête, indexé par numéro de bloc et contenant seulement le dernier texte émis, est à la fois suffisant et borné par le nombre de blocs simultanés.

### 6.4 Pseudocode du dédoublonneur borné

```text
STRUCTURE ÉtatFlux
  dernierÉmis      : table index → chaîne        // un seul instantané par bloc
  empreintesÉmises  : anneau borné de K empreintes  // K = 64 par défaut
  runesPenséeÉmises : entier                     // high-water mark par bloc, pas global

FONCTION DeltaDePensée(index, instantané, état) -> (delta, nouvelÉtat)
  PRÉCONDITION   instantané est l'instantané cumulatif du bloc `index`
  POSTCONDITION  Invariant F1 : aucun suffixe n'est émis deux fois pour un même bloc
                 Invariant F2 : si l'instantané diverge, l'intégralité est émise une fois,
                     sauf si son empreinte figure déjà dans l'anneau
                 Invariant F3 : la mémoire de l'état est Θ(K) + Θ(nombre de blocs actifs),
                     indépendante de la longueur de la session
                 Invariant F4 : aucune suppression ne repose sur un hachage seul ;
                     l'empreinte est toujours confirmée par comparaison de chaînes
  SI instantané = "" ALORS RENDRE ("", état)

  empreinte ← Empreinte(instantané)                       // 64 bits minimum
  SI empreinte ∈ état.empreintesÉmises ALORS
      // F4 : on ne fait pas confiance au seul hachage, on confirme
      SI état.dernierÉmis[index] = instantané ALORS RENDRE ("", état)
  FIN SI

  émis ← état.dernierÉmis[index] OU ""
  SI instantané commence par émis ALORS
      suffixe ← instantané[longueur(émis):]
      SI suffixe est vide ALORS RENDRE ("", état)
      // Cas ré-affichage : si le suffixe rejoue le début de `émis`, on le retire
      rejeu ← plusLongPréfixeDe(suffixe, émis)
      SI longueur(rejeu) > 0 ALORS suffixe ← suffixe[longueur(rejeu):]
      RENDRE (suffixe, Enregistrer(état, index, instantané, empreinte))
  SINON
      // Divergence : émission intégrale, mais une seule fois par empreinte
      RENDRE (instantané, Enregistrer(état, index, instantané, empreinte))
  FIN SI
FIN FONCTION

FONCTION Enregistrer(état, index, instantané, empreinte) -> état'
  état'.dernierÉmis[index] ← instantané
  SI empreinte ∉ état'.empreintesÉmises ALORS
      InsérerDansAnneau(état'.empreintesÉmises, empreinte)   // évince le plus ancien si plein
  FIN SI
  RENDRE état'
FIN FONCTION
```

**Preuve de F1.** Pour un bloc donné, chaque émission met `dernierÉmis[index]` à l'instantané complet. Toute émission ultérieure est donc calculée contre ce nouveau point de départ, et le suffixe émis est strictement postérieur. Dans la branche de divergence, l'instantané entier est émis, puis devient le nouveau point de départ : là encore, aucun suffixe déjà émis n'est réémis. La seule réémission possible serait un instantané **antérieur** au point de départ ; elle est bloquée par la vérification d'empreinte (F2/F4).

**Preuve de F3.** `dernierÉmis` a au plus une entrée par bloc actif (borné par le nombre de candidats, en pratique 1) et `empreintesÉmises` est un anneau de taille fixe `K`. Aucune structure ne grandit avec la durée de la session : la différence avec `sessionDisplayedThinkingHashes` (`request.ts:79`) est structurelle, pas graduelle.

**Preuve de F4.** La seule décision prise sur une empreinte est de **sauter** une émission ; elle est toujours précédée de l'égalité stricte `état.dernierÉmis[index] = instantané`. Une collision de hachage sur deux textes différents ne peut donc pas supprimer un texte différent. C'est la correction exacte du défaut D1.

**Terminaison.** La fonction est un enchaînement de tests et d'affectations, sans boucle, sauf `plusLongPréfixeDe` qui est un calcul de plus long préfixe commun — linéaire en la longueur des deux chaînes, à borner explicitement à la longueur de `émis` pour éviter une comparaison quadratique sur des textes longs. Complexité temps `Θ(|suffixe| · |émis|)` dans le pire cas si l'on implémente naïvement le recouvrement ; une implémentation par `strings.HasPrefix` répété reste acceptable car `|suffixe|` est typiquement court, mais un plafond explicite (par exemple 256 runes) rend le coût linéaire et borné.

**Complexité mémoire** `Θ(K + nombre de blocs)`, soit quelques kilo-octets, contre une croissance non bornée côté amont.

### 6.5 Confrontation à la table de verdicts

**Verdict : transposable, avec une réserve de périmètre.**

- Verdict n°15 (**ALREADY PRESENT**) : garde de flux confirmée — `stallGuardReader` complet (`gemini/stream_guard.go:19-100`), budget 45 s (`gemini/client.go:27-30`), erreur typée `ErrUpstreamStreamStalled` (`stream_guard.go:77`). **Aucune recommandation** sur ce point.
- Verdict n°16 (**PARTIAL**) : confirmé. `nextStreamDelta` ne projette que par préfixe (`gemini/chat.go:306-321`). La correction proposée est légitime, et elle doit inclure le compteur de contenu (`chat_handler.go:721`, `:823-825`) en plus du compteur de pensée, sans quoi le défaut survit sur la moitié du flux.
- Verdict n°17 (**ALREADY PRESENT**) : émission SSE confirmée — heartbeat 15 s avec seuil de 14 s (`api/chat_handler.go:690-708`), deltas de contenu (`:787-845`), `tool_calls` (`:1090-1119`), `finish_reason` (`:1143-1156`). Aucune recommandation sur l'ossature SSE ; seule la *projection* des deltas change.
- Verdict n°21 (**ALREADY PRESENT**) : deltas par rune (`:787-812`) et flush terminal (`:1043-1067`). Le mécanisme de flush existe ; il est correct dans sa forme, mais il hérite du compteur monotone. La recommandation porte donc sur la **valeur** du compteur, pas sur le flush.

---

## 7. Axe 6 — Grounding découplé & passerelle Antigravity

### 7.1 Pourquoi l'API native refuse de combiner recherche et fonctions

La contrainte est explicite dans le contrat amont (`docs/ANTIGRAVITY_API_SPEC.md:227-229`) et documentée en commentaire dans le code (`src/plugin/search.ts:1-7`) : `{googleSearch:{}}` et `{functionDeclarations:[…]}` **ne peuvent pas** cohabiter dans la même requête. Le code amont applique cette contrainte de deux manières cohérentes.

`wrapToolsAsFunctionDeclarations` (`src/plugin/transform/gemini.ts:476-579`) sépare les outils de passage (recherche, exécution de code) des déclarations de fonctions (`:490-514`), puis n'ajoute `{googleSearch:{}}` que **si** le nombre de déclarations est nul (`:563-571`) — et journalise un avertissement dans le cas contraire (`:565-570`). Le resolveur (`src/v2.ts:602-640`) et le plugin (`src/plugin.ts:1354-1401`) exposent alors un **outil virtuel** `google_search` qui intercepte l'intention de recherche et la traduit en une requête dédiée.

Le patron d'interception est donc : outil virtuel déclaré côté hôte → détection de l'appel → **requête séparée** avec `tools: [{googleSearch:{}}]` (+ `{urlContext:{}}` si des URLs sont fournies, `src/plugin/search.ts:239-243`) → parsing de `groundingMetadata` → réinjection d'un résultat Markdown formaté dans la conversation. La raison de fond est que ces deux familles d'outils sont exécutées par des **moteurs différents** côté fournisseur : le grounding est une capacité du backend de recherche, les fonctions sont une capacité du modèle. Les combiner imposerait un routage que le contrat n'expose pas.

### 7.2 Anatomie de `executeSearch`

`executeSearch` (`src/plugin/search.ts:223-313`) est un client HTTP complet, autonome, en quatre étapes.

**Requête.** Charge utile minimale (`:245-260`) : `systemInstruction.parts[0].text` (constante `SEARCH_SYSTEM_INSTRUCTION`, `src/constants.ts:262-274`), un unique `contents[{role:"user"}]`, `tools` avec les seuls outils de grounding, et `generationConfig {temperature: 0, topP: 1}` (`:256-259`) — températures déterministes, ce qui est le bon choix pour une recherche factuelle. Enveloppe externe (`:263-272`) : `project`, `model` (= `SEARCH_MODEL`, `gemini-2.5-flash`, `src/constants.ts:242`), `userAgent`, `requestId`, `request.sessionId`. Endpoint **non-streaming** `/v1internal:generateContent` (`:275`), timeout 60 s (`src/constants.ts:257`, appliqué `:292`).

**En-têtes.** `getAntigravityHeaders()` + `Authorization: Bearer <access_token>` + `Content-Type: application/json` (`:285-290`). Les en-têtes canoniques du contrat comprennent un `User-Agent` très spécifique : `antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; cl=1000335682)` (`docs/ANTIGRAVITY_API_SPEC.md:64-70`).

**Parsing.** `parseSearchResponse` (`:147-211`) extrait trois flux indépendants : `groundingMetadata.webSearchQueries` (`:182-184`), `groundingChunks[].web.{uri,title}` avec **les deux champs requis** (`:186-195`), et `urlContextMetadata.url_metadata[]` avec `retrieved_url` et `url_retrieval_status` (`:199-208`). Le texte de réponse est la concaténation des `parts[].text` (`:171-176`). Le helper `formatSearchResult` (`:113-145`) produit un gabarit Markdown exact à quatre sections : `## Search Results`, `### Sources` (liens `- [titre](url)`), `### URLs Retrieved` (avec `✓` si `URL_RETRIEVAL_STATUS_SUCCESS`, `✗` sinon, `:131-132`), `### Search Queries Used` (guillemets autour de chaque requête).

**Dégradation.** Toute erreur — HTTP non-2xx (`:295-299`), exception réseau (`:308-311`) — retourne une chaîne Markdown `## Search Error\n\n…` plutôt que de lever. C'est un choix défendable pour un outil : le modèle reçoit une explication lisible et peut reformuler.

### 7.3 Le défaut D4, confirmé intégralement

Le paramètre `thinking` est destructuré avec une valeur par défaut (`src/plugin/search.ts:229` : `const { query, urls, thinking = true } = args;`) puis **jamais utilisé**. Vérification par recherche exhaustive : aucune autre occurrence de `thinking` dans le corps de la fonction, et la charge utile (`:245-272`) ne contient aucun `thinkingConfig`. Les constantes `SEARCH_THINKING_BUDGET_DEEP = 16384` et `SEARCH_THINKING_BUDGET_FAST = 4096` (`src/constants.ts:247`, `:252`) n'ont **aucun appelant** dans `src/`. Le paramètre est exposé dans deux schémas d'outil (`src/v2.ts:611`, `src/plugin.ts:1360`) et transmis deux fois (`src/plugin.ts:1394`), mais il ne change rien au comportement.

C'est exactement le type de faux paramétrage qui doit être **écarté** et non transposé : un paramètre documenté, exposé, transmis, et sans effet. La transposition doit soit l'implémenter réellement (`generationConfig.thinkingConfig`), soit ne pas l'exposer.

### 7.4 Réponses aux questions d'audit

**Question 2 — comparaison des deux voies.**

| Critère | Scraping web actuel (proxy Go) | Passerelle Antigravity |
|---|---|---|
| Endpoint | `https://gemini.google.com/u/N/_/BardChatUi/data/…/StreamGenerate` (`gemini/chat.go:225`, `:599`) et `batchexecute` (`:515`) | `https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent` (`ANTIGRAVITY_API_SPEC.md:26`, `:37`) |
| Authentification | cookies de session + jetons extraits du HTML : `SNlM0e`, `FdrFJe`, `cfb2h` (`gemini/session.go:241-243`) | OAuth 2.0 Bearer + `project` dans l'enveloppe (`:45-81`, `:107-123`) |
| Empreinte TLS | simulation Chrome 152 (`gemini/client.go:143`), QUIC/HTTP3 et HTTP/2 | client HTTP standard, en-tête `User-Agent` dédié |
| Fragilité structurelle | **élevée** : le scraping dépend du DOM et du HTML de session, d'où l'erreur explicite `SNlM0e not found - Google might have changed UI or blocked the request` (`gemini/session.go:285`) | **faible** : contrat HTTP/JSON versionné |
| Grounding | inexistant (zéro occurrence de `grounding` dans le dépôt) | natif via `groundingMetadata` |
| Latence | non mesurée dans le dépôt (aucun banc d'essai) — `non vérifié` | non mesurée — `non vérifié` |
| Coût de refactor | nul (existant) | élevé : nouvelle pile d'authentification, nouvelle abstraction de transport, nouveau schéma de requête |
| Risque opérationnel | déjà assumé et instrumenté | nouveau point de défaillance, dépendant d'un quota séparé |

**La conclusion honnête est que la latence ne peut pas être comparée** : aucun des deux dépôts ne fournit de mesure. Toute affirmation chiffrée serait inventée. Le seul critère tranchable factuellement est la fragilité structurelle, et elle penche nettement en faveur de la passerelle.

**Question 3a — OAuth + `project` et cookies `SNlM0e` sont-ils substituables ?** **Non.** Ce sont deux modèles d'authentification incompatibles, et le brief a raison de le souligner comme un prérequis souvent omis. Le scraping Go s'authentifie par **cookies de navigateur** et extrait un jeton anti-CSRF du HTML de la page (`gemini/session.go:241-243`, `:285`). La passerelle exige un **jeton OAuth** avec cinq scopes (`ANTIGRAVITY_API_SPEC.md:54-62`) et un identifiant de projet Google Cloud dans l'enveloppe (`:107-123`). Il n'existe aucun chemin de conversion : un cookie `SNlM0e` ne s'échange pas contre un access token OAuth. Adopter la passerelle signifie donc **ajouter** une seconde voie d'authentification complète, pas remplacer la première. C'est ce qui fait basculer le coût de refactor de « moyen » à « élevé ».

**Question 3b — quel refactor de jointure pour l'abstraction `Transport` ?** Le brief relève l'absence totale d'abstraction : zéro déclaration `interface {` dans tout le dépôt. Les types concrets sont `tls_client.HttpClient` et `*stdhttp.Client` (`gemini/client.go:33-34`), et les quatre chemins (`QUIC`/`HTTP2` × `stream`/`non-stream`) sont quatre fonctions quasi dupliquées (`gemini/transport.go:68-108`, `:110-150`, `:152-197`, `:199-246`), chacune répétant les mêmes en-têtes (`:81-93`, `:126-135`, `:165-177`, `:215-224`).

La jointure **minimale** est une interface à trois méthodes, qui recouvre exactement les quatre points d'appel existants :

```text
TYPE Transport INTERFACE
    RequêteStream(chemin, formulaire, modèle) -> (flux, erreur)
    RequêteSimple(chemin, formulaire, modèle) -> (corps, erreur)
    Nom() -> chaîne                       // pour l'instrumentation et les logs

// Implémentations
TYPE TransportQUIC  SATISFAIT Transport   // stdhttp.Client + http3.Transport
TYPE TransportHTTP2 SATISFAIT Transport   // tls_client.HttpClient + fhttp
TYPE TransportCloud SATISFAIT Transport   // futur : Antigravity, OAuth + JSON
```

**Pourquoi trois méthodes et pas une.** `RequêteStream` et `RequêteSimple` diffèrent par le contrat de retour (flux consommable ligne à ligne vs corps complet) et par le budget de garde associé (`newStallGuardReader` est appliqué dans les deux cas mais avec des consommateurs différents : `readAndProcessStream` vs `io.ReadAll`, `transport.go:107`, `:149`, `:192`, `:241`). Les fusionner obligerait à bufferiser un flux, ce qui annulerait le bénéfice du streaming. `Nom()` n'est pas cosmétique : les logs distinguent déjà `[Request]`, `[HTTP2-Request]`, `[sendRequestQUIC]`, `[sendRequestHTTP2]` (`:87`, `:132`, `:171`, `:221`), et l'instrumentation en dépend.

La duplication des en-têtes (`:81-93` et `:126-135` sont identiques au mode de transport près) doit être extraite dans une fonction partagée `AppliquerEnTêtes(req, modèle, cookies)` : c'est là que vit le vrai gain de maintenance, plus que dans l'interface elle-même.

**Question 3c — mapping des IDs opaques.** C'est l'errata E4, et la table de correspondance est la suivante.

| Couche | Identifiant | Ancre |
|---|---|---|
| Amont Antigravity (inventaire annoncé) | `gemini-4-argon-high` | `docs/ANTIGRAVITY_API_SPEC.md:93`, `src/plugin/transform/model-resolver.ts:70` |
| Registre Go (canonique) | `gemini-4-argon-thinking-high` | `gemini/registry.go:392-404` |
| Registre Go (alias) | `argon-high`, `argon-thinking-high`, `thinking-high` | `gemini/registry.go:400` |

Les deux sont légitimes et désignent la **même capacité** à des couches différentes : `gemini-4-argon-high` est un identifiant de l'inventaire backend (préservé tel quel par le résolveur amont, qui l'inscrit dans `advertisedModelIds` précisément pour empêcher le retrait du suffixe de palier, `model-resolver.ts:67-76`), tandis que `gemini-4-argon-thinking-high` est un identifiant **local** du proxy, qui encode le palier dans un champ `ThinkingLevel` séparé (`registry.go:398`) plutôt que dans la chaîne.

Trois conséquences d'implémentation :

1. **Les recommandations d'implémentation doivent utiliser les IDs Go.** Un patch qui écrirait `gemini-4-argon-high` dans `registry.go` créerait un modèle en doublon, sans `ThinkingLevel`, et retomberait sur le gabarit par défaut.
2. **La table de correspondance doit être explicite dans le code**, pas implicite. Si un connecteur Antigravity est un jour ajouté (question 3a-b), il lui faudra une fonction de traduction bidirectionnelle, car l'inventaire distant renverra `-high` là où le registre local attend `-thinking-high`.
3. **Le suffixe `-high` amont n'est pas un palier retirable.** Le résolveur amont le documente (`model-resolver.ts:62-76`) : ce suffixe fait partie de l'ID réel. Une transposition qui appliquerait la regex `-(minimal|low|medium|high)$` (`:83`) à ces identifiants produirait `gemini-4-argon`, c'est-à-dire **un autre modèle**.

### 7.5 Pseudocode du handler virtuel `google_search`

```text
FONCTION TraiterAppelGoogleSearch(arguments, contexte) -> résultatTexte
  PRÉCONDITION   arguments.query est une chaîne non vide (validée avant appel)
  POSTCONDITION  Invariant G1 : aucune requête contenant des functionDeclarations
                     n'est envoyée avec un outil de grounding
                 Invariant G2 : la sortie est toujours du texte Markdown, jamais une
                     erreur levée — une erreur devient une section ## Search Error
                 Invariant G3 : les sources ne sont listées que si titre ET uri existent
                 Invariant G4 : le budget total est borné par un timeout explicite
                 Invariant G5 : `thinking` a un effet réel, ou n'est pas exposé
  query ← Trim(arguments.query)
  SI query = "" ALORS RENDRE "## Search Error\n\nA non-empty query is required."

  invite ← query
  SI arguments.urls non vide ALORS
      invite ← query + "\n\nURLs to analyze:\n" + joindre(arguments.urls, "\n")
  FIN SI

  outils ← [{googleSearch:{}}]                                  // G1 : jamais de fonctions
  SI arguments.urls non vide ALORS ajouter(outils, {urlContext:{}})
  SI arguments.thinking = vrai ALORS
      generationConfig.thinkingConfig ← {thinkingBudget: BUDGET_DEEP, includeThoughts: faux}
  SINON
      generationConfig.thinkingConfig ← {thinkingBudget: BUDGET_FAST, includeThoughts: faux}
  FIN SI                                                          // G5

  charge ← {
      project: contexte.projet, model: contexte.modèleRecherche,
      requestId: générerIdentifiant(), userAgent: "antigravity",
      request: { systemInstruction:{parts:[{text: INSTRUCTION_RECHERCHE}]},
                 contents:[{role:"user", parts:[{text: invite}]}],
                 tools: outils,
                 generationConfig: {temperature: 0, topP: 1, thinkingConfig: …} },
      sessionId: générerSession()
  }

  RÉPONSE ← POST(contexte.baseURL + "/v1internal:generateContent",
                 enTêtes = canoniques + Bearer(contexte.jeton),
                 corps = charge,
                 délai = DÉLAI_RECHERCHE)                         // G4
  SI RÉPONSE.statut ≠ 2xx ALORS
      RENDRE "## Search Error\n\nFailed to execute search: " + statut + "\n\n" + corps
  FIN SI                                                          // G2

  candidat ← RÉPONSE.corps.response.candidates[0]
  SI candidat absent ALORS RENDRE "## Search Error\n\nNo candidate returned."

  texte ← concaténer(candidat.content.parts[].text)
  sources ← []
  POUR CHAQUE chunk DANS candidat.groundingMetadata.groundingChunks FAIRE
      SI chunk.web.uri non vide ET chunk.web.title non vide ALORS
          ajouter(sources, {titre: chunk.web.title, url: chunk.web.uri})
      FIN SI                                                      // G3
  FIN POUR
  requêtes ← candidat.groundingMetadata.webSearchQueries OU []
  urlsRécupérées ← candidat.urlContextMetadata.url_metadata OU []

  sortie ← "## Search Results\n\n" + texte + "\n\n"
  SI sources non vide ALORS
      sortie ← sortie + "### Sources\n" + pourChaque(s → "- [" + s.titre + "](" + s.url + ")\n") + "\n"
  FIN SI
  SI urlsRécupérées non vide ALORS
      sortie ← sortie + "### URLs Retrieved\n"
      POUR CHAQUE u DANS urlsRécupérées FAIRE
          marque ← si u.statut = "URL_RETRIEVAL_STATUS_SUCCESS" ALORS "✓" SINON "✗"
          sortie ← sortie + "- " + marque + " " + u.url + "\n"
      FIN POUR
      sortie ← sortie + "\n"
  FIN SI
  SI requêtes non vide ALORS
      sortie ← sortie + "### Search Queries Used\n" + pourChaque(q → "- \"" + q + "\"\n")
  FIN SI
  RENDRE sortie
FIN FONCTION
```

**Preuve de G1.** `outils` est construit par deux littéraux, aucun ne contenant `functionDeclarations`. La fonction ne reçoit pas la liste des outils de la requête hôte : il est donc **impossible par construction** qu'elle les combine.

**Preuve de G2.** Les deux sorties d'erreur sont des littéraux `## Search Error`, et tout échec de transport est converti avant retour. La fonction est donc totale : elle rend une chaîne dans tous les cas. C'est le comportement amont (`search.ts:295-311`), et il est bon.

**Preuve de G3.** La condition est explicitement conjonctive sur `uri` **et** `title`, conformément à `search.ts:188`. Une source sans titre est écartée plutôt que rendue comme un lien vide.

**Terminaison.** Deux parcours de listes finies (`urlsRécupérées`, `requêtes`) et un parcours de `groundingChunks`. Aucune récursion. Terminaison immédiate.

**Complexité.** Temps `Θ(|urls| + |chunks| + |requêtes| + |texte|)`, dominé par la réponse réseau. Mémoire `Θ(taille de la réponse)`, libérée après formatage — aucune conservation d'état entre appels, contrairement au `Set` du défaut D1.

### 7.6 Tableau comparatif d'opportunité

| Dimension | Scraping web actuel | Connecteur Antigravity Gateway |
|---|---|---|
| Latence | non mesurée — `non vérifié` | non mesurée — `non vérifié` |
| Fiabilité | faible : dépend du HTML de session (`session.go:285`) | forte : contrat JSON versionné (`ANTIGRAVITY_API_SPEC.md:22-41`) |
| Coût de refactor | nul | élevé : OAuth + projet + abstraction `Transport` + mapping d'IDs |
| Risque opérationnel | déjà assumé, instrumenté, mais cassable à chaque changement d'UI | nouveau : quota distinct, dépendance à un projet Cloud |
| Prérequis | cookies de navigateur valides | 5 scopes OAuth, `project`, `loadCodeAssist`/`onboardUser` |
| Grounding | absent | natif (`groundingMetadata`) |
| Verdict | conserver comme voie par défaut | **non prioritaire** : gain de fiabilité réel mais coût et risque disproportionnés à court terme |

### 7.7 Confrontation à la table de verdicts

**Verdict : transposable en différé ; deux verdicts absents confirmés.**

- Verdict n°18 (**ABSENT**) : confirmé par recherche exhaustive — zéro occurrence de `googleSearch`, `grounding`, `Grounding` dans le dépôt Go ; `gemini/models.go:15-24` ne porte aucun champ de citation ; `gemini/chat.go:576` (`msgInner := []interface{}{prompt, 0, nil, imagesPayload, nil, nil, 0}`) n'a aucun emplacement pour un outil de grounding. La recommandation est donc légitime, y compris dans sa partie « handler virtuel » qui ne dépend pas de la passerelle.
- Verdict n°19 (**ALREADY PRESENT**) : le transport amont existe et est correctement ancré — URLs (`transport.go:41-44`), trois points d'appel (`chat.go:225`, `:515`, `:599`), jetons de session (`session.go:241-243`), en-têtes JSPB (`transport.go:81-93`), empreinte Chrome 152 (`client.go:143`). **Aucune recommandation de remplacement** ; seule l'abstraction (verdict 20) est proposée.
- Verdict n°20 (**ABSENT**) : confirmé — zéro déclaration `interface {` dans tout le dépôt, types concrets aux champs `client`/`quicClient` (`client.go:33-34`). La jointure à trois méthodes est la plus petite qui recouvre les quatre chemins existants.

---

## 8. Défauts du source à ne pas transposer

Chaque défaut est écarté explicitement, avec la contre-mesure retenue dans la proposition. Aucun n'est corrigé dans le dépôt source (lecture seule).

| # | Défaut | Ancre | Contre-mesure dans cet audit |
|---|---|---|---|
| **D1** | `sessionDisplayedThinkingHashes` : `Set` module-level non borné, jamais purgé, clé = DJB2 32 bits | `src/plugin/request.ts:79`, `transformer.ts:13-19` | §6.4 : état local à la requête, anneau borné `K=64`, empreinte **confirmée** par comparaison de chaînes (F4). Aucun état global. |
| **D2** | Deux assainisseurs disjoints : `cleanJSONSchemaForAntigravity` (minuscules, sans `items`) vs `toGeminiSchema` (MAJUSCULES, avec `items`) | `request-helpers.ts:659`, `transform/gemini.ts:52` | §2.4 : une seule fonction `CleanSchemaForGemini4`, types en minuscules (I6), `items` synthétisé après nettoyage (I2). La composition est rendue impossible par construction. |
| **D3** | `fixToolResponseGrouping` mute en place les orphelins collectés | `request-helpers.ts:2168-2171` | §4.5 : `CopierSurface` obligatoire (R3), reconstruction d'une nouvelle liste plutôt qu'insertion dans un tableau itéré. |
| **D4** | Paramètre `thinking` de `executeSearch` mort ; `SEARCH_THINKING_BUDGET_*` sans appelant | `search.ts:229`, `constants.ts:247`, `:252` | §7.5 : invariant G5 — soit `thinkingConfig` est réellement placé dans `generationConfig`, soit le paramètre n'est pas exposé. Pas de troisième voie. |
| **D5** | Incohérence de casse `object{…}` vs `ARRAY_OF_*` | `request-helpers.ts:2482-2500` | §3.5 : casse normalisée dans `HintDeType` (`ARRAY_OF_OBJECTS`, `object{}` → forme unique retenue). Le pseudocode impose une seule convention, documentée en invariant S2. |
| **D6** | `hasPossibleCompactedThinking` n'accepte que `role === "model"` là où l'analyse accepte `"model" \|\| "assistant"` | `thinking-recovery.ts:381-395` vs `:153` | Non transposé : §5.5 ne reprend **aucune** des trois heuristiques de `parts[]`, car le transcript Go est en prose. Le biais de rôle disparaît avec le mécanisme. |
| **D7** | `displayedThinkingHashes` fourni uniquement pour Gemini 3 | `request.ts:1690` | §6.4 : aucun hachage n'est utilisé ; la déduplication est structurelle (préfixe + empreinte confirmée), donc identique pour toutes les familles. |
| **D8** | `detectToolIdMismatches` et `hasPossibleCompactedThinking` : exports morts | `request-helpers.ts:2224`, `thinking-recovery.ts:381` | Aucune recommandation ne cite ces fonctions comme mécanismes actifs. Vérifié : `detectToolIdMismatches` n'a **aucun** appelant hors définition ; `hasPossibleCompactedThinking` n'a que le fichier de test (`thinking-recovery.test.ts:5`, `:286-319`). |

**Défaut supplémentaire, non listé dans le brief §5 mais découvert pendant l'audit :**

| # | Défaut | Ancre | Contre-mesure |
|---|---|---|---|
| **D9** | `transformStreamingPayload` perd les clés d'enveloppe en ré-émettant `data: {"response":…}` et **coexiste** avec `transformSseLine`, qui est le chemin réellement utilisé | `transformer.ts:30-56` vs `:175-224` | §6.1 : ne pas confondre les deux lors d'une transposition ; seul `transformSseLine` fait partie du chemin de production (`:317-325`). |

### 8.1 Traitement de l'errata E1 à E7

| # | Prémisse initiale | Réalité vérifiée dans cette session | Traitement |
|---|---|---|---|
| **E1** | `cleanJSONSchemaForAntigravity` lignes 20-680 | Confirmé : l'export est en `:659-686`, les passes internes sont éparpillées de `:43` à `:651` | §2.1 cite la fonction par son export et chaque passe par sa plage propre, jamais par un bloc unique. |
| **E2** | Fermeture propre des balises `<thought>` en SSE | **Confirmé absent** : zéro occurrence de `closeThought`, `antml`, `unterminated`, `premature` dans `src/`. `flush` injecte seulement un `usageMetadata` synthétique (`transformer.ts:329-361`) | §6.1 déclare l'absence explicitement et réoriente l'analyse vers les deux trous Go. **Aucun mécanisme de fermeture de balise n'est décrit.** |
| **E3** | `docs/MODEL-VARIANTS.md` couvre les paliers Gemini 4 | **Confirmé faux** : aucune occurrence de `gemini-4`, `argon`, `neon`, `hydrogen` dans le document ; seuls Claude, Gemini 3 et Gemini 2.5 y figurent | §5.3 déclare l'absence et reconstruit la matrice **exclusivement** depuis `gemini/registry.go`, avec ancrage ligne par ligne. |
| **E4** | `gemini-4-argon-high` = nom du modèle dans le registre Go | **Confirmé** : c'est l'ID amont (`ANTIGRAVITY_API_SPEC.md:93`, `model-resolver.ts:70`) ; le registre Go le nomme `gemini-4-argon-thinking-high` (alias `argon-high`, `registry.go:392-404`) | §7.4 question 3c : table de correspondance explicite et consigne d'utiliser les IDs Go. |
| **E5** | Le nettoyeur injecte `{type:"STRING"}` pour les `items` manquants | **Confirmé faux** : l'injection est dans `transform/gemini.ts:117-121`, pas dans `cleanJSONSchemaForAntigravity` | §2.3 traite ce point et en tire le constat du défaut D2. |
| **E6** | Réconciliation sur `contents[]`/`functionCall` côté Go | **Confirmé faux** : aucune sérialisation structurée ; historique aplati en prose (`api/chat_handler.go:501-530`) | §4.1 et §4.5 reformulent entièrement au niveau du transcript OpenAI. |
| **E7** | `detectToolIdMismatches` et `hasPossibleCompactedThinking` sont actifs | **Confirmé faux** : exports morts (aucun appelant en production) | §8 D8 et §5.2 traitent ces fonctions comme non actives. |

**Vérification complémentaire de la correction E3, point par point.** Le brief affirme que `MODEL-VARIANTS.md` documente un mapping déprécié `≤8192 → low`, `≤16384 → medium`, `>16384 → high`. Le document contient bien ce mapping pour Gemini 3 (`:141-145`) et un tableau de paliers (`:55-58`) distinguant `minimal`, `low`, `medium`, `high` avec des disponibilités différentes selon Claude et Gemini 3. Il ne contient **rien** sur Gemini 4. L'affirmation du brief est donc exacte dans son esprit, mais il faut préciser qu'elle décrit **Gemini 3** et non Gemini 4 : le mapping déprécié n'a jamais eu vocation à s'appliquer à Gemini 4, puisqu'il est explicitement introduit sous la section Gemini 3 (`:141`).

### 8.2 Ce qu'il ne faut pas copier dans la forme, indépendamment du fond

Trois travers de forme méritent d'être évités même quand le fond est bon.

1. **La cascade « premier disponible »** (`request-helpers.ts:2158-2161`) est une heuristique qui produit des appariements faux et **efface la trace** de son intervention. En prose, où le modèle ne peut pas distinguer un vrai résultat d'un résultat substitué, elle est strictement nuisible. Supprimée, pas atténuée.
2. **Le déclencheur à trois heuristiques en ET** (`thinking-recovery.ts:330-372`) est trop permissif : il classe « compacté » tout tour sans texte avant un appel, y compris ceux d'un modèle qui ne pense pas. Non transposé (voir §5.2).
3. **Le compteur monotone de runes** (`api/chat_handler.go:790`, `:823`) est un défaut **du proxy Go**, pas de l'amont, mais il appartient à la même famille : un high-water mark global qui ignore les réouvertures. Il doit être corrigé dans le même mouvement que la projection de flux, sinon la moitié du problème subsiste.

---

## 9. Feuille de route d'implémentation priorisée

Les priorités suivent trois critères explicites, dans cet ordre : (1) le défaut casse-t-il un comportement observable du proxy ; (2) le correctif est-il local et testable sans changement d'architecture ; (3) le correctif est-il indépendant des autres. Un élément qui satisfait les trois est P0. Un élément qui satisfait (1) mais pas (2) ou (3) est P1. Un élément qui n'est ni cassant ni local est P2.

| ID | Action | Priorité | Effort | Impact attendu Gemini 4 | Réf. source |
|---|---|---|---|---|---|
| **A-01** | Filtrer `required` contre `properties` dans `compactParametersSchema` et `compactPropertySchema` ; supprimer `required` s'il devient vide | **P0** | Faible | Élimine les rejets upstream sur `required` fantôme ; le catalogue MCP cesse de porter des exigences impossibles | `gemini/arranger.go:173-176`, `:322-326` ; `request-helpers.ts:575-606` |
| **A-02** | Synthétiser `items: {type:"string"}` pour tout nœud `type:"array"` qui en est dépourvu, **après** le nettoyage | **P0** | Faible | Supprime la classe d'erreur `parameters.properties[X].items: missing field` ; débloque les outils d'édition à tableau d'objets | `gemini/arranger.go:314-315` ; `transform/gemini.ts:117-121` |
| **A-03** | Sérialiser `ToolCallID` dans la ligne `[Tool Result for …]` et replier proprement sur `Name` quand il est vide | **P0** | Faible | Supprime les lignes `[Tool Result for ]` et rend la bijection appel↔résultat exprimable | `api/chat_handler.go:522-523`, `api/types.go:28` |
| **A-04** | Réconcilier les orphelins après `ThrottleMutatingToolCalls` : synthétiser un `role:"tool"` marqué `[recovered]` pour chaque appel supprimé | **P0** | Moyen | Le modèle ne voit plus d'appel sans réponse ; empêche les reprises incohérentes après une mutation throttlée | `gemini/arranger.go:803-815`, `api/chat_handler.go:1003`, `:1358` |
| **A-05** | Remplacer le compteur monotone `emittedThoughtRunes` par un high-water mark **par bloc**, réinitialisé à chaque nouveau bloc ; idem pour `emittedContentRunes` | **P0** | Moyen | Récupère intégralement les pensées réouvertes, aujourd'hui perdues silencieusement | `api/chat_handler.go:715-716`, `:787-812`, `:823-825`, `:1043-1067` |
| **A-06** | Corriger `nextStreamDelta` : retirer le recouvrement du suffixe avant émission, et ne pas traiter un ré-affichage comme une extension | **P1** | Moyen | Supprime la duplication de paragraphes en fin de flux (`"A"` → `"A\nA"`) | `gemini/chat.go:306-321` |
| **A-07** | Borner l'état de dédoublonnage : anneau de 64 empreintes, local à la requête, confirmé par comparaison de chaînes | **P1** | Moyen | Empêche la suppression d'un bloc de pensée par collision ; mémoire indépendante de la durée de session | `api/chat_handler.go:715` ; cf. D1 `request.ts:79` |
| **A-08** | Injecter les signatures `STRICT PARAMETERS` dans les descriptions d'outils, calculées sur le schéma (jamais sur la description tronquée), avec garde d'idempotence | **P1** | Moyen | Réduit la probabilité d'appel non conforme **avant** validation, donc préserve le budget de 4 tentatives | `gemini/arranger.go:80-129`, `:165-196` ; `request-helpers.ts:2519-2559` |
| **A-09** | Fusionner la clause de durcissement (noms exacts, types d'`items`, priorité du schéma) dans la règle 4 existante plutôt qu'en nouveau bloc | **P1** | Faible | Durcit le contrat sans déplacer le préfixe du prompt ni invalider la continuité de session | `gemini/arranger.go:107` ; `constants.ts:188-199` |
| **A-10** | Ajouter l'escalade de palier de thinking sur échec répété de tour vide : `0 → 1 → 4 → 2`, avec point fixe au sommet | **P1** | Moyen | Transforme un échec définitif (deux tours vides identiques) en une reprise à capacité supérieure | `api/chat_handler.go:119-124`, `:888-900`, `:1257-1269` ; `gemini/registry.go:120-157` |
| **A-11** | Exposer le niveau de raisonnement par requête (`reasoning_effort`) et le traduire en sélection de modèle thinking, en ignorant `tool_choice` ou en le déclarant non supporté | **P1** | Moyen | Rend le palier pilotable sans changer de nom de modèle ; supprime un champ silencieusement ignoré | `api/types.go:67-74` |
| **A-12** | Introduire l'interface `Transport` à trois méthodes et extraire `AppliquerEnTêtes` partagé entre les quatre chemins | **P2** | Élevé | Aucun effet fonctionnel immédiat ; réduit de quatre à un le nombre de points à corriger pour toute évolution d'en-têtes | `gemini/transport.go:68-246`, `gemini/client.go:33-34` |
| **A-13** | Détecter la corruption de pensée **sans** champ de signature, en consommant `extractThoughtsAndContent` existant ; clôturer par deux lignes synthétiques et relancer | **P2** | Moyen | Récupère les tours bloqués dont la pensée est présente mais la sortie absente | `gemini/arranger.go:1528-1608` ; `thinking-recovery.ts:264-294` |
| **A-14** | Implémenter un outil virtuel `google_search` réutilisant la voie de scraping actuelle, sans passerelle Antigravity | **P2** | Élevé | Comble l'absence de grounding sans introduire OAuth ni nouveau point de défaillance | `gemini/chat.go:576`, `gemini/models.go:15-24` ; `search.ts:113-145` |
| **A-15** | Documenter et centraliser la table de correspondance des IDs amont ↔ registre Go (`-high` ↔ `-thinking-high`) | **P2** | Faible | Empêche la création de modèles doublons lors d'un futur branchement de la passerelle | `gemini/registry.go:392-432` ; `ANTIGRAVITY_API_SPEC.md:93` |
| **A-16** | Statuer sur le doublon de définition `gemini-4-hydrogen` (deux entrées, la seconde gagnante) : conserver une seule définition effective et documenter le choix | **P2** | Faible | Supprime un piège de lecture qui fait conclure à un comportement non-thinking alors que le modèle est thinking-low | `gemini/registry.go:175-186` vs `:312-326`, `:554-568` |

### 9.1 Justification des priorités

**Pourquoi A-01 à A-05 sont P0.** Chacun casse un comportement observable, et chacun est corrigeable localement sans toucher aux autres. A-01 et A-02 provoquent des rejets upstream documentés par le contrat (`ANTIGRAVITY_API_SPEC.md:342-346`). A-03 produit une ligne littéralement malformée (`[Tool Result for ]`). A-04 produit un appel sans réponse par construction, dans le chemin nominal de toute mutation. A-05 perd du raisonnement de façon silencieuse et totale — c'est le défaut le plus coûteux en qualité perçue, car rien ne le signale.

**Pourquoi A-06 et A-07 sont P1 et non P0.** Les deux concernent la duplication et la perte de pensée, donc des défauts réels, mais ils sont moins fréquents : A-06 ne se déclenche qu'en fin de flux avec ré-affichage, A-07 qu'en cas de collision ou de dépassement d'anneau. Ils sont aussi plus délicats à tester (il faut un flux synthétique multi-trames).

**Pourquoi A-08 et A-09 sont P1.** Ils sont préventifs, donc leur effet n'est pas démontrable par un test unitaire mais seulement par une mesure de taux d'échec en production. L'amont ne fournit aucune mesure (voir §3.2), il serait donc malhonnête de les promouvoir en P0.

**Pourquoi A-10 et A-11 sont P1.** A-10 corrige un comportement déterministe mais rare (deux tours vides consécutifs). A-11 touche le contrat d'API entrant, ce qui exige une décision produit sur la sémantique exacte de `reasoning_effort` (mapping vers quel palier pour quel modèle) : ce n'est pas un correctif, c'est une fonctionnalité.

**Pourquoi A-12 à A-16 sont P2.** Aucun ne casse un comportement actuel. A-12 est un refactor de structure à haut risque de régression (quatre chemins réseau). A-13 duplique partiellement A-05 et n'a de valeur qu'une fois A-05 en place. A-14 est un ajout de capacité lourd, dont l'alternative (passerelle Antigravity) est encore plus lourde et sans mesure de latence disponible. A-15 et A-16 sont de la dette documentaire et de cohérence.

### 9.2 Ordre d'exécution recommandé

```text
Vague 1 (P0, indépendants)     : A-01 → A-02 → A-03 → A-05 → A-04
Vague 2 (P1, après validation) : A-06 → A-07 → A-08 → A-09 → A-10 → A-11
Vague 3 (P2, opportuniste)     : A-16 → A-15 → A-12 → A-13 → A-14
```

A-05 précède A-04 malgré son rang : A-04 modifie la liste de messages, A-05 modifie les compteurs de flux ; les deux touchent `api/chat_handler.go` et doivent être livrés séparément pour que la régression soit attribuable. A-16 précède A-12 parce qu'une table d'IDs fausse rendrait toute validation de transport ambiguë.

### 9.3 Critères de non-régression par vague

| Vague | Vérification minimale | Ce qui doit rester inchangé |
|---|---|---|
| 1 | `go test -short ./...` plus un test ciblé par correctif | Les 11 règles de `FormatToolsInstruction`, le budget `maxToolChatAttempts = 4`, le heartbeat SSE |
| 2 | Les mêmes, plus un test de flux synthétique multi-trames | La garde de flux à 45 s, les quatre `finish_reason` |
| 3 | Campagne `go test -race -short ./...` | Toutes les URL upstream, l'empreinte Chrome 152, les en-têtes JSPB |

Aucune modification ne doit toucher les zones `ALREADY PRESENT` listées au §3 du brief sans démonstration de faiblesse : c'est la règle de non-duplication, et elle s'applique aussi aux vagues.

---

## 10. Sources des citations

### 10.1 Dépôt source `opencode-antigravity-auth` 2.1.2

| Fichier | Plages citées | Objet |
|---|---|---|
| `src/plugin/request-helpers.ts` | `20-38`, `43-50`, `56-81`, `87-105`, `111-136`, `142-165`, `171-197`, `204-272`, `278-302`, `313-358`, `369-454`, `460-534`, `540-570`, `575-606`, `612-651`, `659-686` | Nettoyage de schéma, passes 1-11 |
| `src/plugin/request-helpers.ts` | `2044-2216`, `2224-2260`, `2452-2505`, `2519-2559`, `2568-2608`, `2622-2656`, `2666-2696`, `2710-2751` | Réconciliation d'orphelins, signatures, durcissement |
| `src/plugin/transform/gemini.ts` | `27-50`, `52-124`, `117-121`, `476-579` | `toGeminiSchema`, injection `items`, wrapper d'outils |
| `src/plugin/thinking-recovery.ts` | `125-179`, `189-219`, `224-…`, `264-294`, `305-307`, `330-372`, `381-395` | Analyse de tour, clôture synthétique, compactage |
| `src/plugin/transform/cross-model-sanitizer.ts` | `30-119`, `200-260` | Assainissement unidirectionnel |
| `src/plugin/core/streaming/transformer.ts` | `13-19`, `21-28`, `30-56`, `58-173`, `175-224`, `226-289`, `291-362` | Hachage DJB2, déduplication, flush |
| `src/plugin/request.ts` | `79`, `1305-1318`, `1157`, `1179-1207`, `1327-1422`, `1431-1435`, `1690` | Câblage des passes, portée `isClaude` |
| `src/plugin/search.ts` | `1-7`, `113-145`, `147-211`, `223-313` | Grounding, parsing, formatage |
| `src/constants.ts` | `188-199`, `205`, `207-208`, `223`, `238-274` | Constantes de durcissement et de recherche |
| `src/plugin/transform/model-resolver.ts` | `40-60`, `62-83`, `98-123` | Alias, IDs annoncés, paliers |
| `src/plugin.ts` | `1354-1401` | Outil virtuel `google_search` |
| `src/v2.ts` | `602-640` | Schéma de l'outil de recherche (v2) |
| `docs/ANTIGRAVITY_API_SPEC.md` | `22-41`, `45-81`, `85-101`, `105-123`, `125-150`, `152-197`, `199-229`, `315-354`, `360-425`, `427-464` | Contrat : endpoints, en-têtes, enveloppes, schéma, réponses |
| `docs/MODEL-VARIANTS.md` | `28-29`, `43-45`, `55-58`, `71-90`, `116-132`, `141-145` | Paliers Claude / Gemini 3 / Gemini 2.5 — **aucune mention Gemini 4** |
| Tests | `request-helpers.test.ts:1230-1400`, `:1408-1560` ; `thinking-recovery.test.ts:5`, `:172-321`, `:284-319` ; `transform/gemini.test.ts:667-905`, `:1244-1279`, `:1279-1404` | Couverture (forme, non comportement) |

### 10.2 Dépôt cible `free-gemini-api` (module `goapi`, v3.5.6)

| Fichier | Plages citées | Objet |
|---|---|---|
| `gemini/registry.go` | `95-105`, `107-114`, `116-157`, `168-172`, `174-186`, `187-199`, `200-224`, `226-263`, `300-326`, `328-365`, `379-432`, `543-568` | Modèles, gabarits JSPB, index 15, enregistrement |
| `gemini/arranger.go` | `38-40`, `76-129`, `131-163`, `165-196`, `198-235`, `269-274`, `276-333`, `351-…`, `427-434`, `798-815`, `1515-1608`, `1610-1623` | Compaction, instructions, extraction, throttle |
| `api/chat_handler.go` | `20`, `46-56`, `58-64`, `66-74`, `76-79`, `110-124`, `126-137`, `441-444`, `449-499`, `501-530`, `532-542`, `592-616`, `618-624`, `643-644`, `680-708`, `710-716`, `780-812`, `814-845`, `877-900`, `901-945`, `960-995`, `1003`, `1020-1042`, `1043-1067`, `1090-1119`, `1143-1156`, `1177-1230`, `1253-1311`, `1331-1340`, `1345-1379` | Handler, budgets, prisons d'essai, SSE |
| `api/types.go` | `13-30`, `67-74`, `97-116` | Types OpenAI, `tool_choice` ignoré |
| `api/doom_loop.go` | `17-38`, `40-57`, `67-108`, `110-140`, `156-199`, `337-357` | Disjoncteur anti-boucle |
| `api/session_transcripts.go` | `470-489`, `615-640`, `724-745` | Directives, notices, tour vide |
| `api/observation_masker.go` | `9-38`, `40-108`, `110-133`, `135-157` | Masquage d'observations |
| `api/chat_service.go` | `149-162` | Résolution de modèle et mode tâche |
| `gemini/chat.go` | `28`, `45-46`, `209-233`, `295-321`, `323-360`, `502-534`, `570-599` | Flux, projection de deltas, RPC |
| `gemini/stream_guard.go` | `10-100` | Garde de flux |
| `gemini/client.go` | `22-30`, `32-55`, `139-144` | Budgets, champs de client, profil TLS |
| `gemini/transport.go` | `16-51`, `68-108`, `110-150`, `152-197`, `199-246` | Quatre chemins de transport |
| `gemini/schema_validator.go` | `16-25`, `27-62`, `64-…`, `271-291` | Validation d'exécution |
| `gemini/models.go` | `13-34` | Absence de champ de citation |
| `gemini/session.go` | `20-25`, `145-175`, `235-290` | Jetons de session, extraction HTML |

### 10.3 Plages déclarées non vérifiées

Par honnêteté méthodologique, les plages suivantes ne sont pas citées dans l'analyse comme preuve positive, ou le sont avec une réserve explicite :

| Plage | Raison |
|---|---|
| `src/plugin/thinking-recovery.ts:224-263` | Fin de `countTrailingToolResults` non lue intégralement ; seule la logique de remontée est utilisée (§5.1), et elle est corroborée par `src/plugin/thinking-recovery.ts:268-269` (`closeToolLoopForThinking`) |
| `gemini/arranger.go:351-…` | Corps de `truncateRunes` supposé linéaire sur les runes ; non relu. Aucune conclusion n'en dépend |
| `gemini/schema_validator.go:64-…` | Suite de `validateToolArgumentSemantics` au-delà de `:80` non relue ; le comportement cité (`path` vide, `oldString` vide) est attesté par `:69-80` |

Aucune autre conclusion de ce rapport ne repose sur une plage non lue.

### 10.4 Méthode de vérification

Les affirmations d'absence (verdicts `ABSENT`) ont été établies par recherche exhaustive sur l'arbre complet, non par échantillonnage :

| Affirmation | Motif recherché | Résultat |
|---|---|---|
| Aucune abstraction de transport | `interface {` | zéro occurrence de **déclaration** (seules des occurrences de `[]interface{}` comme type littéral) |
| Aucun champ de signature de pensée | `thoughtSignature`, `thinkingMetadata` | zéro |
| Aucun grounding | `googleSearch`, `grounding`, `Grounding` | zéro |
| Aucune sérialisation structurée | `functionCall`, `functionResponse` | zéro |
| Aucun hint de paramètre | `STRICT PARAMETERS`, `ARRAY_OF_OBJECTS` | zéro |
| `ToolCallID` jamais sérialisé | `ToolCallID`, `tool_call_id` | deux occurrences hors tests : la déclaration du champ (`api/types.go:28`) et son comptage dans l'estimation de taille (`api/observation_masker.go:113`). Aucune dans la construction du prompt (`api/chat_handler.go:501-530`) |
| Fermeture de balise absente en amont | `closeThought`, `antml`, `unterminated`, `premature` | zéro |
| `SEARCH_THINKING_BUDGET_*` sans appelant | `SEARCH_THINKING_BUDGET` | déclarations seules dans `src/constants.ts:247`, `:252` |
| `detectToolIdMismatches` export mort | `detectToolIdMismatches` | définition seule, aucun appelant |
| `hasPossibleCompactedThinking` limité aux tests | `hasPossibleCompactedThinking` | définition + `thinking-recovery.test.ts` uniquement |

---

**Fin du rapport.**

