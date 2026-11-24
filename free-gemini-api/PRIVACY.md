# Politique de confidentialité et de rétention

**Dernière mise à jour :** 21 novembre 2026

Cette politique décrit les données traitées par **Free Gemini API** (le
« Service »), leur finalité, leur durée de conservation et les moyens offerts
pour les supprimer.

## 1. Responsable du traitement

Le Service est exploité par l'instance qui l'héberge. En déploiement local
mono-utilisateur, l'exploitant et l'utilisateur sont la même personne. En
déploiement partagé, l'exploitant doit compléter ce document avec son identité
et ses coordonnées de contact.

## 2. Données traitées

| Catégorie | Contenu | Finalité |
|---|---|---|
| Messages de chat | Prompt utilisateur, réponse du modèle, modèle utilisé, identifiant de conversation, identifiant de session | Fournir la réponse, afficher l'historique, produire les statistiques |
| Journaux de requêtes | Endpoint, méthode HTTP, adresse IP, code de statut, latence | Sécurité, diagnostic, mesure de disponibilité |
| Métadonnées de médias | Type (image/vidéo/musique), prompt, nom de fichier, URL, identifiant de réponse | Retrouver les fichiers générés |
| Comptes | Identifiant de compte, fichier de cookies, palier, compteur d'usage | Répartir la charge entre comptes Google |
| Cookies Google | Cookies de session fournis par l'extension Chrome | Authentifier les appels à Gemini |

Le Service **ne collecte pas** de données de paiement, de contacts, de données
de localisation précises ni de données de santé. Les prompts sont transmis à
Google Gemini via le compte configuré ; ils sont soumis aux conditions et à la
politique de confidentialité de Google.

## 3. Base légale et finalité

- Exécution du service demandé par l'utilisateur (article 6.1.b du RGPD).
- Intérêt légitime de l'exploitant pour la sécurité et la prévention des abus
  (article 6.1.f), limité aux journaux techniques.
- Consentement de l'utilisateur pour la synchronisation des cookies Google via
  l'extension Chrome.

## 4. Durée de conservation

La durée par défaut est de **30 jours**. Elle est configurable avec la variable
d'environnement `RETENTION_DAYS` :

```bash
RETENTION_DAYS=30   # valeur par défaut
RETENTION_DAYS=7    # conservation courte
RETENTION_DAYS=0    # désactive la purge automatique (déconseillé)
```

Un travailleur de rétention s'exécute au démarrage puis toutes les 24 heures et
supprime :

- les messages de plus de `RETENTION_DAYS` jours ;
- les métadonnées de médias de plus de `RETENTION_DAYS` jours ;
- les journaux de requêtes de plus de `RETENTION_DAYS` jours.

Les fichiers médias présents dans `output/` ne sont pas supprimés
automatiquement par ce travailleur ; l'exploitant doit appliquer sa propre
politique de purge des fichiers si nécessaire.

### 4.1 Journaux sans contenu

Les journaux de tour (`📊 [TURN SUMMARY]`) ne contiennent **aucun prompt brut,
aucun argument d'outil, aucun cookie**. Ils se limitent à des longueurs, des
hashes tronqués SHA-256 et des labels normalisés (`replay=<reason>`,
`class=<label>`, `finish=<stop|error>`). Les identifiants de session et de
conversation y figurent sous forme hachée. Cette conception permet de
diagnostiquer une dérive comportementale sans constituer un nouveau traitement
de données à caractère personnel.

Les journaux de requêtes (endpoint, méthode, IP, statut, latence) restent
soumis à `RETENTION_DAYS` et ne sont jamais exportés dans les rapports
analytiques.

## 5. Droit à l'effacement (article 17)

La route suivante supprime tous les messages et métadonnées de médias associés
à une conversation :

```bash
curl -X DELETE "http://127.0.0.1:8100/history/<conversation_id>" \
  -H "Authorization: Bearer $API_TOKEN"
```

Réponse :

```json
{
  "status": "deleted",
  "conversation_id": "<conversation_id>",
  "messages_deleted": 4
}
```

L'appel est authentifié et idempotent : supprimer une conversation inconnue
renvoie `200` avec `messages_deleted: 0`. Les fichiers médias déjà écrits sur
disque doivent être supprimés séparément par l'exploitant.

## 6. Sécurité des cookies Google

Les cookies de session Google sont des secrets d'authentification. Ils sont
stockés dans `cookies/` avec les permissions `0600` et le répertoire avec
`0700`. Ils ne doivent jamais être :

- commités dans un dépôt Git ;
- envoyés à un service tiers ;
- inclus dans un billet, un journal ou une capture d'écran.

### 6.1 Chiffrement au repos

Recommandé : définir `COOKIE_ENCRYPTION_KEY` pour chiffrer les fichiers
`cookies/account_*.json` et les caches `cookies/session_*.json` en
**AES-256-GCM** avant leur écriture atomique.

```bash
openssl rand -hex 32   # génère une clé de 32 octets
```

- Une valeur de **64 caractères hexadécimaux** est utilisée directement comme
  clé ; toute autre valeur passe par PBKDF2-HMAC-SHA256 (100 000 itérations).
- Les fichiers historiques en clair restent lisibles et sont réécrits sous
  forme chiffrée dès qu'ils sont lus ou resynchronisés avec une clé active.
- `REQUIRE_COOKIE_ENCRYPTION=true` refuse le démarrage si la clé est absente.

Conséquence pour la conformité : un vol de disque ou une sauvegarde non
chiffrée ne révèle plus les cookies. La clé, elle, doit être protégée
séparément (KMS, Vault ou secrets Docker) — voir
[`THREAT_MODEL.md`](THREAT_MODEL.md) §4.

### 6.2 Isolation par sous-domaine

Un export Chrome contient les mêmes noms de cookies (`OSID`,
`__Secure-OSID`, `COMPASS`) pour `gemini.google.com`, `myaccount.google.com` et
`console.cloud.google.com`, avec des **valeurs différentes**. Le proxy utilise
un jar RFC 6265 (`publicsuffix`) et enregistre chaque cookie contre **l'hôte
unique qui le possède** ; un cookie host-only est stocké sans attribut
`Domain`. Les cookies des hôtes frères ne sont donc jamais injectés dans
l'identité Gemini, et l'en-tête `Cookie` est résolu en direct à chaque requête.

Détail technique et tests :
[`../docs/architecture/cookie_scoping_rfc6265.md`](../docs/architecture/cookie_scoping_rfc6265.md).

### 6.3 Rotation après exposition

En cas de compromission suspectée, révoquer les sessions Google du compte
concerné depuis `https://myaccount.google.com/security`, supprimer les anciens
fichiers `cookies/account_*.json` / `session_*.json`, puis resynchroniser les
cookies avec l'extension. La procédure complète figure dans
[`SECURITY.md`](SECURITY.md) §4.

## 7. Destinataires et transferts

Les prompts et médias sont transmis à Google (Gemini) pour génération. Aucun
autre destinataire n'est configuré par défaut. L'exploitant qui ajoute un
reverse proxy, un service d'observabilité ou un stockage externe doit documenter
ces destinataires et les garanties de transfert applicables.

## 8. Droits des personnes

Pour toute question ou demande d'accès, de rectification, d'effacement ou de
portabilité, contacter l'exploitant de l'instance. La suppression d'une
conversation peut être effectuée immédiatement avec l'endpoint décrit à la
section 5.

## 9. Modifications

Cette politique peut être mise à jour pour refléter un changement de
configuration ou de réglementation. La date de dernière mise à jour figure en
tête du document.
