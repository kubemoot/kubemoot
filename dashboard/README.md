# Kubemoot Dashboard

A Kubernetes Dashboard-like web UI for observing Kubemoot CRDs and GPU resources.

## Access

The dashboard serves at the `/dashboard` base path of whatever hostname your Ingress or
Gateway exposes it on. See [Dashboard](../docs/operating/dashboard.md) for the design and
[Installation](../docs/introduction/installation.md) for how it's deployed.

## Features

- **Overview Dashboard**: Summary of all Kubemoot resources with health status
- **Node View**: Cluster nodes with GPU detection and resource info
- **CRD Views**: List and detail views for all 8 Kubemoot CRDs:
  - ModelProviders
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

- **Framework**: SvelteKit 2.0 with Svelte 5
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

### Docker

```bash
docker build -t kubemoot-dashboard .
docker run -p 3000:3000 kubemoot-dashboard
```

## API Endpoints

### Health & Metadata
- `GET /dashboard/api/health` - Health check
- `GET /dashboard/api/version` - App version
- `GET /dashboard/api/namespaces` - List namespaces
- `GET /dashboard/api/nodes` - Nodes with GPU info

### Kubemoot CRDs
All endpoints support `?namespace=` query parameter:
- `GET /dashboard/api/kubemoot/modelproviders[/name]`
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

## License

Copyright 2026. Apache License, Version 2.0.
