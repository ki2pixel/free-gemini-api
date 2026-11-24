# Journal de bord — Fiabilisation Gemini 4 (audit 2026-11)

Dernière mise à jour : 2026-11-23
Référence : [`docs/recherches/audit-opencode-antigravity-auth-gemini-fiabilisation.md`](../recherches/audit-opencode-antigravity-auth-gemini-fiabilisation.md)

## 1. État des vagues

| Vague | Périmètre | Actions | Statut |
|---|---|---|---|
| 1 (P0) | Rejets upstream, bijection appels/résultats, perte de pensée | A-01 → A-05 | Livré dans le working tree |
| 2 (P1) | Duplication de flux, dédoublonnage, durcissement des schémas, escalade de palier | A-06 → A-11 | Livré dans le working tree |
| 3 (P2) | Alignement des IDs amont, doublon `gemini-4-hydrogen` | A-15, A-16 | **Clôturé le 2026-11-23** |
| 3 (P2, écartée) | Refactor `Transport`, récupération sans signature, outil `google_search` | A-12, A-13, A-14 | Écartées (disproportionnées) |

Aucun commit n'a été créé : les modifications restent dans le working tree conformément à la règle projet.

## 2. Vague 1 (P0)

| ID | Correctif | Marqueur de vérification |
|---|---|---|
| A-01 | Filtrage de `required` contre `properties` dans les schémas compactés | `compactParametersSchema` (`gemini/arranger.go`) |
| A-02 | Synthèse de `items: {type:"string"}` pour les tableaux sans `items` | `gemini/arranger.go` + tests d'arranger |
| A-03 | Sérialisation de `ToolCallID` dans les lignes `[Tool Result for …]` | `api/chat_handler.go`, `api/types.go` |
| A-04 | Réconciliation des orphelins après `ThrottleMutatingToolCalls` | marqueur `[recovered]` |
| A-05 | High-water mark par bloc pour les pensées et le contenu émis | compteurs par bloc dans `api/chat_handler.go` |

## 3. Vague 2 (P1)

| ID | Correctif | Marqueur de vérification |
|---|---|---|
| A-06 | Correction de `nextStreamDelta` (plus de recouvrement de suffixe) | `nextStreamDelta` (`gemini/chat.go`) |
| A-07 | Dédoublonnage borné, local à la requête | anneau d'empreintes dans `api/chat_handler.go` |
| A-08 | Signatures `STRICT PARAMETERS` calculées sur le schéma | `gemini/arranger.go` |
| A-09 | Clause de durcissement fusionnée dans la règle 4 | `gemini/arranger.go` |
| A-10 | Escalade de palier de thinking sur tours vides répétés | `thinkingTierOrder` (`gemini/thinking.go`) |
| A-11 | Exposition de `reasoning_effort` par requête | `api/types.go`, `gemini/models.go` |

## 4. Vague 3 (P2) — clôture A-15 / A-16

### A-16 — Définition unique de `gemini-4-hydrogen`

- Suppression de la première entrée morte (ancien gabarit non-thinking sans `long_context`).
- Conservation de l'entrée canonique issue de la capture live du 2026-11-17, seule gagnante à l'enregistrement.
- Fusion de la capacité `"video"` dans cette entrée canonique :
  `chat, stream, vision, video, tools, long_context`.
- Commentaire `A-16` ajouté au-dessus de l'entrée pour documenter l'arbitrage.

### A-15 — Table d'alignement amont Antigravity Hub ↔ registre Go

Les identifiants amont `gemini-4-<famille>-<palier>` sont désormais enregistrés comme alias
des modèles canoniques `gemini-4-<famille>-thinking-<palier>` :

| ID amont | ID canonique | `ThinkingLevel` |
|---|---|---|
| `gemini-4-argon-high` | `gemini-4-argon-thinking-high` | 2 |
| `gemini-4-argon-medium` | `gemini-4-argon-thinking-medium` | 4 |
| `gemini-4-argon-low` | `gemini-4-argon-thinking-low` | 1 |
| `gemini-4-neon-high` | `gemini-4-neon-thinking-high` | 2 |
| `gemini-4-neon-medium` | `gemini-4-neon-thinking-medium` | 4 |
| `gemini-4-neon-low` | `gemini-4-neon-thinking-low` | 1 |
| `gemini-4-hydrogen-high` | `gemini-4-hydrogen-thinking-high` | 2 |
| `gemini-4-hydrogen-medium` | `gemini-4-hydrogen-thinking-medium` | 4 |
| `gemini-4-hydrogen-low` | `gemini-4-hydrogen-thinking-low` | 1 |

La matrice est documentée en commentaire dans `gemini/registry.go`. Elle couvre les
recherches exactes de `Get` et de `lookupLocked` (utilisé par `ModelForThinkingTier`),
qui ne disposent d'aucun matching par sous-chaîne.

## 5. Tests de non-régression Vague 3

Ajoutés dans `free-gemini-api/gemini/registry_test.go` :

- `TestHydrogenSingleCanonicalDefinition` : compte strictement une définition de
  `gemini-4-hydrogen`, vérifie les capacités `video` et `long_context`, et le palier
  thinking Low par défaut (`,8,1,` dans `BuildHeaderValue()`).
- `TestUpstreamAntigravityAliases` : vérifie la matrice complète via `Get` et `Resolve`,
  puis `ModelForThinkingTier("gemini-4-argon-high", "low")`.

## 6. Validation exécutée

```bash
cd free-gemini-api && \
go test -v ./gemini -run "TestHydrogenSingleCanonicalDefinition|TestUpstreamAntigravityAliases|TestModelRegistryResolve" && \
go test -short ./gemini -run "TestThinking|TestArgon|TestNeon" && \
go test -short ./api && \
test -z "$(gofmt -l .)" && \
go vet ./...
```

Résultat du 2026-11-23 : toutes les suites passent, `gofmt` ne signale aucun fichier,
`go vet` est silencieux.

## 7. Actions écartées de la Vague 3

- **A-12** : refactor de l'interface `Transport` — risque de régression élevé sur les
  quatre chemins réseau, sans effet fonctionnel immédiat.
- **A-13** : récupération des pensées sans signature — partiellement redondante avec A-05.
- **A-14** : outil virtuel `google_search` — ajout de capacité lourd, sans mesure de
  latence disponible.
