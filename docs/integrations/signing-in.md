---
title: Signing in
weight: 15
description: What a chat app presents to the crew liaison today, and what is planned for signing in a person.
---

A chat app that reaches a crew has two places where identity can matter: the
liaison's door, and the services a crew's tools call. This page states what is built.

## Today

- **The liaison has no sign-in of its own unless you enable the front below.** It does not run a login page, an OAuth
  flow, or an identity provider. It accepts one of three things, set in the chart:
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
sign-in page and, when they come back, hands the app the upstream's own token. The app
keeps the token and sends it with every question.

Why a front and not the upstream's own sign-in: an MCP app asks for a token for the
server it is calling and names that server as the `resource`. Some services refuse an
authorize request for a resource that is not theirs. The front never passes the app's
`resource` on, so the upstream issues an ordinary token for its own service.

What the front does, in order:

1. The app asks `/mcp` with no token and is answered 401 with a
   `WWW-Authenticate` header that names `/.well-known/oauth-protected-resource/mcp`.
   That document names the liaison as the authorization server.
2. The app reads `/.well-known/oauth-authorization-server`, then registers at
   `/oauth/register`. The registration needs one to ten redirect addresses, each
   `https` or `http` on `localhost`, `127.0.0.1` or `[::1]`, and, when an allow-list is
   set, an allowed host for the `https` ones. The client id it returns is the
   registration sealed into a string, so the front stores nothing.
3. The app sends the person to `/oauth/authorize` with a PKCE challenge (method S256 is
   required). The front checks the client id and redirect address, then sends the person
   to the upstream with its own registered client id, its own `/oauth/callback`, a fresh
   PKCE challenge and no `resource`. A short-lived cookie ties the return to the same
   browser.
4. The upstream sends the person back to `/oauth/callback`. The front redirects to the
   app with a code that is valid for 60 seconds. If the upstream refused the sign-in,
   its error goes to the app unchanged.
5. The app posts the code and its PKCE verifier to `/oauth/token`. The front checks
   them, exchanges the upstream code, and returns the upstream's answer as it received
   it. A `refresh_token` request is passed to the upstream the same way, so a rotated
   refresh token reaches the app.

The front keeps no state. State, codes and client ids are encrypted strings (AES-GCM),
so two liaison replicas work together and a restart loses nothing. An upstream code can
be used once, so a replayed code fails at the upstream. The front never logs a token, a
code or a verifier.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `LIAISON_SIGNIN_UPSTREAM` | empty (front off) | the upstream's authorization endpoint, `https` |
| `LIAISON_SIGNIN_UPSTREAM_TOKEN_URL` | none, required | the upstream's token endpoint, `https` |
| `LIAISON_SIGNIN_UPSTREAM_CLIENT_ID` | none, required | the front's own client id at the upstream. Register it once; it must stay the same, because the upstream binds refresh tokens to the client. Its registered redirect address is `<public URL>/oauth/callback`, an exact match |
| `LIAISON_SIGNIN_UPSTREAM_SCOPES` | `openid offline_access` | scopes asked of the upstream |
| `LIAISON_SIGNIN_PUBLIC_URL` | none, required | the liaison's public base URL, for example `https://<liaison host>`. It is the issuer in the metadata and the base of the callback |
| `LIAISON_SIGNIN_SEAL_KEYS` | none | the sealing keys, one `id:base64key` per line or comma, each key 32 random bytes in base64 |
| `LIAISON_SIGNIN_SEAL_KEYS_FILE` | none | the same list read from a file, for a mounted Secret. One of the two is required |
| `LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS` | empty (any host) | comma separated hosts an `https` redirect address may use. Loopback `http` addresses are always allowed |

Registration has no login and no consent page, so on a shared deployment always set
`LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS`. With it empty, anyone can register an `https`
redirect address on any host and ask a person to sign in to it; the front logs a warning
at start when it is empty.

Setting `LIAISON_SIGNIN_UPSTREAM` without the other required values stops the liaison at
start with a message naming the missing one. The public URL, the endpoints and the
redirect addresses must be `https`, except for `localhost`.

The first key in the list seals new values; every listed key opens older ones. To rotate,
add the new key at the top, roll the liaison, and remove the old key after the longest
sealed value has expired (a code lasts 60 seconds, a sign-in in progress 10 minutes, a
client registration until you remove its key, so apps register again after a removal).
Generate a key with `openssl rand -base64 32`.

The door accepts the token the front hands the app. With service token verifiers set
(`ACCESS_SERVICE_TOKENS` and `ACCESS_HASH_KEY`, the same settings as the crew's gateway),
the liaison checks a person's own token by asking the service that issued it, names the
person by a hashed id, and makes them the owner of their tickets. A token that is missing or
refused gets a 401 whose challenge points at the front's metadata at
`https://<liaison-host>/.well-known/oauth-protected-resource/mcp`, so the app knows where to
sign in.

What the liaison does with the person's token:

- It sends it to the crew's gateway on the question and on the stream, nowhere else.
- The follower that watches the discussion uses it only to open the stream, then holds none.
- When the app sends a fresher token on a later `get_answer`, the liaison passes it to the
  gateway so a long question keeps its credential. It sends a token only when it changed, and
  only for the person who owns the ticket.
- It never stores, logs or traces a token. A ticket keeps a keyed fingerprint of the last
  token sent, which cannot be turned back into the token.

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
| `liaison.signIn.sealKeySecret.name` | empty, required | A Secret you create in the release namespace. The chart mounts its key read-only and sets `LIAISON_SIGNIN_SEAL_KEYS_FILE`; the keys are never in the Deployment or its environment |
| `liaison.signIn.sealKeySecret.key` | `seal-keys` | The key in that Secret, holding the `id:base64key` list |
| `liaison.signIn.allowedRedirectHosts` | `[]` | `LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS`. Set it on a shared deployment |

The front is one for the whole liaison, not one per crew. To hand the person's token to a crew's tools, also set `liaison.auth.serviceTokens` ([Crew liaison](../crew-liaison/#a-persons-own-token-from-a-service)) and the matching `serviceTokens` on the crew.

## What a crew's tools sign in to

A crew's tools may call services that have their own accounts, such as a search
service. Those credentials belong to the crew's installer and are kept in the
crew's tool gateway, not in the chat app. The asker's own account at such a service
is not used today: the tool uses the credential the installer configured, whoever asks.

## Planned

Per-person sign-in to a crew's service is planned: a person would connect their own
account at the service a crew's tool calls (through that service's own sign-in page),
and the tool would use that person's access for their question only. It is not
built, and the chat apps on these pages have nothing to configure for it. Until it
exists, treat a crew's tool credentials as the installer's, and restrict who can reach
the liaison to people the installer is willing to have use them.
