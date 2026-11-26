# Kubemoot Dashboard

A Kubernetes Dashboard-like web UI for observing Kubemoot CRDs and accelerator resources.

## Access

The dashboard serves at the `/dashboard` base path of whatever hostname your Ingress or
Gateway exposes it on. See [Dashboard](../docs/operating/dashboard.md) for the design and
[Installation](../docs/introduction/installation.md) for how it's deployed.

## Features

- **Overview Dashboard**: Summary of all Kubemoot resources with health status
- **Node View**: Cluster nodes with accelerator detection and resource info
- **CRD Views**: List and detail views for all 8 Kubemoot CRDs:
  - ModelServers
  - Models
  - EmbeddingModels
  - MCPServers
  - MCPGateways
  - RAGSources
  - Agents
  - KubemootConfig
- **Namespace Selector**: Filter resources by namespace
- **Real-time Updates**: SSE-based polling for live data
- **Dark Theme**: Consistent with the Homelab Pilot UI

## Tech Stack

- **Framework**: SvelteKit 3 with Svelte 5
- **Adapter**: Node.js adapter for containerization
- **K8s Client**: `@kubernetes/client-node`
- **Base Path**: `/dashboard` for ingress compatibility

## Development

```bash
# Install dependencies
npm install

# Start development server
npm run dev

# Type check
npm run check

# Lint
npm run lint

# Unit tests
npm test

# Build for production
npm run build
```

Before you push, run `npm run lint`, `npm run check`, and `npm test`. `npm run lint`
runs the TypeScript rules of SonarQube's Sonar way profile on `src/` (`.ts` files and
`.svelte` script blocks): the `eslint-plugin-sonarjs` rules in that profile, plus the
typescript-eslint and unicorn rules Sonar runs under its own keys, and a cyclomatic
complexity limit of 10 (see `eslint.config.js`). The repository's
`sonar-project.properties` leaves `dashboard/` out of the SonarQube analysis, so for
now this lint is where those rules are checked for the dashboard.

## Deployment

### Helm

```bash
helm install kubemoot-dashboard ./charts/kubemoot-dashboard \
  --namespace kubemoot \
  --create-namespace
```

### Image

The image is built with Paketo buildpacks, not a Dockerfile. The variables the buildpacks read are in
`buildpacks.env`; the `Procfile` starts the server. To build and run it locally with
[`pack`](https://buildpacks.io/docs/tools/pack/):

```bash
pack build kubemoot-dashboard --builder paketobuildpacks/ubuntu-noble-builder \
  --run-image paketobuildpacks/ubuntu-noble-run --env-file buildpacks.env
docker run -p 3000:3000 kubemoot-dashboard
```

## API Endpoints

### Health & Metadata
- `GET /dashboard/api/health` - Health check
- `GET /dashboard/api/version` - App version
- `GET /dashboard/api/namespaces` - List namespaces
- `GET /dashboard/api/nodes` - Nodes with accelerator info (the response field is still named `gpu`)

### Kubemoot CRDs
All endpoints support `?namespace=` query parameter:
- `GET /dashboard/api/kubemoot/modelservers[/name]`
- `GET /dashboard/api/kubemoot/models[/name]`
- `GET /dashboard/api/kubemoot/embeddingmodels[/name]`
- `GET /dashboard/api/kubemoot/mcpservers[/name]`
- `GET /dashboard/api/kubemoot/mcpgateways[/name]`
- `GET /dashboard/api/kubemoot/ragsources[/name]`
- `GET /dashboard/api/kubemoot/agents[/name]`
- `GET /dashboard/api/kubemoot/config`

### Real-time
- `GET /dashboard/api/kubemoot/watch/<plural>?namespace=` - Kubernetes resource changes as Server-Sent Events
- `GET /dashboard/api/nats/subscribe?subject=` - NATS messages as Server-Sent Events
- `GET /dashboard/api/nats/stream?stream=&subject=&from_seq=` - JetStream replay then live messages as Server-Sent Events
- `GET /dashboard/api/nats/history?stream=&subject=&limit=&before=` - JetStream history: a window of the stream, an older page, or one thread by its subject; `Server-Timing` reports the cost
- `GET /dashboard/api/nats/status` and `GET /dashboard/api/nats/crews` - the NATS tile and page: server, account and store use against limits, and each crew's share

## RBAC

The dashboard requires read access to Kubemoot CRDs and K8s core resources:

```yaml
rules:
  - apiGroups: [""]
    resources: ["nodes", "namespaces", "pods"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["kubemoot.ai"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `NODE_ENV` | Environment mode | `production` |
| `PORT` | Server port | `3000` |
| `HOST` | Server host | `0.0.0.0` |
| `APP_VERSION` | Version displayed in UI | From VERSION file |
| `KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR` | Label selector (for example `kubemoot.ai/workshop-team=true`); only matching namespaces are shown, resolved against the live cluster | empty (all namespaces) |
| `KUBEMOOT_DASHBOARD_INFRA_NAMESPACE` | With a selector set, the operator namespace whose model servers, models, embedding models, MCP catalogs and quality policies stay visible | `kubemoot` |
| `KUBEMOOT_DASHBOARD_ANONYMOUS_READ_ONLY` | `true` lets a visitor who has not signed in see the read views with the dashboard's ServiceAccount; they can never ask or change anything | `false` |
| `KUBEMOOT_DASHBOARD_SESSION_MAX_MINUTES` | The longest a sign-in lasts (a token that expires sooner ends it sooner) | `480` |
| `KUBEMOOT_DASHBOARD_GATEWAY_URL` | Where a crew's discussion gateway is; `{crew}` and `{namespace}` are replaced | `http://<crew>-discussion.<namespace>.svc.cluster.local` |

## Sign-in and read-only

Every page shows a sign-in page until the person pastes a Kubernetes bearer token. The server checks
it with a TokenReview and keeps it only in memory, in a session keyed by a random id held in an
HttpOnly, Secure, SameSite=Strict cookie that ends no later than the token. Every Kubernetes call of
a signed-in request uses that token (`src/lib/server/k8s/client.ts`), so the person's RBAC decides
what they see; a 403 shows "you do not have permission" in the view. Asking a crew goes through the
crew's discussion gateway with the same token. NATS reads still use the dashboard's own read-only
connection.

The server refuses every request that is not GET, HEAD or OPTIONS with 405, except sign-in,
sign-out and asking a crew (`src/lib/server/auth/access.ts`), and every one of those must come from
the dashboard's own origin. No route writes to Kubernetes or NATS, and the chart's ServiceAccount
holds only `create` on tokenreviews unless anonymous read-only viewing is on, when it also holds get,
list and watch. A test fails if a write route, a browser write request, or a write verb in the
chart's RBAC comes back.

### Namespace-scoped mode

The same image can run as a view of a few namespaces, for example for workshop
attendees who `kubectl port-forward` to it. With a selector set, namespace lists, every
Kubemoot list, get and watch, NATS history, stream, subscribe and key-value reads, discussion
artifacts and fitness transcripts show only matching namespaces; a get outside them answers
404. Nodes, the KubemootConfig, operator health and version stay visible (cluster
infrastructure). Every route under `src/routes/api` either uses `#lib/server/scope` or is
listed as cluster-scoped, with a reason, in `src/routes/api/route-modes.test.ts`.

The Helm chart renders it as a second Deployment of the same image with its own
read-only ServiceAccount: `--set dashboard.enabled=true --set dashboard.workshop.enabled=true`
(selector in `dashboard.workshop.namespaceSelector`).

## License

Copyright 2026. Apache License, Version 2.0.
