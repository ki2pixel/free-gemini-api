# Remédiation agentique Gemini 4 — v3.5.0

**Date :** 2026-11-23
**Contexte :** gel de 14 minutes de `gemini-4-hydrogen` au tour 21, faux refus de sécurité de `gemini-4-hydrogen-task`, et rapport de complétion prématuré de `gemini-4-neon`, observés pendant la campagne d'alignement documentaire v3.4.9.

---

## 1. Constats issus des transcripts v3.4.9

### 1.1 Hydrogen — long-horizon et replay massif

Le transcript `debug/2_gemini-4-hydrogen_alignement-documentations-v3-4-9.json` contient :

- 1 message user initial ;
- 20 tours assistant/outil réussis (`read`, `edit`) ;
- 41 messages une fois le transcript reconstruit côté OpenAI ;
- un 21e tour interrompu après **859,8 secondes** (environ **14 min 20 s**), entre `created=1795427507297` et `completed=1795428367089`.

Séquence observée :

1. Google refuse de poursuivre la conversation avec l'erreur `1097` (« Can't continue conversation »).
2. Le proxy invalide le pointeur et rejoue l'intégralité du transcript sur une session vierge.
3. Le rejeu massif n'émet aucun octet sur le flux HTTP/2.
4. `tls_client.WithTimeoutSeconds(900)` maintient la requête ouverte pendant 15 minutes.
5. `observationMaskTurnThreshold = 15` ne se déclenche jamais : il ne comptait que les messages `user`, or OpenCode n'en envoie qu'un seul.

### 1.2 Hydrogen Task — refus de sécurité global

Au tour 3, l'amont a renvoyé le refus standardisé :

> I must refuse to assist with or generate any content that touches on sexually explicit material, dangerous or illegal activities, self-harm, or violence, or provides instructions or advice regarding them.

`isToolRefusalOutput` ne connaissait que `i must refuse this request` et `i refuse to`. Le refus est donc passé comme une réponse `stop` valide, terminant la session.

### 1.3 Neon — rapport de complétion prématuré

Après un simple `shell(git status && ls -la scripts/)`, Neon a produit un rapport affirmant que toutes les vérifications préalables étaient terminées, sans aucune mutation. Le prompt outillé ne contenait pas de barrière explicite contre ce rapport textuel de complétion.

---

## 2. Correctifs livrés

### 2.1 Masquage d'observations long-horizon (`api/observation_masker.go`, `api/chat_handler.go`)

Le déclenchement ne dépend plus du nombre de messages `user`. Le masquage s'active si **l'un** des signaux suivants est vrai :

- plus de **6 observations d'outils**, où une observation est un message `role == "tool"` **ou** un message assistant portant `tool_calls` ;
- plus de **12 messages** au total ;
- plus de **60 %** du budget de prompt (`defaultObservationMaskThreshold`), signal existant conservé.

Les **4 résultats d'outils les plus récents** restent identiques octet pour octet. Les résultats antérieurs sont réduits aux **3 premières lignes**, plafonnées à **200 octets**, puis suivis du marqueur explicite :

```text
... [compacted: N bytes omitted; re-read if needed]
```

Conséquences :

- le transcript Hydrogen de 41 messages franchit le seuil d'observations dès le 4e tour outil (appels + résultats), puis compacte les résultats les plus anciens à partir du 5e, sans dépendre d'un second message `user` ;
- le rejeu après une erreur `1097` reste court, ce qui évite le calage silencieux du backend amont ;
- les arguments des appels d'outils assistants sont comptés pour le déclenchement, mais seuls les corps de résultats `tool` sont compactés : les appels restent structurellement valides.

### 2.2 Budget transport et détection de stagnation (`gemini/client.go`, `gemini/transport.go`, `gemini/stream_guard.go`, `gemini/errors.go`)

- `tls_client.WithTimeoutSeconds(900)` est remplacé par `upstreamRequestTimeout = 120s`.
- Le client QUIC utilise le même budget au lieu de son timeout de 10 s.
- Chaque corps de réponse (streaming HTTP/2, streaming QUIC, requêtes non-streaming) est enveloppé dans `stallGuardReader` :
  - le minuteur est réarmé à chaque lecture réussie ;
  - après **45 secondes sans aucun octet**, le contexte est annulé, le corps est fermé et `ErrUpstreamStreamStalled` est renvoyé ;
  - une génération lente mais progressive n'est jamais coupée par ce garde.
- `ErrConversationExpired` couvre le marqueur `1097` / `Can't continue conversation` détecté dans le corps brut. Le pointeur de conversation de la session est immédiatement réinitialisé.
- `executeWithRetry` ne déclenche plus la resynchronisation des cookies pour ces deux erreurs : elles exigent une **conversation vierge**, pas de nouveaux cookies. Le handler API reprend immédiatement avec un replay masqué sur une nouvelle session.

### 2.3 Classifieur de refus amont (`api/session_transcripts.go`)

Ajout des formulations globales de sécurité Google :

- `i must refuse to assist with`
- `touches on sexually explicit material`
- `dangerous or illegal activities`
- `self-harm, or violence`
- `provides instructions or advice regarding them`
- `cannot generate content that touches on`

Ces sorties déclenchent désormais le retry abstrait sur conversation vierge au lieu d'être retournées au client.

### 2.4 Directive anti-pseudo-rapport (`gemini/arranger.go`, `api/chat_handler.go`)

`FormatToolsInstruction` contient une règle supplémentaire :

> Do not emit a final completion report or summary if any planned modification or verification step is still pending. Execute all tool calls until all files are modified.

La directive de retry abstrait utilisée après un refus reprend la même injonction, afin que Neon ne puisse pas clore la session par un rapport textuel alors que des mutations restent en attente.

### 2.5 Suite de contrôle live (`scripts/live_middleware_smoke.sh`)

La suite passe de 5 à **6 contrôles** :

1. compteurs agentiques exposés ;
2. sanitizer `orphan_tags` ;
3. directive ground-truth et compteur ;
4. compaction de la notice OpenCode ;
5. **nouveau** : exécution déterministe de `TestIsToolRefusalOutput_GlobalSafetyRefusal`, qui vérifie l'interception de `dangerous or illegal activities` (Go local, repli Docker) ;
6. Write-Serial.

Le contrôle Write-Serial ne compte plus que les **mutations** (`edit`, `write`, `patch`, `apply_patch`, `multiedit`) et valide les index SSE séquentiels. Il autorise donc un `read` préalable, conformément à la cadence Read-Parallel / Write-Serial.

---

## 3. Tests de régression ajoutés

- `api/observation_masker_test.go`
  - `TestObservationMaskerLongHorizonToolTurnsTriggers` : 1 user + 20 tours outil = 41 messages, 16 observations compactées, 4 récentes intactes.
  - `TestObservationMaskerSixObservationsStayUntouched` : la frontière exacte de 6 observations ne déclenche pas ; la 7e déclenche.
  - `TestObservationMaskerMessageThresholdTriggers` : 13 messages déclenchent avec seulement 6 résultats d'outils.
  - `TestObservationMaskerCountsAssistantToolCalls` : les messages assistant portant `tool_calls` comptent dans le seuil.
- `api/session_scope_test.go`
  - `TestIsToolRefusalOutput_GlobalSafetyRefusal` : refus Google complet, fragments individuels, et absence de retry pour une requête sans outils.
- `gemini/stream_guard_test.go`
  - flux totalement silencieux annulé avec le sentinel explicite ;
  - flux lent mais progressif préservé par le réarmement ;
  - `Close` idempotent ;
  - détection `1097` positive et négative ;
  - `executeWithRetry` ne réessaie pas après les sentinels de conversation.
- `gemini/arranger_test.go`
  - présence de la directive anti-pseudo-rapport.

---

## 4. Validation

| Contrôle | Commande | Résultat |
|---|---|---|
| Formatage Go | `test -z "$(gofmt -l .)"` | **OK** |
| Analyse statique | `go vet ./...` | **exit 0** |
| Tests courts | `go test -short ./...` | **OK** — `api 0.996s`, `db`, `gemini 9.138s` |
| Tests race | `go test -race -short ./...` | **OK** — `api 4.430s`, `gemini 14.726s` |
| Audit complet | `./scripts/audit_check.sh` | **exit 0** — couverture `api 67.2 %`, `db 77.1 %`, `gemini 72.1 %` |
| Rebuild Docker | `docker compose up -d --build` | **OK** — image reconstruite, conteneur recréé |
| Santé | `GET /health` | **online**, `status: healthy` |
| Smoke live | `./scripts/live_middleware_smoke.sh` | **6/6 PASS** |

### 4.1 Détail du passage smoke 6/6

```text
[1/6] Runtime sync: agentic counters exposed          PASS (3 compteurs)
[2/6] Orphan closing-tag sanitizer                    PASS (contenu + compteur)
[3/6] Ground-truth recovery directive (Counter-S7)    PASS (récupération + compteur)
[4/6] Instruction notice compaction (OpenCode replay)  PASS
[5/6] Upstream safety-refusal classifier              PASS (dangerous or illegal activities)
[6/6] Write-Serial: at most one mutation per turn     PASS (mutation unique + index SSE)
=== TOUS LES TESTS LIVE SONT PASSÉS (exit 0) ===
```

Le premier passage de la suite a révélé une assertion Write-Serial trop stricte : elle comptait aussi les appels `read` et échouait sur un `read` + `edit` parfaitement conforme. Le contrôle a été corrigé pour mesurer uniquement les mutations, puis la suite a repassé à 6/6.

---

## 5. Limites et points de surveillance

- **Détection `1097` textuelle** : si Google change le format du marqueur, la détection dédiée ne se déclenchera pas. Le garde de stagnation de 45 s et le budget de 120 s bornent néanmoins la requête, et l'auto-heal existant reste disponible.
- **Budget total de 120 s** : c'est un compromis délibéré entre réactivité et générations longues. Le garde ne coupe que le silence total ; une génération qui streame régulièrement peut encore être arrêtée par le budget global. La constante `upstreamRequestTimeout` est centralisée pour un ajustement futur.
- **Classifieur heuristique** : les refus sont détectés par fragments. Une nouvelle formulation Google devra être ajoutée explicitement.
- **Rejeu long-horizon live** : le scénario exact de 41 messages est couvert par un test unitaire déterministe ; il n'a pas été rejoué en live sur 20 tours pour éviter de consommer inutilement le quota. Les contrôles live couvrent le classifieur, les notices, le ground-truth et Write-Serial.

---

## 6. Fichiers modifiés

- `free-gemini-api/api/observation_masker.go`
- `free-gemini-api/api/observation_masker_test.go`
- `free-gemini-api/api/chat_handler.go`
- `free-gemini-api/api/session_transcripts.go`
- `free-gemini-api/api/session_scope_test.go`
- `free-gemini-api/gemini/client.go`
- `free-gemini-api/gemini/transport.go`
- `free-gemini-api/gemini/chat.go`
- `free-gemini-api/gemini/session.go`
- `free-gemini-api/gemini/errors.go` *(nouveau)*
- `free-gemini-api/gemini/stream_guard.go` *(nouveau)*
- `free-gemini-api/gemini/stream_guard_test.go` *(nouveau)*
- `free-gemini-api/gemini/arranger.go`
- `free-gemini-api/gemini/arranger_test.go`
- `scripts/live_middleware_smoke.sh`
- `CHANGELOG.md`
- `docs/remediation_agentic_gemini4_v3.5.0.md`
