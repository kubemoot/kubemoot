---
title: "Data Retention and Limits"
description: "What Kubemoot keeps in NATS, how long, who decides, and the limit every store carries."
weight: 4
---

Kubemoot keeps no one's data longer than its crews' retention policies say, and every store the install creates has a declared limit. This page lists what is kept where, who decides how long, and how to change it. The server itself (NATS) is the installer's: its disks, encryption and backups are outside this page.

## Who decides

Two things decide how long data lives:

- **A crew's retention policy** for the data that belongs to a crew: its conversations, its working memory, its discussion messages. It is part of the Crew (`spec.retention`, see [the Crew reference](../reference/crew.md#retention)). The operator runs a cleanup for each crew on the crew's schedule and removes what has expired, by the crew's key prefix, from the shared stores.
- **The install's limits** on each shared store: an age, a size, or both. They are set once when the chart's bootstrap job runs, and the job brings an existing store to them on every upgrade. A store whose limit is lower than a crew's policy wins: the policy can shorten what a store keeps, never extend it past its own limit.

A crew that declares nothing still has the defaults the Crew schema carries. Read them from the cluster, not from this page: `kubectl explain crew.spec.retention`.

## What is kept where

| Data | Store | Who decides how long | Size limit |
|------|-------|----------------------|------------|
| Conversations (earlier turns) | KV `kubemoot_conversations`, key `conv.<namespace>.<crew>.<id>` | the crew: `retention.conversations.window` and `retention.purgeUnused`; the bucket's own expiry is only a backstop at the longest window a crew may declare | yes |
| Crew memory (facts) | KV `kubemoot_crew_memory`, key `<namespace>.<crew>.<topic>.<key>` | the crew: `retention.memory.purgeUnused`, which may be `never`; the bucket has no expiry | yes, plus `memory.maxFacts` per crew |
| Discussion messages | stream `KUBEMOOT_DISCUSS`, subject `kubemoot.discuss.<namespace>.<crew>.>` | the stream's age (chart `nats.streams.discuss`), and the crew may shorten it with `retention.other.window` | yes |
| Coordinator logs | stream `KUBEMOOT_COORDINATOR` | the stream's age; `retention.other.window` may shorten it | yes |
| Queued questions | stream `KUBEMOOT_REQUEST` | the stream's age; `retention.other.window` may shorten it | yes |
| Direct chat | stream `KUBEMOOT_CHAT` | the stream's age | yes |
| Operator and MCP chronicle | streams `KUBEMOOT_OPERATOR`, `KUBEMOOT_CHRONICLE`, `KUBEMOOT_QUALITY` | the stream's age | yes |
| Tool results spilled during a thread | object store `kubemoot_workspace` | deleted when the thread closes; the store's age is the backstop | yes (chart `nats.stores.workspace`) |
| Fitness artifacts | object store `kubemoot_fitness_artifacts` | one age for the whole store, whichever component writes first (chart `nats.stores.fitnessArtifacts`) | yes |
| Scheduled reminders | KV `kubemoot_scheduled` | until the reminder fires; the bucket expires a record after the longest window a crew may declare | yes |
| Scheduler performance history | KV `kubemoot_scheduler_performance` | not a crew's data: no wall-clock expiry; removed when its ModelServer is deleted | yes |
| Leases, tickets, heartbeats, demand, server state, crew resumes | KV buckets of the same names | each has a short expiry or is deleted with its owner | yes |

When a KV bucket reaches its size limit it refuses new writes until entries expire or the cleanup removes them; a stream drops its oldest messages instead. A store that cannot take writes is a reason to look at the policy that governs it, not to raise the limit first.

The bootstrap job removes the legacy `kubemoot_pinned_threads` bucket if it exists; nothing writes it.

## Upgrading

A crew's retention policy takes effect on upgrade. A crew that declared nothing takes the schema defaults, which keep an unused fact for a shorter time than the fact age earlier releases applied, so facts a crew has not used for longer than its policy allows are removed at the first cleanup. A crew that set `memory.ttlDays` keeps that value (the field is deprecated). To keep memory longer, set `retention.memory.purgeUnused` on the Crew before upgrading. The shared stores are brought to their declared limits by the bootstrap job in the same upgrade.

## Seeing what the cleanup did

Each Crew reports its last cleanup in `status.retention` (when it ran and what it removed, by kind) and a `RetentionValid` condition that says whether the policy was read and warns when a duration is finer than the schedule. See [the Crew reference](../reference/crew.md#what-the-crew-reports).

```bash
kubectl get crew <name> -n <namespace> -o jsonpath='{.status.retention}'
kubectl get crew <name> -n <namespace> -o jsonpath='{.status.conditions[?(@.type=="RetentionValid")]}'
```

## Changing it

- **One crew:** set `spec.retention` on the Crew (in the crew's chart values, or the Crew manifest).
- **A store's limit:** the chart values `nats.streams.discuss` and `nats.stores`; the other limits are in the bootstrap job's script (`templates/nats-streams-job.yaml`). Upgrade the chart: the job applies them to existing stores.
