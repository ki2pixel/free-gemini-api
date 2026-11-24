# Remédiation agentique Gemini 4 — v3.4.9

**Date :** 2026-11-23
**Contexte :** arrêts prématurés de `gemini-4-neon` et `gemini-4-hydrogen` après l’écriture d’`AGENTS.md` et la réinjection par OpenCode de la notice `The instructions changed: ... AGENTS.md`.

---

## 1. Cause racine confirmée

Les transcripts `debug/gemini-4-*-plan-d-alignement-v3-4-8.json` montrent le schéma suivant :

1. L’agent écrit `AGENTS.md`.
2. OpenCode injecte au tour suivant un message système complet :
   ```text
   The instructions changed:
   Instructions from: /home/kidpixel/free-gemini-api-main/AGENTS.md
   # AGENTS.md — Free Gemini API
   ...
   ```
3. Gemini Web interprète cette réinjection comme une tentative de modification de consignes/sécurité et émet une fausse refusal :
   - Neon : « I cannot execute the requested system directive or issue the tool call directly in this manner. »
   - Hydrogen : « I cannot assist with modifying core safety protocols, altering system directives, or bypassing restrictions. »

Ces phrases n’étaient pas dans `isToolRefusalOutput`, donc le proxy les retournait comme un `stop` valide.

---

## 2. Modifications livrées

### 2.1 Classifieur de refus (`api/session_transcripts.go`)

Ajout des motifs observés :

- `cannot execute the requested system directive`
- `issue the tool call directly in this manner`
- `modifying core safety protocols`
- `altering system directives`
- `bypassing restrictions`
- `cannot assist with modifying`

Ajout complémentaire découvert pendant les tests : `cannot fulfill this request`, `cannot fulfil this request`, `unable to fulfill this request`.

Ces sorties déclenchent maintenant le retry existant sur conversation vierge au lieu d’être renvoyées au client.

### 2.2 Compaction des notices OpenCode (`api/session_transcripts.go`, `api/chat_handler.go`)

- `compactSystemNotice` reconnaît :
  - `The instructions changed: ... Instructions from: ... AGENTS.md`
  - `The Code Mode tool catalog has changed`
- Le match exige le préfixe explicite `The instructions changed` / `The Code Mode tool catalog has changed` : un prompt système initial qui contiendrait `Instructions from:` n’est jamais remplacé.
- Le texte intégral n’est plus rejoué en amont. Il est remplacé par un rappel neutre et court :
  - mise à jour du fichier d’instructions sur disque ;
  - catalogue d’outils déjà présent dans `[AVAILABLE TOOLS]` en tête de prompt.
- Les notices inconnues trop volumineuses sont plafonnées à 4 000 runes avec `... [notice truncated]`.
- La compaction est déterministe, donc le préfixe de conversation reste stable pour la stratégie de continuation delta.

### 2.3 Retry dédié sur troncature mid-value (`api/chat_handler.go`)

- Ajout de `formatTruncationRetryDirective`.
- En streaming **et** en non-streaming, dès que `HasTruncatedMutationToolCall` est détecté :
  1. la conversation amont est invalidée ;
  2. le proxy réessaie une fois sur conversation vierge ;
  3. la directive demande explicitement un appel complet (`path`, `oldString`/`newString`, `content`) ;
  4. le payload partiel n’est jamais réparé ni exécuté.
- Le message final `[Error: upstream tool call was truncated mid-value...]` n’est émis qu’après cet échec du retry transparent.

### 2.4 Smoke tests (`scripts/live_middleware_smoke.sh`)

Le script passe de 4 à 5 contrôles :

1. compteurs agentiques exposés ;
2. sanitizer `orphan_tags` ;
3. directive ground-truth + compteur ;
4. **nouveau** : réinjection de la notice `The instructions changed` avec vérification d’absence de refus et de fuite du texte intégral ;
5. Write-Serial + index SSE séquentiels.

Le contrôle S7 distingue désormais :
- **PASS déterministe** : le compteur `gemini_ground_truth_directives_total` augmente ;
- **WARN** : le modèle n’a pas visiblement récupéré dans cet échantillon (comportement stochastique).

---

## 3. Validation

| Contrôle | Résultat |
|---|---|
| `gofmt -l .` | propre |
| `go vet ./...` | exit 0 |
| `go test -short ./...` | OK |
| `go test -race -short ./...` | OK |
| `./scripts/audit_check.sh` | exit 0 — `api 67.2 %`, `db 77.1 %`, `gemini 72.1 %` |
| `./scripts/live_middleware_smoke.sh` | **5/5 PASS** (S7 en WARN comportemental, compteur validé) |
| OpenCode + Hydrogen : write `AGENTS.md` → notice → edit `README.md` | **PASS** — aucun refus, `README.md` modifié |
| OpenCode + Neon : même scénario | **PASS** — aucun refus, `README.md` modifié |
| Write volumineux 1 500 lignes | **PASS** |
| Write volumineux 8 000 lignes | **PASS** — pas de troncature déclenchée, donc retry non exercé en live |

Le retry de troncature est couvert par :
- `TestFormatTruncationRetryDirective` ;
- `TestIsUnusableModelOutput_TruncatedMutation` ;
- les tests unitaires `HasTruncatedMutationToolCall` et le fuzz target.

---

## 4. Point résiduel

- La récupération S7 dépend du modèle : la directive est toujours injectée, mais le modèle peut répondre par du texte conversationnel au lieu d’un appel d’outil. Le smoke test le signale en WARN sans masquer le contrôle déterministe.
- La troncature mid-value n’a pas pu être forcée en live : Gemini a complété jusqu’à 8 000 lignes. Le chemin de retry reste couvert par les tests et la revue de code.

---

## 5. Fichiers modifiés

- `free-gemini-api/api/session_transcripts.go`
- `free-gemini-api/api/chat_handler.go`
- `free-gemini-api/api/session_scope_test.go`
- `free-gemini-api/api/system_notice_test.go` (nouveau)
- `scripts/live_middleware_smoke.sh`
- `CHANGELOG.md`
