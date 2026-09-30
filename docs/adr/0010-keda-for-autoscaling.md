---
title: "10. KEDA for Event-Driven Autoscaling"
weight: 10
---

Date: 2026-02-20

## Status

Proposed (not implemented)

Nothing in the operator creates `ScaledObject` resources today. See the [Roadmap](../../introduction/roadmap/) for scale to zero.

## Context

Kubemoot has two scaling concerns:

1. **MCPServer / MCPGateway** - tool servers and gateways that handle request load. Idle most of the time; bursty during discussions. Sometimes serving hundreds of tool calls per minute, sometimes zero for hours.
2. **Agent runtime pods** - each agent's Deployment consumes memory for the Quarkus runtime + NATS subscriptions even when no discussions are active. With 15+ agents, that's a noticeable always-on footprint.

Standard Kubernetes HPA is built around CPU/memory metrics, which don't match the workload. Tool servers may be idle (low CPU) but should still scale up when a discussion starts. Agents are NATS subscribers; the right scaling signal is JetStream consumer lag, not CPU.

Options considered:
- **Custom HPA logic** - operator-managed Deployment replica counts. Reimplements what autoscaling controllers already do.
- **KEDA** (Kubernetes Event-Driven Autoscaling) - CNCF graduated, supports scale-to-zero, drives HPA with event sources (Prometheus, NATS, Redis, SQS, Kafka, many more). Layers on top of standard HPA without conflict.

## Decision

We will use KEDA for event-driven autoscaling of MCPServer, MCPGateway, and Agent workloads. The operator will create `ScaledObject` CRs alongside Deployments when `autoscaling.enabled: true`. Tool servers will scale on Prometheus metrics (request rate); agents will scale `0↔1` on NATS JetStream consumer lag for their crew's `kubemoot.discuss.<namespace>.<crew>.>` subjects.

## Consequences

- Agents at idle consume zero pod resources. KEDA scales `0→1` on the first discussion message in their subscribed channel; pod starts (~3-5s), connects to NATS, joins the discussion.
- Discussion responsiveness during cold-start has a one-time latency hit per agent per idle period. The coordinator's advisory timeout may need tuning if many agents are cold at once; the shared Ollama instance is likely warm from recent traffic, so the latency is pod startup + NATS connect, not model load.
- Same KEDA installation handles both Prometheus-driven and NATS-driven scaling, different trigger types on the same controller. No second autoscaler to manage.
- KEDA scale-from-zero is faster than ephemeral Kubernetes Jobs (which take 5-15s to schedule, too slow for the 2-second settle window in consensus discussions). The Deployment already exists; only replicas change.
- KEDA introduces a dependency on the cluster. KEDA itself runs in a dedicated namespace; its scaler pods are stateless and lightweight.
- ScaledObject CRs add another layer of resource per autoscaled workload. The operator manages them as owned resources; deletion cascades.

## References

- [KEDA - Kubernetes Event-Driven Autoscaling](https://keda.sh)
- [KEDA NATS JetStream Scaler](https://keda.sh/docs/scalers/nats-jetstream/)
- KEDA is deployed as a cluster addon.
