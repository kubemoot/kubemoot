---
title: "Secrets and tools"
weight: 55
description: "Why the model never holds a secret, how credentials reach tools, and how the use grant and its admission check decide who may reference which Secret."
---

A tool that sends mail, queries a database, or calls an API needs a credential. Kubemoot
keeps that credential away from the model. The principle:

**The model never holds a secret; only the tool process does.** An agent calls a tool
and receives results. It never receives the token, password, or key behind the tool.

## Why

A model reads prompts and writes text. Anything placed in a prompt, a ConfigMap an agent
reads, or an agent's environment can appear in a response, a log, a discussion
artifact, or a retrieved document. A credential held only by the tool process cannot be
leaked by what a model says, and a prompt-injected agent has nothing to hand over. The
tool chain reinforces this boundary: agents reach tools only through the
[MCPGateway](../mcp-tools/), and the credential lives in the MCPServer pod behind it.

## How it works today

An [MCPServer](../../reference/mcpserver-guide/) takes credentials from Kubernetes
Secrets in its own namespace:

- `env` entries with `valueFrom.secretKeyRef` inject one key as an environment variable.
- `secretRef` injects every key of a Secret as environment variables.
- `secretVolumes` mounts a Secret as files.

Only the MCPServer pod receives the credential. Agent pods do not get it through these
fields, so the principle holds for the prompt path.

Without the use-grant check below, nothing decides who may reference which Secret. The
operator mounts any Secret the MCPServer names, and the MCPServer validation does not check
that the author may read it. A crew author who can create an MCPServer can have the operator
mount any Secret in that namespace into a pod they control. The `serviceAccountName` field
works the same way: the server runs as any ServiceAccount the author names. Agent pods run
as the namespace `default` ServiceAccount unless the Agent sets another, and an Agent's
`env`, `memory.secretRef` and ServiceAccount are open in the same way.

With the check off, anyone who can create an MCPServer is trusted with every Secret in its
namespace. Namespaces remain the isolation boundary between crews.

## Granting `use` on a Secret

`use` is a custom verb. It is not `get`: holding it lets someone point an MCPServer at the
Secret, and does not let them read its value. An admin grants it on one named Secret with an
ordinary Role, and binds the Role to whoever applies the crew (with Flux, the ServiceAccount
Flux applies that namespace as):

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: use-mail-credentials
  namespace: crew-mooty
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["use"]
    resourceNames: ["mail-credentials"]
  # Only for a tool that calls the Kubernetes API: name the ServiceAccount it runs as.
  - apiGroups: [""]
    resources: ["serviceaccounts"]
    verbs: ["use"]
    resourceNames: ["kubernetes-mcp"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: use-mail-credentials
  namespace: crew-mooty
subjects:
  - kind: ServiceAccount
    name: flux-crew-mooty
    namespace: crew-mooty
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: use-mail-credentials
```

A Role without `resourceNames` grants `use` on every Secret in the namespace; name each
Secret instead. The grant says nothing about reading, so the author still has no `get` or
`list` on Secrets.

## The admission check

The operator chart ships two ValidatingAdmissionPolicies, one for MCPServer and one for
Agent, behind a flag that is off by default:

```yaml
security:
  secretUseGrants:
    enabled: true
```

The policies need Kubernetes 1.30 or later (ValidatingAdmissionPolicy is generally available
and the CEL `authorizer` is present). When the flag is on, a create or update is rejected
unless the requester holds `use` on every Secret and ServiceAccount it references:

- MCPServer: `env[].valueFrom.secretKeyRef`, `secretRef`, `secretVolumes`,
  `registry.authSecretRef`, the `env` and `envFrom` of every `sidecars` container, and
  `serviceAccountName`.
- Agent: `deployment.env` and `harness.env` secret references, `memory.secretRef`, and
  `deployment.serviceAccountName`.

A rejection names the field and the objects the grant is needed on:

```text
MCPServer spec.secretRef: crew-author needs the verb "use" on every one of: secrets/mail-credentials.
Ask an admin for a Role with that verb and resourceNames.
```

Behaviour to know:

- Cluster admins pass without a grant, because their RBAC allows every verb. The operator's
  own ServiceAccount is exempt, so MCPServers the operator creates itself (from an
  MCPGateway credential policy) are not checked, so whoever may edit an MCPGateway is trusted
  with the Secrets its policies name. Add other trusted identities to
  `security.secretUseGrants.exemptUsers`.
- An update that leaves `spec` unchanged passes, so the operator's finalizer and metadata
  writes never fail. Turning the flag on does not touch existing resources; an existing
  MCPServer is checked the next time its spec changes. The check then covers the whole spec:
  an author without `use` on a Secret the resource already references cannot change any part
  of that spec until an admin grants `use` on it, or changes it themselves.
- Naming the `default` ServiceAccount is not a reference: a pod that names no ServiceAccount
  runs as it anyway, so the policy and the operator treat `serviceAccountName: default` the
  same as leaving it out.
- The Kubernetes MCP server's own ServiceAccount (`kubemoot-kubernetes-mcp`) can create and
  update MCPServers and Agents for onboarding. With the flag on, the chart grants it `use` on
  its own ServiceAccount only. It holds no grant on any Secret, so an MCPServer it creates
  that carries a Secret, or another ServiceAccount, is denied by design; an admin applies
  those.
- The message lists every Secret in the failing field, not only the missing one, because
  the authorizer is not available when Kubernetes builds the message.
- Other Kubemoot kinds that reference Secrets (MCPGateway, MCPCatalog, ModelServer,
  RAGSource) are not covered by this check; they are infrastructure resources an admin
  creates.

### Rolling it out

The policies fail closed (`failurePolicy: Fail`): if Kubernetes cannot evaluate one, writes
to MCPServers and Agents are refused. Roll out in two steps:

1. Install with `security.secretUseGrants.validationActions: ["Audit", "Warn"]`. Nothing is
   rejected; the API server warns the requester and records an audit annotation for every
   request the policy would deny. Grant `use` where the warnings show a need, including to
   the identity Flux applies each crew namespace as.
2. Set `validationActions` back to `["Deny"]`.

To limit the check to some namespaces, set `security.secretUseGrants.namespaceSelector`.
The policies need Kubernetes 1.30 or later. The operator tests apply them to the Kubernetes
version the operator builds against.

## Limiting where a tool can connect

A credential is only as safe as the places its tool can send it. An MCPServer can declare
the hosts it may reach with `spec.egress`, and the operator can generate the network
policies that enforce it. See [Network policies](../network-policies/).

## Pods get no ServiceAccount token by default

The operator sets `automountServiceAccountToken: false` on the pods it builds for an Agent
or an MCPServer that names no ServiceAccount. These pods call no Kubernetes API, so a
RoleBinding added later to the namespace `default` ServiceAccount cannot arm them. A pod
whose spec names a ServiceAccount keeps the Kubernetes default, because that account exists
to call the API and its RBAC bounds the pod. This is not behind a flag, and it changes the pod
template: upgrading restarts every Agent and MCPServer pod that names no ServiceAccount, once.

## Namespaces a Kubernetes tool may read

A Kubernetes MCP server runs as a ServiceAccount, and what that account may read decides
what an agent can see through the server. Kubemoot grants that read per namespace, and only
where the namespace asks for it.

A namespace owner opts in by labeling the namespace:

```bash
kubectl label namespace team-a kubemoot.ai/crew-read=true
```

Only the value `true` counts. Any other value, or no label, means the namespace is not
readable. The operator then does the rest:

- In each labeled namespace it keeps a RoleBinding named `kubemoot-kubernetes-mcp-read` to the
  `kubemoot-kubernetes-mcp-read` ClusterRole. The ClusterRole holds reads only: pods, pod
  logs, services, endpoints, ConfigMaps, events, volume claims, workloads, jobs, ingresses,
  network policies, autoscalers, Flux releases and sources, and Kubemoot resources. It holds no
  Secrets, no `exec` and no write verbs.
- The bound accounts are the `spec.serviceAccountName` of every MCPServer that lists the
  `kubernetes` capability, in any namespace. A server without its own ServiceAccount runs as
  `default`, which is never bound, even when a server names it. The label therefore consents
  to every Kubernetes MCP server in the cluster, not to one crew, so whoever may create an
  MCPServer with the `kubernetes` capability and name a ServiceAccount is trusted with every
  labeled namespace. The [use grant](#granting-use-on-a-secret) on ServiceAccounts is the
  control on which accounts an author may name.
- When the label is removed or set to anything but `true`, when the namespace is deleted, or
  when the last Kubernetes MCP server goes, the operator deletes the RoleBinding. It deletes
  only a RoleBinding that carries its own labels.
- It keeps one ClusterRoleBinding, `kubemoot-kubernetes-mcp-namespaces`, to a ClusterRole that
  grants `get`, `list` and `watch` on `namespaces` and nothing else. The namespace list is
  cluster-scoped, so a RoleBinding cannot grant it. A server can therefore see that a
  namespace exists without reading anything in it.

The operator may bind exactly five ClusterRoles, by name (`bind` with `resourceNames`): the
read role, the namespace-list role and the three opt-in roles below, so it cannot hand out
any other role through this path. A compromised operator could still bind those five in any
namespace, labeled or not.

### Secrets and write access

Reading Secrets and changing workloads are separate consents, each with its own label, and a
server must also ask for the access. A label alone binds nobody, and the server's request
alone grants nothing:

| Namespace label | Annotation word on the MCPServer | Role bound there | Grants |
|---|---|---|---|
| `kubemoot.ai/crew-read-secrets=true` | `secrets` | `kubemoot-kubernetes-mcp-secrets` | `get`, `list`, `watch` on Secrets |
| `kubemoot.ai/crew-read-secrets=true` | `secret-get` | `kubemoot-kubernetes-mcp-secret-get` | `get` on Secrets, for a server that checks one named Secret |
| `kubemoot.ai/crew-write=true` | `write` | `kubemoot-kubernetes-mcp-write` | `pods/exec` create; patch and scale on workloads |

Treat `crew-write` as also exposing Secrets: `pods/exec` reads the Secrets mounted into any
pod and the pod's ServiceAccount token, and patching a workload can mount any Secret or run it
as any ServiceAccount in the namespace. Consent is cluster-wide, not per crew: every server
that asks, in any namespace, is bound in every namespace that carries the label, so whoever
may create such an MCPServer and name a ServiceAccount is trusted with all of them.

A server asks with the annotation `kubemoot.ai/kubernetes-access`, a comma separated list of
the words above (an unknown word is ignored), for example `kubemoot.ai/kubernetes-access: "secrets,write"`. The namespace
owner decides whether the access exists at all.

To see how many namespaces consent, read the count on the server:

```bash
kubectl get mcpserver -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,NAMESPACES:.status.readConsentNamespaces
```

`0` means the server reads no namespace. The operator also records a `NoReadConsent` event
on the server in that case and a `ReadConsent` event when the count changes.

Cluster-scoped kinds beyond the namespace list (nodes, volumes, storage classes, CRDs) are
not granted here. A crew whose server needs them grants them in its own chart.

## Target design

Closing the gap uses Kubernetes authorization and admission, with nothing new invented.
Steps 2 and 3 are built behind the `security.secretUseGrants.enabled` chart flag, described
above. The rest is on the [roadmap](../../introduction/roadmap/).

1. **Admin-owned Secrets.** An admin creates each Secret in the crew namespace. Crew
   authors have no `get` or `list` on Secrets.
2. **A `use` grant.** The admin grants the custom verb `use`, not read, on one named
   Secret through a Role with `resourceNames`, bound to whoever applies the crew, such as
   the Flux identity for that namespace.
3. **Admission check.** A ValidatingAdmissionPolicy on MCPServer create and update asks
   the Kubernetes authorizer, through the CEL `authorizer`, whether the requester may
   `use` every referenced Secret and ServiceAccount. If not, the MCPServer is rejected.
   This closes the confused-deputy path where the operator, which can mount Secrets,
   acts for someone who cannot.
4. **Per-tool ServiceAccounts.** The operator mounts a Secret only into its own
   MCPServer's pod, under a ServiceAccount for that server. Agent pods get no Secret
   access.
5. **Cluster access only where needed.** Only tools that call the Kubernetes API, such
   as the Kubernetes MCP server, reference an admin-made ServiceAccount, behind the same
   `use` check, and its RBAC bounds the tool. Every other tool runs as a ServiceAccount
   with no permissions and no mounted token.
6. **Egress per tool.** A NetworkPolicy per MCPServer limits where a tool can connect,
   so the mail tool reaches only the mail server.

Narrow token scopes and human confirmation for actions with side effects complete the
picture. Later steps add a secret store such as OpenBao with External Secrets Operator
for rotation, short-lived credentials, and audit, and per-user OAuth for assistants that
act on behalf of one person.

## What to do without the check

- Turn on `security.secretUseGrants.enabled` and grant `use` per Secret.
- Keep credentials in the namespace of the crew that uses them, and give each crew its
  own namespace.
- Limit who can create MCPServers and Agents in a namespace to people you would trust
  with every Secret in it.
- Scope every token narrowly: read-only where reading is enough, limited to the one
  repository, mailbox, or API the tool needs.
- Never put a secret in a prompt, a `promptHint`, a ConfigMap, or an Agent environment
  variable. Put it in a Secret and reference it from the MCPServer.
