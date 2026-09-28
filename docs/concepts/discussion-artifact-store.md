---
title: "Discussion Artifact Store"
weight: 35
description: "How Kubemoot passes large tool outputs by reference, keeping the message bus and LLM context windows free of bulk data."
---

When a tooler fetches data from a live system, the result can be large: a full
namespace inventory, a Prometheus time-series dump, a JSON response carrying
hundreds of rows. The NATS discussion bus and the LLM context window were not
designed to carry datasets. Dumping bulk data onto the bus truncates it, floods
downstream agents' context, and degrades the quality of reasoning that follows.

Kubemoot passes large outputs **by reference**. The data lands in an object store;
the discussion message carries only a small reference and a preview. Agents that
need the data fetch a targeted slice; most can answer from the preview alone.

## The producer pattern

Any tooler whose output exceeds roughly 4 KiB spills the full result to a NATS
JetStream Object Store bucket named `kubemoot_discussion_artifacts`. The object is
keyed as:

```
{namespace}/{crew}/{threadId}/{agent}/{tool}-{seq}
```

The tooler then publishes its discussion message carrying only a **reference
envelope** rather than the raw data. The full object waits in the store.

## The reference envelope

The reference envelope is a small JSON structure carried inside the tooler's
contribution message. Its fields let any consumer make a smart fetch decision
before opening the object:

| Field | Purpose |
|-------|---------|
| `bucket` | Object store bucket name. |
| `key` | Full object key (namespace/crew/thread/agent/tool-seq). |
| `contentType` | MIME type of the stored object (e.g. `application/json`, `text/csv`). |
| `bytes` | Total object size in bytes. |
| `rows` | Row count for tabular data; 0 for unstructured. |
| `tool` | Name of the MCP tool that produced the data. |
| `agent` | Name of the agent that ran the tool. |
| `createdAt` | ISO-8601 timestamp of the spill. |
| `preview` | Up to 800 characters of the object's leading content. |

The preview and the row/byte counts let many consumers answer the question without
fetching at all. A coordinator synthesizing from "8 namespaces found (preview: default,
kube-system, kubemoot...)" does not need the full list.

## Consumer paths

Not every agent can reach the object store directly, so there are two consume paths
depending on the agent's network posture.

### Network-capable agents (sip from the bus)

Toolers, analysts, and the coordinator can reach NATS directly. They read the
reference envelope, decide whether the preview suffices, and if not, issue a
targeted read through the **read-ops MCP service** (`artifact-access` in `mcp`
mode). That service exposes output-bounded tools that extract only the slice an
agent needs:

| Tool | What it returns |
|------|----------------|
| `head` | First N lines of the object. |
| `tail` | Last N lines. |
| `grep` | Lines matching a pattern. |
| `select` | Named column projection for CSV objects. |
| `count` | Row count (no data transfer). |
| `rows` | Rows at a given offset/limit. |
| `jq` | A JQ expression applied to a JSON object. |

Each tool is output-bounded: it returns only the filtered slice, never the whole
object, so the LLM context receives only what it asked for.

The discipline is "sip, don't slurp": never pull a whole blob into memory or into
an LLM context window. A `grep` for a pod name, a `select` for two columns, or a
`jq` path expression is always cheaper than a full fetch, and keeps the context
tight enough for reliable reasoning.

### Compute sandbox agents (materialised to disk)

Counting, summing, sorting, and filtering are unreliable when a model performs
them by reading a long listing, particularly for small local models; asked how
many pods are running in each namespace, a model can return a confident,
plausible, and wrong number. Kubemoot routes that work to a compute agent
instead: another agent gathers the raw data into the artifact store described
above, the compute agent writes a short Python or bash program that reads it
back, and the sandbox described below runs the program and returns the number
the compute agent reports. The runtime enforces the contract: a compute agent
may not state a number that did not come from running code.

The compute sandbox is
[`code-sandbox`](https://github.com/kubemoot/kubemoot/blob/main/code-sandbox/README.md),
an MCP stdio server that exposes `execute_code` and `validate_code` for Python
(standard library only) or bash. The pod is the sandbox: it holds no credentials
and its egress is limited by the crew's network policy, so it must not reach NATS
or external services directly. A **materializer sidecar** (the same
`artifact-access` binary in its default mode) fetches referenced objects from the
store and writes them into a shared local volume under `/artifacts`. The sandbox
reads the materialised file and computes over it locally, then publishes its
result through the normal discussion signal path.

Each run happens in a fresh temporary directory that is removed afterwards, with
no stdin: data goes in the code itself or in a file under `/artifacts`, never
piped in. A time limit kills the program's whole process group, and stdout and
stderr are each capped.

The materializer runs as an init step per discussion turn, so the sandbox always
sees files that match the current turn's reference envelopes.

## Garbage collection

Every object stored here is temporary. Three layers of garbage collection ensure
no blobs survive their discussion.

**Layer 1: lifecycle-tied deletion.** When a discussion thread closes and leaves
the coordinator's in-memory state, the coordinator issues a prefix-delete for that
thread's artifact key prefix (`{namespace}/{crew}/{threadId}/`). All objects for the thread are
removed immediately.

**Layer 2: bucket TTL.** The `kubemoot_discussion_artifacts` bucket carries a
48-hour object TTL. Any object that Layer 1 misses (coordinator restart mid-thread,
exceptional close paths) is reaped by the NATS server automatically.

**Layer 3: orphan reaper.** A periodic background job scans the bucket for objects
whose thread prefix is no longer in the active-thread registry and that are older
than a configurable grace window. Objects that clear both conditions are deleted.
The reaper runs independently of the per-thread lifecycle so it cleans up across
restarts.

The three layers together mean there are no immortal blobs and no orphaned objects
accumulating in the store.

## Why this design

The pattern mirrors how a CLI agent (or a human analyst) works: data lands on a
filesystem, reasoning tools sip from it selectively, and the working memory holds
only the conclusions. It applies the same discipline to the Kubemoot discussion:
keep the bus and the LLM context lean, let the object store hold weight, and
provide precise retrieval tools so agents fetch only what they need.

## Related

- [Conversations, Turns & Threads](../conversations-turns-and-threads/) - the
  thread whose lifecycle governs artifact GC layer 1.
- [Signals & Protocol](../signals-and-protocol/) - the discussion messages that
  carry reference envelopes.
- [Tooler-Analyst Architecture](../../architecture/tooler-analyst-architecture/) -
  the agent roles that produce and consume artifacts.
- [MCP Tools](../mcp-tools/) - how agents access the read-ops MCP service.
