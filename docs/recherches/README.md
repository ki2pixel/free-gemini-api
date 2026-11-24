# 🔬 Recherches — Index des travaux théoriques et audits comparatifs

Ce dossier contient les analyses qui **fondent** les décisions d'architecture du
proxy. Contrairement aux rapports de [remediation/](../remediation/README.md),
qui documentent ce qui a été livré, ces documents expliquent **pourquoi** ces
choix ont été retenus et ce qui a été écarté.

---

## 📄 Les quatre documents

### 1. Arbitrage du benchmark en vol v3.5.7

[**Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md**](Arbitrage-benchmark-in-flight-v3.5.7-et-remediations.md)
— 24 novembre 2026, 820 lignes.

Confronte le rapport de benchmark comportemental `Mise-a-jour-et-Reorganisation-des-Documentations-v3.5.7`
au code réel, arbitre ses quatre recommandations et établit les remédiations
manquantes.

**Quatre décisions d'arbitrage :**

| Réf. | Recommandation | Verdict | Raison |
|---|---|---|---|
| **R1** | Ancrer les deltas courts/vides | **Acceptée, révisée** | La cause première est l'absence d'invariant d'identité, pas la taille du delta |
| **R2** | Ajouter une clause de permission illimitée | **Rejetée telle quelle** | Déjà présente depuis la v3.5.6, échec empirique, risque *Pink Elephant* |
| **R3** | Promouvoir les JSON de tool call fuités | **Rejetée pour les mutations** | La fuite observée est tronquée et sans `path` ; promouvoir écrirait un fichier incomplet |
| **R4** | Rendre `<thought` idempotent comme balise | **Rejetée comme cause ; sanitizer étroit accepté** | `<thought` sans `>` n'est pas une balise ; le vrai correctif Argon est la prose courte |

**Quatre modes d'échec analysés :**

| Modèle | Symptôme | Cause déterministe |
|---|---|---|
| `gemini-4-neon` | `Hello! How can I help you today?` accepté comme `stop` | Identité de conversation non vérifiée, greeting non classifié |
| `gemini-4-hydrogen` | JSON `write` de 938 octets fuité dans le texte, puis rapport S7 | Objet nu non taggé et tronqué en pleine valeur ; lexique S7 incomplet |
| `gemini-4-hydrogen-task` | 0 appel d'outil, synthèse invoquant un faux quota | Prior paramétrique de fin de budget ; Task n'est pas un mode d'exécution d'outils fiable |
| `gemini-4-argon` | Texte visible de 21 caractères, `finish_reason: "stop"` | Flux amont coupé après le bloc de pensée ; prose courte non classifiée |

**Limite honnête déclarée :** la base `messages` ne conserve que le dernier
message `user` et la sortie finale, pas le prompt delta exact envoyé à Google.
Les conclusions sur le delta reposent sur le code et un test direct de
`planSessionTurn`, pas sur une capture réseau du corps `f.req`.

→ Donne lieu à la vague documentée dans
[remediation v3.5.7](../remediation/remediation_agentic_gemini4_v3.5.7.md).

---

### 2. Audit comparatif opencode-antigravity-auth 2.1.2 → Gemini 4

[**audit-opencode-antigravity-auth-gemini-fiabilisation.md**](audit-opencode-antigravity-auth-gemini-fiabilisation.md)
— 23 novembre 2026, 1 132 lignes.

Audit de transposition des mécanismes de résilience du plugin TypeScript
`opencode-antigravity-auth` 2.1.2 (lecture seule) vers le proxy Go `goapi`,
version de référence 3.5.6, commit `54a9a9a4`.

Chaque plage de code citée a été relue pendant la session d'audit ; les rares
plages non relues sont explicitement marquées `non vérifié`. Le document
distingue les mécanismes **transposables** des mécanismes **non transposables**
(ceux qui dépendent du runtime JavaScript ou d'un accès direct au navigateur) et
identifie les angles morts résiduels du proxy Go.

---

### 3. Brief d'audit

[**BRIEF-audit-opencode-antigravity-auth-gemini-fiabilisation.md**](BRIEF-audit-opencode-antigravity-auth-gemini-fiabilisation.md)
— 23 novembre 2026, 331 lignes.

Le cahier des charges qui a produit le rapport n° 2. Il fixe le périmètre, les
ancrages vérifiés, les verdicts déjà établis côté Go et les critères
d'acceptation du livrable. Il précise également la contrainte projet : le
rapport est déposé dans `docs/recherches/` et **laissé dans le working tree**,
sans commit.

À lire si vous voulez comprendre la méthode d'audit avant les conclusions.

---

### 4. Prompt-engineering vs middleware agentique

[**Prompt-Engineering-vs-Middleware-Agentique.md**](Prompt-Engineering-vs-Middleware-Agentique.md)
— 160 lignes.

Le cadre théorique du projet. Thèse centrale : un LLM est un composant
probabiliste (intention, planification, synthèse) exécuté à l'intérieur d'un
système déterministe. Le prompt-engineering agit sur la **réduction de variance
en entrée** ; il ne peut jamais garantir un invariant sur des trajectoires
longues où les erreurs se composent de façon multiplicative.

**Quatre strates de responsabilité :**

1. Le **client agentique** (OpenCode / SDK) qui consomme un contrat OpenAI SSE strict.
2. Le **middleware proxy déterministe** (free-gemini-api) qui garantit par code
   le contrôle du contexte, la validation JSON Schema, la réparation
   structurelle et les coupe-circuits.
3. La **couche probabiliste d'inférence** (guidage sémantique Gemini 4).
4. La **barrière externe asynchrone** des classifieurs de sécurité amont, hors
   du périmètre causal du prompt.

Le document identifie **six angles morts** durables dans l'arranger : décalage
d'offsets UTF-8 sur le texte français, risque de corruption silencieuse par
auto-fermeture de JSON tronqué en milieu de chaîne, absence de validation contre
`req.Tools`, omission du champ `index` dans les deltas SSE, et deux autres.
Plusieurs ont été corrigés dans les vagues ultérieures ; le document reste la
référence pour comprendre l'origine du durcissement.

---

## 🧭 Par où commencer

| Votre intention | Document |
|---|---|
| Comprendre pourquoi le proxy intercepte autant de sorties | n° 4 — cadre théorique |
| Comprendre un échec précis observé en benchmark | n° 1 — arbitrage |
| Savoir ce qui a été transposé depuis un plugin JS | n° 2 — audit comparatif |
| Reproduire la méthode d'audit | n° 3 — brief |

## 🔗 Voir aussi

- [docs/README.md](../README.md) — hub général
- [docs/architecture/](../architecture/) — spécifications vivantes du système
- [docs/remediation/README.md](../remediation/README.md) — ce qui a été livré
- [audit-2026-11/](../../audit-2026-11/README.md) — audit de sécurité et scorecard
