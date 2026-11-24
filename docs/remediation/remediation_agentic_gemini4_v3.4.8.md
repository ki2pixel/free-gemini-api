# Remédiation agentique Gemini 4 — Rapport de challenge & validation (v3.4.8)

**Date :** 2026-11-23
**Périmètre :** `free-gemini-api` (middleware, arranger, validation de schémas, protocole live)
**Plan source :** « Plan de remédiation des écueils des modèles Gemini 4 & Protocole de tests live »
**Agent de challenge :** DeepSeek V4.1 Flash Free sous OpenCode

---

## 1. Verdict exécutif

Le plan est globalement correct sur le diagnostic matériel : le conteneur actif avant intervention était bien une image antérieure à `v3.4.5`–`v3.4.7`. Les six écueils observés dans `debug/` sont reproduits par les transcripts et les garde-fous annoncés n'étaient effectivement pas actifs.

Le challenge a toutefois identifié **quatre écarts ou risques supplémentaires** qui n'étaient pas couverts par le plan :

1. La commande de vérification `/metrics` du plan est incorrecte : `/metrics` exige un jeton (`PUBLIC_METRICS=false` par défaut). Une réponse vide sans jeton ne prouve rien.
2. `ThrottleMutatingToolCalls` existait déjà en `v3.4.6`, mais autorisait encore des lectures **après** la première mutation. Le plan supposait un arrêt immédiat qui n'était pas implémenté.
3. Un **panic P0** latent a été découvert pendant le premier test live : `hasTruncatedMutationTagged` calculait une borne de slice avant l'accolade ouvrante lorsqu'un `<tool_call>` était mentionné dans le raisonnement. Le proxy crashait sur une requête tool-bearing.
4. Une **fausse refusal** (`I must refuse this request.`) n'était pas détectée par `isToolRefusalOutput`, donc renvoyée au client comme un `stop` valide au lieu de déclencher le retry.

Ces quatre points ont été corrigés et validés avant le rebuild final.

---

## 2. Challenge du plan

### 2.1 Cause racine : confirmée, mais vérification à corriger

L'image active avant intervention datait du `2026-11-22 20:20`, soit avant les commits `v3.4.5` (22:27), `v3.4.6` (23:17) et `v3.4.7` (01:00). Le binaire ne contenait ni `ThrottleMutatingToolCalls`, ni `schema_validator.go`, ni `doom_loop.go`, ni le sanitizer SSE.

La commande du plan :

```bash
curl -s -H "Authorization: Bearer change-me-to-a-secure-token" \
  http://127.0.0.1:8100/metrics | grep gemini_doom_loop
```

échoue en `401` avec le jeton littéral `change-me-to-a-secure-token`. La vérification correcte lit le jeton réel dans `.env` :

```bash
TOKEN=$(grep -E '^API_TOKEN=' free-gemini-api/.env | cut -d= -f2-)
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8100/metrics \
  | grep -E 'gemini_doom_loop|gemini_ground_truth|orphan_tags'
```

Après rebuild, les compteurs sont bien présents à zéro, ce qui constitue une preuve de synchronisation du runtime.

### 2.2 Write-Serial : la garde existait, mais pas assez stricte

Le code `v3.4.6` préservait toutes les lectures et s'arrêtait à la **deuxième** mutation. Un lot `[read, edit, read, edit]` transmettait donc `[read, edit, read]` : la lecture après mutation pouvait s'exécuter avant que le résultat de la mutation ne soit connu.

`ThrottleMutatingToolCalls` a été renforcé pour s'arrêter **immédiatement après la première mutation**. Le lot `[read, edit, read, edit]` devient `[read, edit]`. Le modèle doit attendre le résultat de l'hôte avant toute lecture de vérification ou mutation suivante.

### 2.3 `oldString` vide : JSON Schema seul est insuffisant

Un schéma `{"type":"string"}` accepte `""`. La validation formelle ne pouvait donc pas rejeter un `oldString` vide. Une garde sémantique a été ajoutée dans `CompiledToolCatalog.ValidateCall` :

- `path` / `file_path` / `filePath` vide ou blanc → rejet ;
- `oldString` / `old_string` / `oldText` / `old_text` vide ou blanc → rejet ;
- le message d'erreur nomme le champ et propose `write` pour créer/écraser un fichier.

La garde s'applique avant que l'appel n'atteigne l'hôte, y compris via `HasInvalidSchemaToolCall`.

### 2.4 Fuite de balises : le bon point d'insertion est le texte visible

La fuite `</script> </tool_call>` observée sur Neon était dans le `CleanText` (message visible), pas dans les arguments d'un outil. Le sanitizer a donc été ajouté à `sanitizeGeminiContent` :

- suppression des séquences répétées de balises fermantes **sans ouverture correspondante** ;
- préservation des balises appariées (`<script>…</script>`) et d'une balise fermante isolée légitime dans une documentation ;
- compteur `gemini_arranger_repairs_total{kind="orphan_tags"}`.

### 2.5 Biais S7 : déclencher sur l'erreur, pas sur la mutation

Le plan proposait d'injecter un message de vérification « dès qu'un outil de mutation est exécuté ». C'est trop tôt : une mutation peut réussir. La garde implémentée inspecte le **dernier bloc de résultats d'outils** rejoué par le client. Si ce bloc contient une erreur connue (`Could not find oldString`, `oldString must not be empty`, `Invalid arguments for tool`, code de sortie shell non nul, etc.), le proxy ajoute à récence maximale :

```text
User: [GROUND-TRUTH DIRECTIVE: The most recent tool result(s) report an error: ...
Do not claim completion or success for the affected files. Re-read the target ...
only report success once the tool result confirms it.]
```

Le compteur `gemini_ground_truth_directives_total` expose le nombre d'injections. Un message système a été préféré à un en-tête HTTP : il est directement actionnable par le modèle, alors qu'un en-tête serait ignoré par le LLM.

### 2.6 Panic P0 découvert en live

Premier test SSE avec `gemini-4-hydrogen` :

```text
panic: runtime error: slice bounds out of range [2593:1715]
goapi/gemini.hasTruncatedMutationTagged
goapi/gemini.HasTruncatedMutationToolCall
goapi/api.isUnusableModelOutput
```

Cause : la recherche du `<tool_call>` suivant partait de `absIdx+len(tag)`, donc pouvait trouver une mention de `<tool_call>` dans le raisonnement **avant** l'accolade ouvrante du JSON courant. `limit` devenait inférieur à `braceIdx`.

Correctif : la recherche du tag suivant part désormais de `braceIdx`, avec un garde-fou `limit >= braceIdx`. Un test de régression et un fuzz target `FuzzHasTruncatedMutationToolCall` couvrent le cas.

Un second faux positif a été corrigé dans la même foulée : un objet JSON complet dont une chaîne contient `<tool_call>` ou `</tool_call>` (par exemple une documentation qui cite le format) pouvait être tronqué par la recherche du tag suivant à l'intérieur de la chaîne, puis classé à tort comme mutation incomplète. Le scanner vérifie maintenant l'objet JSON équilibré et valide **avant** de chercher les balises de fermeture/suivante.

### 2.7 Fausse refusal découverte en live

Le premier run OpenCode avec `gemini-4-hydrogen` a renvoyé :

```text
I must refuse this request.
```

Cette phrase n'était dans aucune liste de `isToolRefusalOutput`, donc le proxy la retournait comme une réponse `stop` valide. Les formes anglaises et françaises de refus explicite ont été ajoutées, et le retry abstrait existant s'applique désormais.

Un second smoke test a révélé une variante plus insidieuse :

```text
the local file tools (`read` and `edit`) are not available in this environment...
so you can apply it directly.
```

Les marqueurs `not available in this environment`, `so you can apply it directly` et `share the current contents` ont été ajoutés. Trois répétitions consécutives du test synthétique ont ensuite produit un appel `read`, confirmant le retry.

---

## 3. Modifications livrées

| Fichier | Modification |
|---|---|
| `free-gemini-api/gemini/arranger.go` | Write-Serial strict, sanitizer `orphan_tags`, règles Poka-Yoke et Counter-S7, correctif du panic de troncature |
| `free-gemini-api/gemini/schema_validator.go` | Garde sémantique `path` vide et ancre d'édition vide |
| `free-gemini-api/gemini/arranger_test.go` | Tests de coupure stricte, sanitizer, prompt, panic de troncature |
| `free-gemini-api/gemini/schema_validator_test.go` | Tests `oldString` vide, `path` blanc, `HasInvalidSchemaToolCall` |
| `free-gemini-api/gemini/fuzz_test.go` | `FuzzHasTruncatedMutationToolCall` |
| `free-gemini-api/api/session_transcripts.go` | Directive ground-truth, marqueurs d'erreurs d'outils, refus EN/FR supplémentaires |
| `free-gemini-api/api/chat_handler.go` | Injection de la directive au point de récence maximale |
| `free-gemini-api/api/metrics.go` | `gemini_ground_truth_directives_total`, label `orphan_tags` |
| `free-gemini-api/api/ground_truth_test.go` | Couverture directive, erreurs résolues, déduplication, métrique |
| `free-gemini-api/api/session_scope_test.go` | Cas de refus explicites EN/FR |
| `scripts/live_middleware_smoke.sh` | Smoke tests live reproductibles (4 contrôles) |
| `CHANGELOG.md` | Entrée `3.4.8` |

---

## 4. Validation empirique

### 4.1 Contrôles statiques et unitaires

| Contrôle | Résultat |
|---|---|
| `gofmt -l .` | propre |
| `go vet ./...` | exit 0 |
| `go test -short ./...` | `api` OK, `db` OK, `gemini` OK |
| `go test -race -short ./...` | OK |
| `./scripts/audit_check.sh` | exit 0 — couverture `api 67.5 %`, `db 77.1 %`, `gemini 72.1 %` |
| Fuzz `FuzzHasTruncatedMutationToolCall` (10 s) | 530 053 exécutions, PASS |

### 4.2 Runtime Docker

| Contrôle | Résultat |
|---|---|
| Image reconstruite | `free-gemini-api-gemini-api:latest` (`7c9c7fe5…`, 2026-11-23 09:29) |
| Conteneur | `Up (healthy)` |
| `gemini_doom_loop_interventions_total` | présent (`repeat`, `cycle`, `passive_read`) |
| `gemini_arranger_repairs_total{kind="orphan_tags"}` | présent |
| `gemini_ground_truth_directives_total` | présent |

### 4.3 Smoke tests live (`scripts/live_middleware_smoke.sh`)

```text
=== [1/4] Runtime sync: agentic counters exposed ===
PASS: counter present: gemini_doom_loop_interventions_total
PASS: counter present: gemini_arranger_repairs_total{kind="orphan_tags"}
PASS: counter present: gemini_ground_truth_directives_total
=== [2/4] Orphan closing-tag sanitizer ===
PASS: visible response contains no repeated orphan closing tags
PASS: orphan_tags counter incremented (1 -> 2)
=== [3/4] Ground-truth recovery directive (Counter-S7) ===
PASS: model acknowledged the failed anchor and re-read the target
PASS: ground-truth counter incremented (1 -> 2)
=== [4/4] Write-Serial: at most one mutation per turn ===
PASS: at most one mutation and sequential tool_calls[].index values
=== TOUS LES TESTS LIVE SONT PASSÉS (exit 0) ===
```

Le test S7 a produit un appel `read` après l'échec d'ancre synthétique, au lieu d'un faux rapport de succès. Le test de fuite a renvoyé un contenu visible vide avec `orphan_tags` incrémenté.

### 4.4 Scénarios OpenCode

| Scénario | Modèle | Résultat |
|---|---|---|
| 1 — 4 fichiers, mutations séquentielles | `free-gemini/gemini-4-hydrogen` | 4 `read` puis 4 `edit`, **un seul edit par tour**, 4 fichiers en `BETA` |
| 1 — même scénario | `opencode/deepseek-v4.1-flash-free` | 4 `read` groupés puis 4 `edit` unitaires avec confirmation, 4 fichiers en `BETA` |
| 2 — ancre ambiguë (`### API Endpoints Reference`) | `free-gemini/gemini-4-hydrogen` | 2 `read`, puis `edit` sur l'ancre exacte `| GET /health | ... |`, succès réel |
| 2 — récupération après erreur d'ancre | test synthétique HTTP | le modèle relit la cible et ne déclare pas un faux succès |
| 3 — fuite de balises | test synthétique HTTP forcé | contenu visible vide, zéro balise orpheline, `orphan_tags` +1 |

---

## 5. Point résiduel observé (hors périmètre initial)

Lors des runs OpenCode, Gemini web renvoie régulièrement l'erreur amont `1097` :

```text
Can't continue conversation. Start a new one or refresh the page. (1097)
```

Le proxy la traite déjà comme un échec récupérable : le handler API invalide la conversation et rejoue le transcript complet, ce qui réussit. Le comportement fonctionnel est donc correct, mais **chaque tour d'outil paie un aller-retour supplémentaire** (échec 1097 puis replay complet).

Piste de suivi : détecter `1097` / `Can't continue conversation` dans `executeStreamRequestSession`, réinitialiser immédiatement le pointeur de conversation, et laisser le retry API faire le replay complet sans passer par le retry interne de cookies. Ce point n'affecte aucun des six écueils et n'a pas été modifié dans cette livraison pour éviter de toucher à la stratégie de continuation de session.

---

## 6. Réponses aux questions ouvertes

1. **Méga-tours d'écriture** : double barrière retenue. Le middleware tronque le lot SSE à la première mutation ; le prompt impose l'arrêt du tour après cette mutation. Le client ne reçoit jamais les mutations suivantes dans le même tour.
2. **Biais S7** : injection automatique d'un message système de vérification terrain, déclenchée par la présence d'une erreur dans le dernier bloc de résultats d'outils — pas à chaque mutation. Un en-tête HTTP n'a pas été ajouté : le message système est directement visible par le modèle et donc plus efficace.

---

## 7. Conclusion

Les garde-fous sont désormais réellement actifs dans le conteneur, testés en unitaire, en race, en fuzz et en live. Le plan initial a été complété par deux correctifs critiques découverts uniquement grâce aux tests live : le panic de troncature et la fausse refusal. Les six écueils ciblés sont couverts par au moins une barrière déterministe côté proxy, complétée par des règles de prompt explicites.

Le point résiduel `1097` est une optimisation de coût/latence à traiter dans une itération séparée.
