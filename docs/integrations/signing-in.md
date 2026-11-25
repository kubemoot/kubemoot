---
title: Signing in
weight: 15
description: What a chat app presents to the crew liaison today, and what is planned for signing in a person.
---

A chat app that reaches a crew has two places where identity can matter: the
liaison's door, and the services a crew's tools call. This page states what is built.

## Today

- **The liaison has no sign-in of its own unless you enable one of the two below.** Without them it does not run a login
  page, an OAuth flow, or an identity provider. It accepts one of three things, set in the chart:
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
| `liaison.signIn.publicURL` | empty, required | `LIAISON_SIGNIN_PUBLIC_URL`. Must be `https://` plus the door hostname (`liaison.expose.hostname`, else `gateway.hostname`); the chart refuses to render any other value, because the discovery documents and redirect addresses must name the host the route serves |
| `liaison.signIn.upstream.authorizeURL` | empty, required | `LIAISON_SIGNIN_UPSTREAM` |
| `liaison.signIn.upstream.tokenURL` | empty, required | `LIAISON_SIGNIN_UPSTREAM_TOKEN_URL` |
| `liaison.signIn.upstream.clientId` | empty, required | `LIAISON_SIGNIN_UPSTREAM_CLIENT_ID` |
| `liaison.signIn.upstream.scopes` | `openid offline_access` | `LIAISON_SIGNIN_UPSTREAM_SCOPES` |
| `liaison.signIn.sealKeySecret.name` | empty, required | A Secret you create in the release namespace. The chart mounts its key read-only and sets `LIAISON_SIGNIN_SEAL_KEYS_FILE`; the keys are never in the Deployment or its environment |
| `liaison.signIn.sealKeySecret.key` | `seal-keys` | The key in that Secret, holding the `id:base64key` list |
| `liaison.signIn.allowedRedirectHosts` | `[]` | `LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS`. Set it on a shared deployment |

The front is one for the whole liaison, not one per crew. To hand the person's token to a crew's tools, also set `liaison.auth.serviceTokens` ([Crew liaison](../crew-liaison/#a-persons-own-token-from-a-service)) and the matching `serviceTokens` on the crew.

## The door's own sign-in

The liaison can sign people in itself, through any standard OpenID Connect issuer your install names. The person who
signs in is the person asking: their identity, not an outside service's token, is what the crews see. It is off by
default and is an alternative to the sign-in front above: set one, not both.

To a chat app the liaison is the authorization server for its own `/mcp` door, and the only resource it issues tokens
for. What happens, in order:

1. The app asks `/mcp` with no token and is answered 401 with a `WWW-Authenticate` header that names
   `/.well-known/oauth-protected-resource/mcp`. The metadata at `/.well-known/oauth-authorization-server` lists the
   endpoints, PKCE method `S256`, `authorization_response_iss_parameter_supported` and
   `client_id_metadata_document_supported`.
2. The app identifies itself by dynamic registration at `/oauth/register`, or with a client ID metadata document (below).
3. The app sends the person to `/oauth/authorize` with a PKCE challenge (S256 is required) and the `resource` it wants a
   token for, which must be `https://<liaison host>/mcp`; any other value is `invalid_target`.
4. The person is sent to your issuer: an authorization code flow with PKCE S256, a nonce, and a state that is tied to the
   browser that started it. The ID token is checked for its signature against the issuer's keys, its issuer, its
   audience and its expiry, and its nonce must be this sign-in's.
5. The door shows a consent page naming the app, the host it will send the person back to and, for an app that
   identifies itself by address, that address. The person allows or denies. The page is shown once per app and host in a
   browser; a sealed cookie remembers it for 180 days, for that person only.
6. The door redirects to the app with a code that is valid for 60 seconds and works once, and the `iss` parameter. The
   app posts the code and its PKCE verifier to `/oauth/token` and receives an access token and a refresh token.

**The person's name for RBAC.** The user is `<prefix><subject>` and every group is `<prefix><group>`, with the prefix
`oidc:` unless you set another. The prefix keeps the issuer from naming a Kubernetes system identity or a group an
administrator bound for another purpose: a name that would start with `system:` is refused. Groups come from the ID
token's groups claim (`groups` unless you set another claim), at most 64, each at most 256 characters. The token must
carry them; the door does not call the issuer's userinfo endpoint. Bind roles to these names. For example, to let
everyone in the group `team-a` ask a crew, bind a Role that grants `get` on that Crew to the group `oidc:team-a`.

**Tokens.**

| Token | Lifetime | What it is |
|-------|----------|------------|
| Access token | 15 minutes | A signed JWT (ES256) with `iss`, `aud` (the `/mcp` resource), `sub`, `iat`, `exp` and a unique `jti`. `/mcp` refuses a token for any other audience, an expired one, one signed by a key the door does not list, and one without the access-token type |
| Refresh token | 30 days idle, 168 hours at most | A sealed (encrypted) string. Each use returns a new one; showing a used one again, or a used code again, ends that whole sign-in. Rotation never extends the 168 hours (`LIAISON_SIGNIN_REFRESH_MAX_HOURS`), so the person signs in again then |

The door keeps no store. The one thing it remembers is in the process, and a restart empties it: which codes and refresh tokens it has seen, so a
code works once and a refresh token rotates. After a restart, a code or refresh token that was already used works once more until it expires, and an ended sign-in is not remembered as ended. With more than one liaison replica each has its own memory, so a stolen
code or refresh token can be used once on each replica until it expires; run one replica where that matters. Signing keys
and sealing keys are shared, so any replica verifies a token another issued. `/mcp` also still accepts the shared token
(`liaison.auth.token`) and Kubernetes tokens when you enable them; it never accepts a service's own token when the
door's identity is on (the chart refuses `liaison.auth.serviceTokens` beside it), and it is never open: with the door on and no other credential configured, a request with no
token is refused.

A refresh does not ask the issuer again, so a person removed from a group at the issuer keeps the groups they signed in with until the sign-in ends (at most 168 hours by default). `liaison.auth.allowAnonymous` applies only beside the shared token or Kubernetes tokens: with the door alone, a request with no token is always refused. Any account at the issuer can sign in to the door; what it may then do is decided by RBAC on the person's prefixed user and groups, so restrict who can sign in at the issuer when it is a public one.

**The consent page and the sign-in cookie.** The sign-in cookie is sealed, `HttpOnly`, `Secure` (the public address is
`https`), `SameSite=Lax`, scoped to `/oauth`, and ends after 10 minutes idle or 8 hours. Every form post must carry a
token made for that person and that one request, and a post that a browser says came from another site is refused.
Pages are never cached, cannot be framed, send no referrer, and carry a policy that allows nothing but their own style.

**Client ID metadata documents.** An app may use an `https` address as its `client_id`. The door fetches the document at
that address and reads the app's name and redirect addresses from it. The document must name its own address as
`client_id`, list one to ten redirect addresses that pass the same rule as registration (including
`allowedRedirectHosts`), and hold no client secret or key. The request is guarded, because an attacker chooses the
address:

- Only `https`, with a path, no credentials, no fragment and no dot segments.
- Every address the name resolves to must be a public one. Private, loopback, link-local, shared (100.64.0.0/10),
  documentation, reserved and IPv6 transition addresses are refused, and the connection goes to an address that was
  checked, so a name cannot change its answer between the check and the connection. No proxy is used.
- No redirect is followed. The answer must be `200` with `application/json`, at most 16 KiB, within 5 seconds.
- At most five fetches a second, with a burst of ten, for the whole liaison (one shared limit, so a flood of requests for documents can make other apps that identify themselves this way wait; apps that register dynamically are not affected).

An app that fails any check is answered `unknown client` in the browser, with no detail. A redirect address that the
document does not name is refused the same way. Dynamic registration keeps working beside it.

**Limits today.** The door's identity is OpenID Connect only. `export_conversation` through a person signed in to the
door is served as the liaison itself, which the operator refuses for a crew that verifies callers. The consent
cookie is per browser, so the same person at a second browser is asked once more.

### Configuration of the door's identity

| Variable | Default | Meaning |
|---|---|---|
| `LIAISON_SIGNIN_IDENTITY` | empty (off) | `oidc` turns the door's identity on. Setting `LIAISON_SIGNIN_UPSTREAM` as well stops the liaison at start |
| `LIAISON_SIGNIN_PUBLIC_URL` | none, required | the door's public address, `https://<liaison host>` with no path. It is the issuer of the access tokens and the origin of the pages |
| `LIAISON_SIGNIN_SEAL_KEYS(_FILE)` | none, required | sealing keys, as for the sign-in front |
| `LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS` | empty (any host) | hosts an `https` redirect address may use. Always set it on a shared install |
| `LIAISON_SIGNIN_OIDC_ISSUER` | none, required | the issuer's URL, `https`. The liaison reads `/.well-known/openid-configuration` from it on the first sign-in, not at start, so an issuer that is down does not keep the liaison down |
| `LIAISON_SIGNIN_OIDC_CLIENT_ID` | none, required | the door's client id at the issuer. Register `<public URL>/oauth/callback` as its redirect address |
| `LIAISON_SIGNIN_OIDC_CLIENT_SECRET_FILE` | none, required | a file holding the client secret, from a mounted Secret |
| `LIAISON_SIGNIN_OIDC_SCOPES` | `openid profile` | scopes asked of the issuer |
| `LIAISON_SIGNIN_OIDC_GROUPS_CLAIM` | `groups` | the ID token claim that lists the person's groups |
| `LIAISON_SIGNIN_OIDC_USER_PREFIX` | `oidc:` | the prefix of the user name and of every group |
| `LIAISON_SIGNIN_SIGNING_KEYS_FILE` | none, required | ECDSA P-256 private keys as PEM, at most four. The first signs access tokens; every key verifies |
| `LIAISON_SIGNIN_HOP_KEYS_FILE` | none | ECDSA P-256 private keys as PEM. The first signs the assertion of who is asking that goes to a crew's gateway. Without it nothing is asserted to a crew |
| `LIAISON_SIGNIN_REFRESH_MAX_HOURS` | `168` | the most a sign-in lasts, 1 to 2160 |

Make a key with `openssl ecparam -name prime256v1 -genkey -noout | openssl pkcs8 -topk8 -nocrypt`. To rotate the
signing key, put the new key first and keep the old one listed until its tokens have expired (15 minutes), then remove it.

The chart renders these from `liaison.signIn.identity`:

| Chart value | Default | Becomes |
|-------------|---------|---------|
| `liaison.signIn.enabled` | `false` | Required, with `publicURL` and `sealKeySecret`, as for the front |
| `liaison.signIn.identity.provider` | empty | `oidc` turns it on. The chart refuses to render it with `liaison.signIn.upstream.authorizeURL` or `liaison.auth.serviceTokens` set, and refuses an empty `allowedRedirectHosts` when `liaison.expose.enabled` is on |
| `liaison.signIn.identity.oidc.issuer`, `clientId` | empty, required | the issuer and the client id |
| `liaison.signIn.identity.oidc.clientSecret.name`, `key` | empty, required; `client-secret` | a Secret you create, mounted read-only; the secret is never in the environment |
| `liaison.signIn.identity.oidc.scopes`, `groupsClaim`, `userPrefix` | `openid profile`, `groups`, `oidc:` | the matching variables |
| `liaison.signIn.identity.signingKeySecret.name`, `key` | empty, required; `signing-keys` | a Secret you create, mounted read-only |
| `liaison.signIn.identity.hopKeySecret.name`, `key` | empty; `hop-keys` | optional; see [A door that signs people in](../../concepts/who-is-asking/#a-door-that-signs-people-in) |
| `liaison.signIn.identity.refreshMaxHours` | `168` | `LIAISON_SIGNIN_REFRESH_MAX_HOURS` |

The chart also turns on caller verification for the liaison (`ACCESS_ENFORCE`), with anonymous callers off unless
`liaison.auth.allowAnonymous` is set.

## What a crew's tools sign in to

A crew's tools may call services that have their own accounts, such as a search
service. By default those credentials belong to the crew's installer and are kept in the
crew's tool gateway, not in the chat app: the tool uses the credential the installer
configured, whoever asks.

A crew that supports per-person sign-in (the chart's `perPerson` value, as in O'guide)
does the opposite: with the front above, each person signs in to the service once, and the
crew's tools use that person's own access for their question. The chat app starts the
sign-in when the person connects the server; see [Claude Desktop](../claude-desktop/#connect-with-sign-in).
Treat a crew without per-person sign-in as acting with the installer's credentials, and
restrict who can reach the liaison to people the installer is willing to have use them.
