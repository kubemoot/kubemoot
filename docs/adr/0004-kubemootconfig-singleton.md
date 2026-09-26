---
title: "4. KubemootConfig Cluster-Scoped Singleton"
weight: 4
---

Date: 2025-12-10

## Status

Accepted

## Context

The Kubemoot operator launches several supporting workloads - the indexer (Java) for RAGSource jobs, the query service (Python) per RAGSource, the agent-runtime (Quarkus) for each Agent, the mcp-gateway and mcp-bridge for tool servers. Each needs a container image reference, an imagePullSecret, and a few defaults (vector store type, embedding model, OTel collector endpoint).

Hardcoding these in the operator source means a recompile + rebuild + reroll to update an image version. Putting them in environment variables on the operator Deployment means a Helm upgrade for any image bump. Spreading them across each CR is verbose and error-prone - every Agent would carry the same image reference. Different namespaces in the cluster must agree on these values (an agent in `crew-homelab-pilot` and an agent in `kubemoot` should use the same runtime image, or the user gets surprised when they don't).

## Decision

We will store image versions and operator defaults in a cluster-scoped `KubemootConfig` CRD with a well-known singleton name `default`. Controllers will read from a thread-safe in-memory cache (`internal/config/ConfigCache`) that watches the singleton and exposes `GetImages()` and `GetDefaults()` accessors. CI/CD pipelines will update image versions by patching the singleton; no operator restart or recompile required.

## Consequences

- Image bumps are one-line patches to a single CR; CI/CD runs `kubectl patch kubemootconfig default --patch '{"spec":{"images":{"agentRuntime":"..."}}}'` after each build.
- Every controller pulls from one source of truth; image drift between namespaces is impossible.
- The cluster scope is intentional - there is no "per-namespace KubemootConfig" because that would reintroduce the drift problem.
- `ConfigCache` adds a `sync.RWMutex` on the read path; for the volume of reads (hundreds per reconcile cycle) this is negligible.
- A singleton creates one obvious failure mode: if `KubemootConfig/default` is missing, every controller's first reconcile fails until it's created. The operator chart installs the singleton on `helm install`; failure here is a deployment bug, not a runtime risk.
- Future scope expansion (scheduler strategy, retention windows, default rate limits) lands naturally as new fields on the singleton rather than new CRDs.

## References

- `kubemoot/operator/internal/config/config_cache.go`
- `kubemoot/operator/chart/kubemoot-operator/templates/kubemootconfig.yaml` - singleton installation
- [kubemootconfig-guide.md](../reference/kubemootconfig-guide.md) - usage and field reference
