---
title: "Crew CRD"
description: "The Crew CRD groups Agents into one team and configures crew-wide settings such as the discussion gateway and the shared working memory the agents use."
weight: 2
---

## Overview

A `Crew` groups a set of `Agent`s (joined by the `kubemoot.ai/crew` label) into one collaborating team, and configures crew-wide concerns: the discussion gateway and the crew's **working memory**. Agents are the workers; the Crew is the team-level context they share.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Crew
metadata:
  name: homelab-pilot
  namespace: crew-homelab-pilot
spec:
  description: "Homelab infrastructure crew"
  discussion:
    enabled: true
  memory:
    enabled: true
    maxFacts: 5000
    injectLimit: 8
    verifyOnAdd: true
  retention:
    conversations:
      window: 14d
    memory:
      purgeUnused: never
```

The namespace separates names. The same crew name can run in many namespaces side by
side: the discussion, request, artifact, and memory subjects and keys a crew uses carry its
namespace and crew name after a fixed prefix (some operator subjects carry no namespace), so two crews named
`homelab-pilot` in two different namespaces do not collide. This is naming separation,
not a security boundary: NATS has no authentication in this release, and a client that
can reach it can read any subject. See
[Security model and known limitations](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md#security-model-and-known-limitations).

## Domain-Agnostic by Design

Kubemoot is the orchestration substrate; the **domain** comes from the crew, not from Kubemoot. A crew declares its own Toolers (live data via MCP) and Analysts (reasoning via RAG) in its Helm chart, points them at the operator, and gets a panel of experts with consensus collaboration. The same machinery serves any domain:

| Application | Toolers (MCP Tools) | Analysts (RAG Knowledge) |
|---|---|---|
| **Infrastructure ops** (homelab-pilot) | kubectl, Helm, Proxmox, Prometheus, k8sgpt | Kubernetes/Proxmox/Talos docs |
| **Kafka operations** | Kafka MCP tools (topics, consumer groups, configs) | Kafka / Confluent docs |
| **Oncology research** | (discussion-only) | PubMed papers, trial data, protocols |
| **Security operations** | SIEM / scanner tools | CVE databases, compliance frameworks, playbooks |

Because the domain is the crew's, a crew must be **portable** across clusters - which is exactly why nothing cluster-specific is baked into prompts and why working memory (below) is learned at runtime, per cluster.

## Spec Fields

| Field | Type | Description |
|-------|------|-------------|
| `description` | string | Human-readable purpose |
| `discussion` | object | Discussion gateway config (`enabled`, `resources`, `access`) - the HTTP entry point for external clients. Left out, `resources` asks for 5m CPU and 48Mi, may use 128Mi, and has no CPU limit; the operator sets `GOMEMLIMIT` to 90 percent of whatever memory limit applies. See [Access](#access) |
| `memory` | object | Crew working-memory policy - see below |
| `workspace.defaultAccess` | enum | The workspace access of every agent in the crew that declares none of its own: `none`, `read`, `write` or `readWrite`. An Agent's `spec.workspace.access` overrides it; left out, the KubemootConfig's `workspace.defaultAccess` applies, and with none declared agents behave as they always have. See [Agent](../../reference/agent/#specworkspaceaccess) |
| `defaultTimeZone` | string | The IANA time zone (such as `America/New_York`) in which the crew states today's date when the asker's client sent no zone. Left out, or a name that is not an IANA zone, the crew uses UTC. See [the turn context line](../../concepts/agent-context/#the-turn-context-line) |
| `connectedAccounts` | []object | The outside services whose accounts the crew needs: name, displayName, purpose, usage, scopes, authorizeURL, tokenURL, revocationURL, tokenHosts. See [Connected accounts](../connected-accounts/) |
| `network.allowedIngress` | []LabelSelector | Namespace selectors whose pods may reach the crew's discussion gateway when the operator generates network policies, for example the namespace of the web server that fronts the crew. See [Network policies](../../concepts/network-policies/) |

## Working Memory

The crew learns facts about *its* environment at runtime and recalls them later, so it stops re-discovering what it already figured out. This is what lets a crew be **portable**: nothing about a specific cluster (node names, label schemes, topology) is baked into agent prompts - the crew discovers those at runtime and remembers them per-cluster.

### The discover → remember → recall loop

1. **Discover, never assume.** When a query names a logical resource (e.g. a GPU "gpu-a"), agents do NOT assume how it's labeled. They run a discovery query (e.g. the bare metric to read its real labels), map the logical name to a real label value, then query. (See the `tool-using-specialist-discipline` PromptModule.)
2. **Remember.** When an agent resolves a durable fact, it emits a directive in its response:
   ```
   REMEMBER: <topic> | <key> | <value>
   example: REMEMBER: gpu-topology | gpu-a | DCGM label exported_namespace="ollama-a", RTX 5090
   ```
   The runtime persists it to crew memory and strips the line from the user-facing answer (same structured-output convention as `TOOL_GAP:`).
3. **Recall (auto-injection).** On every query, the runtime injects the crew's *relevant* facts into the agent's system context under `## Crew Working Memory`. The agent starts already knowing - it does not have to choose to look it up. Discovery becomes a one-time cost **per cluster**, amortized across all later queries.

### Storage vs injection

Storage is cheap (NATS KV, facts are a few hundred bytes); injection into LLM context is expensive (tokens + tool-calling degradation). They are configured separately:

- **Storage** (`maxFacts`) is generous.
- **Injection** (`injectLimit`) is small and **relevance-filtered**: facts whose `topic`/`key`/`value` share a token with the query rank first, then by recency, capped at `injectLimit`. An accelerator query surfaces accelerator facts, not the whole memory.

### Dedup & conflict (`verifyOnAdd`)

When `verifyOnAdd` is true, a new fact is vetted against the existing same-key fact before storing:

- identical value → **duplicate** → touch only (refresh usage), no rewrite
- different value → **conflict** → the fresh discovery **supersedes** (the environment is the source of truth); the prior revision is kept in NATS KV history; the supersession is logged

Cross-key semantic duplication/conflict is handled by the LLM itself - it sees the relevant memory via injection and is directed (ADL) not to re-`REMEMBER` known facts and to emit a correcting `REMEMBER` when it finds a contradiction. No extra inference.

### Garbage collection

Facts are kept alive by use and aged out when abandoned:

| Mechanism | Criterion | Effect |
|-----------|-----------|--------|
| Same-key overwrite | re-`REMEMBER` of an existing `topic`/`key` | replaces (dedup/supersede) |
| LRU cap | crew exceeds `maxFacts` on write | evict least-recently-used |
| Touch-on-read | a recalled fact's `usedAt` older than 6h | refresh `usedAt` (keep-alive) |
| Retention | fact unused past `retention.memory.purgeUnused` (or the crew-wide `retention.purgeUnused`) | pruned on read, and removed by the operator's [cleanup](#retention) |

### Lifecycle

- **Crew update** keeps memory.
- **Crew delete** purges that crew's facts (operator finalizer removes the crew's `<namespace>.<crew>.*` keys).
- **How long a fact is kept** is the crew's [retention](#retention), not a bucket expiry: the memory bucket has no expiry of its own, so a crew that keeps its memory longer, or forever, is not cut short.

### `spec.memory` fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Turn working memory on/off (recall + persist) |
| `maxFacts` | int32 | `5000` | Per-crew storage cap; LRU-evicted above this |
| `ttlDays` | int32 | none | Deprecated: use `retention.memory.purgeUnused`. When set, it overrides that value |
| `injectLimit` | int32 | `8` | Max facts injected into context per query (small - context budget, not storage) |
| `verifyOnAdd` | bool | `true` | Vet new facts for duplicates/conflicts on write |

## Access

`spec.discussion.access` says who may call the crew's discussion gateway. Left out, the gateway accepts every caller as the anonymous caller, as it always has: nothing is verified, and every conversation is open to every caller. The Crew says so with the `Authentication` condition set to `False` with reason `Anonymous`. The condition never affects `Ready`.

```yaml
spec:
  discussion:
    access:
      tokenReview: true       # default; check Kubernetes bearer tokens
      allowAnonymous: false   # default; refuse a caller with no token
      tokenAudiences: []      # optional; accept only tokens bound to these audiences
```

Setting `access` turns verification on. A caller sends its Kubernetes token as `Authorization: Bearer <token>`; the gateway checks it with a TokenReview, then asks the API server (a SubjectAccessReview) whether the caller may `get` that Crew, so plain RBAC decides who may use the crew. A request with no token is refused with 401 unless `allowAnonymous` is `true`, and a token that is present and not valid is always refused, never treated as anonymous. The gateway needs no configuration beyond this: the operator gives its ServiceAccount the right to create TokenReviews and SubjectAccessReviews. The gateway passes the verified caller, as a principal, with every question it queues, and each conversation belongs to the caller who started it: a caller sees and continues their own, and an operator with `get` or `list` on `crews/conversations` and a stated reason may read the rest. See [who owns a conversation](../../concepts/conversations-turns-and-threads/#who-owns-a-conversation). See [Who is asking](../../concepts/who-is-asking/).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `access.tokenReview` | bool | `true` | Check Kubernetes bearer tokens |
| `access.allowAnonymous` | bool | `false` | Accept a caller with no token as the anonymous caller |
| `access.tokenAudiences` | []string | none | Accept only tokens bound to one of these audiences, so a token minted for the API server alone is refused. Empty accepts the API server's own audience |

| Condition | Meaning |
|-----------|---------|
| `Authentication` | `False`, reason `Anonymous`: the gateway accepts anonymous callers and verifies no one. `True`, reason `TokenReview`: the gateway verifies callers. `True`, reason `ServiceToken`: the crew lists `serviceTokens`, so a person's own token from a service is verified too. `True`, reason `TrustedHop`: the crew lists `trustedHops`, so a door's assertion of who is asking is verified. `False`, reason `Closed`: the gateway checks no token and accepts no anonymous caller, so it refuses every request. Absent when the crew runs no gateway |
| `CallerCredentials` | Present only when a tool server in the crew's namespace has `registry.callerCredential.mode: Required`. `True`, reason `Ready`: a verifier passes the person's token to tools and the pieces that carry it exist. `False`, with the reason: `NoVerifierPassesTokens` (no `serviceTokens` entry has `passToTools`), `UnknownVerifier` (the server names a verifier the crew does not list, or one without `passToTools`), `NoSingleToolGateway` (the namespace has no MCPGateway, or several), `SeveralCrewsPassTokens` (another crew of the namespace also keeps tokens). It never affects `Ready` |
| `ConnectedAccounts` | Present when the crew declares `spec.connectedAccounts`, or when a server in its namespace names an account no crew of the namespace declares. `True`, reason `Ready`: every account is registered by the install, every account a server names is declared, and no other crew uses a declared name for a different service. `False`, with the reason: `Conflict`, `UnregisteredAccount`, `UnknownAccount`, `NoSingleToolGateway`, `SeveralCrewsPassTokens` (another crew of the namespace also declares accounts or keeps tokens: one crew per namespace). It never affects `Ready`. See [Connected accounts](../connected-accounts/#the-connectedaccounts-condition) |

### What the agents are told about the caller

`spec.discussion.identity.exposeToAgents` (default `false`) puts the caller in each agent's turn context, native or harness: the opaque handle (the principal's hashed id) and, for a Kubernetes caller, the user name as a label. Off, no prompt changes and no user name is on the bus. It needs `access`: a Crew with `exposeToAgents: true` and no `access` is refused. See [Telling the agents who is asking](../../concepts/who-is-asking/#telling-the-agents-who-is-asking).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `identity.exposeToAgents` | bool | `false` | Tell the agents the caller's handle and, for a Kubernetes caller, a label. Needs `access` |

### A door that signs people in

`access.trustedHops` lists the doors whose signed assertion of who is asking the gateway accepts, such as the crew liaison when it signs people in itself (see [Who is asking](../../concepts/who-is-asking/#a-door-that-signs-people-in)). `access.askers` is the one rule that decides which of the people such a door asserts may ask this crew. The door only proves who is asking; the crew decides. Both are empty by default and nothing changes for a crew that leaves them out. With `tokenReview: false` a crew can accept only the door. The operator sets the audience to this crew's own, so an assertion signed for another crew is refused.

```yaml
spec:
  discussion:
    access:
      tokenReview: false
      trustedHops:
        - name: liaison
          publicKeys:
            - |
              -----BEGIN PUBLIC KEY-----
              ...
              -----END PUBLIC KEY-----
      askers:
        kubernetesRBAC: {}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `access.trustedHops[].name` | string | required | The issuer the door signs as, a DNS label. The liaison signs as `liaison`. Names are unique in the list; at most four doors |
| `access.trustedHops[].publicKeys` | []string | required | The door's ECDSA P-256 public keys as PEM, one to four. List the old and the new key while a key rotates. Derive one with `openssl pkey -pubout` from the door's private key. A key that is not an ECDSA P-256 public key makes the `Authentication` condition `False` with reason `InvalidTrustedHops`, and the gateway accepts no assertion until it is fixed |
| `access.askers` | object | required with `trustedHops`, and only valid with it | One rule, exactly one of the five kinds below. The admission check refuses none or several |
| `access.hashKeySecretRef` | object | none | Needed by every kind but `kubernetesRBAC`: a person who is not a Kubernetes user is named on the bus by an id keyed with the install's hash key |

The kinds of `access.askers`. A rule admits a person for this crew only: the gateway refuses the same person at another crew name in a request path. The rule applies only to people a trusted door asserts: a Kubernetes token is always checked by RBAC and a service token by its own `authorize` setting. Only `kubernetesRBAC` turns the person into a Kubernetes user and calls the Kubernetes API for them; no other kind does, and the operator gives the gateway the right to create SubjectAccessReviews only for that kind.

| Kind | Who may ask | What is enforced |
|------|-------------|------------------|
| `kubernetesRBAC: {}` | The person's user name and groups, as the door asserts them, may `get` this Crew in Kubernetes RBAC | A SubjectAccessReview per request. The person's conversations are the same ones a Kubernetes token for that user name would reach |
| `connectedAccount: <name>` | Anyone the door signed in. The rule names the account the crew's tools act with | The name must be in `spec.connectedAccounts` (admission refuses it otherwise, and the operator reports `Authentication` `False` if it is not). Today the gateway admits every signed-in person and does not check that they connected the account: connecting it and leasing it to the crew's tools is a later step, so a person who has not connected it is admitted and the outside service refuses the tool call. The outside service decides entitlement on each call |
| `issuerGroups: [<group>, ...]` | A person whose asserted groups, exactly as the door sends them (with its group prefix), include at least one listed | A string match on the asserted groups, one to 32 names |
| `anySignedIn: {}` | Every person the door asserts | Nothing beyond the door's own sign-in |
| `external: {url, ...}` | Whatever an external policy decision point decides | An OpenID AuthZEN Authorization API 1.0 access evaluation call per request (below) |

Examples, one per way a crew decides:

```yaml
# Only the cluster's administrators may ask.
askers:
  kubernetesRBAC: {}
---
# Anyone the sign-in provider accepts, whose account at an outside service the crew's tools use.
askers:
  connectedAccount: library
---
askers:
  issuerGroups: ["oidc:staff", "oidc:support"]
---
askers:
  anySignedIn: {}
```

**`external`.** The gateway sends the policy decision point a `POST` of `{subject, action, resource}` (the [AuthZEN](https://openid.net/specs/authorization-api-1_0.html) access evaluation request) and admits only an HTTP 200 whose `decision` is `true`. The subject is `{type, id: <asserted user>, properties: {groups: [...]}}`, the resource is `{type, id: "<namespace>/<crew>"}` and the action is `{name}`. Any other answer, an error, a redirect, a timeout (5 seconds) or a response over 64 KiB refuses the request. Nothing is cached.

| Field | Default | Description |
|-------|---------|-------------|
| `askers.external.url` | required | The decision point's access evaluation endpoint, `https` only (the specification requires a secured connection). The gateway does not discover it. Its host must be on the install's list (below) |
| `askers.external.subjectType`, `resourceType`, `action` | `user`, `crew`, `ask` | The names the request carries |
| `askers.external.tokenSecretRef` | none | A Secret key (`name`, `key` defaulting to `token`) with a bearer token the gateway presents to the decision point |

The gateway sends each asker's identity to this address, so the install decides where it may go, not the crew author. The operator lists the trusted hosts in `discussionGateway.askerPolicyHosts` in the chart (plain host names, for example `pdp.example.org`). A crew whose `external.url` names another host reports `Authentication=False` with reason `UntrustedPolicyHost`, its gateway accepts no assertion of who is asking, and so everyone is refused for that rule; an empty list trusts no host. Every call goes only to a public address: a private, loopback, link-local, shared-address or cluster address is refused after the name is resolved (a name that answers differently the next time reaches nothing new), no proxy is used and a redirect is never followed. A decision point therefore needs a public address; put it behind one rather than listing an in-cluster name.

### A person's own token from a service

`access.serviceTokens` lets a person ask the crew with the token they already hold for another service, for example a chat app signed in to a library. The gateway asks the service whether it accepts the token, so the crew never issues or checks the token itself (see [Who is asking](../../concepts/who-is-asking/#subscribers)). It is empty by default and nothing changes for a crew that leaves it out.

```yaml
spec:
  discussion:
    access:
      hashKeySecretRef:
        name: kubemoot-hash-key   # a Secret in the crew's namespace that you create
      serviceTokens:
        - name: oreilly
          verifyURL: https://api.example.com/mcp
          subjectClaim: upstream_claims.sub
          passToTools: true
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `access.serviceTokens[].name` | string | required | The issuer of these people, a DNS label other than `kubernetes`. It is part of each person's id, so renaming it changes every id. Names are unique in the list |
| `access.serviceTokens[].verifyURL` | string | required | The service endpoint the token is tried against. `https` only |
| `access.serviceTokens[].verifyMethod` | string | `McpInitialize` | How the token is tried. `McpInitialize` posts an MCP initialize request |
| `access.serviceTokens[].subjectClaim` | string | required | The dotted path in the token's payload that names the person, such as `upstream_claims.sub` |
| `access.serviceTokens[].audiences` | []string | none | Values the token's `aud` must have one of. With more than one entry, each entry needs audiences, so a token is only tried against the service it was issued for |
| `access.serviceTokens[].maxCacheSeconds` | int | `300` | How long an accepted token is remembered, never beyond its own expiry. 1 to 900 |
| `access.serviceTokens[].passToTools` | bool | `false` | The gateway keeps the accepted token in memory for the turn so a tool server with `registry.callerCredential.mode: Required` can call the service as the person |
| `access.serviceTokens[].authorize` | string | `AnyAccepted` | `AnyAccepted` lets every person the service accepts use the crew. `Subjects` lets only the listed subjects |
| `access.serviceTokens[].subjects` | []string | none | Raw account ids allowed with `Subjects`. Needs at least one, and is refused with `AnyAccepted` |
| `access.hashKeySecretRef.name` | string | | A Secret in the crew's namespace. Required while `serviceTokens` is set |
| `access.hashKeySecretRef.key` | string | `hash-key` | The key in that Secret that holds the install's hash key |

The hash key turns a person's account id into the opaque id the bus carries. It must be the same value in every door of the install: the Secret each crew names, and the Secret of `liaison.auth.hashKeySecret` in the operator chart. Create it once, for example `kubectl create secret generic kubemoot-hash-key --from-literal=hash-key="$(openssl rand -base64 32)"` in each namespace. The operator writes the list into the gateway's `ACCESS_SERVICE_TOKENS`, the crew's `<namespace>/<name>` into `ACCESS_CREW` and the key into `ACCESS_HASH_KEY` from the Secret; the key is never written into the Deployment. A person a crew's verifier accepted may use that crew only: another crew that lists the same service accepts them through its own verifier, never through this one.

#### Passing the token to tools

A verifier with `passToTools: true` makes the operator wire the two hops that carry the token to a tool, none of which exist for a crew without one:

- The discussion gateway gets `ACCESS_BROKER_SUBJECT` (the ServiceAccount of the namespace's MCPGateway) and listens for leases on port 8081. The crew's Service gains a port named `internal` for it, and the gateway pod carries the label `kubemoot.ai/credential-lease`.
- With generated network policies on, the policy `km-crew-<crew>-gateway-lease` lets only the MCPGateway pods of the crew's namespace reach port 8081. The namespace baseline leaves a pod with that label out of its every-port rule, so agent pods and other namespaces cannot reach it; the serving port stays open to the same callers as before. When the MCPGateway's own outbound traffic is restricted (egress mode `Declared` and an external server behind it), its policy `km-gateway-<gateway>-egress` allows the same pods on port 8081; see [A gateway's outbound traffic](../../concepts/network-policies/#a-gateways-outbound-traffic).
- The gateway's ServiceAccount may create TokenReviews even when `tokenReview` is `false`, to check the tool gateway's token.
- The MCPGateway is described in [MCPGateway guide](../mcpgateway-guide/#calls-with-the-asking-persons-own-credential) and the server field in the [MCPServer guide](../mcpserver-guide/#calls-with-the-asking-persons-own-token).

The tool gateway leases from one crew, so keep tokens in one crew per namespace; a second crew that passes tokens turns `CallerCredentials` to `False`. With generated network policies off, nothing but the lease's own ServiceAccount check stops another pod from calling port 8081.

## Retention

`spec.retention` says how long the crew's data is kept in NATS and when the operator removes what has expired. Kubemoot keeps no one's data by default beyond what a crew needs to answer, so a crew that declares nothing still has a policy: the API server fills the defaults from the Crew schema, which is the one place they are written (`kubectl explain crew.spec.retention`).

| Field | Governs |
|-------|---------|
| `retention.schedule` | When the cleanup runs, in UTC: a five-field cron expression, or `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly` |
| `retention.purgeUnused` | Anything with no activity (written or recalled) for this long is removed. Applies to conversations and memory |
| `retention.conversations.window` | Conversations rotate first in, first out: one is kept this long after its last turn, and the oldest go first |
| `retention.memory.purgeUnused` | How long an unused fact is kept; unset takes `retention.purgeUnused`. `never` keeps every fact until it is superseded, invalidated, evicted by `memory.maxFacts`, or deleted |
| `retention.other.window` | The crew's discussion messages and coordinator logs. Unset leaves them to the age and size limits of their streams; set, it can only shorten what those streams keep |

**Durations** use the form Prometheus uses for its retention: a number and a unit (`s`, `m`, `h`, `d`, `w`, `y`), several combined from the largest unit down with each unit at most once (`36h`, `90d`, `1y6w`), or `never`. The API server refuses anything else: a missing unit, zero, a fraction, a unit written twice or out of order (`1d1y`), spaces, capitals. `never` is accepted for memory and for `purgeUnused`; conversations and other data always age out, with a longest window the shared stores can hold.

**The cleanup** runs in the operator, per crew, on `retention.schedule`. It removes only what carries the crew's own key prefix (`<namespace>.<crew>`) from the shared buckets and streams, so a crew's policy never touches another crew's data. Retention is only as fine as the schedule: with a daily schedule, a 12-hour window removes data within a day of its expiry, not at 12 hours. The Crew reports a duration finer than its schedule (see below). A crew whose policy has an invalid value has nothing removed until it is fixed.

The scheduling crew declares its memory `never`: what it learned about unchanged hardware is not forgotten because a clock ran out.

Conversation history has no wall-clock expiry in its bucket beyond a backstop as long as the longest window a crew may declare, so the crew's window decides. Scheduler performance history is not a crew's data and follows its ModelServer: it is removed when the server is deleted.

### What the Crew reports

| Where | What |
|-------|------|
| `status.retention.lastRunTime` | When the last cleanup finished |
| `status.retention.removed` | Counts of what it removed: `conversations`, `memory`, `other` |
| `status.retention.error` | Why the last run did not finish; empty when it did. The next scheduled run tries again |
| condition `RetentionValid` | `True` when the policy was read, with reason `FinerThanSchedule` when a duration is finer than the schedule (the message names it); `False` with reason `Invalid` and the field and reason when a value could not be read. A warning event is sent when the reason changes |

## Status and conditions

A Crew reports two separate things: whether it is reconciled as declared, and whether a model can run its agents now.

| Condition | `True` | `False` |
|-----------|--------|---------|
| `Ready` | The crew is reconciled as declared: its coordinator `Agent` runs, and its agents, gateways and policies exist as specified. Reason `Reconciled`. | A real failure or an unfinished start: a reconcile step failed (reason `Error`, for example an invalid spec or a failed apply), or the crew has no coordinator or its coordinator is not running yet (reason `Pending`). |
| `CapacityAvailable` | A model in the cluster catalog can run the coordinator (reason `ModelAvailable`). The message names any specialist that has no model. | No model in the catalog can run the coordinator, or none can run any specialist (reason `NoModelAvailable`). The message carries the scheduler's reason, for example `coordinator homelab-coordinator is unschedulable: no model in the cluster catalog (2 available) satisfies the require of phase=mulling (capability/tool-calling=true)`. `Unknown` (reason `CoordinatorNotRunning`) while the crew has no running coordinator, so there is nothing to assess. |

`status.phase` is what `kubectl get crews` shows next to `Capacity`:

| Phase | Meaning |
|-------|---------|
| `Pending` | The coordinator is missing, in `Error`, or not running yet. `Ready` is `False`. |
| `Ready` | Reconciled, and a model can run the crew's agents. |
| `WaitingForCapacity` | Reconciled, but no model in the catalog can run the coordinator or any specialist now, so the crew cannot answer. `Ready` stays `True` and `CapacityAvailable` is `False`. |
| `Error` | A reconcile step failed. `Ready` is `False`. |

`status.ready` mirrors the `Ready` condition, so it is `true` in `WaitingForCapacity`. To ask whether a crew can answer now, read the phase or `CapacityAvailable`, not `status.ready` alone. `status.observedGeneration` is the Crew generation the status describes.

**Why capacity is not part of `Ready`.** Models are picked from the cluster catalog, and capacity comes and goes: leased accelerators stop, an accelerator node goes down, the accelerators are lent to another workload. A crew declared correctly is still declared correctly while that happens, as a Deployment's spec is applied while its pods are Pending. Helm's wait and Flux's health checks judge a custom resource with kstatus, which reads `metadata.generation` against `status.observedGeneration` and the `Reconciling`, `Stalled` and `Ready` conditions, and never `status.phase`. A `Ready` condition of `False` is `InProgress` to kstatus and a `True` one is `Current`, so a GitOps upgrade of a crew succeeds while accelerators are away, and the crew answers once a model returns: the operator keeps retrying, and no one re-applies anything. An `Agent` that no model can run carries no `Ready` condition (its `phase` is `Unschedulable`), so kstatus reads it as current and a wait on it does not block either.

Genuine failures still hold a wait: a reconcile error or an invalid spec leaves the crew not `Ready`, and an image that cannot be pulled keeps the agent's Deployment unavailable, which the wait on that Deployment catches. The crew's `Ready` does not read pod readiness.

## Namespace lifecycle

Deleting a Crew leaves its namespace alone by default. A Crew that carries the annotation
`kubemoot.ai/manage-namespace: "true"` asks the operator to delete the namespace with it,
and the operator does so only when the namespace itself opts in with the label
`kubemoot.ai/managed-namespace: "true"`. Someone with rights on the `Namespace` object sets
that label; a namespaced Crew author cannot, and the operator never writes it. The operator
never deletes `kube-system`, `default`, `kube-public`, `kube-node-lease`, or its own
namespace, whatever the annotation and labels say.

## What deleting a Crew removes

Deleting a Crew deletes every Agent in its namespace that carries the label
`kubemoot.ai/crew: <crew name>`, and Kubernetes then removes each Agent's Deployment, Service,
and pods. The operator does this two ways: it adds the Crew as an owner of each member Agent,
so garbage collection cascades, and the Crew finalizer deletes any member Agents still
present, which covers Agents created before the owner reference existed. Agents of another
Crew and Agents without the label are left alone. The finalizer also removes the Crew's
cluster RBAC and working memory.

The finalizer also closes the Crew's discussions by purging every message under
`kubemoot.discuss.<namespace>.<crew>.>` from the `KUBEMOOT_DISCUSS` stream and deleting the
liveness heartbeats of its Agents from `kubemoot_agent_state`. The subjects are namespace
scoped, so a Crew of the same name in another namespace keeps its threads. If NATS is
unreachable the finalizer stays, the Crew shows a `DiscussionPurgeFailed` warning event, and
the operator retries until the purge succeeds. This includes Agents a Helm release manages: they return on
the next Helm upgrade.

## Storage

Working memory lives in the NATS KV bucket `kubemoot_crew_memory` (provisioned by the operator's nats-streams-job). Keys are namespace-and-crew-scoped: `<namespace>.<crew>.<topic>.<key>`; values carry `value`, `learnedBy`, `learnedInThread` (the discussion thread it was learned in, when there was one), `learnedAt`, `usedAt`. The agent-runtime reads/writes it directly (`CrewMemoryClient`), native-safe via `readTree`, degrading to a no-op when NATS is unavailable.

## Related

- [Agent CRD](../reference/agent.md) - the workers a crew groups
- [Agentic Consensus](../architecture/agentic-consensus.md) - how the crew discusses
