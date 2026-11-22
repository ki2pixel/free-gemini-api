# Sécurité — Free Gemini API (P0/P1/P2, novembre 2026)

Ce document décrit les contrôles ajoutés par les phases **P0**, **P1** et **P2**
ainsi que les opérations de rotation à effectuer après une exposition réseau.

## 1. Authentification obligatoire

Toutes les routes, sauf `GET /health`, exigent un jeton :

```bash
curl -H "Authorization: Bearer $API_TOKEN" http://127.0.0.1:8100/history
# ou
curl -H "X-API-Token: $API_TOKEN" http://127.0.0.1:8100/history
```

Comportement :

- `API_TOKEN` vide ⇒ **toutes** les routes protégées répondent `401`. Le serveur
  journalise un avertissement au démarrage mais ne s'ouvre jamais silencieusement.
- `ADMIN_TOKEN` non vide ⇒ il est seul accepté sur `POST /v1/models`,
  `POST /v1/models/discover` et `POST /api/sync-cookies`. Sinon `API_TOKEN` sert
  de repli.
- Les pré-vols CORS (`OPTIONS`) sont exemptés pour que les navigateurs puissent
  négocier avant d'envoyer le jeton.
- `PUBLIC_HELP=true` rend `GET /` et `GET /help` publics ; par défaut ils sont
  protégés.

Exemple d'appel chat :

```bash
curl -N -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.8-flash","prompt":"Bonjour"}' \
  http://127.0.0.1:8100/chat
```

## 2. Port et interface d'écoute

Le serveur écoute par défaut sur **`127.0.0.1:8100`** (`API_HOST`, `PORT`). Le
bridge WebSocket écoute sur **`127.0.0.1:9226`** (`WS_HOST`, `WS_PORT`).

Dans Docker, `API_HOST=0.0.0.0` et `WS_HOST=0.0.0.0` sont utilisés **à
l'intérieur** du conteneur, mais `docker-compose.yml` publie les ports
uniquement sur `127.0.0.1`.

## 2 bis. Limitation de débit

Un limiteur à fenêtre fixe protège toutes les routes (sauf `GET /health` et
les pré-vols `OPTIONS`). La clé combine l'IP et un hachage du jeton, ce qui
donne un budget indépendant à chaque client sans stocker le secret. Valeurs par
défaut : **600 requêtes / 60 s**, ajustables via `RATE_LIMIT_MAX` et
`RATE_LIMIT_WINDOW_SECONDS`. Les réponses `429` incluent `Retry-After`.

## 3. WebSocket de synchronisation

La connexion exige un jeton (`WS_TOKEN`, sinon `API_TOKEN`) :

```
ws://127.0.0.1:9226/?token=<WS_TOKEN>
```

Sont également appliqués :

- validation de `Origin` (`chrome-extension://…`, loopback uniquement) ;
- limite de message de 1 Mio ;
- validation stricte des cookies (domaines Google, noms autorisés, valeurs
  bornées, 256 cookies maximum) ;
- timeouts HTTP `ReadHeaderTimeout=5s`, `ReadTimeout=10s`, `IdleTimeout=60s`.

L'extension Chrome se configure dans son popup : URL du serveur, port WS et
jeton. Elle n'effectue plus de balayage du réseau local.

## 4. Cookies Google — rotation obligatoire

Considérez les cookies de `cookies/` comme **potentiellement compromis** si :

- l'instance a été accessible depuis un autre poste que le loopback ;
- `/output/*` a été interrogé avec un chemin suspect ;
- l'extension a été configurée avec une URL non-loopback.

Procédure :

1. Ouvrir <https://myaccount.google.com/security> pour chaque compte concerné.
2. « Vos appareils » → déconnecter les sessions inconnues.
3. Vérifier l'activité récente (Gmail, Drive, Gemini).
4. En cas de doute, changer le mot de passe et activer la validation en deux étapes.
5. Supprimer les anciens fichiers `cookies/account_*.json` et
   `cookies/session_*.json`.
6. Reconnecter Chrome puis relancer la synchronisation depuis le popup.

Ne jamais committer `cookies/`, `data/` ou `.env`.

## 4 bis. Chiffrement des cookies au repos (A-24)

Lorsque `COOKIE_ENCRYPTION_KEY` est définie, les fichiers
`cookies/account_*.json` et les caches `cookies/session_*.json` sont chiffrés
avec **AES-256-GCM** avant leur écriture atomique. Le format sur disque est une
enveloppe JSON :

```json
{
  "__gemini_encrypted": true,
  "version": 1,
  "algorithm": "AES-256-GCM",
  "nonce": "<base64, 12 octets>",
  "ciphertext": "<base64, données + tag 16 octets>"
}
```

Dérivation de clé :

- une valeur de **64 caractères hexadécimaux** est utilisée directement comme
  clé AES de 32 octets ;
- toute autre valeur est étirée avec **PBKDF2-HMAC-SHA256** (100 000 itérations,
  sel applicatif fixe).

Générer une clé :

```bash
openssl rand -hex 32
```

Comportement de migration :

- les fichiers chiffrés sont déchiffrés de façon transparente au chargement ;
- les fichiers historiques en clair restent lisibles et sont **réécrits sous
  forme chiffrée** dès qu'ils sont lus ou synchronisés avec une clé active ;
- si la clé est absente, le serveur démarre en clair avec un avertissement ;
- `REQUIRE_COOKIE_ENCRYPTION=true` transforme l'absence de clé en erreur fatale
  au démarrage.

Après une rotation de clé, les anciens fichiers chiffrés ne sont plus lisibles :
ressynchronisez les cookies depuis l'extension ou supprimez les fichiers
concernés.

## 4 ter. Métriques Prometheus (A-27)

`GET /metrics` expose un format texte Prometheus (`text/plain; version=0.0.4`)
avec la santé des comptes, l'état des workers, les requêtes HTTP, les tokens et
les rejets du limiteur de débit. Par défaut la route exige `API_TOKEN` (ou
`ADMIN_TOKEN`).

`PUBLIC_METRICS=true` autorise un scrape **sans jeton uniquement depuis le
loopback** (`127.0.0.1` ou `::1`). Ne l'activez pas derrière un reverse proxy
qui présenterait toutes les requêtes comme locales.

## 5. Permissions sur disque

Le serveur applique :

| Chemin | Permission |
|---|---|
| `cookies/` | `0700` |
| `cookies/*.json` | `0600` |
| `data/` | `0700` |
| `data/*.db`, `*.db-wal`, `*.db-shm` | `0600` |
| `data/models.json` | `0600` |
| fichiers média téléchargés | `0600` puis `0644` après publication |

Vérification :

```bash
stat -c "%a %n" cookies/*.json data/*.db* data/models.json 2>/dev/null || true
# attendu : 600
stat -c "%a %n" cookies data
# attendu : 700
```

Pour corriger manuellement une installation existante :

```bash
chmod 700 cookies data
chmod 600 cookies/*.json data/*.db* data/models.json 2>/dev/null || true
```

## 6. Conteneur Docker

L'image tourne par défaut avec l'UID/GID `10001:10001`, un rootfs en lecture
seule, `cap_drop: ALL`, `no-new-privileges`, une limite mémoire de 1 Go et 256 PID.

Pour que les volumes montés restent accessibles au toolchain Go local,
`docker-compose.yml` mappe `${HOST_UID}:${HOST_GID}` (par défaut
`10001:10001`). Définissez ces variables dans `.env` :

```bash
HOST_UID=$(id -u)
HOST_GID=$(id -g)
```

Puis alignez les volumes hôtes sur cet UID/GID :

```bash
mkdir -p cookies data output
sudo chown -R "$(id -u):$(id -g)" cookies data output
chmod 700 cookies data output
```

Puis :

```bash
docker compose down
docker compose up -d --build
docker inspect free-gemini-api --format '{{.Config.User}} {{.HostConfig.ReadonlyRootfs}} {{.State.Health.Status}}'
```

## 7. Réseau sortant des téléchargements

`DownloadFile` :

- n'accepte que `https://` vers `*.google.com`, `*.googleusercontent.com`,
  `*.gstatic.com`, `*.googleapis.com` et `*.googlevideo.com` ;
- ne réinjecte le cookie de session que sur le même domaine (ou un sous-domaine) ;
- borne chaque téléchargement à **20 Mio** (`maxMediaBytes`).

## 8. Exports analytics

`GET /export/json` et `GET /export/excel` génèrent la réponse **en mémoire**.
Les exports automatiques passent par un worker coalescent (canal de taille 1,
debounce de 5 s) et des écritures atomiques. Le chemin des requêtes chat n'attend
plus la génération des rapports. Les deux routes sont limitées à **5 requêtes
par minute** par couple IP + empreinte de jeton ; au-delà, la réponse est `429`
avec `Retry-After`.

## 9. Serveur MCP (stdio)

Le serveur MCP (`--mcp`) applique les contrôles suivants :

- racine des fichiers limitée à `MCP_MEDIA_ROOT` (défaut `./output`) ;
- rejet des chemins absolus, de la traversée (`..`), des liens symboliques qui
  sortent de la racine et des fichiers de plus de **20 Mio** ;
- validation du type MIME des images de référence ;
- négociation explicite de la version du protocole (`2024-11-05`, `2025-03-26`,
  `2025-06-18`) ; une version inconnue est refusée ;
- jeton `MCP_TOKEN` (repli `API_TOKEN`) pour les appels vers l'API locale ;
- timeouts HTTP de 60 s et deux tentatives supplémentaires ;
- journal d'audit sans secret : identifiants hachés en SHA-256 tronqué.

## 10. Rétention et effacement (RGPD)

La purge est pilotée par `RETENTION_DAYS` (défaut **30**, `0` désactive). Elle
s'exécute au démarrage puis toutes les 24 heures et supprime les messages, les
métadonnées de médias et les journaux de requêtes expirés.

`DELETE /history/:conversation_id` exige `API_TOKEN` et supprime les messages
et métadonnées de médias de la conversation. L'opération est idempotente et
journalisée (`audit action=gdpr_delete`). Voir `PRIVACY.md` pour la base légale
et les droits des personnes.

## 11. Chaîne d'approvisionnement et scan de secrets (A-28)

- `go mod verify` est exécuté en CI avant les tests pour détecter toute
  altération des modules téléchargés.
- `gitleaks` scanne l'historique Git et les PR avec la configuration
  `.gitleaks.toml` (règles par défaut + cookies Google + clé de chiffrement).
- `.github/dependabot.yml` ouvre au plus 5 PR par semaine pour les modules Go
  et les actions GitHub.
- Les actions GitHub restent épinglées par SHA ; la CI conserve
  `permissions: contents: read`.

Pour scanner localement :

```bash
gitleaks git --config .gitleaks.toml --redact --no-banner .
```

## 12. Limites connues

- Il n'y a pas encore de RBAC multi-utilisateur ; `API_TOKEN` est un secret
  partagé.
- Les exports ne sont pas signés ; ils sont générés à la demande et limités en
  débit.
- Le chiffrement des cookies protège le disque, pas une machine compromise
  pendant l'exécution : la clé et les cookies déchiffrés sont en mémoire.
- La clé `COOKIE_ENCRYPTION_KEY` est lue depuis l'environnement ; pour un
  déploiement à forte exigence, utilisez un KMS/Vault ou des secrets Docker.
