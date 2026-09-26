---
title: "MCPServer Guide"
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

### Bridge Probes and the MCP Initialize Handshake

For stdio-transport MCPServers the operator wires three Kubernetes
probes on the bridge sidecar. All three target `/healthz`
(pipes-connected); see *Probe design* below for why readiness gates on
`/healthz` rather than the stricter `/readyz` handshake signal.

The probes:

| Probe | Endpoint | Period | Failure threshold | Role |
|-------|----------|--------|-------------------|------|
| `startupProbe` | TCP socket | 1s | 10 | Gates the rest of the lifecycle while the bridge process starts up. While startupProbe is running, readinessProbe + livenessProbe are suspended. |
| `readinessProbe` | `/healthz` | 10s | 3 | Gates Service routing - returns 200 when the bridge process is alive AND its stdio pipes are connected to the MCP server. |
| `livenessProbe` | TCP socket | 30s | 3 | Restarts the pod when the bridge process itself is dead. |

#### Probe design

The bridge ships a `/readyz` endpoint that returns 200 only after the
bridge observes a successful MCP `initialize` response come back from
the server. This signal would gate Service routing on the actual
protocol-defined "ready to serve tool calls" handshake, eliminating
the race where stdio MCP servers (FastMCP and similar) open their
stdin/stdout pipes early but take 30-60s of cold-start work before
they can answer tool calls. In that window, `/healthz` returns 200
(pipes connected) but tool calls fail with `Received request before
initialization was complete`.

The probes target `/healthz` rather than `/readyz` because gating
readiness on `/readyz` creates a chicken-and-egg deadlock: `/readyz`
waits for a client-initiated `initialize` to come through the bridge,
clients only connect to pods the Service routes to, and the Service
only routes to pods passing readiness. New pods could never enter the
rotation; they would cycle through startup-probe failures
indefinitely. Gating on `/healthz` (pipes connected) avoids the
deadlock, and the layers below compensate for the looser guarantee.

The `/readyz` endpoint and its handshake-tracking logic remain in the
bridge so a stricter readiness gate becomes possible once the bridge
itself sends the initial `initialize` request as a proxy-client when
pipes first connect, making `/readyz` reachable before any external
client connection.

Layered resilience that compensates for the absence of strict
readiness gating:

- **Pod-level (probes):** `/healthz` ensures the bridge process and
  its pipes are alive before Service routes to a pod.
- **Service-level (replicas):** `replicas: 2` (the default) means a
  pod restarting cleanly never takes the service down - the other
  replica continues to serve.
- **Agent-level:** the agent runtime's failure-signal architecture
  bounds tool retries and publishes structured `failure` consensus
  signals when an MCP call still doesn't return cleanly.

The bridge exposes two endpoints to distinguish "alive" from "ready":

- **`/healthz`** returns `200` when the bridge process is running AND
  its stdio pipes are connected to the MCP server. This is a
  liveness-style signal - used by the livenessProbe only. It does NOT
  guarantee the MCP server will accept tool calls.

- **`/readyz`** returns `200` only when the bridge has observed a
  successful MCP `initialize` response from the server (the
  protocol-defined "I am ready" handshake completed). Resets to
  `503` on every stdio disconnect so a restarted MCP server appears
  un-ready until it completes a fresh handshake. Used by
  startupProbe + readinessProbe.

Internally the bridge tracks the handshake by parsing JSON-RPC traffic
through its `handleMessage` and `readStdoutPipe` paths: outgoing
`method=initialize` request IDs are recorded; incoming responses with
matching IDs and a non-error result flip the `initialized` flag. Error
responses to initialize (server failed its own handshake) intentionally
do NOT flip readiness - the pod stays un-ready and Kubernetes will
eventually restart it via the liveness path if it never recovers.

This three-probe pattern combined with `replicas: 2` (the default)
gives three independent layers handling MCP transience:

1. **Pod-level (probes):** never route a tool call to a pod whose MCP
   isn't initialized.
2. **Service-level (replicas):** if one replica is restarting, the
   Service has another to route to.
3. **Agent-level:** the agent runtime's failure-signal architecture
   bounds tool retries and publishes structured `failure` consensus
   signals when an MCP call still doesn't return cleanly.

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

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `image` | string | | Container image. Either this or `externalEndpoint` required |
| `externalEndpoint` | string | | URL of an external MCP server (no deployment created) |
| `command` | []string | | Override container entrypoint |
| `args` | []string | | Arguments to the MCP server |
| `transport` | enum | `http` | Transport protocol: `http`, `sse`, `stdio` |
| `securityMode` | enum | `relaxed` | Pod security: `relaxed` or `strict` |
| `proxyInjection` | object | | Override mcp-bridge injection settings |
| `port` | int32 | 3000 | Port the server listens on |
| `replicas` | int32 | 2 | Number of pod replicas |
| `env` | []EnvVar | | Environment variables |
| `secretRef` | string | | Secret name for env vars (envFrom) |
| `secretVolumes` | []SecretVolume | | Secrets mounted as files |
| `emptyDirVolumes` | []EmptyDirVolume | | EmptyDir volumes (override baked-in configs) |
| `resources` | ResourceRequirements | | CPU/memory requests and limits |
| `capabilities` | []string | | Declared capabilities (informational) |
| `healthPath` | string | | HTTP health check path (TCP probe if unset) |
| `readinessPath` | string | | HTTP readiness path (falls back to healthPath) |
| `serviceAccountName` | string | | K8s service account (needed for cluster access) |
| `registry` | RegistryConfig | | Gateway registration settings |

### RegistryConfig

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Register with matching MCPGateways |
| `categories` | []string | | Searchable tags for tool discovery |
| `authSecretRef` | string | | Secret with auth credentials for this server |

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
| `ready` | bool | Server is accepting connections |
| `endpoint` | string | Service endpoint URL |
| `replicas` | int32 | Current ready replicas |
| `tools` | []MCPTool | Discovered tool names and descriptions |
| `discoverable` | bool | Registered with at least one gateway |
| `registeredWith` | []RegistryStatus | Gateway registration details |

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
2. MCPGateway's `mcpServerSelector` matches this server's labels
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
