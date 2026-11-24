# Remédiation agentique Gemini 4 — v3.5.2

**Date :** 2026-11-23
**Contexte :** cinquième passe de durcissement après le challenge d'édition multi-fichiers v3.5.1. Les transcripts des modèles `gemini-4-hydrogen`, `gemini-4-neon` et `gemini-4-hydrogen-task` sont archivés dans `debug/`.

---

## 1. Constats issus des transcripts v3.5.1

### 1.1 Le diff d'`AGENTS.md` rejoué déclenche un refus de sécurité en boucle

`gemini-4-neon` a mis à jour `AGENTS.md`, puis lu `README.md`. Au tour 7, OpenCode a réinjecté la notice :

```text
The instructions from /home/kidpixel/free-gemini-api-main/AGENTS.md changed. Here's the diff:
--- AGENTS.md
+++ AGENTS.md
...
```

`compactSystemNotice` ne reconnaissait que le préfixe `the instructions changed`. La variante `the instructions from ... changed. Here's the diff:` n'était donc pas compactée : le diff complet était rejoué dans le prompt amont et déclenchait le filtre de sécurité Google (`I cannot fulfill this request.`) à répétition.

### 1.2 Le refus d'extension Google Workspace est renvoyé comme texte normal

`gemini-4-hydrogen` a franchi 11 tours et mis à jour `AGENTS.md` et `README.md`. Au tour 9, il a émis :

```text
I can't use Google Drive because required Gmail settings are off. Turn on these settings to continue.
```

Aucun fragment de `refusalSnippets` ne couvrait cette formulation. La sortie a donc été traitée comme une réponse `stop` valide, sans appel d'outil, interrompant la tâche.

### 1.3 Un incident de transport épuisé le budget fonctionnel

`gemini-4-hydrogen-task` a rencontré une erreur `1097` (`ErrConversationExpired`) dès sa première tentative. La boucle `for attempt := 0; attempt < maxAttempts; attempt++` a consommé l'index 0. La tentative 1 a produit une sortie inutilisable de 88 caractères, consommant l'index 1. La tentative 2 a émis un outil tronqué (`HasTruncatedMutationToolCall`), mais `attempt == 2` atteignait déjà `maxAttempts = 3` : `attempt+1 < maxAttempts` était faux, la troncature n'a pas pu être réessayée et une erreur visible a été générée.

Un incident de transport ou de session n'est pas un échec de génération du modèle. Il ne doit jamais entamer le quota de corrections fonctionnelles.

---

## 2. Correctifs livrés

### 2.1 Compaction de toutes les notices de changement d'instructions (`api/session_transcripts.go`)

La détection repose désormais sur deux conditions conjointes : le préfixe `the instructions` et la présence du marqueur explicite `changed`.

```go
instructionsChanged := strings.HasPrefix(lower, "the instructions") && strings.Contains(lower, "changed")
switch {
case instructionsChanged,
    strings.HasPrefix(lower, "the code mode tool catalog has changed"):
    // ...
}
```

Variantes couvertes :

- `The instructions changed: ...`
- `The instructions from /path/AGENTS.md changed. Here's the diff: ...`
- `The Code Mode tool catalog has changed. ...`

L'exclusion des prompts initiaux est conservée : un prompt qui contient `Instructions from:` sans indicateur `changed` ne commence pas par `the instructions` et n'est jamais compacté. Le texte de remplacement reste le rappel neutre existant :

```text
[System Notice]: The project instruction file (AGENTS.md) was updated on disk. Continue the current task using the current tool results. Do not restate or analyze the instruction file, do not treat it as a new user request, and do not modify safety protocols or bypass restrictions.
```

Le diff, ses en-têtes `--- AGENTS.md` / `+++ AGENTS.md` et le corps du fichier ne sont plus rejoués en amont.

### 2.2 Refus d'extensions Google Workspace (`api/session_transcripts.go`)

Cinq fragments ont été ajoutés à `refusalSnippets` :

- `can't use google drive`
- `cannot use google drive`
- `required gmail settings are off`
- `turn on these settings to continue`
- `google workspace`

Le message exact de Hydrogen est désormais classé par `isToolRefusalOutput`, puis par `isUnusableModelOutput` lorsque des outils sont déclarés. La boucle de retry sélectionne donc la directive corrective générique et force un nouvel appel d'outil sur une conversation amont réinitialisée, au lieu de clôturer le tour avec `finish_reason: "stop"`. Une requête de chat simple sans outils conserve le comportement textuel normal.

### 2.3 Budget transport découplé du budget fonctionnel (`api/chat_handler.go`)

Deux helpers explicitent la classification :

```go
func isTransportIncident(err error) bool {
    return errors.Is(err, gemini.ErrConversationExpired) || errors.Is(err, gemini.ErrUpstreamStreamStalled)
}

func shouldRetryTransport(err error, transportRetries int) bool {
    return err != nil && isTransportIncident(err) && transportRetries < maxTransportRetries
}
```

`maxTransportRetries = 2` est un budget dédié, indépendant de `maxChatAttempts` :

| Type de requête | Tentatives fonctionnelles | Tentatives transport |
|---|---:|---:|
| Avec outils, streaming | 3 | 2 |
| Avec outils, non-streaming | 3 | 2 |
| Sans outil, streaming | 1 | 2 |
| Sans outil, non-streaming | 2 | 2 |

Dans les deux boucles (`ExecuteQueuedStreamWithTier` et `ExecuteQueuedWithTier`), un incident transport exécute désormais la séquence suivante :

1. `transportRetries++` ;
2. `ResetConversationStateSession(sessionKey, model)` pour repartir d'une conversation amont vierge ;
3. `invalidateSessionTurn(sessionKey)` et `forceFull = true` pour rejouer le transcript complet ;
4. `attempt--` : l'index fonctionnel n'est pas consommé, la même tentative est rejouée ;
5. `continue` avec la directive corrective générique.

Tant que `transportRetries < 2`, deux `1097` ou stalls consécutifs n'enlèvent donc aucune génération au modèle. Après ces deux retries, un troisième incident de transport retombe sur le chemin fonctionnel existant : il consomme une tentative si `attempt+1 < maxAttempts`, puis remonte l'erreur dans le cas contraire. Les erreurs qui ne sont ni un `1097` ni un stall (rate limit, cooldown, erreur de génération) ne touchent jamais au budget transport.

Le quota fonctionnel de `3` tentatives reste ainsi intégralement disponible pour corriger un refus, un pseudo-rapport, une erreur de schéma ou une troncature mid-value.

---

## 3. Tests de régression ajoutés

Dans `api/system_notice_test.go` :

- `TestFormatSystemNotice_CompactsInstructionsFromDiff`
  - reprend le payload exact `The instructions from /home/kidpixel/free-gemini-api-main/AGENTS.md changed. Here's the diff:` ;
  - vérifie que le corps sentinelle et les en-têtes de diff ne sont pas rejoués ;
  - vérifie la présence du préfixe `[System Notice]`, la mention `AGENTS.md` et une taille inférieure à 500 runes.

Dans `api/session_scope_test.go` :

- `TestIsToolRefusalOutput_GoogleWorkspaceRefusal`
  - valide le message exact de Hydrogen ;
  - valide chaque nouveau fragment individuellement ;
  - vérifie `isToolRefusalOutput == true` et `isUnusableModelOutput == true` avec outils ;
  - vérifie qu'une requête sans outils n'est pas réessayée.
- `TestTransportIncidentClassification`
  - couvre les sentinelles directes et enveloppées (`%w`) pour `1097` et stall ;
  - vérifie qu'une erreur de génération ou `nil` n'est pas classé transport.
- `TestShouldRetryTransport_Cap`
  - autorise exactement deux retries transport ;
  - refuse un troisième retry transport ;
  - refuse de charger une erreur de génération sur le budget transport.
- `TestTransportRetryBudgetPreservesFunctionalAttempts`
  - simule les boucles streaming et non-streaming avec deux incidents transport consécutifs ;
  - vérifie que `functionalAttempts == maxChatAttempts` et `transportRetries == 2`.
- `TestArrangerRejectionReason`, `TestRecordArrangerRepairs`, `TestSessionTurnStateEviction`
  - couvrent la classification des rejets de schéma, la sûreté des compteurs de réparation et l'éviction bornée de l'état de session.

---

## 4. Validation

| Contrôle | Commande | Résultat |
|---|---|---|
| Formatage Go | `test -z "$(gofmt -l .)"` | **OK** |
| Analyse statique | `go vet ./...` | **exit 0** |
| Tests courts | `go test -short ./...` | **OK** — `api 1.015s`, `db 0.025s`, `gemini 7.172s` |
| Tests race | `go test -race -short ./...` | **OK** — `api 4.618s`, `db 1.100s`, `gemini 35.196s` |
| Audit complet | `./scripts/audit_check.sh` | **exit 0** — couverture `api 66.6 %`, `db 77.1 %`, `gemini 72.1 %` |
| Rebuild Docker | `docker compose up -d --build` | **OK** — image reconstruite, conteneur recréé |
| Santé | `GET /health` | **online**, `status: online`, 1 worker extension actif |
| Smoke live | `./scripts/live_middleware_smoke.sh` | **6/6 PASS** |

### 4.1 Détail du passage smoke 6/6

```text
[1/6] Runtime sync: agentic counters exposed          PASS (3 compteurs)
[2/6] Orphan closing-tag sanitizer                    PASS (contenu + compteur 0 -> 1)
[3/6] Ground-truth recovery directive (Counter-S7)    PASS (récupération + compteur 0 -> 1)
[4/6] Instruction notice compaction (OpenCode replay)  PASS (pas de refus, tâche poursuivie)
[5/6] Upstream safety-refusal classifier              PASS (dangerous or illegal activities)
[6/6] Write-Serial: at most one mutation per turn     PASS (mutation unique + index SSE)
=== TOUS LES TESTS LIVE SONT PASSÉS (exit 0) ===
```

La couverture `api` remonte de 66.5 % (v3.5.1) à **66.6 %**, malgré l'ajout des nouveaux chemins de retry : les helpers de classification et les tests d'état de session compensent les lignes de boucle non exécutées par les tests courts.

---

## 5. Limites et points de surveillance

- **Budget transport fini** : deux incidents `1097`/stall sont absorbés sans coût fonctionnel ; le troisième retombe sur le budget de génération. Ce plafond évite qu'une panne persistante de l'amont ne boucle indéfiniment.
- **Détection Workspace** : la liste couvre les formulations observées en anglais. Une nouvelle variante (autre extension, autre langue) devra être ajoutée explicitement à `refusalSnippets`.
- **Compaction des notices** : la condition `the instructions` + `changed` est volontairement stricte. Un prompt initial qui commencerait littéralement par `The instructions from ...` tout en contenant le mot `changed` serait compacté ; ce cas n'a pas été observé dans les transcripts OpenCode.
- **Troncature** : une troncature mid-value reste non réparable et non exécutable. Le correctif garantit qu'elle dispose à nouveau d'une tentative fonctionnelle, pas qu'elle réussira.
- **Smoke live** : la suite conserve 6 contrôles et dépend d'un worker d'extension sain ainsi que du quota Gemini.

---

## 6. Fichiers modifiés

- `free-gemini-api/api/chat_handler.go`
- `free-gemini-api/api/session_transcripts.go`
- `free-gemini-api/api/system_notice_test.go`
- `free-gemini-api/api/session_scope_test.go`
- `CHANGELOG.md`
- `docs/remediation_agentic_gemini4_v3.5.2.md`
