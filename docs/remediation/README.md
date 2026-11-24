# 🩹 Remédiation — Index chronologique

Archives des vagues de durcissement du proxy **Free Gemini API**, de la
première passe de challenge (v3.4.8) à la clôture du benchmark « en vol »
(v3.5.7). Chaque rapport suit la même structure : constats issus des transcripts
→ cause racine → correctifs livrés → tests de non-régression → validation
exécutée.

Le [CHANGELOG.md](../../CHANGELOG.md) reste la source sémantique officielle par
version ; ces rapports contiennent l'analyse causale et les preuves.

---

## 📅 Chronologie

| Version | Date | Commit | Objet | Rapport |
|---|---|---|---|---|
| **v3.4.8** | 2026-11-23 | — | Challenge & validation du plan initial : cause racine confirmée, Write-Serial renforcé, `oldString` vide rejeté, fuite de balises bloquée dans le texte visible, biais S7 corrigé, panic et faux refus découverts en live | [v3.4.8](remediation_agentic_gemini4_v3.4.8.md) |
| **v3.4.9** | 2026-11-23 | — | Arrêts prématurés après réinjection de la notice `The instructions changed: … AGENTS.md` : classifieur de refus, compaction des notices OpenCode, retry dédié sur troncature mid-value, premier smoke test live | [v3.4.9](remediation_agentic_gemini4_v3.4.9.md) |
| **v3.5.0** | 2026-11-23 | — | Gel de 14 min (Hydrogen, tour 21), faux refus de sécurité global (Hydrogen-Task), rapport de complétion prématuré (Neon) : masquage d'observations, budget transport, `stream_guard`, directive anti-pseudo-rapport | [v3.5.0](remediation_agentic_gemini4_v3.5.0.md) |
| **v3.5.1** | 2026-11-23 | — | Le retry 1097 consommait le budget fonctionnel ; `<tool_call>` en prose neutralisait le classifieur de refus ; refus français non couverts ; bilan intermédiaire accepté comme fin de tour | [v3.5.1](remediation_agentic_gemini4_v3.5.1.md) |
| **v3.5.2** | 2026-11-23 | — | Le diff d'`AGENTS.md` rejoué déclenchait un refus en boucle ; refus d'extension Google Workspace restitué comme texte normal ; incident de transport épuisant le budget fonctionnel | [v3.5.2](remediation_agentic_gemini4_v3.5.2.md) |
| **v3.5.3** | 2026-11-23 | — | Une notice système OpenCode traitée comme prompt utilisateur ; bilans en prose hors marqueurs ; citation JSON de la synthèse validée à tort comme appel d'outil | [v3.5.3](remediation_agentic_gemini4_v3.5.3.md) |
| **v3.5.4** | 2026-11-23 | — | Pensée coupée dès le premier message (Hydrogen), raisonnement streamé puis fuite textuelle (Hydrogen-Task), pensée imbriquée après édition (Neon) : extracteur unifié `extractThoughtsAndContent`, détection pensée-seule | [v3.5.4](remediation_agentic_gemini4_v3.5.4.md) |
| **v3.5.5** | 2026-11-23 | — | Challenge 8 : faux bilan dès le premier message (Neon), modifications décrites sans être appliquées (Hydrogen), plan déclaré appliqué après un seul `edit` (Hydrogen-Task) | [v3.5.5](remediation_agentic_gemini4_v3.5.5.md) |
| **v3.5.6** | 2026-11-23 | — | Challenge 9 : erreur amont *canned* restituée comme réponse finale, `finish_reason: "stop"` sur un chunk d'erreur, pseudo-rapport non couvert, hallucination « 0 remaining » induite par le contexte | [v3.5.6](remediation_agentic_gemini4_v3.5.6.md) |
| **v3.5.7** | 2026-11-24 | `e5d3629` (P0), `4bf3f6f` (P1), `95245fd` (P2) | **Clôture du benchmark en vol** : continuité de session stricte, ancre `[Continuation]`, quarantaine des JSON non taggés, `SessionLedger` anti-S7, prose tronquée, routage Task→Chat, métriques par classe, budget LRU 64 Mio, sanitizer `<thought`, tests de propriété et fuzzing | [v3.5.7](remediation_agentic_gemini4_v3.5.7.md) |

---

## 🧭 Lire un rapport sans lire les dix

Chaque vague répond à un mode de défaillance précis. Si vous cherchez une
réponse ciblée :

| Question | Rapport |
|---|---|
| Pourquoi les appels d'outils perdent-ils leurs résultats ? | [v3.4.8](remediation_agentic_gemini4_v3.4.8.md) — sérialisation de `ToolCallID`, réconciliation des orphelins |
| Pourquoi un fichier `AGENTS.md` modifié provoque-t-il un refus ? | [v3.4.9](remediation_agentic_gemini4_v3.4.9.md), [v3.5.2](remediation_agentic_gemini4_v3.5.2.md), [v3.5.3](remediation_agentic_gemini4_v3.5.3.md) |
| Pourquoi le proxy reste-t-il bloqué 14 minutes ? | [v3.5.0](remediation_agentic_gemini4_v3.5.0.md), [v3.5.1](remediation_agentic_gemini4_v3.5.1.md) — `stream_guard`, budget 1097 |
| Pourquoi la trace de raisonnement disparaît-elle ? | [v3.5.4](remediation_agentic_gemini4_v3.5.4.md) — `extractThoughtsAndContent` |
| Pourquoi le modèle annonce-t-il un faux bilan ? | [v3.5.5](remediation_agentic_gemini4_v3.5.5.md), [v3.5.6](remediation_agentic_gemini4_v3.5.6.md), [v3.5.7](remediation_agentic_gemini4_v3.5.7.md) |
| Pourquoi un JSON d'outil apparaît-il dans le texte visible ? | [v3.5.6](remediation_agentic_gemini4_v3.5.6.md), [v3.5.7](remediation_agentic_gemini4_v3.5.7.md) — quarantaine |
| Pourquoi une requête Task avec outils n'exécute-t-elle aucun outil ? | [v3.5.7](remediation_agentic_gemini4_v3.5.7.md) — routage Task `[40]` → Chat `[0]` |
| Pourquoi le client reçoit-il `finish_reason: "stop"` sur une réponse vide ? | [v3.5.6](remediation_agentic_gemini4_v3.5.6.md), [v3.5.7](remediation_agentic_gemini4_v3.5.7.md) — `newStreamErrorChunk` |

---

## 📓 Journal de bord des vagues 1–3

[walkthrough_fiabilisation_v3.5.7.md](walkthrough_fiabilisation_v3.5.7.md)
couvre les vagues **1 (P0)**, **2 (P1)** et **3 (P2)** de l'audit 2026-11 :
correctifs **A-01 → A-16**, matrice d'alignement des alias Antigravity, tests de
non-régression et validation exécutée le 2026-11-23.

Ce journal est distinct des rapports de remédiation ci-dessus : il documente la
**fiabilisation du moteur** (schémas, transport, IDs, registre de modèles),
tandis que les rapports v3.4.8 → v3.5.7 documentent la **fiabilisation
comportementale** des modèles agentiques.

---

## 🔗 Traçabilité Git

Les rapports ont été déplacés dans ce dossier via `git mv` :
`git log --follow docs/remediation/<nom-du-fichier>` permet de retracer l'ensemble
des commits et modifications historiques de chaque document.
