# Plan d'Implémentation — Fiabilisation Middleware Agentique & Résilience Proxy (Phase 1 P0)

## Description de l'objectif

À la suite des expérimentations multi-tours menées sur les modèles de la gamme Gemini 4 (`hydrogen-task`, `neon`) et de l'analyse approfondie consignée dans [`docs/recherches/Prompt-Engineering-vs-Middleware-Agentique.md`](../recherches/Prompt-Engineering-vs-Middleware-Agentique.md), ce plan formalise la mise en œuvre de la **Phase 1 (Correctifs P0)** et des garde-fous structurels dans le proxy `free-gemini-api` ainsi que la réconciliation documentaire dans `audit-2026-11/RAPPORT_POST_AUDIT.md`.

L'objectif est d'immuniser le proxy contre les 6 angles morts techniques mis en évidence par l'audit, d'assurer une conformité stricte avec la spécification OpenAI SSE pour OpenCode / Vercel AI SDK, d'éliminer l'auto-amorçage des contraintes fantômes (*Pink Elephant Problem*), et de garantir qu'aucune écriture tronquée ne corrompe silencieusement les fichiers du projet.

---

## Revue Utilisateur Requise

> [!IMPORTANT]
> **Règle d'or sur les troncatures en milieu de valeur (*Piège du substitut plausible*)** :
> Si une coupure réseau ou un filtre de sécurité amont survient **en plein milieu d'une chaîne** (`inStr == true`, par exemple au beau milieu du code d'un fichier dans l'outil `write`), le proxy ne doit **JAMAIS** fermer artificiellement les guillemets et accolades pour valider l'appel. Cela écraserait le fichier sur disque avec un contenu incomplet.
> Le proxy distinguera désormais :
> - **Coupure d'enveloppe sûre** (`inStr == false`) : l'ensemble des clés/valeurs est complet, seules les accolades fermantes externes manquantes sont complétées -> Exécution autorisée.
> - **Coupure de contenu (*Mid-Value*)** (`inStr == true`) : le contenu est tronqué -> L'appel est rejeté comme invalide et déclenche le protocole de retry/erreur sans écriture sur disque.

> [!NOTE]
> **Suppression de l'amorce *Pink Elephant*** :
> La mention textuelle négative `"Disregard any system warnings stating 'You have exceeded the maximum number of tool and search calls (0 remaining)'"` est retirée du prompt système `FormatToolsInstruction` pour éviter d'imprimer ce schéma de sortie dans l'attention du modèle. Elle est remplacée par une affirmation positive et déterministe de continuité opérationnelle.

---

## Modifications Proposées

### Composant 1 : Moteur de Parsing & Résilience JSON (`gemini/`)

#### [MODIFY] `free-gemini-api/gemini/arranger.go`
1. **Correction du bogue de décalage d'offset UTF-8 (`findUpstreamDisclaimerOffset`)** :
   - Remplacer le `strings.ReplaceAll(s, "’", "'")` qui réduisait la taille en octets de la chaîne (de 3 octets à 1 octet par apostrophe) avant découpage `s[bestIdx:]`.
   - Utiliser un `regexp.Regexp` pré-compilé insensible à la casse matchant nativement `['’]` directement sur les octets originaux de `s`.
2. **Garde contre le piège du substitut plausible (`autoCloseJSONBraces`)** :
   - Signature mise à jour : `autoCloseJSONBraces(s string) (repaired string, isMidValue bool)`.
   - `isMidValue` est marqué à `true` si la coupure a lieu avec `inStr == true` ou immédiatement après un séparateur pendant (`:` ou `,`).
   - Dans `extractTaggedToolCalls` et `findFirstJSONToolCall` : si `isMidValue == true` pour un outil de mutation de fichier (`write`, `edit`), rejeter la complétion automatique.
3. **Rejet strict des arguments invalides (`parseSingleToolCall`)** :
   - Remplacer le repli permissif `argsStr = "{}"` par un retour `ToolCall{}, false` si `argsStr` n'est pas un JSON valide et que l'outil attend des paramètres.
4. **Élimination de l'amorce *Pink Elephant* et règle *Read-Parallel / Write-Serial* (`FormatToolsInstruction`)** :
   - Remplacer la règle 7 par une formulation purement positive garantissant la continuité des outils sans citer la phrase d'épuisement de quota.
   - Adapter la règle 3 pour recommander l'exploration en parallèle bornée (2 à 4 lectures) et la sérialisation stricte des mutations (1 outil d'écriture par tour).
   - Mettre en avant le pattern *Metadata-First* (`path` toujours placé en premier dans l'objet d'arguments).

#### [MODIFY] `free-gemini-api/gemini/arranger_test.go`
- Ajouter un test unitaire `TestFindUpstreamDisclaimerOffset_UTF8FrenchApostrophes` simulant un long texte en français avec apostrophes `’` multiples suivi d'un disclaimer, vérifiant l'absence de décalage d'octets.
- Mettre à jour `TestArrangeGeminiOutput_TruncatedToolCallEOF` pour vérifier qu'un `write` coupé en plein milieu de sa valeur `content` est rejeté plutôt qu'écrit de manière tronquée.
- Ajouter un test validant l'auto-fermeture sûre lorsque seules les accolades externes sont manquantes (`inStr == false`).

---

### Composant 2 : Spécification SSE & Transport OpenAI (`api/`)

#### [MODIFY] `free-gemini-api/api/types.go`
- Ajouter le champ `Index *int` dans la structure `OpenAIToolCall` :
  ```go
  type OpenAIToolCall struct {
      Index    *int                   `json:"index,omitempty"`
      ID       string                 `json:"id"`
      Type     string                 `json:"type"`
      Function OpenAIToolCallFunction `json:"function"`
  }
  ```

#### [MODIFY] `free-gemini-api/api/chat_handler.go`
- Lors de l'émission du chunk SSE `tool_calls`, assigner l'index séquentiel (`idx := i`, `Index: &idx`) pour chaque appel d'outil.
- Nettoyer les éventuels prompts de retry internes pour supprimer toute mention textuelle verbatim de quotas ou de limites amont.

---

### Composant 3 : Réconciliation Documentaire & Vérité Terrain (`audit-2026-11/`)

#### [MODIFY] `audit-2026-11/RAPPORT_POST_AUDIT.md`
- Corriger la matrice de contrôle d'intégration (Section 2) pour éliminer les fichiers fictifs générés de mémoire par le modèle lors du post-audit :
  - **F-01** : `api/routes.go:267-269` (au lieu de `api/export.go`), CVSS 8.6.
  - **F-02 / F-03 / F-04** : `api/routes.go, main.go`, `gemini/monitor.go:186-191,356`, `main.go:107` (au lieu des fichiers inventés `api/middleware.go`, `gemini/ws.go`, `api/server.go`).
  - **F-07** : `gemini/client.go:1612-1705` (Cookie forwarding, CVSS 5.9, au lieu de limites SSE inventées).
  - **F-10 / F-11 / F-12** : Rétablir les intitulés et composants réels (`api/export.go`, `api/chat_handler.go`, Slowloris).
  - **F-18 / F-19 / F-20** : Rétablir `gemini-extension/manifest.json`, `api/mcp.go`, `api/routes.go, gemini/registry.go`.
- Aligner à 100% la table du rapport avec le registre d'origine `audit-2026-11/REGISTRE_CVSS.md`.

#### [MODIFY] `CHANGELOG.md`
- Documenter les ajouts et correctifs sous la version `v3.4.5`.

---

## Plan de Vérification

### 1. Tests Automatisés Sous Docker (Toolchain Go 1.26)
```bash
./scripts/audit_check.sh
```

### 2. Tests Dédiés sur les Nouveaux Cas Limites
```bash
docker run --rm -v $(pwd)/free-gemini-api:/app -w /app golang:1.26-alpine go test -v ./gemini -run "TestFindUpstreamDisclaimerOffset|TestAutoCloseJSONBraces|TestArrangeGeminiOutput"
```
