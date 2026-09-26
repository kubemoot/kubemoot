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

# Build for production
npm run build
```

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
- `GET /dashboard/api/sse?namespace=` - Server-Sent Events

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
