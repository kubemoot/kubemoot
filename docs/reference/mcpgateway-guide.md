---
title: "MCPGateway Guide"
description: "MCPGateway deploys the hub that routes agent tool calls to MCPServers. Agents connect to the gateway, which discovers and manages the matching servers."
weight: 7
---

## Overview

MCPGateway is a Kubemoot CRD that deploys an MCP gateway - the central hub that routes agent tool calls to MCPServers. Agents connect to the gateway (not individual MCPServers), and the gateway discovers and manages connections to all matching MCPServers.

## Architecture

```
Agent Runtime
    │
    │  KUBEMOOT_GATEWAY_ENDPOINT
    ▼
MCPGateway (Deployment + Service)
    │
    ├── MCPServer: kubernetes-mcp  (via label selector)
    ├── MCPServer: nats-mcp        (via label selector)
    ├── MCPServer: github-mcp      (via label selector)
    └── MCPServer: fetch-mcp       (via label selector)
```

The gateway:
1. Discovers MCPServers matching its `mcpServerSelector`
2. Connects to each server's HTTP/SSE endpoint (provided by the mcp-bridge for stdio servers)
3. Performs MCP protocol initialization (initialize → notifications/initialized → tools/list)
4. Aggregates all discovered tools and exposes them to agents
5. Routes tool calls from agents to the correct MCPServer

## Admin API access

The `kubemoot` gateway serves two ports.

| Port | Serves | Who can call it |
|------|--------|-----------------|
| Tool port (`spec.port`, 8080) | Tool calls, the MCP endpoints, and the read-only tool lists `GET /admin/tools`, `GET /admin/servers/{id}/tools` and `GET /admin/search/tools` | Every pod, as before. Agents and their sidecars read the tool list here |
| Admin port (8081) | Registration and removal of MCP servers (`POST /admin/servers`, `DELETE /admin/servers/{id}`), the server list, and the feedback scrape (`/admin/feedback`) | The operator only |

`/mcp/messages` stays on the tool port by design: tool calls are the crew's normal path. Admin paths are normalized before they are matched (decoded, slashes collapsed, path parameters and trailing slash removed), and ambiguous encodings are refused with 400.

An external MCP server's API key travels in a registration, so the registration routes are not served on the tool port at all (they answer 404 there). On the admin port every request needs `Authorization: Bearer <token>`; a missing or wrong token gets 401, and the token is compared in constant time and never logged.

The operator generates the token, one per MCPGateway:

- It creates the Secret `<gateway>-admin-token` (key `token`, 32 random bytes) the first time it reconciles the gateway, owned by the MCPGateway, and reuses it on every later reconcile. Deleting the MCPGateway deletes the Secret.
- The Secret is mounted into the gateway as `MCP_GATEWAY_ADMIN_TOKEN` (with `MCP_GATEWAY_ADMIN_PORT`), and the operator presents it on every registration, removal and feedback call.
- The operator also creates a NetworkPolicy `<gateway>-admin` that admits the tool port to every caller and the admin port to the operator pod only. Enforcement needs a CNI that implements NetworkPolicy; the token protects the port either way.

To rotate the token, delete the Secret: the operator creates a new one, and because a short fingerprint of the token (never the token) sits on the pod template as an annotation, the gateway pods roll onto it. The operator sends no registration request while it cannot read the token, and retries. If you set `spec.port` to 8081, the admin port moves to 8082.

Existing gateways need no action. On the first reconcile after the operator upgrade, the operator adds the Secret, the NetworkPolicy, the admin port on the Deployment and Service, and the gateway pods roll to the image that serves the admin port. Run the operator and the gateway image from the same release; an older gateway image does not serve the admin port, so registration fails until it is replaced. The ContextForge implementation keeps its own single port and `adminAPISecretRef` token.

## Implementations

| Implementation | Description |
|---------------|-------------|
| `kubemoot` (default) | Kubemoot's native gateway (Java/Spring WebFlux) |
| `contextforge` | IBM ContextForge MCP Gateway (legacy) |
| `microsoft` | Microsoft MCP Gateway |
| `docker` | Docker MCP Gateway |

The reference deployment uses the `kubemoot` implementation. The gateway image comes from KubemootConfig (`spec.images.mcpGateway`).

## Spec Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `implementation` | enum | `kubemoot` | Gateway implementation to deploy |
| `mcpServerSelector` | LabelSelector | | Selects MCPServers to register (all in namespace if empty) |
| `coreServerSelector` | LabelSelector | | Also registers the core MCP servers this selects; unset registers none, empty registers all |
| `logLevel` | enum | `INFO` | Gateway log level: `DEBUG`, `INFO`, `WARN` |
| `port` | int32 | 8080 | Gateway service port |
| `replicas` | int32 | 1 | Number of gateway instances |
| `auth` | MCPGatewayAuth | | Authentication configuration |
| `adminUI` | bool | `true` | Enable admin interface; unset means enabled, an explicit `false` turns it off |
| `resources` | ResourceRequirements | | CPU/memory requests and limits |
| `imagePullSecrets` | []LocalObjectReference | | Image pull secrets |
| `registries` | MCPRegistriesConfig | | External MCP registry sources |
| `toolIndex` | ToolIndexConfig | | Vector store for semantic tool search |
| `metaTools` | MetaToolsConfig | | Discovery meta-tools (search_tools, load_tools) |
| `credentialPolicies` | []CredentialPolicy | | Credentials for dynamically discovered MCPs |
| `qualityPolicyRef` | string | | MCPQualityPolicy for filtering discovered MCPs |
| `catalogRefs` | []string | | MCPCatalog resources for server discovery |

The `kubemoot` gateway logs each tool call at INFO (`ToolController`, "Tool call request") and a server registration at INFO only when it is new or changed, so `kubectl logs` keeps hours of tool calls and confirms tool use. Set `logLevel: DEBUG` on one gateway to see every re-registration; no rebuild is needed.

### MCPGatewayAuth

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Require authentication |
| `type` | enum | `none` | Auth type: `jwt`, `basic`, `none` |
| `jwtSecretRef` | string | | Secret with JWT signing key |
| `basicAuth` | BasicAuthConfig | | Basic auth credentials |

### MetaToolsConfig

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable search_tools and load_tools meta-tools |
| `maxResultsPerSearch` | int | 10 | Max results from search_tools |
| `discovery` | DiscoveryConfig | | On-demand tool discovery via catalog agent |

### CredentialPolicy

Credential policies define how dynamically discovered MCP servers get authentication. First matching policy wins. Explicit MCPServer CRs always take priority.

| Field | Type | Description |
|-------|------|-------------|
| `categories` | []string | Categories this policy applies to (use `["*"]` as catch-all) |
| `transport` | enum | Override transport protocol for matched servers |
| `serviceAccountName` | string | K8s service account for RBAC |
| `secretRef` | string | Secret for env vars |
| `secretVolumes` | []SecretVolume | Secrets mounted as files |
| `emptyDirVolumes` | []EmptyDirVolume | EmptyDir volumes |

## Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Pending`, `Deploying`, `Indexing`, `Ready`, `Error` |
| `ready` | bool | Gateway accepting connections |
| `endpoint` | string | Gateway service URL |
| `adminEndpoint` | string | Admin API URL on the admin port (if enabled); reachable by the operator only, it needs the bearer token and is not browser-reachable |
| `registeredServers` | int | Count of registered MCPServers |
| `mcpServers` | []string | Names of registered servers |
| `toolIndex` | ToolIndexStatus | Tool index readiness and count |
| `catalogSync` | CatalogSyncStatus | Catalog sync progress |

## Server Selection

The gateway discovers MCPServers via `mcpServerSelector`. The most common pattern uses Helm release labels:

```yaml
spec:
  mcpServerSelector:
    matchLabels:
      app.kubernetes.io/instance: homelab-pilot
```

This selects all MCPServers deployed by the same Helm release. MCPServers must also have `registry.enabled: true` (the default).

If `mcpServerSelector` is empty, all MCPServers in the namespace are registered.

### Core MCP servers

Core MCP servers run once, in the operator's namespace, and any crew can use them
instead of running its own copy. A server is offered to crews when it carries the label
`kubemoot.ai/core-mcp: "true"`; other servers in the operator's namespace stay private.
A gateway registers the offered servers that its `coreServerSelector` matches:

```yaml
spec:
  mcpServerSelector:
    matchLabels:
      kubemoot.ai/crew: my-crew
  coreServerSelector: {}          # every offered core server
```

or only some of them:

```yaml
  coreServerSelector:
    matchLabels:
      kubemoot.ai/tools: accelerators
```

Things to know:

- **Names are shared.** The gateway registers a server by its bare name, so a server in
  the gateway's own namespace with the same name as a core server takes precedence.
  When that changes (the crew adds or removes a server of that name), the gateway
  registers the name again at the new endpoint. Give core servers names crews will not
  reuse.
- **The opt-in is the access boundary.** Anyone who can create an MCPGateway can select
  any server labeled `kubemoot.ai/core-mcp: "true"`; nothing else limits it. Label only
  servers every crew may use.
- **No credentials cross namespaces.** A core server with `registry.authSecretRef` is
  not offered to crews, since its secret lives in the operator's namespace.
- A gateway in the operator's namespace already sees those servers through
  `mcpServerSelector`.
- The gateway reaches a core server by its cluster address, so a network policy that
  isolates the operator's namespace must allow crew gateways in.

## MCP Protocol Initialization

The gateway must complete the MCP protocol handshake before tools are available:

1. Gateway sends `initialize` request to the MCPServer (via bridge SSE endpoint)
2. Server responds with capabilities
3. Gateway sends `notifications/initialized` notification
4. Gateway sends `tools/list` to discover available tools

For stdio/bridge transport, the gateway uses fire-and-forget initialization: send initialize, wait 500ms, send notifications/initialized, wait 500ms, then discover tools. A retry (2s delay) runs if 0 tools are found initially.

## Tool List Changes

A server's tools can change while its URL stays the same: its pod is replaced with new arguments (for example `--toolsets=core,config,helm` adds `helm_list`), its container restarts with a new image, or the server changes its tools at runtime. The gateway lists the tools again when one of these states shows:

| Signal | What the gateway does |
|--------|-----------------------|
| A stdio (bridge) server sends `notifications/tools/list_changed` on its SSE stream | Lists the tools again on the open session. The gateway does not hold a listening stream to streamable HTTP servers, so for them the signals below apply |
| The MCP server process restarts inside its pod | The mcp-bridge sends `notifications/tools/list_changed` to its SSE clients once the new process has completed its handshake, which re-lists as above |
| The SSE stream to the server ends (the pod was replaced or went away) | Marks the server `DISCONNECTED`; the next tool call or operator re-registration opens a new session and lists the tools on it |
| A streamable HTTP server answers `400`, `401` or `404` for the session | Opens a new session, retries the call, and lists the tools on the new session |
| The operator re-registers a server that is `DISCONNECTED`, `ERROR`, or has no tools | Connects again and lists the tools. A burst of re-registrations starts one connect |
| The operator re-registers a connected server whose list is older than `mcp.gateway.tool-list-max-age` (default `10m`) | Lists the tools again. This is a safety net for a change that none of the signals above reported |

A re-list that returns no tools, or fails, keeps the tools already known (a replacement server may still be starting) and marks the server `ERROR`, so the next re-registration connects again. When several re-lists overlap, only the most recently started one updates the list. `GET /admin/tools` and `GET /admin/servers/{id}/tools` always serve the latest list.

Agents read `GET /admin/tools` when they start and again before an evaluation once their copy is older than 60 seconds (10 seconds while it is empty). A tool that is added, removed, or given a new description or input schema reaches running agents that way, without restarting them.

## Tool Argument Types

A model often sends an argument in the wrong JSON type: a number as a string (`"n_items": "8"`) or a list as one comma-separated string (`"content_types": "books,videos,courses"`). A tool server that validates its input rejects the call, the agent retries, and the repeated error text crowds the context. The gateway knows each tool's input schema from `tools/list`, so before it forwards a `tools/call` it converts an argument to the type the schema declares when the conversion loses nothing:

| Schema wants | Value sent | Forwarded as |
|--------------|-----------|--------------|
| `integer` | a numeric string such as `"8"` or `"8.0"`, or an integral decimal such as `8.0` | the integer `8` |
| `number` | a numeric string such as `"0.25"` | the number `0.25` |
| `boolean` | `"true"` or `"false"` in any letter case | the boolean |
| array of strings (items have no `format`, `enum` or `const`) | a comma-separated string `"a, b"` | `["a", "b"]` |
| array | a single value | a one-item array, the item itself coerced to the item type |

Objects and arrays are walked recursively, so nested properties and array items are converted by their own schemas. An optional parameter declared as `anyOf` or `oneOf` of one type and `null` is treated as that type. A schema that resolves no type (a `$ref`, an `allOf`, or an `anyOf` with more than one non-null branch) is not converted. Whitespace around each item is trimmed when a string is split.

Everything else is sent as the model wrote it and the tool server's error comes back unchanged: a decimal for an integer (`"8.5"`), a number that would lose precision, `"yes"` or `"1"` for a boolean, an empty string, a string that starts with `[` or `{`, a list with an empty element (`"a,,b"`), and a value that matches no declared type. A property the schema does not declare is not touched. A property marked `writeOnly`, `format: password`, `secret: true` or `x-secret: true` is never converted, and neither is anything inside it.

The conversion applies to every route into a tool server (the MCP endpoint and `POST /tools/{name}/call`), because it runs where the gateway forwards the call. Each conversion is logged at debug with the tool and field name (for example `opts.deep`, or `tags[]` for an array item) and never the value.

The MCP Java SDK and the JSON Schema validator it uses (networknt) only validate arguments; the validator's loose-type mode accepts a mismatched type without changing the value that is sent, so the tool server would still reject it. The gateway keeps the conversion in `ToolArgumentCoercion`.

## Request metadata (_meta)

MCP's `tools/call` carries per-request metadata in `params._meta`. The gateway forwards only the three requester keys listed below, on every route into a tool server: the MCP endpoint reads them from the `_meta` the caller sent, and `POST /tools/{name}/call` takes an optional `_meta` object next to `arguments`. It re-validates each value (`timeZone` must be a known IANA zone id of at most 64 characters, `localDateTime` an ISO local date-time with a four-digit year and no offset, `requestDate` an ISO date) and drops every other key and every value that fails its check, so a caller cannot pass other fields to a tool through `_meta`. A call with nothing left to forward carries no `_meta`. The gateway does not add values.

Agents use it to give a tool the requester's date and time zone:

| `_meta` key | Value |
|-------------|-------|
| `timeZone` | the requester's IANA time zone, such as `America/Los_Angeles` |
| `localDateTime` | the requester's local date and time, ISO 8601 without an offset |
| `requestDate` | the requester's date, `YYYY-MM-DD` |

A key is present only when the requester's client sent it (and `requestDate` whenever either of the others is). A tool that needs "today" should read it from here and not from its own clock; see [Read the requester's date in a tool](../../user-guides/onboard-mcp-tools/#read-the-requesters-date-in-a-tool).

## Connections to MCP servers

A hosted MCP server's edge closes connections that sit idle, and a request sent on a connection the remote side already closed fails with "Connection reset by peer". The gateway keeps its HTTP connections to MCP servers in one pool that drops a connection before that happens:

| Property | Default | Meaning |
|----------|---------|---------|
| `mcp.gateway.connections.max-idle-time` | `15s` | A connection idle this long is closed and never reused |
| `mcp.gateway.connections.evict-interval` | `10s` | How often the pool looks for connections to close |
| `mcp.gateway.connections.max-life-time` | `5m` | A connection older than this is closed instead of being reused |

Each value must be a positive duration; the gateway refuses to start with the name of the key otherwise.

When a reset still happens on a streamable-http server, the gateway sends `initialize`, `notifications/initialized` and the first `tools/list` once more on a new connection, because they read state and change nothing. Servers reached over SSE or stdio are not retried. A second reset in a row fails the connect and marks the server `ERROR`. The gateway never sends `tools/call` again after a reset: the server may have received and run the call before the connection dropped, and a second send would run it twice. The caller gets the error and decides.

## Example

### Example Gateway

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPGateway
metadata:
  name: example-gateway
spec:
  implementation: kubemoot
  port: 8080
  replicas: 1
  mcpServerSelector:
    matchLabels:
      app.kubernetes.io/instance: homelab-pilot
  adminUI: true
  auth:
    enabled: false
    type: none
```

### Gateway with Tool Index and Meta-Tools

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPGateway
metadata:
  name: gateway-with-search
spec:
  implementation: kubemoot
  mcpServerSelector:
    matchLabels:
      app.kubernetes.io/instance: homelab-pilot
  toolIndex:
    vectorStore:
      type: pgvector
      host: pgvector.pgvector
      port: 5432
      database: vectors
      secretRef: pgvector-credentials
    embeddingModelRef: nomic-embed   # the EmbeddingModel resource; its bound server supplies the endpoint
  metaTools:
    enabled: true
    maxResultsPerSearch: 10
```

`toolIndex.embeddingModelRef` names the `EmbeddingModel` resource the tool index embeds with (in the gateway's namespace, else cluster-wide). The operator takes the endpoint from that resource's `status.endpoint`, so the gateway names no server. Without it, `toolIndex.embeddingModel.model` names the resource, then the KubemootConfig default. The gateway's `ToolIndexEmbeddingResolved` condition is `False` and names the missing resource and the field that named it when no such `EmbeddingModel` exists. Changing the ref on a gateway whose tool index exists updates its RAGSource, which re-indexes the tool index once.

### Gateway with Credential Policies

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPGateway
metadata:
  name: gateway-with-creds
spec:
  implementation: kubemoot
  credentialPolicies:
    - categories: ["kubernetes", "k8s"]
      serviceAccountName: mcp-k8s-access
    - categories: ["github"]
      secretRef: github-token
    - categories: ["*"]
      transport: stdio
```

## Related CRDs: MCPCatalog, MCPQualityPolicy, MCPServerReport

The MCPGateway works with three related CRDs for dynamic server discovery and quality filtering. These are used by the autonomic onboarding system (see [onboarding-guide.md](../operating/onboarding-guide.md)).

### MCPCatalog

MCPCatalog defines an external MCP server registry to discover servers from. The gateway references catalogs via `catalogRefs`.

**Short name:** `mcpcat`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | enum | (required) | `official-registry`, `smithery`, `glama`, `docker`, `npm`, `agent` |
| `url` | string | (required) | Catalog API endpoint or starting URL |
| `agentRef` | string | | Agent CR for navigation (required for `type: agent`) |
| `queries` | []string | | Capabilities to search for (empty = all) |
| `syncInterval` | string | `24h` | Re-discovery interval |
| `maxServers` | int | 100 | Max servers to discover |
| `auth` | CatalogAuth | | Authentication for the catalog API |
| `qualityPolicyRef` | string | | MCPQualityPolicy to evaluate discovered servers |

**Status** tracks `serversDiscovered`, `serversAllowed`, `serversBlocked`, and per-server details including quality decisions.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPCatalog
metadata:
  name: official-registry
spec:
  type: official-registry
  url: https://registry.modelcontextprotocol.io/v0/servers
  syncInterval: "24h"
  maxServers: 50
  qualityPolicyRef: quality-policy
```

**Warning:** Auto-syncing the official MCP registry can deploy community servers that crash. Keep MCPCatalog disabled until quality filtering is robust enough to prevent broken deployments.

### MCPQualityPolicy

MCPQualityPolicy defines trust and quality filtering for discovered MCP servers. Evaluation order: allowing → blocking → tested → considering (AI evaluation).

**Short name:** `mqp`

#### Evaluation Pipeline

```
Discovered Server
    │
    ├── Allowing list match? ──── YES → Allow immediately
    │                              (essential infrastructure MCPs)
    ├── Blocking list match? ──── YES → Block immediately
    │                              (known bad, deprecated, suspicious)
    ├── Tested (MCPServerReport)? ── successRate > threshold → Allow
    │                              ── verdict: "avoid" → Block
    │
    └── Considering (AI eval) ──── agentRef evaluates via LLM
                                   ── confidence > threshold → decision
                                   ── below threshold → fallbackAction
```

| Field | Type | Description |
|-------|------|-------------|
| `allowing` | []AllowingEntry | Servers accepted without evaluation (name or author match) |
| `blocking` | []BlockingEntry | Servers rejected without evaluation (supports glob/regex, semver) |
| `tested` | TestedConfig | Use MCPServerReport trial history for decisions |
| `considering` | ConsideringConfig | AI-based evaluation for remaining servers |

#### ConsideringConfig

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable AI evaluation |
| `agentRef` | string | (required) | Quality evaluator Agent CR |
| `criteria` | string | | Inline evaluation criteria |
| `criteriaFromConfigMap` | ConfigMapKeyRef | | Load criteria from ConfigMap |
| `confidenceThreshold` | string | `0.7` | Min confidence to accept AI decision |
| `timeoutSeconds` | int | 30 | AI evaluation timeout |
| `fallbackAction` | enum | `deny` | Action when AI unavailable: `allow` or `deny` |

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPQualityPolicy
metadata:
  name: quality-policy
spec:
  allowing:
    - author: "modelcontextprotocol"  # Official MCP servers
    - name: "mcp/kubernetes"          # Essential infrastructure
  blocking:
    - name:
        type: glob
        value: "*crypto*"
      reason: "Cryptocurrency-related servers not needed"
    - author:
        type: exact
        value: "known-bad-actor"
      reason: "Known malicious publisher"
  tested:
    enabled: true
    minSuccessRate: "0.8"
    blockBroken: true
  considering:
    enabled: true
    agentRef: quality-evaluator
    confidenceThreshold: "0.7"
    fallbackAction: deny
```

### MCPServerReport

MCPServerReport tracks deployment and runtime experience for an MCP server. One report per server accumulates trial records, enabling Kubemoot to learn which servers work reliably and which to avoid.

**Short name:** `mcprpt`

#### Verdicts

| Verdict | Meaning |
|---------|---------|
| `use` | Working reliably, recommended |
| `caution` | Flaky or mixed results |
| `avoid` | Broken, should not be deployed |
| `untested` | No trials recorded yet |

#### Trial Phases

Each trial records where in the lifecycle it succeeded or failed:

| Phase | Description |
|-------|-------------|
| `deploy` | Container started successfully |
| `connect` | Gateway connected to the server |
| `initialize` | MCP protocol handshake completed |
| `discover` | `tools/list` returned tools |
| `call` | A tool call executed successfully |

#### Spec (identity + admin curation)

| Field | Type | Description |
|-------|------|-------------|
| `serverName` | string | Canonical server name |
| `githubUrl` | string | Source repository URL |
| `registryType` | string | Package registry (npm, docker, pip) |
| `packageIdentifier` | string | Registry-specific package ID |
| `adminVerdict` | enum | Human-pinned verdict override: `use`, `caution`, `avoid` |
| `adminNotes` | string | Explanation for pinned verdict |
| `adminAuthor` | string | Who pinned the verdict |

#### Status (continuously updated)

| Field | Type | Description |
|-------|------|-------------|
| `verdict` | enum | Computed or admin-pinned verdict |
| `recommendedTransport` | string | Most reliable transport |
| `recommendedVersion` | string | Most recent successful version |
| `successCount` | int64 | Total successful trials |
| `failureCount` | int64 | Total failed trials |
| `successRate` | string | Computed ratio (0.0-1.0) |
| `trials` | []TrialRecord | Recent trial history (capped at 20) |
| `chroniclerNotes` | string | AI-generated analysis |

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServerReport
metadata:
  name: kafka-mcp-report
spec:
  serverName: kafka-mcp
  githubUrl: https://github.com/example/kafka-mcp
  registryType: docker
status:
  verdict: use
  recommendedTransport: stdio
  successRate: "0.95"
  successCount: 19
  failureCount: 1
```

Admin override example:
```bash
kubectl patch mcprpt kafka-mcp-report --type=merge -p \
  '{"spec":{"adminVerdict":"avoid","adminNotes":"Crashes under load","adminAuthor":"ops-team"}}'
```

## Troubleshooting

### 0 tools discovered

Check the full chain:
1. MCPServer pods are Running and Ready
2. MCPServer `status.endpoint` is set
3. MCPGateway's `mcpServerSelector` matches the server's labels
4. Gateway pod logs for initialization errors

### Gateway caches failed connections

If the gateway fails to connect to an MCPServer on first registration, it caches the failure and never retries. Delete the gateway pod to force re-connection:
```bash
kubectl delete pod -l kubemoot.ai/mcpgateway=<name>
```

### Agent has no tools

Verify the agent's `KUBEMOOT_GATEWAY_ENDPOINT` env var points to the correct gateway service:
```bash
kubectl exec deploy/<agent-name> -- env | grep GATEWAY
```

### MCP bridge session ID missing

The bridge SSE endpoint event must include `sessionId=` in the data field. If the gateway logs show "skipping initialization" for a server, check the bridge version - older versions may not include the session ID.
