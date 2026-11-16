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
what an agent can see through the server. The crew that deploys the server grants that
access, with Kubernetes RBAC and nothing else: the crew chart ships the server's
ServiceAccount, its roles, and a RoleBinding in each namespace the chart's values list (or one
ClusterRoleBinding when the values say cluster-wide). Whoever can create a RoleBinding in a
namespace consents for it, and `kubectl get rolebinding -n team-a` shows the grant. The operator
creates no binding for these servers and watches no namespace label.

The reference crew's values, for example:

```yaml
kubernetesAccess:
  namespaces: [team-a, team-b]   # read role bound in each
  secretRead:
    namespaces: [team-a]         # Secret reads, per namespace
  write:
    namespaces: []               # exec, patch and scale, per namespace
```

With no namespace listed and `clusterWide` false the chart binds no read role, and the server
reads nothing inside a namespace. See the
[crew's cluster permissions](https://github.com/kubemoot/crews/blob/main/homelab-pilot-crew/README.md#cluster-permissions)
for the roles and what each reads. Reading Secrets and changing workloads are separate tiers,
each bound only in the namespaces it lists and off by default. Treat the write tier as also
exposing Secrets: `pods/exec` reads the Secrets mounted into any pod and the pod's
ServiceAccount token, and patching a workload can mount any Secret or run it as any
ServiceAccount in the namespace.

Kinds that exist only cluster-wide (the namespace list, nodes, volumes, storage classes, CRDs,
cluster RBAC objects) cannot be granted by a RoleBinding. The crew chart binds a small
read-only ClusterRole of just those kinds, and names the agent that reads each.

Writing your own crew or MCPServer with the `kubernetes` capability follows the same pattern:
ship the ServiceAccount, the roles and the bindings with it, scoped to what the tools need.
The operator's internal `kubernetes-mcp` server (`internalAgents`) reads only its own
namespace's Kubemoot resources until you bind a read role to its ServiceAccount in the
namespaces it should read.

### Upgrading from namespace labels

Earlier releases granted this access from the namespace labels `kubemoot.ai/crew-read`,
`kubemoot.ai/crew-read-secrets` and `kubemoot.ai/crew-write` and the MCPServer annotation
`kubemoot.ai/kubernetes-access`. They do nothing now. Upgrade in this order:

1. Upgrade the crew chart. Its own bindings appear in the namespaces you list, next to the
   operator's old ones.
2. Upgrade the operator. When it starts it deletes the RoleBindings the label controller made
   (labels `app.kubernetes.io/managed-by=kubemoot-operator` and
   `app.kubernetes.io/component=kubernetes-mcp-read-consent`) and the
   `kubemoot-kubernetes-mcp-namespaces` ClusterRoleBinding; later starts find nothing to delete.
   A binding without the operator's labels stays. Upgrading the operator first leaves the crew with no read access until the crew
   chart is upgraded.
3. Remove the three labels from your namespaces when convenient. They are inert.

The `status.readConsentNamespaces` field and the `NoReadConsent` event are gone with the
controller. The count came from labels and has no replacement: the crew chart's own bindings show the grant, and a
render that lists no namespace says so in the chart notes.

## Applying a crew under a tenant account

The use-grant check only constrains an identity that is not a cluster admin. A GitOps
controller that applies every crew as cluster-admin passes every check, so the controller
should apply each crew as an account that holds only that crew's namespace rights plus its
`use` grants. The pattern has five parts, the same with Flux or Argo CD.

1. **An identity per crew namespace.** A ServiceAccount in the crew namespace
   (`flux-crew-mooty` in the examples above). It needs no token: the controller
   impersonates it, so `automountServiceAccountToken: false` stays.
2. **A Role for what the chart renders.** `get`, `list`, `watch`, `create`, `update`,
   `patch` and `delete` on every kind the chart renders, Kubemoot kinds and core kinds
   alike. Run `helm template` on the chart with your values and list the kinds it
   produces; the Role must cover all of them. The reference crew's chart, for example,
   renders `secrets`, `configmaps`, `serviceaccounts`, `networkpolicies`, and the
   Kubemoot kinds `agents`, `crews`, `promptmodules`, `crewschedulingpolicies`,
   `embeddingmodels`, `ragsources`, `mcpservers`, `mcpgateways`, `mcpcatalogs` and
   `mcpqualitypolicies`, so its Role names each of those. A chart that renders a new
   kind needs the Role extended first, or the next upgrade fails `Forbidden` and the
   release stays at its last revision.
3. **The `use` grants.** The Role from [Granting `use` on a Secret](#granting-use-on-a-secret),
   bound to the same account, with `resourceNames` for every Secret and ServiceAccount the
   crew's MCPServers and Agents name.
4. **Apply as that account**, from the crew namespace (below).
5. **Cluster-wide RBAC through a second, admin release** (below).

Check the result before relying on it. These commands are `kubectl auth can-i` with the
account impersonated (write `${ns}:flux-crew-mooty` with braces in zsh, or the shell
mangles the name):

```bash
ns=crew-mooty
u=system:serviceaccount:${ns}:flux-crew-mooty
kubectl auth can-i create mcpservers.kubemoot.ai --as=$u -n $ns        # yes
kubectl auth can-i use secrets/mail-credentials   --as=$u -n $ns        # yes
kubectl auth can-i use secrets/registry-pull      --as=$u -n $ns        # no
kubectl auth can-i create clusterroles            --as=$u               # no
```

A server-side dry run of an MCPServer that names a Secret without a grant, run as the
account, is rejected with the message in [The admission check](#the-admission-check).

### With Flux

Put the HelmRelease in the crew namespace and name the account. helm-controller
impersonates it for every Helm action (read, install, upgrade, drift detection, wait), so
each call is authorized against the account's Role. The controller itself reads the chart
source and its registry credentials, so the account never sees them. Where the controller
allows cross-namespace references, the OCIRepository can stay in the Flux namespace and the
HelmRelease points at it with `chartRef.namespace`, as below. With
`--no-cross-namespace-refs` (which this page recommends for a multi-tenant cluster) that
reference is refused, so create the OCIRepository, and the registry Secret it names, in
the crew namespace; the tenant account needs no rights on either, because the
controller reads them.

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: mooty-crew
  namespace: crew-mooty
spec:
  serviceAccountName: flux-crew-mooty
  releaseName: crew-mooty-mooty-crew   # pin it; see below
  interval: 10m
  chartRef:
    kind: OCIRepository
    name: mooty-crew
    namespace: flux-system   # drop this line when the OCIRepository is in crew-mooty
  values: {}
```

- **The release record is a Secret in the crew namespace.** Helm stores each revision as
  `sh.helm.release.v1.<release>.v<N>` in the HelmRelease's namespace, so the account needs
  `create`, `get`, `list`, `update` and `delete` on Secrets there. `create` and `list` cannot
  be limited by `resourceNames`. The account can therefore read Secrets in its own
  namespace; the `use` check constrains what an MCPServer may reference, not what the
  applier may read.
- **Omitting `serviceAccountName` runs the release as the controller**, which is an admin.
  Enforce the field with an admission policy that rejects a HelmRelease in a crew
  namespace without it, or enable Flux's multi-tenancy lockdown
  (`--default-service-account`), which also covers every other release in the cluster.
- **Moving an existing release into the crew namespace.** Pin `releaseName` to the old
  release name so Helm sees the same release and the chart-derived names do not change. Suspend the old
  HelmRelease first (deleting a suspended HelmRelease removes it without uninstalling the
  release), copy the release Secrets (`kubectl get secret -l owner=helm,name=<release>`) into
  the crew namespace, then apply the new HelmRelease: it upgrades the release in place and
  the crew's objects keep their identity. Copy each record with its server-set metadata
  removed, and with `kubectl create`: `kubectl apply` stores the whole object in an
  annotation, and a large release record exceeds its size limit.

  ```bash
  REL=crew-mooty-mooty-crew; NS=crew-mooty; OLD=flux-system
  for s in $(kubectl -n $OLD get secret -l owner=helm,name=$REL -o name); do
    kubectl -n $OLD get "$s" -o yaml \
      | yq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp, .metadata.managedFields, .metadata.annotations, .metadata.ownerReferences) | .metadata.namespace = "'$NS'"' \
      | kubectl create -f -
  done
  ```

### With Argo CD

Argo CD renders the chart and applies the manifests itself, so it keeps no Helm release
Secrets. The account still needs rights on Secrets when the chart renders one, and the
reference crew's chart does, so its account holds `create`, `get`, `list`, `update`,
`patch` and `delete` on Secrets in the crew namespace. A chart that renders no Secret
needs none. Limit what an
Application may do with an AppProject, and use sync impersonation (a beta feature in Argo
CD, off by default) so the apply runs as the crew's account instead of the controller's:

```yaml
# argocd-cm
data:
  application.sync.impersonation.enabled: "true"
---
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: crew-mooty
  namespace: argocd
spec:
  sourceRepos: ["https://example.com/crews.git"]
  destinations:
    - server: https://kubernetes.default.svc
      namespace: crew-mooty
  clusterResourceWhitelist: []
  destinationServiceAccounts:
    - server: https://kubernetes.default.svc
      namespace: crew-mooty
      defaultServiceAccount: flux-crew-mooty
```

An Application in this project with destination namespace `crew-mooty` then syncs as
`flux-crew-mooty`, and the same Role and `use` grants apply. Without impersonation the
project limits where Argo CD applies, not who applies, so the use-grant check sees the
Argo CD controller and passes. Impersonation is strict by default: a sync with no matching
account fails rather than falling back to the controller.

### A crew that renders cluster-wide RBAC

A tenant account cannot create ClusterRoles, ClusterRoleBindings or RoleBindings, and a
crew whose tool servers read the cluster renders them (the reference crew does, for its
Kubernetes tool servers). Install the same chart twice from the same values, each release
rendering its part:

| Release | Applied by | Values | Renders |
|---|---|---|---|
| tenant | the crew's account | `rbac.render: namespaced` | everything except the RBAC objects |
| RBAC | an admin | `rbac.render: cluster`, `rbac.namePrefix: <tenant release name>` | the ClusterRoles, ClusterRoleBindings and RoleBindings |

Install the RBAC release into the same namespace as the tenant release, because the
bindings name the ServiceAccounts in the namespace they render for, and keep the
`kubernetesAccess` values equal in both. With Flux the RBAC release is a second HelmRelease
without `serviceAccountName` (the controller applies it as an admin) with a different
`releaseName`; with Argo CD it is an Application in an admin project. The tenant account
then needs no RBAC rights at all, so a crew author cannot widen the access the tool servers
hold: whoever may create bindings still decides it. The values and the objects each release
renders are in the
[crew's README](https://github.com/kubemoot/crews/blob/main/homelab-pilot-crew/README.md#applying-with-a-tenant-account).
A chart that does not support `rbac.render` renders its RBAC in the one release, so it needs
an admin applier or the RBAC objects applied separately.

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
