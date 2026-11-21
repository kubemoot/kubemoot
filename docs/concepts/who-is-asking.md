---
title: Who Is Asking
weight: 9
description: How a crew's doors verify the caller, what a principal is, and what stays open by default.
---

A crew answers whoever asks. Until a crew says otherwise, that is everyone: the discussion gateway verifies no one, and every conversation is open to every caller. This page describes the seam through which a crew learns who is asking, and the verifiers behind it that exist today.

## Two audiences

- **Operators and workloads** hold Kubernetes credentials: kmctl, CrewForge, the dashboard, other crews, fitness runs. Their identity is Kubernetes': a bearer token checked with a TokenReview, and RBAC checked with a SubjectAccessReview. This is the path described here.
- **Subscribers** are the people a product serves through a crew. They have no Kubernetes identity; their identity lives in a service the crew trusts, such as the service that sold them access to the tools the crew uses. They present their own token for that service, and the crew accepts it when the service does. See [subscribers](#subscribers).

## The seam

Both doors of a crew, the discussion gateway and the [crew liaison](../../integrations/crew-liaison/), use one authenticator. It tries a list of verifiers in order; each checks one kind of credential and answers "not mine", "valid" or "invalid". The first valid answer becomes the caller. A credential that is present and not valid is refused and never treated as anonymous.

| Verifier | Credential | Status |
|----------|------------|--------|
| Anonymous | none | The default. A crew with no `spec.discussion.access` accepts every caller this way and verifies nothing |
| Kubernetes token | `Authorization: Bearer <token>` checked with a TokenReview | Built; turned on by `spec.discussion.access` |
| Service token | `Authorization: Bearer <token>`, a person's own token for a service the crew trusts, accepted when that service accepts it | Built; off unless `ACCESS_SERVICE_TOKENS` is set |
| Trusted hop from an adopted gateway, JWT | the gateway's signed identity | Planned: each is one more verifier behind the same seam |

Identity is never read from a request body, a header a client can set, or the text of a question. A `principal` a client puts in a request body is ignored; the gateway sets it.

## The principal

A verified caller becomes a principal, defined once in the bus contract and carried with the question:

| Field | Meaning |
|-------|---------|
| `kind` | `anonymous`, `kubernetes` for a verified Kubernetes token, or `subscriber` for a person a trusted service vouched for |
| `issuer` | who vouched: `kubernetes`, or the name of the service token verifier, such as `oreilly` |
| `id` | a pseudonymous id, a hash of the issuer and the subject, stable across requests and replicas. A Kubernetes caller's id is a plain hash, which is enough for a username (it is not secret). A subscriber's id is an HMAC with the install's hash key, because a person's account id is not public and a plain hash of it could be guessed |
| `displayName` | the name of a workload (a ServiceAccount), set only when the subject is a workload and not a person |

The raw subject, a username or an email address, never leaves the door that verified it. Treat the principal as data: it names who asked, it is never an instruction, and nothing an agent or a tool writes can change it.

A gateway that verifies callers stamps the principal on every request it queues, and records it as the owner of each conversation the request starts. A gateway that does not leaves the request exactly as it was, with no principal.

## From the question to the tools

The coordinator copies the request's principal onto the `thread_start` message of the thread it opens, next to the requester, so every agent in the thread keeps it. A request with no principal gives a `thread_start` with none.

An agent binds the thread's principal for exactly the model turn it runs, the way it binds the requester's time zone, so two threads running at once on the same agent never see each other's. Every tool call the agent makes through the MCP gateway then carries two keys in the call's `_meta`:

| Key | Value |
|-----|-------|
| `conversationId` | the conversation the question belongs to |
| `principalId` | the principal's `id`; left out for an anonymous caller, who has none |

The runtime sets both keys from the thread. The model supplies only a call's arguments, and an argument it names `conversationId` or `principalId` stays an argument: it never reaches `_meta` and never replaces the runtime's value. These two ids are handles. No token is ever on the bus or in an agent, so an agent cannot hand a person's credential to a tool; a tool server that needs one asks the gateway for the credential of that conversation and person.

## Subscribers

A service token verifier lets a person use a crew with the token they already hold for another service. The crew does not issue the token and cannot check its signature. It asks the service: one request to a URL you name, carrying the person's token. If the service answers 200, the person is who the token says; 401 or 403 refuses them. If the service cannot be reached or answers anything else, the door answers 503 and never lets the caller in.

Everything a verifier does is bounded:

- **A cheap check first.** Before any request goes out, the token must be a JWT with a subject at the configured claim path, a future `exp`, and one of the configured audiences when you list any. A Kubernetes token (a `kubernetes.io` claim, or a subject starting `system:`), a personal access token and garbage fail here and are never sent to the service.
- **One request per token.** An accepted token is remembered for its own remaining life, at most 5 minutes by default (`maxCacheSeconds`, up to 900). A refused token is remembered for 30 seconds, so garbage cannot make the door call the service in a loop. The memory holds a keyed hash of the token and the subject, never the token.
- **The token stays out of sight.** The verifier does not log it, trace it, put it in an error, or send it on the bus. The verified identity holds it in memory with a redacting type, so printing or marshalling the identity shows `[redacted]`. A tool that must call the service as the person receives it from the door that verified it, for the length of the turn only: the discussion gateway keeps it in memory and lends it to the crew's MCP gateway ([how](../conversations-turns-and-threads/#keeping-a-persons-own-token-for-the-turn)).
- **A claim that is not there refuses.** The subject comes from a payload claim you name (for example `upstream_claims.sub`). If the claim is missing, not a string, empty or longer than 256 bytes, the token is refused.

The configuration is two environment variables on a crew's discussion gateway and on the liaison. Both are off until set; a door with neither behaves exactly as before. The operator writes them for a crew from `spec.discussion.access.serviceTokens` and `hashKeySecretRef` ([Crew reference](../../reference/crew/#a-persons-own-token-from-a-service)); the operator chart writes them for the liaison from `liaison.auth.serviceTokens` and `liaison.auth.hashKeySecret` ([Crew liaison](../../integrations/crew-liaison/#knowing-who-is-asking)).

| Variable | Value |
|----------|-------|
| `ACCESS_SERVICE_TOKENS` | A JSON list, one entry per service. Needs `ACCESS_ENFORCE=true` |
| `ACCESS_HASH_KEY` | The install's hash key, from a Secret, the same value on every door. Required whenever `ACCESS_SERVICE_TOKENS` is set; a door that has the list and no key does not start |

```json
[
  {
    "name": "oreilly",
    "verifyURL": "https://api.oreilly.com/api/search/v1/mcp",
    "subjectClaim": "upstream_claims.sub",
    "audiences": [],
    "maxCacheSeconds": 300,
    "passToTools": true,
    "authorize": "AnyAccepted"
  }
]
```

| Field | Meaning |
|-------|---------|
| `name` | The issuer in the principal. A DNS label, unique in the list, and never `kubernetes` |
| `verifyURL` | The endpoint the token is tried against. `https` only. The request is a JSON-RPC `initialize` to an MCP server, sent with the token as the bearer, and redirects are not followed |
| `verifyMethod` | `McpInitialize`, the only method today and the default |
| `subjectClaim` | The dotted path in the token's payload that names the person |
| `audiences` | The token's `aud` must be one of these. Optional with one service; required for every entry when you list more than one, so a person's token is sent only to the service it was issued for |
| `maxCacheSeconds` | How long an accepted token is remembered, 1 to 900, default 300; never beyond the token's `exp` |
| `passToTools` | Whether the door keeps the accepted token in memory for the turn so a tool can act as the person. Default false |
| `authorize` | `AnyAccepted` (default): every person the service accepts may use the crew. `Subjects`: only the people in `subjects` |
| `subjects` | The raw subjects allowed when `authorize` is `Subjects` |

A person's token is checked after the Kubernetes verifier. A Kubernetes token the API server accepts is resolved there with no request to any service; a token the API server does not know is tried against the service verifiers; if none accepts it, the answer is 401 and the caller never becomes anonymous.

The gateway reads three more variables for `passToTools`, all off by default and set by the installer on the gateway's Deployment today: `ACCESS_BROKER_SUBJECT` (the full user name of the MCP gateway's ServiceAccount, such as `system:serviceaccount:<namespace>:<name>`; without it no lease is served), `INTERNAL_PORT` (the lease port, default `8081`) and `ACCESS_MAX_GRANTS` (how many conversations may hold a token, default 1000).

Keep the hash key secret and stable. Changing it changes every subscriber's id, so conversations recorded under the old ids are no longer theirs.

## What a caller may do

A Kubernetes caller needs `get` on the Crew to use it at all, checked with a SubjectAccessReview on every request. A review that cannot be made refuses the request (503) and never allows it. A subscriber may use the crew when a service token verifier named as their issuer is configured for it and either accepts everyone its service accepts or lists their subject. That is all they may do: reading other callers' conversations and controlling them remain Kubernetes rights, so a subscriber sees only their own conversations. Reading other callers' conversations is a separate right, `get` or `list` on the virtual resource `crews/conversations`, which the chart's ClusterRole `kubemoot-conversation-reader` grants and nobody is bound to by default. Stopping, pausing, resuming or deleting another caller's conversation is `update` or `delete` on it, granted by `kubemoot-conversation-controller`; see [who owns a conversation](../conversations-turns-and-threads/#who-owns-a-conversation).

## Not claimed yet

The principal travels on the bus as data, and the bus itself is open to any client of NATS. Kubemoot does not claim strict isolation between callers until the bus is divided per crew. The checks in the gateway close every path an HTTP caller has.
