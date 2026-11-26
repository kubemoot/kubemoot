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

**The person's name.** The user is `<prefix><subject>` and every group is `<prefix><group>`, with the prefix
`oidc:` unless you set another. The prefix keeps the issuer from naming a Kubernetes system identity or a group an
administrator bound for another purpose: a name that would start with `system:` is refused. Groups come from the ID
token's groups claim (`groups` unless you set another claim), at most 64, each at most 256 characters. The token must
carry them; the door does not call the issuer's userinfo endpoint. A crew with the `kubernetesRBAC` rule decides by binding roles to these names: to let
everyone in the group `team-a` ask such a crew, bind a Role that grants `get` on that Crew to the group `oidc:team-a`. A crew with the `issuerGroups` rule lists the same names, such as `oidc:team-a`.

**Tokens.**

| Token | Lifetime | What it is |
|-------|----------|------------|
| Access token | 15 minutes | A signed JWT (ES256) with `iss`, `aud` (the `/mcp` resource), `sub`, `iat`, `exp` and a unique `jti`. `/mcp` refuses a token for any other audience, an expired one, one signed by a key the door does not list, and one without the access-token type |
| Refresh token | 30 days idle, 24 hours at most | A sealed (encrypted) string. Each use returns a new one; showing a used one again, or a used code again, ends that whole sign-in. Rotation never extends the 24 hours (`LIAISON_SIGNIN_REFRESH_MAX_HOURS`), so the person signs in again then |

The door keeps no record of a person. The one thing it remembers is which codes and refresh tokens it has seen, so a
code works once and a refresh token rotates. By default that memory is in the process: a restart empties it, a code or
refresh token that was already used works once more until it expires, and with more than one liaison replica each has its
own memory, so a stolen code or refresh token can be used once on each replica until it expires. Set
`liaison.state.backend` to `nats` (the chart picks it when the door is on and `nats.enabled` is true) and the marks live in a
NATS JetStream key-value bucket every replica reads, each sealed with the door's own keys and gone when the code or sign-in
would have expired anyway. A used code or refresh token is then refused on every replica, and a restart forgets nothing. If the
shared store cannot answer, the door refuses: a doubt never admits a code. Signing keys and sealing keys are shared, so any replica verifies a
token another issued. `/mcp` also still accepts the shared token
(`liaison.auth.token`) and Kubernetes tokens when you enable them; it never accepts a service's own token when the
door's identity is on (the chart refuses `liaison.auth.serviceTokens` beside it), and it is never open: with the door on and no other credential configured, a request with no
token is refused.

A refresh does not ask the issuer again, so a person removed from a group at the issuer keeps the groups they signed in with until the sign-in ends (at most 24 hours by default). `liaison.auth.allowAnonymous` applies only beside the shared token or Kubernetes tokens: with the door alone, a request with no token is always refused. Any account at the issuer can sign in to the door; which crews it may then ask is decided by each crew's `askers` rule ([Crew reference](../../reference/crew/#a-door-that-signs-people-in)), so restrict who can sign in at the issuer when it is a public one and give a crew the rule that fits it.

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

**Limits today.** The door's identity is OpenID Connect only. The consent
cookie is per browser, so the same person at a second browser is asked once more. A person signed in at the door can export
their own conversations with `export_conversation` (the operator is shown the same assertion the crew's gateway is shown and
matches the person to the conversation's owner), on a crew whose askers rule is not `kubernetesRBAC`; they can export nothing else.

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
| `LIAISON_SIGNIN_REFRESH_MAX_HOURS` | `24` | the most a sign-in lasts, 1 to 2160 |

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
| `liaison.signIn.identity.refreshMaxHours` | `24` | `LIAISON_SIGNIN_REFRESH_MAX_HOURS` |

The chart also turns on caller verification for the liaison (`ACCESS_ENFORCE`), with anonymous callers off unless
`liaison.auth.allowAnonymous` is set.

### Connected accounts

A crew can need an outside service that entitles each person: a library, a document store. The person connects their own
account once, the door keeps a sealed token for them, and the crew's tools act as that person there. The chat app never
holds the service's token and never passes it on: it holds a token for the door alone. The declaration a crew makes is in the
[connected accounts reference](../../reference/connected-accounts/).

**Kubemoot keeps no one's data by default.** Nothing is kept unless the install turns this on
(`liaison.connectedAccounts.enabled`), a crew declares the account, **and** the person presses Connect. What is kept, per person
and per account, is one sealed record: the service's refresh and access tokens, the scopes granted, when it was connected and
which crews the person agreed may use it. It is encrypted (AES-GCM) with keys of its own, with the person and the account as
additional authenticated data, so a record copied to another person's or account's key fails to open; the store holds ciphertext and hashed key names only.

| Ends when | How |
|-----------|-----|
| The person disconnects | The accounts page asks the service to revoke the token (RFC 7009) when the account declares an endpoint, then deletes the record whether or not the service answered, and tells the person which happened |
| The person signs out | The door's `/oauth/revoke` (a chat app signing out, RFC 7009) and the Sign out button on the accounts page delete every record the person has |
| It goes unused for 30 days | Every use writes the record again with a time to live of 30 days; the store drops it when that passes |
| The service's own lifetime ends | The time to live is never longer than the service's: the lifetime its token answer states for the refresh token, the access token's end when there is no refresh token, or the account's `maxLifetimeHours` from the install |
| A crew stops declaring the account | The next lease checks the declaration, so the token stops being used at once |

**The pages.** All are under `/oauth`, over `https`, never cached, never framed, send no referrer and allow no script.

| Page | What it does |
|------|--------------|
| `GET /oauth/connect/<account>?c=<handle>` | The link the liaison hands a person who has to connect. The handle is sealed, names the person, the account and the crew, expires in 10 minutes and grants nothing alone. A person with no door session signs in at your issuer and returns here. If the signed-in person is not the one the link names (Alice's link opened by Bob), the page refuses and the refusal is logged without any detail of either. Otherwise it shows what is asked of the service, the crews that will use the account, what is kept and for how long, and a Connect button |
| `POST /oauth/connect/<account>` | Needs the session, a token made for this person and this handle, and a same-site post. A handle starts one connection. The person is sent to the service's authorization address with the install's client id, PKCE S256, the declared scopes and a sealed state; the `resource` a chat app sent is never passed on |
| `GET /oauth/callback` | The same address the sign-in uses. The state must be ours, unexpired, tied to this browser by cookie and to the same signed-in person, and an `iss` the service sends (RFC 9207) must be the account's `issuer` or, when the install gave none, an `https` address on the host of the service's authorization endpoint. The code is exchanged at the service's token endpoint; nothing is kept when any step fails. The page that follows says Connected and shows no token |
| `GET /oauth/accounts` | The person's accounts, each with a Disconnect button, a Connect link for those not yet connected, and Sign out |
| `POST /oauth/accounts/disconnect`, `POST /oauth/signout` | State-changing, so each needs the session and its own token |

No token appears in a page, a redirect, a URL or a log line. The service's authorization address is the only place the person
leaves the door, and the service's answers are read, bounded and never echoed.

**Who a person is.** A connection is kept under the id a crew's gateway gives the person it is asked by: the install's keyed hash of the
door's assertion issuer and the user name. That is why connected accounts need `liaison.auth.hashKeySecret` (the same value each crew names in
`hashKeySecretRef`), and why they serve crews whose askers rule is not `kubernetesRBAC`: those people are Kubernetes users and carry
Kubernetes tokens of their own.

**The broker.** A crew's tools never see the store. The crew's discussion gateway asks the liaison's account broker, on its own
internal port (`liaison.connectedAccounts.brokerPort`, never behind the route), with a ServiceAccount token made for that broker
alone and checked with a TokenReview. The crew comes from the caller's ServiceAccount (`<crew>-discussion`), never from the
request. The broker answers a lease with the token, when it ends and the hosts it may be sent to, or one of three reasons:
`NO_GRANT` (not connected, or not offered to this crew), `NEEDS_RECONNECT` (the service no longer honors it, or the person never agreed to
this crew) and `EXPIRED`. A token about to end is refreshed once however many callers ask, and a rotated refresh token is written with a
compare-and-swap before the new access token is returned, so a second replica that lost the race uses the winner's record and never a spent token. The service
answering `invalid_grant` marks the connection for reconnecting and the person is sent a link.

**The `connectedAccount` asker rule.** A crew whose askers rule is `connectedAccount` admits a person only while they hold a live
connection to that account. The gateway asks the broker, by the person's opaque id, whether one exists. A gateway with no broker
configured, or one the broker does not answer, refuses: no answer is never a yes. The liaison tells the person to connect before
the question starts (see [the crew liaison](../crew-liaison/#connecting-outside-accounts)); the gateway's refusal is the backstop.

#### Configuration of connected accounts

| Variable | Default | Meaning |
|---|---|---|
| `LIAISON_STATE_BACKEND` | `memory` | where state is kept: `memory`, `nats` or `openbao`. `nats` and `openbao` share it across replicas |
| `NATS_URL` and the other `NATS_*` names | none | the connection every Kubemoot component reads ([Connecting to NATS](../../operating/nats-connection/)); needed for `nats`. The liaison keeps its state in the buckets `kubemoot_liaison_door` and `kubemoot_liaison_accounts` |
| `LIAISON_OPENBAO_ADDR`, `_MOUNT`, `_PREFIX`, `_TOKEN_FILE`, `_CA_FILE` | none; mount `secret`; prefix `kubemoot-liaison` | an OpenBao (or compatible) KV version 2 mount. The address must be `https`. The token is read from its file on each call |
| `LIAISON_ACCOUNTS_ENABLED` | `false` | `true` turns connected accounts on; the liaison stops at start if the door's identity, the hash key or the keys below are missing |
| `LIAISON_ACCOUNTS_SEAL_KEYS_FILE` | none, required when on | `id:base64key` lines, apart from the door's keys. The first seals; older ones still open |
| `LIAISON_ACCOUNTS` | empty | JSON object of registrations by account name: `clientId`, `clientSecretFile`, `issuer`, `allowedCrews`, `maxLifetimeHours` |
| `LIAISON_INTERNAL_PORT` | `8081` | the broker's port |
| `ACCESS_HASH_KEY` | none, required when on | the install's hash key |

| Chart value | Default | Becomes |
|-------------|---------|---------|
| `liaison.state.backend` | empty | empty picks `nats` when the door is on and `nats.enabled`, else `memory`. The chart refuses an unknown value, `nats` without `nats.enabled`, and `openbao` without an address and token Secret |
| `liaison.state.openbao.*` | see values | address, mount, prefix, a token Secret and an optional CA Secret, mounted read-only |
| `liaison.connectedAccounts.enabled` | `false` | `LIAISON_ACCOUNTS_ENABLED`. The chart refuses it without the door's identity, `sealKeySecret`, `liaison.auth.hashKeySecret`, a `clientId` for each account, or with more than one replica and the `memory` backend |
| `liaison.connectedAccounts.sealKeySecret.name`, `key` | empty; `vault-keys` | a Secret you create, mounted read-only |
| `liaison.connectedAccounts.brokerPort` | `8081` | the broker's port and its Service `<release>-liaison-broker`; the operator hands the address to the gateway of each `connectedAccount` crew with a projected token for the broker alone |
| `liaison.connectedAccounts.accounts.<name>` | none | `clientId`, an optional `clientSecret` Secret (mounted as a file, never in the environment), `issuer`, `allowedCrews` and `maxLifetimeHours` |

Register `<publicURL>/oauth/callback` as the redirect address of your client at each service. An install that signs people in
through the same address registers nothing new.

If a network policy limits egress from a crew's namespace, allow its discussion gateway to reach the broker Service on its port.

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
