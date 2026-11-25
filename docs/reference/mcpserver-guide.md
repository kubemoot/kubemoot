---
title: "MCPServer Guide"
description: "The MCPServer CRD deploys Model Context Protocol servers on Kubernetes, injects the mcp-bridge sidecar for stdio, sets probes, and registers with gateways."
weight: 6
---

## Overview

MCPServer is a Kubemoot CRD that deploys and manages Model Context Protocol servers in Kubernetes. Each MCPServer provides tools that agents can invoke through an MCPGateway.

The operator handles the complete lifecycle: deploying the container, injecting the mcp-bridge sidecar for stdio transport, configuring health probes, and registering with gateways.

## Architecture

```
MCPServer CR
    │
    ├─── Deployment (operator-managed)
    │       ├── MCP Server Container (your image)
    │       └── MCP Bridge Sidecar (auto-injected for stdio)
    │
    ├─── Service (ClusterIP)
    │       └── Exposes the bridge HTTP/SSE endpoint
    │
    └─── Gateway Registration
            └── Registers tools with matching MCPGateways
```

### Tool Chain

Agents never connect to MCPServers directly. The full chain is:

```
Agent → MCPGateway → MCPServer
```

The gateway discovers MCPServers via label selectors and routes tool calls to the appropriate server.

## Transport Modes

### stdio (Recommended)

Most MCP server images only support stdio transport. The operator automatically injects an mcp-bridge sidecar that bridges HTTP/SSE to stdio.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: kubernetes-mcp
spec:
  image: mcp/kubernetes:latest
  transport: stdio
  command: ["node", "dist/index.js"]
```

How it works:
1. Operator adds an init container that copies the mcp-bridge binary to a shared volume
2. The bridge runs as a native sidecar (init container with `restartPolicy: Always`)
3. Bridge creates named pipes (FIFOs) in a shared emptyDir volume
4. The MCP server's stdin/stdout are connected to these pipes
5. Bridge exposes HTTP/SSE endpoints that the gateway connects to
6. Startup, readiness, and liveness probes target the bridge - see *Bridge Probes and the MCP Initialize Handshake* below

The bridge image comes from KubemootConfig (`spec.images.mcpBridge`).

**Why a sidecar.** Most MCP server images speak only stdio, while the gateway needs
HTTP/SSE, and many images are distroless or scratch-based with no shell for a wrapper
script. The bridge is a single static Go binary with no dependencies, run as a native
sidecar so that a bridge failure restarts the bridge alone, and so that the startup probe
holds the main container back until the HTTP/SSE endpoint is ready. It runs in one of two
modes: *shell mode* (`sh -c "exec <cmd> < /pipes/stdin > /pipes/stdout"`) for images with
a shell, and *exec mode* for distroless and scratch images, where the bridge copies itself
into the shared volume and starts the server with `dup2` and `exec`. A tool reply larger than
1 MiB is not delivered: the bridge discards it as it streams, answers the calling agent
right away with an error that asks for a smaller result, and keeps serving later
messages. The limit follows from the model context window, about 128 KB of text, so a
larger result is unusable to an agent. Set `KUBEMOOT_MCP_BRIDGE_MAX_REPLY_BYTES` on the
bridge container to change it. The cost is one extra container and
an `emptyDir` per stdio server.

### Bridge Probes and the MCP Initialize Handshake

For stdio-transport MCPServers the operator wires three Kubernetes probes on the bridge
sidecar:

| Probe | Check | Period | Failure threshold | Role |
|-------|-------|--------|-------------------|------|
| `startupProbe` | TCP socket | 2s | 60 | Gives the bridge process up to 120 seconds to start listening. Readiness and liveness are suspended while it runs. |
| `readinessProbe` | `/readyz` | 5s | 3 | Gates Service routing. Returns 200 only after the bridge has seen a successful MCP `initialize` response from the server. |
| `livenessProbe` | `/healthz` | 30s | 3 | Restarts the bridge when the process is dead or its stdio pipes are disconnected. |

The bridge exposes two endpoints to distinguish "alive" from "ready":

- **`/healthz`** returns `200` when the bridge process is running and its stdio pipes
  are connected to the MCP server. It does not guarantee the MCP server accepts tool
  calls.
- **`/readyz`** returns `200` only when the bridge has observed a successful MCP
  `initialize` response from the server. It resets to `503` on every stdio disconnect,
  so a restarted MCP server appears un-ready until it completes a fresh handshake. An
  error response to `initialize` does not flip readiness.

Together with `replicas: 2` (the default), the probes give layered handling of MCP
transience:

1. **Pod level (probes):** a tool call is not routed to a pod whose MCP server has not
   initialized.
2. **Service level (replicas):** while one replica restarts, the Service routes to the
   other.
3. **Agent level:** the agent runtime bounds tool retries and publishes structured
   `failure` consensus signals when an MCP call still does not return cleanly.

### http

For MCP servers that natively expose an HTTP API. No bridge sidecar is injected.

```yaml
spec:
  transport: http
  port: 8080
```

### sse

For MCP servers using Server-Sent Events. No bridge sidecar is injected.

```yaml
spec:
  transport: sse
  port: 3000
```

**Important:** Most community MCP server images (mcp/kubernetes, cnadb/mcp-nats, github-mcp-server) only support stdio. Using `transport: http` or `transport: sse` with these images causes CrashLoopBackOff because the server never opens the HTTP port.

## Security Modes

### relaxed (Default)

Does not force `runAsUser`/`runAsGroup`. Required for Python images built with uv where the interpreter lives under `/root/` (mode 700). Still enforces: no privilege escalation, drop ALL capabilities, seccomp.

```yaml
spec:
  securityMode: relaxed
```

### strict

Enforces restricted Pod Security Standards: `runAsNonRoot`, `runAsUser: 1000`. Use for images that don't require root filesystem access (e.g., Node.js, Go binaries).

```yaml
spec:
  securityMode: strict
```

## Spec Reference

> **Upgrading:** Helm installs the CRDs in a chart's `crds/` directory once and never updates them. A cluster that was installed before `spec.description` existed shows no descriptions until you apply the new CRDs yourself, for example `kubectl apply --server-side -f` the chart's `crds/` files, before `helm upgrade`. Flux installs that set `crds: CreateReplace` on the HelmRelease update them on their own. Until then the API server rejects or drops a `description` in a manifest.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `description` | string | | What the server is for, in a sentence or two (up to 500 characters). The dashboard shows it on the server's tile and detail page. Agents do not see it; they see the tool descriptions the server reports |
| `image` | string | | Container image. Exactly one of `image`, `kubemootImage`, or `externalEndpoint` is required |
| `kubemootImage` | enum | | Run a Kubemoot-built image without naming a version: `workspaceAccess` (`artifactAccess` is its old name and is removed in the next release). The operator uses the matching `spec.images` entry of [KubemootConfig](../kubemootconfig-guide/). Mutually exclusive with `image` and `externalEndpoint` |
| `externalEndpoint` | string | | URL of an external MCP server (no deployment created). The gateway that fronts it makes the call, so with egress mode `Declared` the operator allows that gateway the URL's host and port; see [A gateway's outbound traffic](../../concepts/network-policies/#a-gateways-outbound-traffic) |
| `command` | []string | | Override container entrypoint |
| `args` | []string | | Arguments to the MCP server |
| `transport` | enum | `http` | Transport protocol: `http`, `sse`, `stdio` |
| `securityMode` | enum | `relaxed` | Pod security: `relaxed` or `strict` |
| `securityContext` | SecurityContext | none | Fields that override the MCP server container's security context set by `securityMode`, for example `readOnlyRootFilesystem: true`; fields left out keep the mode's values |
| `proxyInjection` | object | | Override mcp-bridge injection settings |
| `port` | int32 | 3000 | Port the server listens on |
| `replicas` | int32 | 2 | Number of pod replicas |
| `env` | []EnvVar | | Environment variables |
| `secretRef` | string | | Secret name for env vars (envFrom); see [Secrets and tools](../../concepts/secrets-and-tools/) |
| `secretVolumes` | []SecretVolume | | Secrets mounted as files; every Secret, `env` secret reference and `serviceAccountName` needs the requester's `use` grant when `security.secretUseGrants.enabled` is on, see [Secrets and tools](../../concepts/secrets-and-tools/) |
| `emptyDirVolumes` | []EmptyDirVolume | | EmptyDir volumes (override baked-in configs) |
| `resources` | ResourceRequirements | | CPU/memory requests and limits |
| `capabilities` | []string | | Declared capabilities (informational) |
| `healthPath` | string | | HTTP health check path (TCP probe if unset) |
| `readinessPath` | string | | HTTP readiness path (falls back to healthPath) |
| `serviceAccountName` | string | | K8s service account (needed for cluster access); see [Secrets and tools](../../concepts/secrets-and-tools/) |
| `runtimeClassName` | string | | Kubernetes RuntimeClass for the pod, for example `gvisor`. Empty or unset, the pod sets no `runtimeClassName` and uses the node's default runtime. There is no realm default. While the class does not exist the operator holds the Deployment and reports `RuntimeClassAvailable=False`. See [Isolation](../../concepts/isolation/) and the [code sandbox](../../concepts/code-sandbox/) |
| `registry` | RegistryConfig | | Gateway registration settings |
| `toolOverrides` | []ToolOverride | | Corrected tool and parameter descriptions served to agents, and rules that reject arguments the server would refuse |
| `smokeInputs` | []ToolSmokeInput | | Safe test calls (`name`, optional `arguments`, optional `timeout`) the fitness tool smoke makes through the gateway before any model runs. Read-only tools only. See [Tool smoke](crewfitnesssuite.md#tool-smoke) |
| `egress` | EgressRules | | Where the server may connect. With `security.networkPolicies` on, a server that sets it is restricted to DNS plus this list; see ### EgressRules |

### RegistryConfig

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Register with matching MCPGateways |
| `categories` | []string | | Searchable tags for tool discovery |
| `authSecretRef` | string | | Secret with the key this server needs: `bearer-token` or `api-key` (see [Connect an external MCP server that needs a key](#connect-an-external-mcp-server-that-needs-a-key)) |
| `authHeader` | string | `X-API-Key` | Header that carries the Secret's `api-key`; ignored for a `bearer-token` |
| `callerCredential` | CallerCredential | none | Make a call carry the asking person's own token instead of the server's credential (see [Calls with the asking person's own token](#calls-with-the-asking-persons-own-token)). Unset, the server's own credential is used as before |

### CallerCredential

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mode` | string | `Off` | `Off` or `Required`. `Required` calls the server with the asking person's token and with nothing else |
| `verifier` | string | | Required with `Required`. The name of the crew's `serviceTokens` entry whose people's tokens this server takes |
| `leaseUrl` | string | | The broker's lease endpoint for this server. Empty uses the address the operator sets, the crew's discussion gateway |
| `onForbidden` | []string | none | At most four endpoints tried in order, with the same token, when the server answers 403 to the person. Each receives the token, so each must be `https`; plain `http` is accepted only for `localhost`, `127.0.0.0/8` and `[::1]` |
| `relayContext` | bool | `false` | Keep `conversationId` and `principalId` in the `_meta` forwarded to the server, for a tool server that calls back through the gateway. Works in mode `Off` too: the server is then still called with its own credential, and only the two ids are added, never a token |
| `accountName` | string | the verifier name | How a refusal names the account to connect |

### ToolOverride

An override carries a description and optional rules for one tool. Use it when an upstream description is wrong or misleading and you cannot change the image, or when a description alone does not stop the model sending an argument the server always refuses. The operator sends the overrides to the Kubemoot gateway when it registers the server. The gateway applies the descriptions to the tool list it serves to agents and checks the rules before it forwards a call. The tool's name, parameters, and behavior stay the server's own.

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Tool the override applies to (unique within the list) |
| `description` | string | Replaces the tool description; unset keeps the server's own |
| `parameters` | map[string]{description, rules} | Per parameter, keyed by parameter name: `description` (required) replaces the description; `rules` (optional, at most 16) reject a call before it is forwarded |

A rule is `{reject, message}`: `reject` is an RE2 regular expression (1 to 200 characters) searched anywhere in the parameter's text (the gateway matches with RE2 too, in time linear in the argument), and `message` is the tool error the model gets back (1 to 1000 characters; write it in ADL and name the alternative call). When a string argument matches, the gateway answers with the message and does not call the server; rules are tried in order and the first match answers. An absent, empty, or non-string argument never matches, and only a top-level string is checked: strings inside an object or array are not scanned. A pattern that is not valid RE2 is rejected when the MCPServer is created or updated. There is no separate allow list: a pattern that needs "anything except" is written as a negated character class, such as `(^|,)[^=,]+(,|$)` for a selector term with no `=`. See [Tool Argument Rules](../mcpgateway-guide/#tool-argument-rules).

A name the server does not list is logged by the gateway as a warning and ignored. An edit takes effect on the next reconcile, without restarting the server or the gateway. Other gateway implementations ignore the field.

```yaml
spec:
  toolOverrides:
    - name: helm_list
      description: Lists Helm releases in one namespace, or in every namespace with all_namespaces=true.
      parameters:
        all_namespaces:
          description: Boolean true lists releases in every namespace.
    - name: resources_list
      parameters:
        fieldSelector:
          description: Server-side filter; each term is field=value.
          rules:
            - reject: "=~|!~|[<>]"
              message: "ASSERT a fieldSelector term is field=value or field!=value. NEVER use regex or comparison operators; list without a selector and read the table."
```

### EgressRules

Optional. It has an effect only when the chart value `security.networkPolicies.egress.mode`
is `Declared`; a server without `egress` is never restricted. The same type is used by
[Agent](../agent/#specegress). What each CNI enforces is in [Network policies](../../concepts/network-policies/).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `allowedHosts` | []string | | DNS names the server may reach: `api.example.com`, or `*.example.com` (wildcard in the leftmost label only). Lower case, no scheme, port, or path. Enforced on Cilium; elsewhere the server may reach the internet outside the private ranges |
| `allowedServices` | []EgressService | | Services inside the cluster the server may reach, each `{namespace, name, ports}`. `namespace` defaults to the server's own; `ports` are the ports the Service lists (empty means every TCP port). The operator reads the Service and writes a rule for its namespace, its pod selector and the port its pods listen on, so it works on every CNI. The Service needs a pod selector. Use this, not `allowedHosts`, for a destination inside the cluster |
| `allowedCIDRs` | []EgressCIDR | | Address ranges (`cidr`, and `except` ranges inside it). Enforced on every CNI that enforces NetworkPolicy |
| `ports` | []int32 | `[443]` | TCP ports allowed to the hosts and ranges, 1 to 65535 |
| `bus` | bool | `false` | Allow the message bus (NATS), at the address the operator itself is configured with (the chart value `nats.url`). Set it on a tool server that reads or writes the bus, such as one that reads artifacts. An Agent always reaches the bus |
| `apiServer` | bool | `false` | Allow the Kubernetes API server, for a tool that talks to the cluster |

```yaml
spec:
  egress:
    allowedHosts: ["smtp.example.com"]
    ports: [587]
```

A tool server that queries Prometheus in the cluster and reads artifacts from the bus:

```yaml
spec:
  egress:
    bus: true
    allowedServices:
      - namespace: observability
        name: kube-prometheus-stack-prometheus
        ports: [9090]
```

With the flag on, the operator also generates `km-mcp-<name>-ingress`, which admits only the
crew's gateway and the operator to the server's port.

### ProxyInjectionConfig

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | auto | Defaults to true for stdio, false for http/sse |
| `image` | string | KubemootConfig | Override mcp-bridge image |
| `port` | int32 | 8080 | Bridge HTTP port |

## Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Pending`, `Deploying`, `Ready`, `Error` |
| `image` | string | The image the Deployment runs: `spec.image`, or the KubemootConfig image `spec.kubemootImage` resolved to |
| `ready` | bool | Server is accepting connections |
| `endpoint` | string | Service endpoint URL |
| `replicas` | int32 | Current ready replicas |
| `tools` | []MCPTool | Discovered tool names and descriptions |
| `discoverable` | bool | Registered with at least one gateway |
| `registeredWith` | []RegistryStatus | Gateway registration details |
| `conditions` | []Condition | `Ready`; `ImageResolved` on a managed server (False with the reason when `kubemootImage` cannot resolve); `DeprecatedNames` (True) on a managed server that still uses an old workspace name, with the names to change in its message: `kubemootImage: artifactAccess`, an `ARTIFACT_*` variable workspace-access reads, a pinned `artifact-access` image (which ignores `WORKSPACE_QUERY_ENGINE`), or an `emptyDirVolumes` mount at `/artifacts` on a workspace server; `CredentialReady` on an external server with `registry.authSecretRef`; `EgressEnforced` when the server sets `egress` and the operator generates network policies (False with reason `FQDNNotSupported` when the CNI cannot enforce `allowedHosts`, or `ServiceNotResolved` when an `allowedServices` entry has no Service, no pod selector or no matching port yet); `RuntimeClassAvailable` on a managed server that asks for a RuntimeClass (`True` when the class exists, `False` with reason `RuntimeClassMissing` and phase `Pending` when it does not) |

## Examples

### Kubernetes MCP (stdio, with RBAC)

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: kubernetes-mcp
spec:
  image: mcp/kubernetes:latest
  transport: stdio
  securityMode: relaxed
  command: ["node", "dist/index.js"]
  replicas: 1
  serviceAccountName: kubernetes-mcp
  capabilities:
    - kubernetes
    - kubectl
    - helm
  registry:
    enabled: true
    categories:
      - kubernetes
      - infrastructure
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 200m
      memory: 128Mi
```

A server with the `kubernetes` capability reads what its ServiceAccount's RBAC allows, and
the operator grants none of it. The crew chart that deploys the server ships the ServiceAccount,
its roles, and a RoleBinding in each namespace it should read; whoever creates a RoleBinding in a
namespace consents for it. Name the account in `serviceAccountName`: a server that runs as the
namespace `default` account has no access beyond what you bind to `default`. See
[Namespaces a Kubernetes tool may read](../../concepts/secrets-and-tools/#namespaces-a-kubernetes-tool-may-read).

### NATS MCP (stdio, strict security)

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: nats-mcp
spec:
  image: cnadb/mcp-nats:latest
  transport: stdio
  command: ["/app/mcp-nats"]
  securityMode: strict
  replicas: 1
  env:
    - name: NATS_URL
      value: "nats://nats.nats.svc.cluster.local:4222"
    - name: NATS_NO_AUTHENTICATION
      value: "true"
  capabilities:
    - messaging
    - pubsub
  registry:
    enabled: true
    categories:
      - messaging
      - nats
```

### GitHub MCP (stdio, with secret)

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: github-mcp
spec:
  image: ghcr.io/github/github-mcp-server:latest
  transport: stdio
  command: ["/server/github-mcp-server"]
  securityMode: relaxed
  replicas: 1
  env:
    - name: GITHUB_PERSONAL_ACCESS_TOKEN
      valueFrom:
        secretKeyRef:
          name: github-token
          key: password
  capabilities:
    - github
    - repositories
  registry:
    enabled: true
    categories:
      - github
      - vcs
```

Note: The github-mcp-server binary is at `/server/github-mcp-server`, not `/github-mcp-server`. The image uses `gcr.io/distroless/base-debian12`.

### Run a Kubemoot-built server without naming a version

A crew written as plain manifests never pins or floats an image tag for a server Kubemoot
builds. Name it with `kubemootImage` and the operator fills in the image from the release
that installed it:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: artifact-readops-mcp
  namespace: my-crew
spec:
  kubemootImage: workspaceAccess   # no image, no tag
  transport: stdio
  command: ["/cnb/process/web"]
  port: 3000
  replicas: 1
  env:
    - name: WORKSPACE_MODE
      value: "mcp"
    - name: NATS_URL
      value: "nats://nats.nats.svc:4222"
    - name: WORKSPACE_BUCKET
      value: "kubemoot_workspace"
  capabilities: [data-access, data-analysis]
  securityContext:
    readOnlyRootFilesystem: true
    allowPrivilegeEscalation: false
    capabilities:
      drop: [ALL]
    seccompProfile:
      type: RuntimeDefault
```

This is the read-ops server that agents call (`workspace_head`, `workspace_tail`, `workspace_grep`,
`workspace_query`; the older `artifact_*` names are served too and are removed in the next release) to read a tool result, which is always stored as a file in the artifact store. See [Discussion workspace](../../concepts/workspace/).

How it behaves:

- `kubemootImage`, `image`, and `externalEndpoint` are mutually exclusive. The CRD rejects
  a spec that sets more than one, and the admission webhook rejects a spec that sets none.
- The operator reads the image from `spec.images` of the `default` KubemootConfig, which the
  operator chart stamps from the release. `status.image` shows the image in use.
- When the KubemootConfig changes the image (an operator upgrade), the operator updates the
  Deployment and the pods roll to the new image. Nothing in the crew changes.
- When the KubemootConfig does not exist, or has no image for that name, the server stays
  in phase `Error` with `ImageResolved=False` and a message naming what is missing. It
  recovers on its own once the image appears.
- The sidecars of an MCPServer (the workspace-access materializer next to the code sandbox)
  still name their image; `kubemootImage` applies to the server's main container.

### Calls with the asking person's own token

Some services bill or entitle each person, so a call must carry the token of the person who asked, not one key for everyone. Set `registry.callerCredential.mode: Required` on the server. The crew then needs a [`serviceTokens` entry with `passToTools: true`](../crew/#a-persons-own-token-from-a-service) whose `name` is the server's `verifier`; the crew's Crew resource reports the result in its `CallerCredentials` condition.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: example-search
spec:
  externalEndpoint: https://mcp.example.com/api/v1/mcp/
  transport: http              # Required needs http
  registry:
    authSecretRef: example-mcp-auth   # lists the tools; never sent on a call
    callerCredential:
      mode: Required
      verifier: example
      onForbidden:
        - https://mcp.example.com/api/v1/search/mcp
```

The operator passes the field through the registration and wires the rest, only for a gateway whose servers include one in mode `Required`: the MCPGateway runs as its own ServiceAccount (`<gateway>-credential-broker`), carries a projected token for the audience `kubemoot-credential-broker`, and is told the address of the crew's lease port. The server's `authSecretRef` lists the tools at registration and is never sent on a call in this mode: with no token for the turn the call fails, and the agent reports that the person's account is not connected. How the gateway makes the call and what the agent sees is in the [MCPGateway guide](../mcpgateway-guide/#calls-with-the-asking-persons-own-credential).

The transport must be `http`; the API server refuses `Required` on any other. Each `onForbidden` entry must be `https`, because it receives the person's token; a gateway whose outbound traffic is restricted is allowed each one and the crew's lease port, see [A gateway's outbound traffic](../../concepts/network-policies/#a-gateways-outbound-traffic). A namespace uses one MCPGateway and one crew that keeps tokens; with more, the crew's `CallerCredentials` condition says so.

### Connect an external MCP server that needs a key

Many hosted MCP servers want a key on every request. Keep the key in a Secret in the
same namespace as the MCPServer and point `registry.authSecretRef` at it. The operator
hands the key to the MCPGateway when it registers the server, and the gateway sends it
as a header on every call to that server (the initialize handshake, tool listing, and
tool calls) and on no call to any other server.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: example-mcp-auth
type: Opaque
stringData:
  api-key: "<your key>"      # or bearer-token: "<your token>"
---
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: example-search
spec:
  externalEndpoint: https://mcp.example.com/api/v1/mcp/
  transport: http
  registry:
    authSecretRef: example-mcp-auth
    authHeader: X-Example-Key   # only for api-key; defaults to X-API-Key
```

| Secret key | Header sent |
|------------|-------------|
| `bearer-token` | `Authorization: Bearer <token>` |
| `api-key` | `<authHeader>: <key>`; `authHeader` defaults to `X-API-Key`. Set `authHeader: Authorization` and store the whole value (for example `Token abc`) when the server expects a different scheme |

If both keys are present, `bearer-token` is used. An `externalEndpoint` whose path already
ends in `/mcp` (with or without a trailing slash) is called at exactly that URL; any other
streamable HTTP URL gets `/mcp` appended.

The MCPServer reports whether it could read the key in the `CredentialReady` condition.
A missing Secret (`SecretNotFound`), a Secret with neither key (`KeyMissing`), or an
invalid header name or multi-line value (`InvalidHeader`) also leaves the server not
ready, and it is not registered with the gateway until the Secret is fixed. The operator
watches the Secret, so creating or rotating it needs no other action. If the Secret is later removed or becomes invalid, the gateway keeps the last key it was given until the server is registered again or removed.

How the key is handled:

- It is read from the Secret only when a registration is sent, and never appears in the
  MCPServer status, events, metrics, logs or error messages.
- The gateway keeps it in memory only. The gateway's server list and tool endpoints never
  return it, and a gateway restart waits for the operator to register the server again.
- The operator registers servers with the gateway over the in-cluster Service. The
  gateway's admin API has no authentication of its own, so the key travels in plain HTTP
  inside the cluster, like the other credentials the operator sends to a gateway. Restrict
  who can reach the gateway Service with a NetworkPolicy where that matters.

## Troubleshooting

### CrashLoopBackOff with stdio servers

If using `transport: http` or `transport: sse` with an image that only supports stdio, the container crashes because it never opens an HTTP port. Always use `transport: stdio` for community MCP server images.

### MCP Bridge not injected

The bridge is only injected when `transport: stdio`. Verify with:
```bash
kubectl get pod -l kubemoot.ai/mcpserver=<name> -o jsonpath='{.items[0].spec.initContainers[*].name}'
```

### Tools not discovered

Tools are discovered by the MCPGateway, not the MCPServer controller. Check:
1. MCPServer has `registry.enabled: true` (default)
2. MCPGateway's `mcpServerSelector` matches this server's labels, or, for a core server in the operator's namespace, the server is labeled `kubemoot.ai/core-mcp: "true"` and the gateway's `coreServerSelector` matches it
3. Gateway pod logs for connection errors

### Gateway caches failed connections

If the gateway fails to connect on first registration, it caches the failure. Delete the gateway pod to force re-connection:
```bash
kubectl delete pod -l kubemoot.ai/mcpgateway=<gateway-name>
```

### Binary path wrong

Always verify the binary path inside MCP server images before deploying:
```bash
kubectl run --rm -it test --image=<image> -- ls /
```

Known paths:
- `github-mcp-server`: `/server/github-mcp-server`
- `cnadb/mcp-nats`: `/app/mcp-nats`
- `mcp/kubernetes`: `node dist/index.js`
