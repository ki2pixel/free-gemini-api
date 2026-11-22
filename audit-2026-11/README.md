# Audit 2026-11 — Free Gemini API

Ce dossier contient les livrables de l'audit de sécurité et de maturité réalisé le **21 novembre 2026** sur le dépôt `free-gemini-api-main`.

## Lecture rapide

| Fichier | Contenu |
|---|---|
| [`RAPPORT_AUDIT.md`](RAPPORT_AUDIT.md) | Rapport principal : synthèse exécutive, architecture, 6 axes, indice de maturité, conformité, conclusion |
| [`REGISTRE_CVSS.md`](REGISTRE_CVSS.md) | 25 fiches de vulnérabilité avec vecteur CVSS v3.1, CWE/OWASP, preuves, impact, correctif |
| [`REGISTRE_CVSS.csv`](REGISTRE_CVSS.csv) | Même registre, format tableur/ticketing |
| [`CAHIER_POC.md`](CAHIER_POC.md) | 18 PoC avec commandes exactes et résultats observés |
| [`PLAN_REMEDIATION.md`](PLAN_REMEDIATION.md) | Roadmap P0/P1/P2, patches, Docker/CI durcis, plan de vérification |
| [`evidence/`](evidence/) | Sorties brutes : SAST, scans, benchmarks, stress, logs, preuves PoC |
| [`poc/`](poc/) | Tests Go d'audit, mock backend MCP, script de préparation |

## Verdict

> **Indice global de maturité : 38/100 (niveau 2/5).**  
> **NO-GO** pour une exposition réseau, un usage multi-utilisateur ou une publication Chrome Web Store en l'état.  
> **GO local mono-utilisateur** après les quick wins P0.

## Chiffres clés

| Métrique | Valeur |
|---|---:|
| Constats | 25 (4 Critical, 5 High dont F-25 sans score CVSS, 13 Medium, 3 Low) |
| Vulnérabilités Go (trivy) | 28, dont 13 HIGH |
| Vulnérabilités reachables (govulncheck) | 2 (brotli, klauspost/compress) |
| Constats gosec | 84 (8 HIGH, 25 MEDIUM, 51 LOW) |
| Couverture de tests | 28,2 % |
| Écritures SQLite concurrentes | 807/s, 0 échec |
| Export Excel 5 000 messages | 407 ms, 78 Mo alloués |
| Tokenizer 100 000 caractères | 45 ms, 12 Mo alloués |
| Stress `/health` 100 parallèles | 100/100, p95 11 ms |
| Stress `/export/excel` 30 parallèles | 26/30, 4 erreurs, 4 XLSX invalides |

## Top 5 des risques

1. **Traversée `/output/*`** → lecture des cookies Google, de `/etc/passwd` et du WAL SQLite (F-01, CVSS 8.6).
2. **Aucune authentification** sur l'API, l'historique, les exports et l'administration (F-02, CVSS 9.1).
3. **Extension Chrome** : envoi des cookies Google à tout hôte du LAN qui répond `/health` (F-05, CVSS 9.3).
4. **CORS `*` + WebSocket `CheckOrigin=true`** → drive-by et injection de cookies (F-03/F-04, CVSS 9.1/9.3).
5. **DoS non authentifié** : export analytics à chaque requête, tokenizer 50 Mo, pas de rate limit (F-10/F-11, CVSS 7.5).

## Reproductibilité

```bash
cd audit-2026-11/poc
chmod +x prepare_audit_env.sh
./prepare_audit_env.sh /home/kidpixel/free-gemini-api-main
# puis suivre CAHIER_POC.md
```

L'environnement est isolé dans `/tmp/opencode/audit-src` et `/tmp/opencode/audit-run`, sans cookies réels. Les PoC ne doivent pas être exécutés contre une instance de production.

## Avertissement

- Les scores CVSS sont des scores de base, sans métriques temporelles/environnementales.
- Les tests Google live (compte jetable) et le stress 50–100 requêtes vers Google **n'ont pas été exécutés** faute de consentement ; le protocole est prêt dans `RAPPORT_AUDIT.md` §8.
- Aucune modification du code de production n'a été effectuée. Les fichiers `poc/*.go` sont des tests d'audit à copier dans les packages correspondants, pas des tests de non-régression à fusionner tels quels.
- Les cookies Google réels présents dans `free-gemini-api/cookies/` n'ont jamais été lus, copiés ou exfiltrés. Si l'instance a été exposée, considérez-les comme compromis et révoquez les sessions.

## Questions de cadrage (réponses)

| Question | Décision |
|---|---|
| Compte Google jetable pour les tests live ? | **Non exécuté** — en attente de votre autorisation explicite. Protocole prêt (§8.1). |
| Autoriser 50–100 requêtes parallèles vers Google ? | **Non exécuté** — risque de rate-limit/bannissement. Le stress local 100 requêtes a été réalisé (sans Google). Protocole prêt (§8.2). |
| Inclure la conformité Chrome Web Store MV3 ? | **Oui** — §6.8 et F-18/F-25. |
| Sévérité orientée conformité (OWASP/CWE) ou exploitation ? | **Les deux** : CVSS v3.1 + criticité métier + mapping OWASP/CWE, avec PoC exploit-oriented. |
| Chapitre dédié MCP (`api/mcp.go`) ? | **Oui** — §6.7 et F-19, avec PoC d'exfiltration. |
