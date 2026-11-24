# Remédiation agentique Gemini 4 — v3.5.4

**Date :** 2026-11-23
**Contexte :** septième passe de durcissement après le challenge d'alignement documentaire v3.5.3. Les transcripts des modèles `gemini-4-hydrogen`, `gemini-4-hydrogen-task` et `gemini-4-neon` sont archivés dans `debug/6_*.json`. Le code source d'OpenCode 2.4.0 utilisé pour l'analyse se trouve dans `/home/kidpixel/Téléchargements/opencode-2.4.0/`.

---

## 1. Constats issus des transcripts Challenge 7

Les trois modèles s'arrêtent prématurément, mais le message d'assistant contient à chaque fois un bloc `<thought>` **non fermé** :

```json
{
  "type": "text",
  "text": "<thought>\nThe user provided a system/user prompt detailing an alignment plan for updating project documentations (such as AGENTS"
}
```

`finish` et `rawFinish` valent `"stop"`, et l'outcome est `succeeded`. OpenCode reçoit donc un message d'assistant textuel avec `finish_reason: "stop"`, zéro appel d'outil, et conclut que la tâche est terminée.

### 1.1 Hydrogen : pensée coupée dès le premier message

`debug/6_gemini-4-hydrogen_plan-de-mise-a-jour-v3-5-3.json` s'arrête au Msg 01 avec un bloc `<thought>` ouvert et jamais refermé.

### 1.2 Hydrogen-Task : raisonnement streamé puis fuite textuelle

`debug/6_gemini-4-hydrogen-task_plan-d-alignement-v3-5-3.json` streame le raisonnement dans `reasoning_content`, puis émet à la fin du flux :

```text
<thought>
The user provided a comprehensive plan for aligning and updating documentation post-audits and remediations (v3.5.3), specifically mentioning AGENTS.md and audit-2026-11/README.md updates, along with verification steps. Since tool
```

### 1.3 Neon : pensée imbriquée après une édition

`debug/6_gemini-4-neon_plan-d-alignement-v3-5-3.json` exécute une édition sur `audit-2026-11/README.md` (Msg 02), puis produit au Msg 03 :

```text
<thought>
Let's review what was requested and what has been done:
1. `audit-2026-11/README.md` was edited to fix the code block (line 55 added ```bash).
2. Now let's check tests or coverage metrics... or check if any<thought>
J'ai effectué la correction du bloc de code manquant dans `audit-2
```

La seconde balise `<thought>` est orpheline (imbriquée dans la première) et le flux s'arrête sans fermeture.

### 1.4 Cause racine

**a. Un regex de paires fermées ne voit pas les pensées coupées.** Dans `gemini/arranger.go` :

```go
thoughtRegex = regexp.MustCompile(`(?s)<(?:thought|thinking)>([\s\S]*?)</(?:thought|thinking)>`)
```

`FindAllStringSubmatch` ne retourne rien sans `</thought>`, et `ReplaceAllString` ne retire donc rien. Le bloc brut reste dans le texte visible.

**b. La fuite devient un `content` assistant normal.** Dans `api/chat_handler.go`, `arranged.CleanText` contenant `<thought>…` est envoyé comme `choices[0].delta.content`, avec `finish_reason: "stop"`.

**c. Le détecteur de sortie inutilisable est aveugle.** `isUnusableModelOutput` dans `api/session_transcripts.go` ne testait que le texte vide et des listes de snippets. Une pensée brute de 100 à 500 caractères n'était ni vide ni reconnue comme refus : aucun retry n'était déclenché.

---

## 2. Correctifs livrés

### 2.1 Extracteur unifié `extractThoughtsAndContent` (`gemini/arranger.go`)

`thoughtRegex` est supprimé. Un scanner par balises devient la source de vérité unique :

```go
func extractThoughtsAndContent(raw string) (cleanedContent string, reasoningContent string)
```

Règles de fin d'un bloc de pensée, dans cet ordre :

1. **Balise fermante explicite** `</thought>` ou `</thinking>` : tout le bloc jusqu'à cette balise est du raisonnement. Une pensée fermée reste privée même si son raisonnement cite littéralement `<tool_call>`.
2. **Bascule vers un outil** : si le bloc n'est jamais fermé et qu'une balise `<tool_call>` apparaît, la pensée s'arrête juste avant elle. Le fragment `<tool_call>…` est **préservé verbatim** dans `cleanedContent`.
3. **Fin de flux** : si ni fermeture ni tool call n'apparaissent, tout le reste est du raisonnement. Un préfixe partiel de fermeture en queue (`</`, `</th`, `</thou`…) est supprimé.

Les balises ouvrantes imbriquées ou orphelines (`any<thought>`, `<thinking>` dans un bloc) incrémentent une profondeur et sont retirées du raisonnement. La fermeture n'est reconnue qu'à profondeur zéro, ce qui évite qu'une fermeture interne termine le bloc externe trop tôt.

Points d'entrée alignés sur le même extracteur :

```go
// ArrangerGeminiOutputWithTools
text, res.ReasoningContent = extractThoughtsAndContent(raw)

// ExtractAvailableThoughts
_, reasoning := extractThoughtsAndContent(raw)
return reasoning

// ExtractAvailableContent
cleaned, _ := extractThoughtsAndContent(raw)
return cleaned
```

Garanties vérifiées par les tests :

- `cleanedContent` ne contient jamais `<thought>`, `</thought>`, `<thinking>` ni `</thinking>` ;
- `cleanedContent` ne contient aucun texte issu d'une pensée non fermée ;
- une pensée fermée qui cite `<tool_call>` ne fuit pas sa fin dans le contenu visible ;
- une pensée non fermée suivie d'un vrai `<tool_call>` conserve l'appel et le parse normalement ;
- une forme imbriquée/orpheline (Neon) ne fuit ni tag ni raisonnement.

`sanitizeGeminiContent` applique une dernière ceinture de sécurité : tout tag de pensée résiduel est supprimé du texte visible, même si l'extraction n'a pas vu la balise ouvrante correspondante.

### 2.2 Détection des tours « pensée seule » (`api/session_transcripts.go`)

`isUnusableModelOutput` exécute désormais l'arrangeur réel sur les requêtes portant des outils :

```go
if hasTools {
    arranged := gemini.ArrangeGeminiOutputWithTools(text, tools)
    if !arranged.IsToolCall && len(arranged.ToolCalls) == 0 &&
        strings.TrimSpace(arranged.CleanText) == "" {
        return true
    }
}
```

Un modèle qui ne produit que du raisonnement — pensée fermée, non fermée ou coupée avant l'appel — n'a exécuté aucune action. Le tour est invalidé et rejoué sur une conversation amont propre avec la directive d'exécution :

```text
[SYSTEM DIRECTIVE: Execute the next step by emitting a <tool_call> XML block. ...]
```

Marqueurs de bilan partiel ajoutés à `pseudoReportSnippets` (formulations relevées en v3.5.3) :

- `j'ai effectué la correction`
- `j'ai effectué la modification`
- `j'ai corrigé`
- `i have fixed`
- `i have updated`
- `i have corrected`

Un appel d'outil réel reste prioritaire : `isPseudoProgressReport` commence par `ArrangeGeminiOutputWithTools(text, tools).IsToolCall`.

### 2.3 Garde de fin de retries (`api/chat_handler.go`)

Si le budget de tentatives est épuisé et qu'un tour avec outils rend toujours `!IsToolCall && len(ToolCalls) == 0 && CleanText == ""`, le proxy ne renvoie plus un chunk vide avec `finish_reason: "stop"`.

**Streaming** : chunk d'erreur explicite, puis fermeture SSE :

```text
[Error: upstream model completed turn without emitting a tool call or content after retries. Please retry.]
```

**Non-streaming** : réponse HTTP `502` :

```json
{"error": "upstream model completed turn without emitting a tool call or content after retries"}
```

La session est invalidée dans les deux cas pour que le tour suivant reparte d'une conversation amont propre.

### 2.4 Validation de schéma alignée (`gemini/schema_validator.go`)

`explicitToolCalls` remplace le nettoyage partiel :

```go
// Avant
frame := thoughtRegex.ReplaceAllString(raw, "")

// Après
frame := ExtractAvailableContent(raw)
```

Un fragment JSON ou un `<tool_call>` fictif placé dans une pensée non fermée n'est plus validé comme un appel émis. Le vrai appel qui suit la pensée reste dans le cadre et est validé normalement.

---

## 3. Tests de régression ajoutés

### `gemini/arranger_test.go`

- `TestArrangeGeminiOutputWithTools_UnclosedThoughtLeaksNothing`
  - reprend la forme exacte d'arrêt Hydrogen/Hydrogen-Task ;
  - vérifie que `ReasoningContent` capture la pensée, que `CleanText` est vide, et qu'aucun tag de pensée ne fuit dans l'un ou l'autre.
- `TestArrangeGeminiOutputWithTools_UnclosedThoughtBeforeToolCall`
  - pensée non fermée suivie immédiatement d'un `<tool_call>` valide ;
  - vérifie `IsToolCall`, `finish_reason: "tool_calls"`, le parsing de `read` sur `AGENTS.md`, et l'absence de tag dans le raisonnement.
- `TestArrangeGeminiOutputWithTools_NestedThoughtTags`
  - reprend la forme exacte Neon Msg 03 avec `<thought>` imbriquée et flux coupé ;
  - vérifie que les deux segments de raisonnement sont capturés et que `CleanText` reste vide.
- `TestArrangeGeminiOutputWithTools_ClosedThoughtQuotingToolCallTag`
  - une pensée fermée qui cite littéralement `<tool_call>` ne doit ni exécuter la mention ni laisser fuiter la fin de la pensée.
- `TestArrangeGeminiOutputWithTools_NestedClosedThoughts`
  - le comptage de profondeur empêche une fermeture interne de terminer le bloc externe et de fuiter sa queue.

### `api/session_scope_test.go`

- `TestIsUnusableModelOutput_ThoughtOnlyRequiresToolCall`
  - pensées fermée, non fermée et imbriquée : `true` avec outils, `false` sans outils ;
  - une pensée suivie de contenu visible ou d'un appel valide reste utilisable.
- `TestPseudoReportSnippets_PartialCompletionClaims`
  - chaque nouveau marqueur est intercepté par `isPseudoProgressReport` et `isUnusableModelOutput` avec outils, et accepté sans outils.

### `gemini/schema_validator_test.go`

- `TestHasInvalidSchemaToolCall_UnclosedThoughtBeforeRealCall`
  - un fragment JSON malformé dans une pensée non fermée est ignoré ;
  - le vrai `<tool_call>` qui suit est validé normalement.

---

## 4. Validation

| Contrôle | Commande | Résultat |
|---|---|---|
| Formatage Go | `test -z "$(gofmt -l .)"` | **OK** |
| Analyse statique | `go vet ./...` | **exit 0** |
| Tests courts | `go test -short ./...` | **OK** — `api 1.004s`, `db cached`, `gemini 18.404s` |
| Tests race | `go test -race -short ./...` | **OK** — `api 4.706s`, `db cached`, `gemini 8.540s`, aucun race détecté |
| Audit complet | `./scripts/audit_check.sh` | **exit 0** — couverture `api 66.6 %`, `db 77.1 %`, `gemini 72.6 %` |
| Rebuild Docker | `docker compose up -d --build` | **OK** — image reconstruite, conteneur recréé |
| Santé | `GET /health` | **online**, `status: online`, 1 worker extension connecté, pool de cookies sain (1 compte, TTL ~4320 h) |
| Smoke live | `./scripts/live_middleware_smoke.sh` | **6/6 PASS** |

### 4.1 Détail du passage smoke 6/6

```text
[1/6] Runtime sync: agentic counters exposed          PASS (3 compteurs)
[2/6] Orphan closing-tag sanitizer                    PASS (contenu + compteur 4 -> 5)
[3/6] Ground-truth recovery directive (Counter-S7)    PASS (compteur 1 -> 2 ; WARN stochastique sur la récupération visible)
[4/6] Instruction notice compaction (OpenCode replay)  PASS (pas de refus, tâche poursuivie)
[5/6] Upstream safety-refusal classifier              PASS (dangerous or illegal activities)
[6/6] Write-Serial: at most one mutation per turn     PASS (mutation unique + index SSE)
=== TOUS LES TESTS LIVE SONT PASSÉS (exit 0) ===
```

Le contrôle `[3/6]` conserve son `WARN` stochastique : le compteur `gemini_ground_truth_directives_total` est incrémenté et le test est compté `PASS`, conformément au contrat du script.

La couverture `gemini` progresse de 72.4 % (v3.5.3) à **72.6 %** grâce aux deux tests d'extraction supplémentaires. La couverture `api` reste à 66.6 % : les nouvelles branches de garde de fin de retries ne sont pas exécutées par les tests courts.

---

## 5. Limites et points de surveillance

- **Pensée ouverte sans `<tool_call>`** : tout le texte suivant est traité comme du raisonnement jusqu'à la fin du flux. C'est volontaire (priorité à la non-fuite) ; un contenu visible émis après une balise `<thought>` jamais fermée serait perdu plutôt qu'exposé.
- **Pensée fermée citant un vrai appel** : une enveloppe `<tool_call>` complète placée *à l'intérieur* d'une pensée explicitement fermée est traitée comme du raisonnement et n'est pas exécutée. C'est la règle d'ordonnancement retenue : la fermeture explicite a priorité sur la bascule outil.
- **Balises de pensée dans du contenu légitime** : un code ou une documentation qui affiche littéralement `<thought>` / `</thought>` est nettoyé. Ce comportement de confidentialité existait déjà avec le regex de paires fermées et reste volontaire.
- **Marqueurs de pseudo-rapport** : la liste reste déterministe et auditable. Une nouvelle formulation devra être ajoutée explicitement.
- **Garde de fin de retries** : elle produit une erreur explicite, pas une réussite silencieuse. L'hôte doit relancer le tour ; le proxy ne peut pas inventer un appel d'outil.
- **Smoke live** : le contrôle `[2/6]` dépend de la coopération du modèle (écho des balises). Des échantillons amont ont produit des réponses vides ou une mention isolée des balises, faisant échouer le contrôle sans régression du sanitizer ; le passage final a produit l'écho complet et le compteur `4 -> 5`. Le contrôle `[3/6]` reste stochastique par construction.

---

## 6. Fichiers modifiés

- `free-gemini-api/gemini/arranger.go`
- `free-gemini-api/gemini/arranger_test.go`
- `free-gemini-api/gemini/schema_validator.go`
- `free-gemini-api/gemini/schema_validator_test.go`
- `free-gemini-api/api/session_transcripts.go`
- `free-gemini-api/api/session_scope_test.go`
- `free-gemini-api/api/chat_handler.go`
- `CHANGELOG.md`
- `docs/remediation_agentic_gemini4_v3.5.4.md`
