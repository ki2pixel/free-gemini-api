# Scoping des cookies — RFC 6265 et isolation des sous-domaines

**Version documentée :** v3.5.7
**Code de référence :** `free-gemini-api/gemini/session.go`, `gemini/client.go`,
`gemini/transport.go`, `gemini/media.go`, `gemini/crypto.go`
**Tests de référence :** `gemini/session_cookie_scope_test.go`

---

## 1. Le problème : collisions de noms entre sous-domaines Google

Un export de cookies Chrome contient, dans un **même fichier**, les cookies de
plusieurs hôtes Google. Exemple réel (`cookies/account_ga000FAlQD.json`) :

| Nom | Domaines présents dans l'export |
|---|---|
| `OSID` | `gemini.google.com`, `myaccount.google.com`, `console.cloud.google.com` |
| `__Secure-OSID` | les trois mêmes hôtes |
| `COMPASS` | `gemini.google.com`, `console.cloud.google.com` |

Les **noms sont identiques**, les **valeurs diffèrent**. Si le jar ne distingue
pas les hôtes, la dernière valeur rencontrée dans l'export écrase silencieusement
celle de Gemini.

### 1.1 Ce qui échouait avant le correctif

Deux mécanismes se cumulaient :

1. **Double enregistrement.** `loadCookies` inscrivait chaque entrée **deux
   fois** : une fois contre son propre domaine, une fois contre
   `https://gemini.google.com`. Les cookies de `console.cloud.google.com` et
   `myaccount.google.com` étaient donc injectés dans l'identité Gemini.

2. **Jar par eTLD+1.** `tls_client.NewCookieJar()` regroupe les cookies par
   **eTLD+1** : un seul compartiment `google.com` pour tous les hôtes Google. Il
   déduplique en outre sur le **nom seul**, en ignorant domaine et chemin. La
   dernière valeur de l'export remplaçait donc la valeur Gemini.

### 1.2 Conséquence observable

Les modèles invités/gratuits (Hydrogen) continuaient de fonctionner : les seuls
cookies `.google.com` suffisent à les authentifier. Le chemin **abonnement**
(Neon, `neon-thinking-high`) échouait en `401` : il exige le jeu
`OSID` / `__Secure-OSID` / `COMPASS` **porté par l'hôte Gemini**.

---

## 2. Le correctif : jar RFC 6265 + un seul hôte propriétaire

### 2.1 Jar avec liste de suffixes publics

`gemini/client.go` :

```go
// tls_client.NewCookieJar() is deliberately avoided: it buckets cookies by
// eTLD+1 ...
jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
```

Le jar est un `fhttp/cookiejar` conforme RFC 6265, adossé à
`golang.org/x/net/publicsuffix`. Conséquences :

- `gemini.google.com` et `console.cloud.google.com` sont des compartiments
  **distincts** (le suffixe public est `google.com`, mais le domaine de cookie
  effectif distingue les hôtes) ;
- la déduplication tient compte du **domaine et du chemin**, pas seulement du
  nom.

### 2.2 Enregistrement d'un seul hôte par cookie

`scopedCookie` (dans `gemini/session.go`) est le point d'entrée unique. Pour
chaque cookie exporté :

```text
1. Normaliser le domaine : trim espaces, minuscules, retirer le point initial.
2. Rejeter si le nom est vide ou si le domaine contient / ? # @.
3. Déterminer la nature du cookie :
     domainCookie = (rawDomain == "") OU (¬HostOnly) OU (rawDomain commence par ".")
4. Si le domaine est vide → "google.com" (compatibilité historique).
5. Construire l'origine https://<hôte propriétaire>.
6. Construire le cookie ; chemin par défaut "/".
7. Si domainCookie : renseigner cookie.Domain = host.
   Sinon : laisser Domain vide → cookie host-only.
8. Reporter Expires depuis ExpirationDate si présent.
```

Le cookie est ensuite inscrit **uniquement** contre l'origine de l'hôte qui le
possède :

```go
jar.SetCookies(origin, []*http.Cookie{cookie})
```

Il n'y a plus de second enregistrement contre `gemini.google.com`.

### 2.3 Host-only vs domain cookie

La distinction est le cœur de l'isolation :

| Type | Attribut `Domain` | Portée d'envoi |
|---|---|---|
| **Host-only** | absent | uniquement l'hôte exact qui l'a déposé |
| **Domain** | présent (`google.com`) | l'hôte et ses sous-domaines |

Stocker un cookie host-only **sans** attribut `Domain` garantit qu'il ne part
jamais vers un hôte frère. C'est la règle RFC 6265 §5.3 que le code applique à
la lettre.

Le fichier `gemini/session_cookie_scope_test.go` vérifie les quatre propriétés
suivantes :

| Test | Contrat |
|---|---|
| `TestLoadCookiesKeepsSubdomainCookiesScoped` | rejoue l'export réel : les valeurs `OSID` / `__Secure-OSID` / `COMPASS` de Gemini gagnent, aucune valeur `myaccount` ou `console` n'apparaît |
| `TestCookieJarScopesByRequestHost` | le jar renvoie `gemini-osid` pour l'origine Gemini, `console-osid` pour l'origine console, et ne fuit jamais un cookie host-only |
| `TestCookieJar_HostOnlyVsDomainAttribute` | séparation stricte entre host-only (sans attribut) et domain-scoped entre hôtes frères et domaine parent |
| `TestScopedCookieHonorsHostOnly` | le flag `HostOnly` de l'export est honoré |

---

## 3. Rendu de l'en-tête `Cookie`

Trois fonctions, un seul principe : **le jar est interrogé pour l'hôte de la
requête**, jamais un instantané figé.

### 3.1 `cookieHeaderFromJar(jar, u)`

```text
parts ← []
pour chaque cookie de jar.Cookies(u) :
    si cookie.Name ≠ "" : parts ← parts + "name=value"
retourner join(parts, "; ")
```

### 3.2 `cookieHeaderForHost(jar, host)`

Parse `https://<host>/` puis délègue à `cookieHeaderFromJar`.

### 3.3 `(c *GeminiClient) cookieHeader()`

Résout l'en-tête pour `geminiCookieHost = "gemini.google.com"` **en direct
depuis le jar**.

Pourquoi c'est critique : les chemins QUIC utilisaient auparavant l'instantané
`rawCookies` pris au chargement. Les cookies rotés par une réponse `/app`
(`__Secure-1PSIDTS`, `__Secure-3PSIDTS`) n'étaient donc **jamais** envoyés
après rotation, ce qui produisait des `401` intermittents en cours de session.
La résolution live corrige ce point.

### 3.4 Téléchargements de médias

`DownloadFile` (`gemini/media.go`) résout désormais les cookies **par saut**
depuis le jar, au lieu de réexpédier l'en-tête complet du compte à chaque hop :

- un cookie `.google.com` n'est pas envoyé à `googleusercontent.com` ;
- un cookie host-only ne quitte jamais son hôte.

Combiné à l'allowlist d'hôtes HTTPS et à la borne de 20 Mio
(`maxMediaBytes`), cela ferme la fuite de session lors d'une redirection.

---

## 4. Synchronisation live et formats acceptés

### 4.1 Pont WebSocket

L'extension Chrome pousse les cookies vers `ws://127.0.0.1:9226/?token=<WS_TOKEN>`
(`WS_TOKEN` vide ⇒ repli sur `API_TOKEN`). Le serveur applique :

- validation de `Origin` (`chrome-extension://…`, loopback uniquement) ;
- limite de message de **1 Mio** ;
- validation stricte : domaines Google, noms autorisés, valeurs bornées,
  **256 cookies maximum** ;
- timeouts `ReadHeaderTimeout=5s`, `ReadTimeout=10s`, `IdleTimeout=60s`.

### 4.2 Repli HTTP

`POST /api/sync-cookies` accepte le même contenu en JSON. Le champ optionnel
`profile_id` permet de nommer le fichier `account_profile_<N>.json` au lieu de
dériver le nom du hash de valeur. Sans ce champ — cas de l'extension Chrome —
le nommage historique par hash est conservé.

> **Important :** `__Secure-1PSID` tourne régulièrement. Sans `profile_id`, le
> nom dérivé du hash de valeur crée un **nouveau** fichier à presque chaque
> rafraîchissement, ce qui fait grossir le pool avec des doublons du même
> compte Google. Mesuré le 2026-09-27 : 54 → 55 workers après un seul refresh.

### 4.3 Trois formats de fichier

| Format | Détection | Traitement |
|---|---|---|
| **JSON array** (extension) | `json.Unmarshal` vers `[]CookieObject` réussit | `scopedCookie` par entrée |
| **Objet legacy** (chaîne) | objet avec `cookies` non vide | parse `"a=b; c=d"`, portée `.google.com` |
| **Absent / `guest`** | fichier manquant ou nom `guest` | mode invité, palier `free` |

Un cookie invalide (nom vide, domaine contenant des caractères interdits) est
**ignoré avec un avertissement explicite**, jamais injecté. Le démarrage ne
casse pas sur un fichier partiellement corrompu.

---

## 5. Chiffrement au repos

(`gemini/crypto.go`)

Quand `COOKIE_ENCRYPTION_KEY` est définie, les fichiers
`cookies/account_*.json` et les caches `cookies/session_*.json` sont chiffrés
avec **AES-256-GCM** avant écriture atomique.

Enveloppe sur disque :

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

- **64 caractères hexadécimaux** → utilisés directement comme clé de 32 octets ;
- toute autre valeur → **PBKDF2-HMAC-SHA256**, 100 000 itérations, sel
  applicatif fixe.

Migration transparente : les fichiers en clair restent lisibles et sont
réécrits sous forme chiffrée dès qu'ils sont lus ou resynchronisés avec une clé
active. `REQUIRE_COOKIE_ENCRYPTION=true` transforme l'absence de clé en erreur
fatale au démarrage.

---

## 6. Permissions sur disque

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

---

## 7. Invariants à préserver

Toute modification du chemin cookie doit conserver ces propriétés :

```text
C1  Tout cookie exporté est enregistré contre l'hôte qui le possède, une seule fois.
C2  Un cookie host-only est stocké sans attribut Domain.
C3  L'en-tête Cookie de gemini.google.com ne contient jamais une valeur
    provenant de myaccount.google.com ou console.cloud.google.com.
C4  L'en-tête est résolu depuis le jar à chaque requête, jamais depuis un
    instantané de chargement.
C5  Un cookie invalide est ignoré avec un avertissement, jamais injecté.
C6  Un téléchargement hors origine ne reçoit que les cookies applicables à son
    propre hôte.
```

Les invariants C1 à C3 sont vérifiés par
`gemini/session_cookie_scope_test.go`. C4 est couvert par le comportement du
transport ; C5 et C6 par les tests de média et de sécurité de téléchargement.

---

## 8. Diagnostiquer un `401` sur le palier abonnement

1. Vérifier que le fichier de cookies contient bien les entrées **avec**
   `"domain": "gemini.google.com"` pour `OSID`, `__Secure-OSID` et `COMPASS`.
2. Vérifier qu'aucune entrée `console.cloud.google.com` ou
   `myaccount.google.com` ne porte une valeur différente pour ces mêmes noms.
3. Lancer le test de scope :

   ```bash
   cd free-gemini-api
   go test -v ./gemini -run 'TestLoadCookiesKeepsSubdomainCookiesScoped|TestCookieJar'
   ```

4. Contrôler les logs de démarrage : un cookie ignoré produit la ligne
   `⚠️ [GeminiClient] Skipping invalid cookie ...`.
5. Si les valeurs semblent bonnes mais le `401` persiste, forcer une
   resynchronisation depuis l'extension (rotation de `__Secure-1PSIDTS`).

---

## 9. Voir aussi

- [pipeline_flow.md](pipeline_flow.md) — transport HTTP/3, Stall Guard, jar dans le pipeline
- [agentic_resilience.md](agentic_resilience.md) — résilience des tours agentiques
- [../../free-gemini-api/SECURITY.md](../../free-gemini-api/SECURITY.md) — rotation, chiffrement, permissions
- [../../free-gemini-api/THREAT_MODEL.md](../../free-gemini-api/THREAT_MODEL.md) — menace T-15 (fuite inter-sous-domaines)
- [../../gemini-extension/README.md](../../gemini-extension/README.md) — configuration du pont WebSocket
