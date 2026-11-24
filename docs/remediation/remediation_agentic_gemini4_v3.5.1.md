# Remédiation agentique Gemini 4 — v3.5.1

**Date :** 2026-11-23
**Contexte :** quatrième passe de durcissement après le challenge d'alignement documentaire v3.5.0. Les transcripts sont archivés dans `debug/` pour `gemini-4-hydrogen`, `gemini-4-hydrogen-task` et `gemini-4-neon`.

---

## 1. Constats issus des transcripts v3.5.0

### 1.1 Le retry 1097 consomme le budget fonctionnel

Dans `api/chat_handler.go`, le chemin streaming accordait **2 tentatives** aux requêtes outillées. Lorsqu'une conversation expirée (`1097`) était détectée au début d'un tour, la tentative 0 était consommée par l'auto-guérison sur conversation vierge. La tentative 1 devenait alors la dernière occasion de génération : si le modèle produisait un refus, un pseudo-rapport ou une sortie inutilisable, `attempt+1 < maxAttempts` était faux et la réponse brute était renvoyée avec `finish_reason: "stop"`.

L'expiration `1097` est un **incident de transport**, pas un échec de génération. Elle ne doit pas réduire le nombre de tentatives dont dispose le modèle pour produire une sortie exploitable.

### 1.2 `<tool_call>` en prose neutralise le classifieur de refus

`debug/3_gemini-4-hydrogen_alignement-des-documentations-v3-5-0.json` contient une sortie qui commence par :

> I'm having a hard time fulfilling your request. Can I help you with something else instead?

puis rejoue le corps d'`AGENTS.md`, qui contient textuellement :

> ... in JSON `<tool_call>` arguments ...

L'ancien `isToolRefusalOutput` commençait par :

```go
if strings.Contains(text, "<tool_call>") || strings.Contains(text, "</tool_call>") {
    return false
}
```

La simple mention textuelle du tag, dans une phrase ou une documentation rejouée, suffisait donc à court-circuiter la détection. Le classifieur ne distinguait pas un vrai appel d'outil d'une citation.

### 1.3 Refus français non couvert

`debug/3_gemini-4-neon_plan-d-alignement-documentaire-v3-5-0.json` contient verbatim :

> Je ne peux pas répondre à cette demande.

Aucun motif existant ne couvrait cette formulation. La sortie pouvait donc être traitée comme une réponse `stop` valide.

### 1.4 Bilan intermédiaire accepté comme fin de tour

`debug/3_gemini-4-hydrogen-task_plan-d-alignement-v3-5-0.json` montre qu'après la mise à jour de `AGENTS.md` et `README.md`, le modèle a émis un bilan en prose sans appel d'outil :

> Le plan d'alignement et de synchronisation des documentations post-audit pour la version **v3.5.0** a été initié et les composants clés (`AGENTS.md`, `README.md`) ont été mis à jour avec succès...

Le proxy n'avait aucune règle lui permettant de reconnaître ce type de bilan comme un arrêt prématuré. Le tour était accepté avec `stop`, alors que des fichiers restaient à inspecter et à modifier.

---

## 2. Correctifs livrés

### 2.1 Budget de génération et dissociation du 1097 (`api/chat_handler.go`)

La constante de budget est centralisée dans `maxChatAttempts` :

| Type de requête | Streaming | Non-streaming |
|---|---:|---:|
| Avec outils | **3** | **3** |
| Sans outil | 1 | 2 |

Conséquences pour une requête outillée :

1. une expiration `1097` consomme la tentative 0 et force un replay complet sur conversation vierge ;
2. deux tentatives fonctionnelles restent disponibles pour un refus, un pseudo-rapport, une erreur de schéma ou une troncature ;
3. la tentative corrective finale peut donc encore réinjecter une directive et obtenir un appel d'outil.

Le chemin non-streaming a également été aligné : `ErrConversationExpired` et `ErrUpstreamStreamStalled` déclenchent désormais un replay sur conversation vierge au lieu de remonter immédiatement en erreur HTTP. Les autres erreurs (rate limit, cooldown, réseau) conservent leur comportement d'échec immédiat.

### 2.2 Garde anti-pseudo-rapport active (`api/session_transcripts.go`, `api/chat_handler.go`)

Ajout de `isPseudoProgressReport`, qui détecte les bilans purement textuels sur une requête outillée à partir des marqueurs observés :

- `a été initié`
- `ont été mis à jour avec succès`
- `rapport d'exécution`
- `composants clés`

La détection est conditionnée par l'absence d'appel d'outil réel :

```go
if gemini.ArrangeGeminiOutputWithTools(text, tools).IsToolCall {
    return false
}
```

Ainsi, un préambule ou un contenu d'outil qui cite un marqueur à côté d'un `<tool_call>` valide n'est pas reclassé.

`isUnusableModelOutput` traite désormais ces bilans comme inutilisables lorsque `len(tools) > 0`. Dans les deux boucles de retry, le motif est classé avant le refus générique et sélectionne la directive dédiée :

```text
[SYSTEM DIRECTIVE: Do not emit conversational progress updates or completion summaries.
You still have files to inspect and modify. Call the required read/edit tools directly.]
```

Le bilan n'est donc plus renvoyé avec `finish_reason: "stop"` tant qu'une tentative corrective reste disponible.

### 2.3 Classifieur de refus robuste aux mentions en prose (`api/session_transcripts.go`)

`isToolRefusalOutput` prend maintenant la liste des outils déclarés et ne considère qu'un appel réel :

```go
func isToolRefusalOutput(text string, tools []map[string]any) bool {
    if gemini.ArrangeGeminiOutputWithTools(text, tools).IsToolCall {
        return false
    }
    // ... analyse des motifs de refus ...
}
```

Un `<tool_call>` cité dans `AGENTS.md`, dans un bloc de documentation ou dans une phrase explicative ne masque plus un refus. Un appel qui survit à l'arranger et à la validation de schéma reste prioritaire et n'est jamais classé comme refus.

La formulation exacte de Hydrogen (`I'm having a hard time fulfilling your request`) a également été ajoutée aux fragments de refus explicites. Elle n'était auparavant reconnue que par la liste générique des sorties inutilisables, ce qui privait le chemin de refus de sa classification et de son compteur dédiés.

Un contrôle temporaire a été exécuté sur les trois transcripts v3.5.0 avant suppression du fichier de sonde :

```text
3_gemini-4-hydrogen_alignement-des-documentations-v3-5-0.json  refusal=true   pseudo=false  unusable=true
3_gemini-4-neon_plan-d-alignement-documentaire-v3-5-0.json      refusal=true   pseudo=false  unusable=true
3_gemini-4-hydrogen-task_plan-d-alignement-v3-5-0.json          refusal=false  pseudo=true   unusable=true
```

### 2.4 Refus français (`api/session_transcripts.go`)

Ajout de deux motifs :

- `je ne peux pas répondre`
- `ne peux pas répondre`

Ils couvrent la sortie exacte de Neon et la variante courte, sans modifier le comportement des requêtes sans outils (un refus en chat simple reste une réponse textuelle légitime).

---

## 3. Tests de régression ajoutés

Dans `api/session_scope_test.go` :

- `TestIsToolRefusalOutput_ProseToolCallMention`
  - un refus contenant `<tool_call>` sous forme de citation est détecté ;
  - le préfixe exact de Hydrogen (`I'm having a hard time fulfilling your request`) suivi d'une mention en prose du tag est détecté ;
  - un appel `read` réel, même entouré de prose de refus, n'est pas classé comme refus.
- `TestIsToolRefusalOutput_FrenchCannotAnswer`
  - couvre `Je ne peux pas répondre à cette demande.` ;
  - couvre le fragment court `ne peux pas répondre` ;
  - vérifie qu'une requête sans outils n'est pas réessayée.
- `TestIsPseudoProgressReport`
  - reprend le bilan exact de Hydrogen-Task ;
  - vérifie chaque marqueur individuellement ;
  - vérifie qu'une requête sans outils accepte le résumé ;
  - vérifie qu'un appel `write` valide contenant les marqueurs dans son payload reste utilisable.
- `TestFormatPseudoReportRetryDirective`
  - vérifie le contenu exact de la directive corrective.
- `TestMaxChatAttempts`
  - vérifie `3` tentatives pour les requêtes outillées en streaming et non-streaming ;
  - vérifie la conservation de `1` et `2` pour le chat simple.

Dans `api/more_test.go`, les appels aux classifieurs ont été mis à jour vers la signature `(text, tools)`.

---

## 4. Validation

| Contrôle | Commande | Résultat |
|---|---|---|
| Formatage Go | `test -z "$(gofmt -l .)"` | **OK** |
| Analyse statique | `go vet ./...` | **exit 0** |
| Tests courts | `go test -short ./...` | **OK** — `api 0.965s`, `db 0.017s`, `gemini 13.195s` |
| Tests race | `go test -race -short ./...` | **OK** — `api 4.478s`, `db 1.100s`, `gemini 18.219s` |
| Audit complet | `./scripts/audit_check.sh` | **exit 0** — couverture `api 66.5 %`, `db 77.1 %`, `gemini 72.1 %` |
| Rebuild Docker | `docker compose up -d --build` | **OK** — image reconstruite, conteneur recréé |
| Santé | `GET /health` | **online**, `status: healthy`, 1 worker extension actif |
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

---

## 5. Limites et points de surveillance

- **Budget fini** : une requête outillée dispose de 3 générations. Un premier `1097` en consomme une et laisse deux tentatives fonctionnelles ; deux `1097` consécutifs n'en laissent qu'une ; un troisième épuise le budget. Le garde de stagnation de 45 s et le budget transport de 120 s restent la première ligne de défense.
- **Marqueurs de pseudo-rapport** : la liste est volontairement spécifique et francophone, car elle est issue des transcripts v3.5.0. Une nouvelle formulation (autre langue, autre tournure) devra être ajoutée explicitement. Le risque assumé est de réessayer une fois un vrai bilan final contenant l'un de ces marqueurs ; c'est préférable à un arrêt silencieux au milieu d'une tâche.
- **Classifieur de refus** : la détection reste heuristique par fragments. La nouveauté est que la présence du tag `<tool_call>` n'est plus un critère suffisant ; seul un appel validé par l'arranger compte.
- **Chemin non-streaming** : les sentinelles de transport y sont désormais réessayées, mais les erreurs de rate limit ou de cooldown échouent toujours immédiatement, afin de ne pas amplifier la pression sur le pool.
- **Smoke live** : la suite conserve 6 contrôles. Les nouveaux comportements v3.5.1 sont couverts par des tests unitaires déterministes exécutés par `audit_check.sh` et par les commandes `go test`.

---

## 6. Fichiers modifiés

- `free-gemini-api/api/chat_handler.go`
- `free-gemini-api/api/session_transcripts.go`
- `free-gemini-api/api/session_scope_test.go`
- `free-gemini-api/api/more_test.go`
- `CHANGELOG.md`
- `docs/remediation_agentic_gemini4_v3.5.1.md`
