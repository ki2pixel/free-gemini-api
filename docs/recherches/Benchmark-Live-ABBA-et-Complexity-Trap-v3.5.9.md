# Campagnes live v3.5.9 — Évaluation `AB/BA` (`Chat [0]` vs `Task [40]`) et protocole *The Complexity Trap*

> **Date** : 25 novembre 2026  
> **Version évaluée** : `v3.5.9` (commits [`9494e42`](../../free-gemini-api/), [`72f124f`](../../scripts/), [`666d990`](../../CHANGELOG.md), [`116bdf5`](../../scripts/benchmark_matrix.sh))  
> **Sources primaires archivées** : base SQLite `free-gemini-api/data/gemini.db` (`benchmark_trials`, `agent_sessions`, `agent_turns`, `tool_call_spans`, `turn_anomalies`) et 7 exports `results.tsv` sous `debug/` (non suivis par Git). Ce document consolide l'intégralité des mesures et analyses afin de rester autoportant sans réinspection de `debug/`.

---

## Résumé exécutif

Les campagnes expérimentales prévues par le plan final **`v3.5.9`** ont été exécutées sur quota Google réel à travers OpenCode (`--standalone`, en-têtes `X-Benchmark-Trial-ID` et `X-Allow-Task-Mode-For-Tools: true` sur `-task`) et le harnais [`scripts/benchmark_matrix.sh`](../../scripts/benchmark_matrix.sh) adossé aux oracles déterministes `verify.sh` de [`scripts/benchmark_tasks/`](../../scripts/benchmark_tasks) :

1. **Phase 3 — Suite stratifiée de 6 tâches (`S1`–`S3`, 60 essais valides)** :
   - Sur les **24 paires `AB/BA` complètes** (**Hydrogen** et **Neon**), le mode **Task `[40]`** avec opt-in explicite surpasse le mode **Chat `[0]`** de **+0.250** (`23/24 = 0.958` contre `17/24 = 0.708`, erreur-type par grappe `SE = 0.090`, IC 95 % `[0.073 ; 0.427]`). Le gain est statistiquement net sur Hydrogen (`+0.333`, `8/12 → 12/12`, `pass^2 : 0.424 → 1.000`) et positif mais à IC incluant zéro sur Neon (`+0.167`, `9/12 → 11/12`, `pass^2 : 0.545 → 0.833`).
   - **`gemini-4-argon`** (`MinTier: free`, sans équivalent `-task` dans [`registry.go`](../../free-gemini-api/gemini/registry.go#L384-L437)) a été évalué en mode `sequential` Chat `[0]` sur les mêmes 6 tâches (`8/12 = 0.667`, `pass^2 = 0.424`, `8/9 = 0.889` hors couche A), au même niveau brut qu'Hydrogen Chat (`8/12`) et juste derrière Neon Chat (`9/12`).
   - **Zéro occurrence de `E_FALSE_QUOTA` et `E_GREETING_LOOP`** sur les **78 essais valides** (60 Phase 3 + 18 Phase 4), confirmant comportementalement les invariants **I2** et **I6**.
2. **Phase 4 — Protocole *The Complexity Trap* sur `s2_long_horizon_chain` (`keep=4` vs `keep=8`, 18 essais en palier `high`)** :
   - Les deux prérequis de validité sont vérifiés en base : **`18/18` essais** ont effectivement `thinking_tier = 'high'`, et le masquage s'est déclenché sur les deux bras (`411 256` octets économisés en `keep=4` sur `71` tours masqués contre `142 171` octets en `keep=8` sur `40` tours).
   - En cumulé sur les 3 familles (**`Hydrogen-high`**, **`Neon-high`**, **`Argon-high`**), la fenêtre étendue *thinking-aware* `keep=8` obtient **`6/9` (`0.667`, `pass^2 = 0.417`)** contre **`4/9` (`0.444`, `pass^2 = 0.167`)** en `keep=4`, tout en réduisant le *Cost-of-Pass* moyen de **364 108.5** à **171 949.2** tokens/pass (`3 335.0 → 1 098.3` output tokens/pass) et les erreurs d'outils de `8` à `4`.
   - **Limite honnête** : à `n = 3` par modèle en bras séquentiels, l'effet reste **dépendant de la famille** (`Hydrogen-high` `0/3 → 3/3`, `Neon-high` stable `2/3 → 2/3`, `Argon-high` `2/3 → 1/3` dont 2 arrêts précoces à `0` outil avant tout masquage) et n'est donc pas statistiquement conclusif pris isolément.

---

## 1. Périmètre, protocole et traçabilité des jeux de données

### 1.1 Conditions expérimentales

- **Pool et workers** : proxy Docker `free-gemini-api` (`127.0.0.1:8100`) avec 2 workers actifs en pool (`ultra` et `free`) et l'extension Chrome synchronisée (`active_extension_workers = 1`).
- **Configuration OpenCode** : `~/.config/opencode/opencode.json` complété avant le second tir Neon pour exposer `gemini-4-neon-task` et les alias de réflexion (`-high`/`-medium`/`-low`) ; `gemini-4-argon` et `gemini-4-argon-high` étaient déjà déclarés (`tools: true`, contexte `1 000 000`, sortie `64 000`).
- **Isolation et corrélation** : chaque essai s'exécute dans un worktree Git jetable créé depuis un snapshot + commit baseline après `setup.sh`, avec `--standalone` et `X-Benchmark-Trial-ID`. Les verdicts `POST /v1/benchmark/trials` attendent le drain asynchrone (`WaitTelemetryDrain`) puis raffinent les codes grossiers (`E_ORACLE_ASSERTION_FAIL`, `E_NO_TOOL_CALL`, `E_ERROR_FINISH`, `none`) depuis la session corrélée (`tools_declared_count > 0` prioritaire).

### 1.2 Inventaire des suites enregistrées et règles d'exclusion

| Suite ID (`benchmark_trials`) | Modèles | Mode | Essais | Statut dans l'analyse | Chemin TSV local (`debug/`, non suivi) |
|---|---|---|---:|---|---|
| `live-abba-v1` | `hydrogen`, `hydrogen-task` | `abba` | 24 | **Inclus** (`Hydrogen` Chat vs Task) | `debug/live_abba_v1/results.tsv` |
| `live-abba-v1` | `neon`, `neon-task` | `abba` | 24 | **Exclus du cumul** (`neon-task` bloqué à `0/12` sur `provider.no-route` côté OpenCode avant proxy, `with_session = 0`) | `debug/live_abba_v1/results.tsv` |
| `live-neon-abba-v1` | `neon`, `neon-task` | `abba` | 24 | **Inclus** (reprise propre complète `Neon` Chat vs Task) | `debug/live_neon_abba_v1/results.tsv` |
| `live-argon-v1` | `gemini-4-argon` | `sequential` | 12 | **Inclus** (`Argon` Chat `[0]`, pas de modèle `-task` dans `registry.go`) | `debug/live_argon_v1/results.tsv` |
| `ctrap-keep4` | `hydrogen-high`, `neon-high` | `sequential` (`keep=4`) | 6 | **Inclus** (Phase 4 bras A) | `debug/ctrap_keep4/results.tsv` |
| `ctrap-keep8` | `hydrogen-high`, `neon-high` | `sequential` (`keep=8`) | 6 | **Inclus** (Phase 4 bras B) | `debug/ctrap_keep8/results.tsv` |
| `ctrap-argon-keep4` | `argon-high` | `sequential` (`keep=4`) | 3 | **Inclus** (Phase 4 bras A — Argon) | `debug/ctrap_argon_keep4/results.tsv` |
| `ctrap-argon-keep8` | `argon-high` | `sequential` (`keep=8`) | 3 | **Inclus** (Phase 4 bras B — Argon) | `debug/ctrap_argon_keep8/results.tsv` |

> [!NOTE]
> **Pourquoi `couche A exclue = 0` dans le TSV mais > 0 en SQLite ?**  
> L'analyseur TSV hors-ligne classe les échecs d'oracle en `E_ORACLE_ASSERTION_FAIL` lorsque le transcript ne contient pas explicitement un greeting, un faux quota, un claim S7 ou une prose courte. Côté proxy, `UpsertBenchmarkTrial` remplace ensuite ce code grossier via `failureCodeFromCorrelation` (`StreamStalled || TransportRetries > 0` → `E_STALL_OR_TIMEOUT`, `TurnClass == 'refusal'` → `E_UPSTREAM_REFUSAL`, `ThoughtUnclosed` → `E_UNCLOSED_THOUGHT`). Toutes les statistiques conditionnelles ci-dessous utilisent les **codes raffinés SQLite** pour rester comparables entre familles.

> [!NOTE]
> **Pourquoi `gemini-4-argon` n'a-t-il pas de bras `AB/BA` ?**  
> Dans [`free-gemini-api/gemini/registry.go`](../../free-gemini-api/gemini/registry.go#L384-L437), seules les familles Hydrogen (`56fdd199312815e2`) et Neon (`e6fa609c3fa255c0`) disposent d'entrées `*-task` (`AppMode: 40`) validées par capture CDP ; `gemini-4-argon` (`95a4c0abcba24364`) n'en possède pas. L'exécution en mode `sequential` évite de synthétiser un en-tête `[40]` non vérifié.

---

## 2. Phase 3 — Évaluation 6 tâches (`S1`–`S3`) & Appariement `AB/BA`

### 2.1 Vue consolidée par modèle et paradigme (`n = 12`, `k = 2`)

Chaque bras exécute **2 runs sur chacune des 6 tâches** (`S1`: `s1_edge_case_guard`, `s1_unit_fix` ; `S2`: `s2_long_horizon_chain`, `s2_multi_file_wiring` ; `S3`: `s3_missing_prereq_abstention`, `s3_readonly_contract`). Les estimateurs de Yao et al. sont sans biais : `pass^1 = c/n`, `pass^2 = C(c,2)/C(n,2)`, `pass@2 = 1 - C(n-c,2)/C(n,2)`.

| Modèle / Bras | Suite | Passes | `pass^1` | `pass^2` | `pass@2` | Couche A strict (`hors A`) | `CoP` tok/pass | `CoP` out/pass | Tours obs. | Outils émis / spans | Err. outils | `rt` méd / moy / p95 (ms) | Wall moy (ms) | Anomalies | Masqué (o) |
|---|---|---:|---:|---:|---:|---|---:|---:|---:|---:|---:|---|---:|---:|---:|
| `gemini-4-hydrogen` (Chat `[0]`) | `live-abba-v1` | 8/12 | 0.667 | 0.424 | 0.909 | `2` (`8/10 = 0.800`) | 141 908.4 | 1 317.5 | 70 | 78 / 78 | 4 | 107.5 / 3 678.8 / 14 938 | 107 166 | 115 | 103 454 |
| `gemini-4-hydrogen-task` (Task `[40]`) | `live-abba-v1` | **12/12** | **1.000** | **1.000** | **1.000** | `0` (`12/12 = 1.000`) | 125 620.8 | 1 098.0 | 94 | 121 / 125 | 7 | 105.0 / 1 701.8 / 496 | 100 333 | 146 | 266 925 |
| `gemini-4-neon` (Chat `[0]`) | `live-neon-abba-v1` | 9/12 | 0.750 | 0.545 | 0.955 | `3` (`9/9 = 1.000`) | 126 128.8 | 1 256.6 | 73 | 95 / 98 | 5 | 109.0 / 2 308.6 / 391 | 119 056 | 130 | 104 419 |
| `gemini-4-neon-task` (Task `[40]`) | `live-neon-abba-v1` | **11/12** | **0.917** | **0.833** | **1.000** | `0` (`11/12 = 0.917`)* | **116 499.4** | 1 110.5 | 83 | 98 / 102 | 4 | 102.0 / 1 080.4 / 434 | 149 845 | 152 | 93 006 |
| `gemini-4-argon` (Chat `[0]`) | `live-argon-v1` | 8/12 | 0.667 | 0.424 | 0.909 | `3` (`8/9 = 0.889`) | 140 348.9 | 1 568.5 | 73 | 73 / 73 | 4 | 114.0 / 360.8 / 173 | 158 406 | 136 | 62 225 |

*\*`gemini-4-neon-task` compte 1 échec `E_UNCLOSED_THOUGHT` sur `s2_long_horizon_chain` run 2 : hors couche A strict (`E_STALL_OR_TIMEOUT`, `E_UPSTREAM_REFUSAL`, `E_INCOMPLETE_PROSE`) il vaut `11/12 = 0.917` ; en excluant également `E_UNCLOSED_THOUGHT`, il atteint `11/11 = 1.000`.*

**Corrélation SQLite** : sur l'ensemble des **60 essais valides**, **60/60** portent un `session_id` et `observed_turns > 0`, et **100 % des spans émis** ont un `roundtrip_ms` calculé (`465` spans émis : `78 + 117` pour Hydrogen, `92 + 98` pour Neon, `73` pour Argon — les `11` spans restants étant des rejets/quarantaines avant exécution).

### 2.2 Différence appariée `AB/BA` (`Task [40] − Chat [0]`)

| Paire | Paires complètes | Chat | Task | `Task > Chat` | `Task < Chat` | Égalité | Δ moyen (`Task − Chat`) | SE grappe | IC 95 % approx. |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| **Hydrogen** (`live-abba-v1`) | 12 | 8/12 (0.667) | 12/12 (1.000) | 4 | 0 | 8 | **+0.333** | 0.142 | `[0.055 ; 0.612]` |
| **Neon** (`live-neon-abba-v1`) | 12 | 9/12 (0.750) | 11/12 (0.917) | 3 | 1 | 8 | **+0.167** | 0.112 | `[-0.054 ; 0.387]` |
| **Combiné (Hydrogen + Neon)** | **24** | **17/24 (0.708)** | **23/24 (0.958)** | **7** | **1** | **16** | **+0.250** | **0.090** | **`[0.073 ; 0.427]`** |

### 2.3 Matrice par tâche et par modèle (taux de succès sur `n = 2`)

| Tâche (Strate) | `hydrogen` (Chat) | `hydrogen-task` (Task) | `neon` (Chat) | `neon-task` (Task) | `argon` (Chat) |
|---|---:|---:|---:|---:|---:|
| `s1_edge_case_guard` (`S1`) | 1/2 | 2/2 | 1/2 | 2/2 | 0/2 |
| `s1_unit_fix` (`S1`) | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 |
| `s2_long_horizon_chain` (`S2`) | 1/2 | 2/2 | 1/2 | 1/2 | 1/2 |
| `s2_multi_file_wiring` (`S2`) | 0/2 | 2/2 | 1/2 | 2/2 | 1/2 |
| `s3_missing_prereq_abstention` (`S3`) | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 |
| `s3_readonly_contract` (`S3`) | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 |
| **Total** | **8/12** | **12/12** | **9/12** | **11/12** | **8/12** |

Observations majeures :
- **Strate `S3` (abstention & contrat lecture seule)** : **20/20** (`100 %`) sur les 5 bras sans exception — aucun modèle n'a modifié de fichier interdit ni fabriqué `missing_secret_policy.yaml`.
- **Strate `S2` (multi-fichiers et horizon long)** : c'est là que se concentre l'avantage du mode `Task [40]` (Hydrogen passe de `1/4` en Chat à `4/4` en Task ; Neon de `2/4` à `3/4`).
- **Argon sur `s1_edge_case_guard`** : les deux échecs (`0/2`) proviennent de `E_STALL_OR_TIMEOUT` (retries transport sur les runs 1 et 2), pas d'une erreur d'algorithme.

### 2.4 Dispositions de spans rejetés, escalades `thinking_tier` et anomalies (Phase 3)

- **Escalades automatiques `EscalateThinkingModel`** (3 occurrences en Phase 3 sur modèles de base sans `-high`) :
  - `live-abba-v1` / `gemini-4-hydrogen` / `s1_unit_fix` run 1 : tour 8 escaladé vers `gemini-4-hydrogen-thinking-medium` → `PASS` (`thinking_tier = 'medium'`).
  - `live-neon-abba-v1` / `gemini-4-neon` / `s1_unit_fix` run 1 : tour 6 escaladé vers `gemini-4-neon-thinking-low` → `PASS` (`thinking_tier = 'low'`).
  - `live-argon-v1` / `gemini-4-argon` / `s2_long_horizon_chain` run 1 : tour 2 escaladé vers `gemini-4-argon-thinking-low` après retries transport → `FAIL` (`E_STALL_OR_TIMEOUT`, `thinking_tier = 'low'`).
- **Spans interceptés avant exécution (`tool_call_spans.disposition != 'emitted'`)** : `11` interceptions en Phase 3 sans jamais atteindre l'hôte OpenCode : `9` `rejected_mid_value_truncation` (`3` `hydrogen-task`, `2` `neon`, `4` `neon-task`), `1` `rejected_schema_invalid` (`hydrogen-task`) et `1` `quarantined_untagged_mutation` (`neon`).
- **Ventilation des anomalies (`turn_anomalies`)** :

| Catégorie / code | `hydrogen` | `hydrogen-task` | `neon` | `neon-task` | `argon` | Total Phase 3 |
|---|---:|---:|---:|---:|---:|---:|
| `context_reset / forced_replay` | 70 | 94 | 73 | 83 | 73 | **393** |
| `arranger_repair / control_chars` | 13 | 20 | 17 | 18 | 15 | **83** |
| `refusal_retry / refusal` | 3 | 8 | 21 | 28 | 30 | **90** |
| `context_reset / pseudo_report` | 21 | 8 | 10 | 8 | 5 | **52** |
| `doom_loop / repeat` | 6 | 7 | 6 | 5 | 3 | **27** |
| `arranger_rejection / mid_value_truncation` | 0 | 2 | 1 | 2 | 0 | **5** |
| `ground_truth_directive / recent_tool_error` | 0 | 3 | 0 | 2 | 3 | **8** |
| `stream_stall / stall_45s` | 1 | 1 | 0 | 2 | 3 | **7** |
| `unbacked_completion / unbacked_completion` | 1 | 1 | 0 | 2 | 4 | **8** |
| `quarantine / untagged_mutation` | 0 | 1 | 2 | 2 | 0 | **5** |
| `arranger_rejection / schema_invalid` | 0 | 1 | 0 | 0 | 0 | **1** |
| **Total** | **115** | **146** | **130** | **152** | **136** | **679** |

---

## 3. Phase 4 — Protocole *The Complexity Trap* (`keep=4` vs `keep=8`)

Cible unique : **`s2_long_horizon_chain`** (lecture de 10 nœuds documentés `> 250` octets avant correction de 2 nœuds non contigus + validation `go vet`/`go test`), `n = 3` essais par bras et par modèle, `k = 2`.

### 3.1 Contrôle de validité préalable

- **Palier *thinking* actif** : **18/18 essais** ont bien `thinking_tier = 'high'` en base (`benchmark_trials` et `agent_sessions`).
- **Activation effective du masquage (`SUM(masked_obs_bytes_saved) > 0`)** : vérifiée sur les deux bras : **411 256 octets** économisés sur `71/90` tours en `keep=4`, contre **142 171 octets** sur `40/64` tours en `keep=8`. L'override `OBSERVATION_MASK_THINKING_KEEP_RECENT=4` était vérifié sur le conteneur avant chaque campagne `keep=4`, puis retiré (`8` implicite) avant `keep=8`.

### 3.2 Synthèse par modèle et agrégat 3 familles

| Modèle | Bras | Passes | `pass^1` | `pass^2` | `pass@2` | `CoP` tok/pass | `CoP` out/pass | Thought tok | Masqué (o) / tours | Tours obs. | Outils émis / spans | Err. outils | `rt` méd / moy / p95 (ms) | Wall moy (ms) | Anomalies |
|---|---|---:|---:|---:|---:|---:|---:|---:|---|---:|---:|---:|---|---:|---:|
| `gemini-4-hydrogen-high` | `keep=4` | 0/3 | 0.000 | 0.000 | 0.000 | n/a *(203 560 tot)* | n/a *(2 069)* | 1 244 | 23 215 / 7 | 13 | 24 / 24 | 1 | 103.5 / 103.7 / 124 | 69 786 | 26 |
| `gemini-4-hydrogen-high` | `keep=8` | **3/3** | **1.000** | **1.000** | **1.000** | **190 943.7** | **1 283.0** | 2 325 | 105 208 / 26 | 35 | 61 / 66 | 4 | 103.0 / 116.4 / 159 | 257 568 | 67 |
| `gemini-4-neon-high` | `keep=4` | 2/3 | 0.667 | 0.333 | 1.000 | 317 718.0 | 3 356.5 | 5 574 | 198 217 / 33 | 39 | 64 / 64 | 4 | 105.0 / 116.5 / 146 | 157 457 | 63 |
| `gemini-4-neon-high` | `keep=8` | 2/3 | 0.667 | 0.333 | 1.000 | **142 614.0** | **728.5** | 800 | 23 684 / 9 | 18 | 33 / 38 | 0 | 106.0 / 1 563.0 / 152 | 374 552 | 39 |
| `gemini-4-argon-high` | `keep=4` | 2/3 | 0.667 | 0.333 | 1.000 | 308 719.0 | 2 279.0 | 3 272 | 189 824 / 31 | 38 | 62 / 63 | 3 | 101.0 / 291.4 / 163 | 297 162 | 65 |
| `gemini-4-argon-high` | `keep=8` | 1/3 | 0.333 | 0.000 | 0.667 | 173 636.0 | 1 284.0 | 845 | 13 279 / 5 | 11 | 18 / 21 | 0 | 114.0 / 109.8 / 125 | 72 989 | 25 |
| **Combiné (3 familles)** | **`keep=4`** | **4/9** | **0.444** | **0.167** | **0.722** | **364 108.5** | **3 335.0** | **10 090** | **411 256 / 71** | **90** | **150 / 151** | **8** | — | **174 802** | **154** |
| **Combiné (3 familles)** | **`keep=8`** | **6/9** | **0.667** | **0.417** | **0.917** | **171 949.2** | **1 098.3** | **3 970** | **142 171 / 40** | **64** | **112 / 125** | **4** | — | **235 036** | **131** |

### 3.3 Détail complet des 18 essais (Phase 4)

| Suite | Modèle | Run | Résultat | Code SQLite | Tours | Tok totaux | Thought tok | Masqué (o) | Émis / spans | Err. | `rt` moy (ms) | Wall (ms) |
|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| `ctrap-keep4` | `hydrogen-high` | 1 | FAIL | `E_ORACLE_ASSERTION_FAIL` | 5 | 78 261 | 433 | 8 923 | 13 / 13 | 0 | 106.2 | 54 779 |
| `ctrap-keep4` | `hydrogen-high` | 2 | FAIL | `E_UNCLOSED_THOUGHT` | 7 | 110 526 | 811 | 14 292 | 11 / 11 | 1 | 100.7 | 122 441 |
| `ctrap-keep4` | `hydrogen-high` | 3 | FAIL | `E_ORACLE_ASSERTION_FAIL` | 1 | 14 773 | 0 | 0 | 0 / 0 | 0 | — | 32 138 |
| `ctrap-keep4` | `neon-high` | 1 | FAIL | `E_UNCLOSED_THOUGHT` | 8 | 127 736 | 1 199 | 25 790 | 16 / 16 | 1 | 119.9 | 81 397 |
| `ctrap-keep4` | `neon-high` | 2 | **PASS** | `none` | 12 | 195 080 | 2 175 | 63 447 | 21 / 21 | 1 | 125.3 | 121 003 |
| `ctrap-keep4` | `neon-high` | 3 | **PASS** | `none` | 19 | 312 620 | 2 200 | 108 980 | 27 / 27 | 2 | 107.7 | 269 970 |
| `ctrap-argon-keep4` | `argon-high` | 1 | **PASS** | `none` | 14 | 226 909 | 783 | 75 458 | 24 / 24 | 0 | 114.7 | 347 365 |
| `ctrap-argon-keep4` | `argon-high` | 2 | **PASS** | `none` | 19 | 311 461 | 1 367 | 108 327 | 26 / 27 | 2 | 539.8 | 394 402 |
| `ctrap-argon-keep4` | `argon-high` | 3 | FAIL | `E_ORACLE_ASSERTION_FAIL` | 5 | 79 068 | 1 122 | 6 039 | 12 / 12 | 1 | 106.5 | 149 719 |
| `ctrap-keep8` | `hydrogen-high` | 1 | **PASS** | `none` | 15 | 247 223 | 924 | 59 083 | 26 / 27 | 2 | 114.9 | 403 254 |
| `ctrap-keep8` | `hydrogen-high` | 2 | **PASS** | `none` | 12 | 196 871 | 1 196 | 31 653 | 19 / 23 | 1 | 125.1 | 246 628 |
| `ctrap-keep8` | `hydrogen-high` | 3 | **PASS** | `none` | 8 | 128 737 | 205 | 14 472 | 16 / 16 | 1 | 108.3 | 122 823 |
| `ctrap-keep8` | `neon-high` | 1 | **PASS** | `none` | 7 | 112 556 | 506 | 9 997 | 14 / 16 | 0 | 107.6 | 109 621 |
| `ctrap-keep8` | `neon-high` | 2 | **PASS** | `none` | 8 | 128 597 | 294 | 13 687 | 19 / 22 | 0 | 2 836.6 | 440 470 |
| `ctrap-keep8` | `neon-high` | 3 | FAIL | `E_UPSTREAM_REFUSAL` | 3 | 44 075 | 0 | 0 | 0 / 0 | 0 | — | 573 565 |
| `ctrap-argon-keep8` | `argon-high` | 1 | **PASS** | `none` | 8 | 129 529 | 845 | 13 279 | 18 / 21 | 0 | 109.8 | 159 640 |
| `ctrap-argon-keep8` | `argon-high` | 2 | FAIL | `E_ORACLE_ASSERTION_FAIL` | 2 | 29 342 | 0 | 0 | 0 / 0 | 0 | — | 43 787 |
| `ctrap-argon-keep8` | `argon-high` | 3 | FAIL | `E_ORACLE_ASSERTION_FAIL` | 1 | 14 765 | 0 | 0 | 0 / 0 | 0 | — | 15 540 |

### 3.4 Analyse des échecs et lecture mécanique de l'écart

1. **Pourquoi `Hydrogen-high` passe de `0/3` (`keep=4`) à `3/3` (`keep=8`)** :
   - Sur `ctrap-keep4` run 1 (`5` tours, `8 923` o masqués), le modèle appelle des outils aux tours 0–3, déclenche deux avertissements *LoopRoute/DoomLoop* et conclut au tour 4 (`946` car.) sans corriger l'ensemble des nœuds ; sur le run 2 (`7` tours, `14 292` o masqués), il termine sur une pensée non fermée (`E_UNCLOSED_THOUGHT`). En `keep=8`, les trois essais conservent suffisamment de lectures brutes récentes pour éditer `node04.go` et `node09.go` en `8` à `15` tours (`3/3`).
2. **Pourquoi `Argon-high` affiche `2/3` (`keep=4`) contre `1/3` (`keep=8`) sans contredire l'effet du masquage** :
   - Les deux échecs de `ctrap-argon-keep8` ne sont **pas** dus à la fenêtre `keep=8` : ils s'arrêtent avant d'atteindre `> 8` observations (`0` outil émis, `0` octet masqué). Au run 2, le tour 0 subit 3 `refusal_retry` et sort en `turn_class = 'incomplete'` (`finish_reason = 'error'`), puis le tour 1 répond en `8` caractères ; au run 3, le modèle répond en texte pur au tour 0 (`603` car.) sans appeler d'outil. Le seul essai qui exécute des outils (`run 1`) réussit en `8` tours (`129 529` tokens, `13 279` o masqués).
3. **Protection effective par l'arranger en `keep=8`** :
   - En `keep=8`, **`13` spans** non émis ont été interceptés avant d'atteindre le client (`11` `rejected_mid_value_truncation` + `2` `rejected_schema_invalid`), et sur `keep=4` (`argon-high` run 2), `1` appel a été bridé par `throttled_write_serial`, confirmant la complémentarité entre rétention contextuelle et garde-fous déterministes.

---

## 4. Enseignements techniques et suites recommandées

### 4.1 Ce que ces campagnes valident

- **Verrou Task `[40]` → Chat `[0]`** : une fois l'opt-in `X-Allow-Task-Mode-For-Tools: true` acheminé par OpenCode `--standalone`, le mode Task `[40]` apporte un gain mesurable sur la suite `S1`–`S3` (`+0.250`, IC 95 % `[0.073 ; 0.427]`), contrairement à l'expérience dégradée v3.5.7 pré-verrou.
- **Disparition de trois classes historiques de pannes** : sur les **78 essais valides**, aucune boucle de salutation (`E_GREETING_LOOP`) ni aucun faux quota (`E_FALSE_QUOTA`) n'a provoqué d'échec final, et les `11 + 13` troncatures/schémas invalides ou écritures non taggées détectées en phase 3 et phase 4 ont toutes été filtrées avant exécution hôte.
- **Corrélation SQLite de bout en bout** : la jointure `benchmark_trials ↔ agent_turns ↔ tool_call_spans ↔ turn_anomalies` explique chaque échec (retries transport, refus amont, pensée non fermée, arrêt à `0` outil) sans conserver le texte des prompts ni les arguments complets d'outils.

### 4.2 Angle mort d'observabilité détecté et correctif post-campagne (`E_INCOMPLETE_PROSE`)

Lors des campagnes `v3.5.9`, [`failureCodeFromCorrelation`](../../free-gemini-api/db/telemetry.go#L599-L621) ne traitait pas `corr.TurnClass == "incomplete"`. Conséquence observée sur `ctrap-argon-keep8` run 2 : un tour 0 terminé en `incomplete`/`error` (`visible_len = 28`) après `3` `refusal_retries`, suivi d'un tour 1 de `8` caractères (`0` outil, `transport_retries = 0`, `thought_unclosed = 0`), a conservé le code générique `E_ORACLE_ASSERTION_FAIL` dans `gemini.db`.

Un mapping inconditionnel `TurnClass == "incomplete" → E_INCOMPLETE_PROSE` aurait toutefois été trop large, car [`classifyTurnOutput`](../../free-gemini-api/api/chat_handler.go#L195-L242) émet `"incomplete"` pour deux situations distinctes :
- une **génération vide** (`strings.TrimSpace(clean) == ""`, `visible_len = 0`) ;
- un **fragment de prose court sans ponctuation finale** (`isIncompleteFinalProse`, `visible_len > 0`).

Le correctif appliqué post-campagne (dans [`free-gemini-api/db/telemetry.go`](../../free-gemini-api/db/telemetry.go#L426-L621) et [`free-gemini-api/db/benchmark_test.go`](../../free-gemini-api/db/benchmark_test.go#L309-L418), présent dans le working tree pour relecture) ajoute `VisibleLen` à `SessionCorrelation` et ne raffine vers `E_INCOMPLETE_PROSE` que lorsque `corr.TurnClass == "incomplete" && corr.VisibleLen > 0` (et uniquement sur un essai en échec `oracle_passed = false`), tout en conservant :
- la priorité de `E_STALL_OR_TIMEOUT` si `transport_retries > 0` ou `stream_stalled = true` ;
- le code oracle générique lorsque `turn_class = "incomplete"` et `visible_len = 0` (ou `E_UNCLOSED_THOUGHT` si `thought_unclosed = true`).

> [!NOTE]
> Les lignes historiques déjà enregistrées dans `gemini.db` (dont `ctrap-argon-keep8` run 2) restent volontairement inchangées en `E_ORACLE_ASSERTION_FAIL` : le correctif s'applique à toute future insertion/upsert d'essai.

### 4.3 Limites et protocole recommandé pour une confirmation statistique

- Les comparaisons *Complexity Trap* portent sur **`n = 3` essais séquentiels** par modèle et par bras : elles établissent un signal encourageant en CoP et en fiabilité d'Hydrogen, pas une preuve statistique universelle multi-familles.
- Le harnais [`scripts/benchmark_matrix.sh`](../../scripts/benchmark_matrix.sh) ne sait pas alterner `keep=4` et `keep=8` au sein d'une même exécution, car `OBSERVATION_MASK_THINKING_KEEP_RECENT` est lu au démarrage du conteneur proxy.
- Pour trancher statistiquement *Complexity Trap*, relancer uniquement `^s2_long_horizon_chain$` avec **`n >= 5`** via un orchestrateur externe redémarrant le conteneur entre chaque essai (`keep=4` / `keep=8` alternés avec un `BENCHMARK_SUITE_ID` distinct, soit env. `10` essais / `30–60 min` pour `Argon-high` seul ou `30` essais / `2–4 h` pour les 3 familles) et en analysant à part les abandons précoces à `0` outil émis.
