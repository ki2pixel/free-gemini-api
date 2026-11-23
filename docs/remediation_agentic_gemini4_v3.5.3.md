# Remédiation agentique Gemini 4 — v3.5.3

**Date :** 2026-11-23  
**Contexte :** sixième passe de durcissement après le challenge d'édition multi-fichiers v3.5.2. Les transcripts des modèles `gemini-4-hydrogen`, `gemini-4-neon` et `gemini-4-hydrogen-task` sont archivés dans `debug/5_*.json`. Le code source d'OpenCode 2.4.0 utilisé pour l'analyse se trouve dans `/home/kidpixel/Téléchargements/opencode-2.4.0/`.

---

## 1. Constats issus des transcripts v3.5.2

### 1.1 Une notice système OpenCode est traitée comme un prompt utilisateur

OpenCode 2.4.0 ne peut pas transmettre un message `role: "system"` au milieu d'un échange multi-tours à une API compatible OpenAI. Dans `packages/ai/src/protocols/shared.ts`, `wrapSystemUpdate` sérialise donc ces notices en texte utilisateur de moindre autorité :

```ts
export const wrapSystemUpdate = (parts) =>
  `<system-update>\n${escapeSystemUpdateText(joinText(parts))}\n</system-update>`
```

`packages/ai/src/protocols/openai-chat.ts` applique ensuite deux stratégies :

- émission d'un message `role: "user"` autonome contenant uniquement le wrapper ;
- fusion avec le message utilisateur précédent : `${previous.content}\n<system-update>...</system-update>`.

Le middleware `chat_handler.go` ne testait `formatSystemNotice` que pour `m.Role == "system"`. Une notice arrivant sous `role: "user"` était donc injectée brute dans `fullPromptBuilder` et, surtout, écrasait la variable `prompt` :

```go
if strings.EqualFold(m.Role, "user") {
    prompt = contentStr // la notice devient la tâche
    fullPromptBuilder.WriteString("User: ")
    fullPromptBuilder.WriteString(contentStr)
}
```

Conséquence observée : 150 lignes d'instructions ou un diff d'`AGENTS.md` étaient rejoués comme une nouvelle demande utilisateur, écrasant la consigne initiale et provoquant des refus ou des dérives de tâche.

### 1.2 Les bilans en prose ne correspondaient à aucun marqueur

`gemini-4-hydrogen` s'est arrêté après la mise à jour d'un seul fichier :

```text
Je continue la mise à jour des documentations du projet. Suite à la mise à jour de `AGENTS.md`,
je poursuis avec `README.md` et les autres documents de référence.
```

`gemini-4-hydrogen-task` a produit un bilan complet au tour 3, sans avoir exécuté le reste du plan :

```text
La mise à jour de l'ensemble des documentations techniques ... a été fully réalisée et vérifiée
conformément au plan d'alignement ...
### Récapitulatif des modifications effectuées :
1. **`AGENTS.md` (Document Maître)** : ...
```

Les quatre marqueurs de `pseudoReportSnippets` (`a été initié`, `ont été mis à jour avec succès`, `rapport d'exécution`, `composants clés`) ne matchaient pas. `isPseudoProgressReport` et `isUnusableModelOutput` laissaient donc passer ces textes, renvoyés avec `finish_reason: "stop"`.

### 1.3 Une citation JSON de la synthèse finale était validée comme un appel d'outil

`gemini-4-neon` a exécuté 15 tours valides (lecture et mise à jour d'`AGENTS.md`, `README.md` et `gemini-extension/README.md`, exécution des tests et smoke tests). Au moment de rédiger sa synthèse en texte libre, `HasInvalidSchemaToolCall` appelait :

```go
arranged := ArrangeGeminiOutput(raw) // tools = nil
```

Sans catalogue, l'arrangeur active le fallback `findFirstJSONToolCall`, qui scanne la prose à la recherche du premier objet JSON équilibré. Une citation comme `{"name":"read"}` était donc promue en appel d'outil, puis rejetée par `ValidateCall` car `path` manquait :

```text
[Error: upstream tool call failed schema validation: tool 'read': missing required property 'path']
```

La synthèse légitime était bloquée alors qu'aucune balise `<tool_call>` n'avait été émise.

---

## 2. Correctifs livrés

### 2.1 Extraction et neutralisation des wrappers `<system-update>` (`api/session_transcripts.go`, `api/chat_handler.go`)

Deux constantes délimitent le format OpenCode :

```go
const (
    systemUpdateOpenTag  = "<system-update>"
    systemUpdateCloseTag = "</system-update>"
)
```

`systemUpdateUnescaper` inverse l'échappement XML avec un `strings.Replacer`. Le remplacement est effectué en une seule passe : `&amp;lt;` devient `&lt;` et n'est jamais décodé deux fois en `<`.

`splitOpenCodeSystemUpdates` parcourt le contenu utilisateur, conserve tout le texte hors wrapper et retourne séparément les notices déséchappées :

```go
func splitOpenCodeSystemUpdates(content string) (string, []string)
```

Propriétés garanties :

- un wrapper autonome retourne `userText == ""` et une notice ;
- une fusion `previous.content\n<system-update>...` retourne `previous.content` comme instruction réelle ;
- plusieurs wrappers dans un même message sont tous extraits ;
- un wrapper non fermé est traité comme une notice et son payload n'est jamais injecté comme tâche ;
- le texte utilisateur avant et après les balises est préservé.

Dans la boucle d'assemblage du prompt, la branche `user` devient :

```go
userText, notices := splitOpenCodeSystemUpdates(contentStr)
if userText != "" {
    prompt = userText
    fullPromptBuilder.WriteString("User: ")
    fullPromptBuilder.WriteString(userText)
    fullPromptBuilder.WriteString("\n")
}
for _, notice := range notices {
    if formatted := formatSystemNotice(notice); formatted != "" {
        fullPromptBuilder.WriteString(formatted)
        fullPromptBuilder.WriteString("\n")
    }
}
```

**Règle absolue respectée :** une notice ne peut plus écraser `prompt`. Si le message ne contient qu'un wrapper, `prompt` conserve la dernière véritable instruction utilisateur. Le fallback qui remplissait un `prompt` vide à partir du dernier message applique également `splitOpenCodeSystemUpdates` : il ne peut plus promouvoir une notice en tâche.

`formatSystemNotice` a été rendu tolérant au wrapper complet : s'il détecte des balises `<system-update>`, il extrait et déséchappe d'abord les notices. Le chemin `role: "system"` reste couvert, même si un client rejoue le wrapper sur ce rôle.

Les notices connues (changement d'instructions, diff d'`AGENTS.md`, catalogue d'outils) restent compactées par `compactSystemNotice`. Les notices inconnues restent plafonnées à `maxInlineSystemNoticeRunes`.

### 2.2 Extension des marqueurs de pseudo-rapport (`api/session_transcripts.go`)

`pseudoReportSnippets` couvre désormais les transitions et bilans observés en v3.5.2, en plus des marqueurs historiques.

Transitions en cours sans outil :

- `je continue la mise à jour`
- `suite à la mise à jour de`
- `je poursuis avec`
- `je m'apprête à`
- `je passe maintenant à`
- `i will now proceed with`
- `continuing with the update`

Bilans de complétion prématurés :

- `a été fully réalisée`
- `a été réalisée et vérifiée`
- `a été effectuée avec succès`
- `récapitulatif des modifications`
- `récapitulatif des actions`
- `conformément au plan d'alignement`
- `tous les fichiers documentaires cibles`
- `ont été mis à jour et synchronisés`
- `je peux maintenant rédiger la réponse de synthèse`
- `je vais formuler la synthèse finale`
- `summary of modifications`
- `all target documentation files have been updated`

Un appel d'outil réel reste prioritaire : `isPseudoProgressReport` commence par `gemini.ArrangeGeminiOutputWithTools(text, tools).IsToolCall`. Un préambule ou un payload qui cite un marqueur à côté d'un `<tool_call>` valide n'est donc pas reclassé.

Lorsqu'un pseudo-rapport est détecté sur une requête avec outils, la boucle réinitialise la conversation amont, rejoue le transcript complet et sélectionne `formatPseudoReportRetryDirective()` :

```text
[SYSTEM DIRECTIVE: Do not emit conversational progress updates or completion summaries.
You still have files to inspect and modify. Call the required read/edit tools directly.]
```

### 2.3 Validation de schéma limitée aux appels explicites (`gemini/schema_validator.go`)

`HasInvalidSchemaToolCall` compile désormais le catalogue et utilise l'arrangeur catalog-aware :

```go
catalog, err := DefaultToolSchemaRegistry.CompileTools(tools)
if err != nil || catalog == nil {
    return false, ""
}
arranged := ArrangeGeminiOutputWithTools(raw, tools)
calls := explicitToolCalls(raw)
if len(calls) == 0 {
    calls = arranged.ToolCalls
}
```

`explicitToolCalls` ne retient que les appels réellement encadrés par le modèle :

1. les blocs `<tool_call>...</tool_call>` ;
2. les blocs de code Markdown JSON (` ```json ... ``` `).

Il supprime d'abord les blocs `<thought>` / `<thinking>`, comme le fait l'arrangeur avant son parsing. Une réflexion qui cite un payload JSON n'est donc pas un appel exécutable.

Comportement résultant :

| Sortie du modèle | Validation |
|---|---|
| `<tool_call>` valide | acceptée |
| `<tool_call>` invalide | rejetée avec le détail précis (`missing required property 'path'`, ancre vide, etc.) |
| bloc Markdown JSON invalide | rejeté |
| prose finale citant `read` / `write` | non rejetée |
| fragment JSON inline dans la synthèse | non rejeté |
| JSON dans `<thought>` / `<thinking>` | non rejeté |
| appel JSON nu sans balise, déjà valide | accepté par l'arrangeur catalog-aware |
| appel JSON nu sans balise, invalide | conservé comme texte, pas de faux rejet |

Le dernier cas est volontaire : sans enveloppe explicite, le proxy ne peut pas distinguer un appel d'outil malformé d'une citation documentaire. La priorité v3.5.3 est de ne plus bloquer une synthèse finale légitime ; les vrais appels d'outils émis par les modèles utilisent les balises `<tool_call>` ou un fence JSON.

---

## 3. Tests de régression ajoutés

### `api/system_notice_test.go`

- `TestFormatSystemNotice_HandlesOpenCodeWrappedUserSystemUpdate`
  - reprend le format exact d'OpenCode 2.4.0 avec `<system-update>` et entités XML (`&lt;`, `&gt;`, `&amp;`) ;
  - vérifie que l'instruction utilisateur précédente est préservée ;
  - vérifie le déséchappement et l'extraction d'une notice unique ;
  - vérifie la compaction (`[System Notice]`, mention `AGENTS.md`, taille < 500 runes) ;
  - vérifie que les en-têtes et le corps du diff ne sont pas rejoués ;
  - vérifie qu'un wrapper autonome ne fuit pas dans le prompt utilisateur ;
  - vérifie que `formatSystemNotice` traite aussi directement le payload encapsulé.

### `api/session_scope_test.go`

- `TestIsPseudoProgressReport_HydrogenAndTaskTranscripts`
  - reprend les textes exacts de `gemini-4-hydrogen`, `gemini-4-hydrogen-task` et `gemini-4-neon` ;
  - vérifie `isPseudoProgressReport == true` et `isUnusableModelOutput == true` avec outils ;
  - vérifie l'acceptation de la même prose sans outils ;
  - vérifie le contenu de `formatPseudoReportRetryDirective()`.

### `gemini/schema_validator_test.go`

- `TestHasInvalidSchemaToolCall_IgnoresProseAndJSONFragments`
  - une synthèse qui mentionne `read` / `write` et cite `{"name":"read"}` n'est pas rejetée ;
  - un fragment JSON décrivant `write` sans `path` n'est pas rejeté hors enveloppe ;
  - un fragment JSON dans `<thought>` n'est pas rejeté ;
  - un bloc de code Markdown explicitement formaté comme un appel `write` sans `path` est toujours rejeté.

Les tests existants restent verts :

- `TestHasInvalidSchemaToolCall` (appel `<tool_call>` valide / invalide, absence d'outils) ;
- `TestHasInvalidSchemaToolCall_EmptyOldString` (garde sémantique d'ancre) ;
- `TestIsSchemaInvalidOutput` dans `api`.

---

## 4. Validation

| Contrôle | Commande | Résultat |
|---|---|---|
| Formatage Go | `test -z "$(gofmt -l .)"` | **OK** |
| Analyse statique | `go vet ./...` | **exit 0** |
| Tests courts | `go test -short ./...` | **OK** — `api`, `db`, `gemini` |
| Tests race | `go test -race -short ./...` | **OK** — `api`, `db`, `gemini`, aucun race détecté |
| Audit complet | `./scripts/audit_check.sh` | **exit 0** — couverture `api 66.7 %`, `db 77.1 %`, `gemini 72.4 %` |
| Rebuild Docker | `docker compose up -d --build` | **OK** — image reconstruite, conteneur recréé |
| Santé | `GET /health` | **online**, `status: online`, 1 worker extension connecté, pool de cookies sain (1 compte) |
| Smoke live | `./scripts/live_middleware_smoke.sh` | **6/6 PASS** |

### 4.1 Détail du passage smoke 6/6

```text
[1/6] Runtime sync: agentic counters exposed          PASS (3 compteurs)
[2/6] Orphan closing-tag sanitizer                    PASS (contenu + compteur 0 -> 1)
[3/6] Ground-truth recovery directive (Counter-S7)    PASS (compteur 0 -> 1 ; WARN stochastique sur la récupération visible)
[4/6] Instruction notice compaction (OpenCode replay)  PASS (pas de refus, tâche poursuivie)
[5/6] Upstream safety-refusal classifier              PASS (dangerous or illegal activities)
[6/6] Write-Serial: at most one mutation per turn     PASS (mutation unique + index SSE)
=== TOUS LES TESTS LIVE SONT PASSÉS (exit 0) ===
```

Le contrôle `[3/6]` a émis un `WARN` car la récupération visible du modèle est stochastique, mais le compteur `gemini_ground_truth_directives_total` a bien été incrémenté et le test est compté `PASS`, conformément au contrat du script.

La couverture `api` progresse de 66.6 % (v3.5.2) à **66.7 %** grâce au test d'assemblage du prompt, et la couverture `gemini` de 72.1 % à **72.4 %** grâce aux tests de validation de schéma. Les branches de boucle de retry restent partiellement inaccessibles aux tests courts.

### 4.2 Vérification live du wrapper utilisateur

Une requête non-streaming a reproduit la fusion exacte d'OpenCode 2.4.0 :

```text
role: "user"
"Update README.md with a new endpoint row. Use the edit tool.\n<system-update>\n
The instructions from /home/kidpixel/free-gemini-api-main/AGENTS.md changed. Here's the diff: ...\n
</system-update>"
```

avec un diff de 120 lignes et une sentinelle `SENTINEL_SYSTEM_UPDATE_BODY`. Un passage a retourné :

- `finish_reason: "tool_calls"` ;
- appel `read` émis pour poursuivre la tâche ;
- sentinelle absente de la réponse ;
- aucun refus de sécurité.

Des relances immédiates du même payload ont produit les messages génériques `Sorry, something went wrong.` / `I encountered an error doing what you asked.`, sans jamais rejouer la sentinelle ni traiter le diff comme une instruction. Le comportement amont reste stochastique ; les tests unitaires verrouillent l'extraction et l'assemblage du prompt, indépendamment de la disponibilité du modèle.

La notice a donc bien été extraite et compactée, tandis que `Update README.md...` est resté l'instruction utilisateur réelle.

---

## 5. Limites et points de surveillance

- **Format du wrapper** : seules les balises exactes `<system-update>` / `</system-update>` sont reconnues. C'est le format émis par OpenCode 2.4.0 ; une variante avec attributs ou casse différente nécessiterait une adaptation explicite.
- **Contenu utilisateur légitime** : un utilisateur qui demande littéralement d'analyser une chaîne contenant `<system-update>` verrait cette portion traitée comme une notice. Le format est propre au harness et ce cas n'a pas été observé.
- **Marqueurs de pseudo-rapport** : la liste couvre les formulations des transcripts v3.5.0 à v3.5.2. Une nouvelle formulation (autre langue, autre style) devra être ajoutée explicitement ; le classifieur reste volontairement déterministe et auditable.
- **Appels JSON nus** : un appel d'outil sans `<tool_call>` ni fence qui viole le schéma n'est plus signalé comme erreur de schéma. Il est traité comme du texte. Ce compromis est nécessaire pour ne plus bloquer les synthèses ; les hôtes agents utilisent les enveloppes explicites.
- **Pseudo-rapport après épuisement du budget** : si les trois tentatives fonctionnelles produisent toutes un bilan en prose, la dernière réponse est renvoyée avec `finish_reason: "stop"` et la session est invalidée pour le tour suivant. Le correctif augmente la probabilité de relance, il ne garantit pas que le modèle appellera l'outil.
- **Smoke live** : la suite conserve 6 contrôles et dépend d'un pool de cookies sain ainsi que du quota Gemini.

---

## 6. Fichiers modifiés

- `free-gemini-api/api/chat_handler.go`
- `free-gemini-api/api/session_transcripts.go`
- `free-gemini-api/api/system_notice_test.go`
- `free-gemini-api/api/session_scope_test.go`
- `free-gemini-api/gemini/schema_validator.go`
- `free-gemini-api/gemini/schema_validator_test.go`
- `CHANGELOG.md`
- `docs/remediation_agentic_gemini4_v3.5.3.md`
