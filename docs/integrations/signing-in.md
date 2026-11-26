---
title: Signing in
weight: 15
description: What a chat app presents to the crew liaison today, and what is planned for signing in a person.
---

A chat app that reaches a crew has two places where identity can matter: the
liaison's door, and the services a crew's tools call. This page states what is built.

## Today

- **The liaison has no sign-in of its own unless you enable the front below.** Without it, it runs no login page, no OAuth
  flow and no identity provider. It accepts one of three things, set in the chart:
  - No token and no Kubernetes token check: every caller is accepted, as anonymous.
  - A shared token (`liaison.auth.token`): the app sends
    `Authorization: Bearer <token>`. Everyone who holds the token is the same caller.
  - `liaison.auth.kubernetesTokens: true`: the liaison checks a Kubernetes bearer
    token with a TokenReview, so the caller is a Kubernetes identity (a user or a
    ServiceAccount). Each ticket belongs to the caller who asked.
  A request that presents a token that is not valid is refused. See
  [Knowing who is asking](../crew-liaison/#knowing-who-is-asking).
- **The discussion gateway verifies Kubernetes tokens per crew.** With
  `spec.discussion.access` on a crew, the gateway checks the caller's Kubernetes
  token, authorizes it against the Crew, and records the caller as the owner of the
  conversations it starts. The liaison passes the caller's token on to the gateway.
  See [Who is asking](../../concepts/who-is-asking/).
- **A chat app sends one static header.** Every app on these pages can attach a fixed
  `Authorization` header to its connection. The app holds the token; the person
  typing does not present their own. Two people using the same app instance with a
  shared token are one caller to Kubemoot.

Per-user placeholders some apps offer in headers (a user id or email in a header) are
sent to the liaison but are not read: identity comes from a verified token, never
from a header a client can set.

## The sign-in front

The liaison can run a sign-in front for an upstream service that signs people in with
plain OAuth (an authorization endpoint, a token endpoint, no ID token). It is off
unless `LIAISON_SIGNIN_UPSTREAM` is set.

To a chat app, the liaison is then the authorization server for its own `/mcp` door.
The app finds it through the usual MCP discovery, registers itself, and sends the
person to the liaison's sign-in. The liaison sends the person on to the upstream's
sign-in page. When they come back, two different tokens exist and they never mix:

- **The chat app holds the liaison's own token.** It is issued by the liaison, for the
  liaison's `/mcp`, and is good nowhere else. It is the only token `/mcp` accepts from a
  person who signed in.
- **The liaison keeps the upstream's token for that person.** It is sealed (encrypted),
  bound to the person, and used only for that person's questions. The chat app never
  holds it, and it is never in a redirect, an answer or a log.

`/mcp` does not accept the upstream's own token, even though the liaison could check it:
that token was issued for the upstream service, and a door accepts only tokens issued for
it. A chat app that presents one is answered 401 and signs in.

Why a front and not the upstream's own sign-in: an MCP app asks for a token for the
server it is calling and names that server as the `resource`. The front issues exactly
that, a token for this liaison's `/mcp`, and refuses a request that names any other
resource (`invalid_target`). It never passes the app's `resource` on, so the upstream
issues an ordinary token for its own service, to the liaison.

What the front does, in order:

1. The app asks `/mcp` with no token and is answered 401 with a
   `WWW-Authenticate` header that names `/.well-known/oauth-protected-resource/mcp`.
   That document names the liaison as the authorization server.
2. The app reads `/.well-known/oauth-authorization-server`, then registers at
   `/oauth/register`. The registration needs one to ten redirect addresses, each
   `https` or `http` on `localhost`, `127.0.0.1` or `[::1]`, and, when an allow-list is
   set, an allowed host for the `https` ones. The client id it returns is the
   registration sealed into a string, so no client record is stored.
3. The app sends the person to `/oauth/authorize` with a PKCE challenge (method S256 is
   required). The front checks the client id and redirect address, then sends the person
   to the upstream with its own registered client id, its own `/oauth/callback`, a fresh
   PKCE challenge and no `resource`. A short-lived cookie ties the return to the same
   browser.
4. The upstream sends the person back to `/oauth/callback`. The liaison exchanges the
   upstream's code itself, asks the service who the token belongs to (the verifier in
   `liaison.auth.serviceTokens`), and keeps the upstream's token for that person. It then
   redirects to the app with a code of its own that is valid for 60 seconds and works
   once. If the upstream refused the sign-in, its error code goes to the app; if the
   person cannot be named, the app is told `access_denied` and nothing is kept.
5. The app posts the code and its PKCE verifier to `/oauth/token` and receives the
   liaison's token pair: an access token for `/mcp` that lasts 15 minutes and a refresh
   token. A refresh token works once and each use returns a new one; presenting a spent
   one ends that app's sign-in, because it means the token was copied or replayed.
6. On each question the liaison reads its own token, finds the upstream's token it keeps
   for that person, renews it at the upstream when it is about to expire or the upstream
   stops accepting it, and presents it to the crew's gateway. A kept token must still name
   the same person, so it can never serve anyone else.

### What the liaison keeps, and for how long

Nothing is kept until a person signs in. Then the liaison keeps, sealed with the keys
below:

| Kept | For | Until |
|---|---|---|
| The upstream's access and refresh token | each person | 30 days without use (each use starts the 30 days again), never past the upstream's own limit, and deleted at once when the person signs out or the upstream refuses to renew it |
| One record per app that signed in | each app's sign-in | 30 days without a refresh, or sign-out |
| The id of a used code | 2 minutes | so a code cannot be used twice |

The upstream's limit is what its token answer states for the refresh token
(`refresh_expires_in` or `refresh_token_expires_in`), else
`LIAISON_SIGNIN_UPSTREAM_TOKEN_LIFETIME` counted from the sign-in, else, for an upstream
that issues no refresh token, the access token's own expiry. With none of the three, the
30 days of no use and the upstream's refusal are what end it.

Signing out is `POST /oauth/revoke` (RFC 7009) with the app's token, which chat apps call
when a person disconnects the server. It ends that app's sign-in and deletes the
upstream's token kept for the person at once. A person can also revoke the liaison's
access at the upstream service; the next question then finds the token refused, deletes
it and asks them to sign in.

The store is the liaison's own memory: nothing to install, and nothing written to the
cluster. It follows that the sign-in runs on one liaison replica (the chart refuses more),
and that a liaison restart asks everyone to sign in again. A store that replicas share
sits behind the same interface.

The front never logs a token, a code or a verifier.

### Upgrading from a liaison that handed the upstream's token to the app

Earlier the front handed the chat app the upstream's own token and `/mcp` accepted it.
After the upgrade that token is refused with 401 and the usual sign-in challenge, so
each connected chat app signs in once more (most do so on their own, some need the person
to reconnect the server). A person's id is computed the same way as before, so their
conversations stay theirs. The sign-in now needs `liaison.auth.serviceTokens`, which an
install that passed tokens to tools already has, and one liaison replica.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `LIAISON_SIGNIN_UPSTREAM` | empty (front off) | the upstream's authorization endpoint, `https` |
| `LIAISON_SIGNIN_UPSTREAM_TOKEN_URL` | none, required | the upstream's token endpoint, `https` |
| `LIAISON_SIGNIN_UPSTREAM_CLIENT_ID` | none, required | the front's own client id at the upstream. Register it once; it must stay the same, because the upstream binds refresh tokens to the client. Its registered redirect address is `<public URL>/oauth/callback`, an exact match |
| `LIAISON_SIGNIN_UPSTREAM_SCOPES` | `openid offline_access` | scopes asked of the upstream |
| `LIAISON_SIGNIN_UPSTREAM_NAME` | `upstream` | the name a person's account at the upstream is kept under, a DNS label |
| `LIAISON_SIGNIN_UPSTREAM_TOKEN_LIFETIME` | empty | the longest the upstream lets one sign-in last, as a duration such as `720h`, counted from the sign-in. A person's token is never kept past it |
| `LIAISON_SIGNIN_PUBLIC_URL` | none, required | the liaison's public base URL, for example `https://<liaison host>`. It is the issuer in the metadata, the base of the callback, and with `/mcp` the audience of every token the liaison issues |
| `LIAISON_SIGNIN_SEAL_KEYS` | none | the sealing keys, one `id:base64key` per line or comma, each key 32 random bytes in base64 |
| `LIAISON_SIGNIN_SEAL_KEYS_FILE` | none | the same list read from a file, for a mounted Secret. One of the two is required |
| `LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS` | empty (any host) | comma separated hosts an `https` redirect address may use. Loopback `http` addresses are always allowed |

Registration has no login and no consent page, so on a shared deployment always set
`LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS`. With it empty, anyone can register an `https`
redirect address on any host and ask a person to sign in to it; the front logs a warning
at start when it is empty.

Setting `LIAISON_SIGNIN_UPSTREAM` without the other required values stops the liaison at
start with a message naming the missing one. So does a sign-in with no verifier that names
the person (`ACCESS_SERVICE_TOKENS` and `ACCESS_HASH_KEY`, the same settings as the crew's
gateway): the liaison issues no token for someone it cannot name. The public URL, the
endpoints and the redirect addresses must be `https`, except for `localhost`.

The first key in the list seals new values; every listed key opens older ones. To rotate,
add the new key at the top, roll the liaison, and remove the old key after the longest
sealed value has expired (a code lasts 60 seconds, a sign-in in progress 10 minutes, an
access token 15 minutes, a refresh token 30 days, a client registration until you remove
its key, so apps register again after a removal). Generate a key with
`openssl rand -base64 32`.

A request to `/mcp` with no token, a token of the liaison's that expired, or any token the
liaison did not issue gets a 401 whose challenge points at the front's metadata at
`https://<liaison-host>/.well-known/oauth-protected-resource/mcp`, so the app knows where
to sign in. A Kubernetes token and the shared token work as they do without the front.

What the liaison does with the upstream's token it keeps for a person:

- It sends it to the crew's gateway on the question and on the stream, nowhere else.
- The follower that watches the discussion uses it only to open the stream, then holds none.
- When the token was renewed while a question is running, the next `get_answer` passes the
  new one to the gateway so a long question keeps its credential. It sends a token only
  when it changed, and only for the person who owns the ticket.
- It never logs or traces a token. A ticket keeps a keyed fingerprint of the last token
  sent, which cannot be turned back into the token.

Whether a crew's tools then use the person's own account is up to the crew's gateway; see
below.

### Chart values

The operator chart renders the variables above from `liaison.signIn`. It is off by default (`liaison.signIn.enabled: false`) and renders nothing when off. With `liaison.expose.enabled` on, the liaison's route also carries `/oauth` and `/.well-known`, so a chat app and a person's browser reach the front on the hostname.

| Chart value | Default | Becomes |
|-------------|---------|---------|
| `liaison.signIn.enabled` | `false` | Turns the front on. The chart refuses to render without the required values below |
| `liaison.signIn.publicURL` | empty, required | `LIAISON_SIGNIN_PUBLIC_URL` |
| `liaison.signIn.upstream.authorizeURL` | empty, required | `LIAISON_SIGNIN_UPSTREAM` |
| `liaison.signIn.upstream.tokenURL` | empty, required | `LIAISON_SIGNIN_UPSTREAM_TOKEN_URL` |
| `liaison.signIn.upstream.clientId` | empty, required | `LIAISON_SIGNIN_UPSTREAM_CLIENT_ID` |
| `liaison.signIn.upstream.scopes` | `openid offline_access` | `LIAISON_SIGNIN_UPSTREAM_SCOPES` |
| `liaison.signIn.upstream.name` | `upstream` | `LIAISON_SIGNIN_UPSTREAM_NAME` |
| `liaison.signIn.upstream.tokenLifetime` | empty | `LIAISON_SIGNIN_UPSTREAM_TOKEN_LIFETIME` |
| `liaison.signIn.sealKeySecret.name` | empty, required | A Secret you create in the release namespace. The chart mounts its key read-only and sets `LIAISON_SIGNIN_SEAL_KEYS_FILE`; the keys are never in the Deployment or its environment |
| `liaison.signIn.sealKeySecret.key` | `seal-keys` | The key in that Secret, holding the `id:base64key` list |
| `liaison.signIn.allowedRedirectHosts` | `[]` | `LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS`. Set it on a shared deployment |

The front is one for the whole liaison, not one per crew. It needs `liaison.auth.serviceTokens` ([Crew liaison](../crew-liaison/#naming-the-person-who-signed-in)), the verifier that names the person who signed in, and the chart refuses to render the sign-in without it or with more than one liaison replica. Set the matching `serviceTokens` on each crew whose gateway should accept that person.

## What a crew's tools sign in to

A crew's tools may call services that have their own accounts, such as a search
service. By default those credentials belong to the crew's installer and are kept in the
crew's tool gateway, not in the chat app: the tool uses the credential the installer
configured, whoever asks.

A crew that supports per-person sign-in (the chart's `perPerson` value, as in O'guide)
does the opposite: with the front above, each person signs in to the service once, the
liaison keeps that person's token, and the crew's tools use that person's own access for
their question. The chat app starts the
sign-in when the person clicks Authenticate on the server; see [LibreChat](../librechat/).
Treat a crew without per-person sign-in as acting with the installer's credentials, and
restrict who can reach the liaison to people the installer is willing to have use them.
