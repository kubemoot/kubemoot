---
title: "Kubemoot Architecture - Distributed Consensus vs Single-Model Orchestration"
description: "Compare Kubemoot's committee of small specialist models and a coordinator with a single large-model agent, and what each architecture gains and costs."
weight: 1
linkTitle: "Consensus vs Single-Model"
---

> *"Kubemoot composes capability horizontally - many small specialists, because no single model is big enough to do it all; intelligence is emergent from a committee. A single-model agent is the opposite: capability concentrated in one large reasoner that drives tools and, when needed, spawns copies of itself."*
> - a single-model coding agent, contrasting itself with Kubemoot

## Purpose

There are two broad shapes an agentic system can take. Kubemoot is one of them; most well-known agent systems are the other. The fastest way to understand *why Kubemoot is built the way it is* - many small models, a coordinator, consensus signals over a message bus - is to hold it next to its opposite and see what each shape buys and costs.

This document does that. It is an architectural orientation, not a sales pitch and not an API reference.

## The two shapes

**Single-model orchestration.** One large, capable model sits in an agent loop. It reasons, calls tools, and when a task is big it spawns *sub-agents* - ephemeral copies of itself, each with its own context - does the work, and synthesizes the result. Coordination is **delegation**: a strong central reasoner hands out work and stitches the answers back together. Most coding assistants and agent frameworks built around a single frontier model take this shape.

**Distributed consensus (Kubemoot).** Many small domain-focused models run as independent agents. No one of them is capable enough to answer alone, so they *deliberate*: each contributes from its domain, publishes a signal (`agree`, `concern`, `block`, `stand_aside`), and a coordinator settles the result from the assembly. Coordination is **negotiation**: capability is composed from many parts rather than concentrated in one. (The name fits - a *moot* is an assembly that deliberates.)

Almost every concrete difference below follows from one root fact: **where the capability lives.** Single-model systems concentrate it; Kubemoot distributes it. Concentrated capability is naturally *delegated*; distributed capability must be *negotiated*.

## The dimensions of contrast

### Capability locus - vertical vs horizontal

Single-model systems scale **vertically**: a bigger model does more, and breadth comes from giving that one model more tools and more sub-agent clones. Kubemoot scales **horizontally**: breadth comes from adding more specialists, each small. This is a deliberate bet that *many small minds in consensus* can reach answers a single large mind would, without depending on access to a frontier model.

### Coordination bus - shared context vs message-passing

This is the cleanest distributed-systems distinction:

- **Single-model: shared memory.** The model's context window *is* the coordination medium. Tool results, sub-agent returns, retrieved documents, and instructions all funnel into one context. Keeping that context coherent (and summarizing it as it grows) is the central engineering concern.
- **Kubemoot: message-passing.** There is no shared brainspace. Each agent has its own small context and coordinates by publishing signals over NATS JetStream (`kubemoot.discuss.<namespace>.<crew>.<channel>.<thread>`). The coordinator assembles a conclusion from the messages; Toolers and Analysts never share a window.

Shared-memory versus message-passing is the same dichotomy as threads versus actors. The single-model approach keeps everything coherent in one place but is bounded by one context window; Kubemoot's approach scales across many nodes but must carry meaning explicitly in messages.

### Retrieval - agentic fetch vs embedding RAG

Single-model systems typically retrieve **agentically**: the reasoner decides it needs something and uses a tool (search, file read) to pull it into context - retrieval *by action*. Kubemoot retrieves by **embedding**: documents and agent résumés are vectorized into pgvector and pulled in by semantic similarity - retrieval *by search*.

The résumé-selection path is the sharpest illustration. Kubemoot embeds each agent's résumé and selects the subcommittee by semantic match to the question - the system *searches* for who should speak. A single-model system makes the equivalent choice *in-context*: the central reasoner simply decides which sub-agent to spawn. Both end with the right participants engaged; one gets there by similarity search, the other by a reasoner choosing.

### Tool overload - partition vs lazy-load

Both shapes hit the same wall: too many tools degrade tool selection (any model chooses worse when the option set is large). They solve it differently. Kubemoot **partitions** tools across agents - each Tooler is given only the tools it needs (the gateway and tool-splitting), so no single agent faces an overwhelming set. A single-model system instead **lazy-loads** tool schemas into its one context on demand, keeping the active set small. Partition-across-agents versus narrow-the-set-for-one - horizontal versus vertical again.

### Parallelism and the coordinator bottleneck

Kubemoot's Toolers and Analysts are genuinely peer-parallel: many small models run concurrently across GPUs. Single-model systems parallelize more narrowly - concurrent tool calls and concurrent sub-agents - but the central reasoner is sequential. Worth noting: *both* shapes have a coordinator that serializes the final synthesis even when the work beneath it ran in parallel. Kubemoot simply has far more real parallelism underneath that chokepoint.

### GPU multiplexing - application layer vs device layer

"Many workloads, few GPUs" gets solved at different altitudes. Kubemoot solves it at the **application/scheduling** layer: just-in-time provider selection picks which model on which GPU at inference time, from live provider state. Device-level approaches (MIG partitions, MPS, time-slicing, GPU operators) solve it at the **hardware/driver** layer by slicing the silicon. These are complementary, not competing - Kubemoot's just-in-time scheduling can run on top of device-partitioned GPUs. The distinction matters because Kubemoot's horizontal design makes GPU multiplexing a first-class scheduling problem rather than an afterthought.

### Summary

| Dimension | Single-model orchestration | Kubemoot (distributed consensus) |
|---|---|---|
| Capability locus | Concentrated (one large model) | Distributed (many small models) |
| Coordination | Delegation & synthesis | Negotiation to consensus |
| Bus | Shared context window | NATS message-passing |
| Retrieval | Agentic (reasoner fetches) | Embedding RAG (semantic search) |
| Participant selection | Reasoner picks sub-agents in-context | Résumé embeddings select subcommittee |
| Tool overload | Lazy-load schemas into one context | Partition tools across agents |
| Parallelism | Narrow (tools, sub-agents) | Broad (peer Toolers and Analysts) |
| GPU sharing | (model-serving concern) | Application-layer JIT scheduling |
| Hard dependency | A single frontier model | None - composes commodity/local models |

## Why Kubemoot takes the distributed path

The single-model shape is simpler and, with a strong enough model, very capable. Kubemoot accepts more coordination complexity in exchange for properties the concentrated shape cannot offer:

- **No dependence on a frontier model.** Capability is assembled from small, swappable models that run on commodity or local GPUs. The system's ceiling is not gated by access to one large model.
- **Modularity and composition.** A capability is a Tooler plus its tools and prompt (or an Analyst plus its RAG sources), added without touching the others. The crew is assembled from parts, declaratively.
- **Consensus as a quality mechanism.** Multiple independent perspectives, with disagreement (`concern`, `block`) and abstention (`stand_aside`) as first-class signals, is a built-in cross-check rather than a bolt-on. Failure is signal, not silence.
- **Portability.** Crews carry no cluster topology in their prompts; agents discover what they need at run time, so the same crew runs on different clusters.

The costs are real and worth naming: consensus has coordination overhead and latency; message-passing demands that meaning be made explicit; and orchestrating many small models is harder than prompting one large one. Kubemoot's design - the coordinator's signal-based settling, just-in-time scheduling, embedding-based selection, partitioned tools - is largely the machinery for paying those costs well.

## When each shape fits

A single large model with delegation is the right default when a frontier model is available, the work fits one coherent context, and simplicity matters. The distributed-consensus shape earns its complexity when you must run without a frontier model (local or cost-constrained), when capability should be composed and swapped modularly, when independent cross-checking is valuable, and when scaling horizontally across many GPUs beats scaling one model vertically.

## The open question

Kubemoot is, at heart, a wager: that *many small models in consensus* can match *one large model with delegation* on real work - and win on portability, cost, and modularity. Whether and where that holds is exactly what the architecture exists to find out, and what its fitness functions are built to measure.
