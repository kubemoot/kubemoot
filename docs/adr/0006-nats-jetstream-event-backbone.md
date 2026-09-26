---
title: "6. NATS JetStream as Event Backbone"
weight: 6
---

Date: 2026-01-20

## Status

Accepted

## Context

Kubemoot has several real-time event flows that don't fit Kubernetes-watch semantics:

- Inter-agent discussion threads with multiple concurrent contributions
- Per-agent chat events streaming to the dashboard
- Operator audit/chronicle events
- MCP quality evaluation results
- Dashboard real-time topology updates

Kubernetes watch APIs are good for resource state (`status` field changes) but not for application-level events (a chat message, a discussion contribution, an audit entry). These need pub/sub semantics with persistence and replay so the dashboard can show historical context on page load and so crashed agents can re-read the threads they missed.

Options considered:
- **Redis pub/sub** - no persistence, no replay
- **Kafka** - operational overhead too high for a small cluster; ZooKeeper or KRaft, broker tuning, topic partitioning
- **Kubernetes-native CRD events** - Kubernetes throttles events and prunes them after 1h; not suitable for application messaging
- **NATS JetStream** - lightweight broker, persistent streams with retention windows, wildcard subscriptions, WebSocket transport for browser clients, single Helm chart deploy

## Decision

We will use NATS JetStream as the event backbone for all real-time communication between operator, agents, dashboard, and inter-agent collaboration. NATS will be deployed via the official `nats/nats` Helm chart in a dedicated `nats` namespace, with a memory limit set on the broker (for example `GOMEMLIMIT=400MiB`, sized to the broker's expected load) and JetStream file storage on a durable CSI storage class (for example `nfs-csi`, or whatever ReadWriteOnce class the cluster provides). The subject hierarchy will be `kubemoot.<domain>.<...>` with five streams: `KUBEMOOT_CHRONICLE`, `KUBEMOOT_QUALITY`, `KUBEMOOT_CHAT`, `KUBEMOOT_OPERATOR`, `KUBEMOOT_DISCUSS`. Agent runtime, operator, and dashboard will share a single lazy NATS connection per process; when `NATS_URL` is unset, all NATS operations are no-ops (graceful degradation).

## Consequences

- One broker, one Helm chart, one set of credentials to manage; operational footprint is light.
- Persistent streams give the dashboard historical replay on page load and let agents recover from crashes mid-discussion.
- Wildcard subscriptions (`kubemoot.discuss.*.<threadId>`) let the coordinator collect signals from all channels with a single subscription.
- WebSocket transport (port 8080) means the browser can subscribe directly to NATS subjects via `nats.ws`; the dashboard's SSE proxy is a thin server-side bridge.
- A `GOMEMLIMIT` is mandatory: without one, the Go runtime allocator can drive the broker pod into OOMKill under bursty load.
- JetStream storage on a durable CSI class is intentional: file-based, durable, no SQL backend to manage. A small PVC (a couple of GiB) suffices for a single-cluster deployment; retention windows (24h on `KUBEMOOT_DISCUSS`, count limits elsewhere) prevent unbounded growth.
- Tight coupling to NATS at the messaging layer. If NATS proves unsuitable at scale, the lazy-connection abstraction means the migration boundary is narrow, but the subject patterns are baked into the agent-runtime and dashboard code.

## References

- `kubemoot/agent-runtime/src/main/java/.../NatsConnectionProvider.java`
- `kubemoot/operator/internal/nats/publisher.go`
- NATS is deployed as a cluster addon.
- [NATS JetStream documentation](https://docs.nats.io/nats-concepts/jetstream)
