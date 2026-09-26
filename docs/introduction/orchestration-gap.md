---
title: "The Orchestration Gap"
weight: 30
description: "Frameworks build an agent and Kubernetes runs a container, but neither operates a multi-agent AI workflow as infrastructure. That gap is Kubemoot's lane."
---

A prototype agent is easy to build and hard to operate. The distance between "it
works on my machine" and "it runs as dependable infrastructure" is where most agentic
AI projects stall. This page describes that distance, and why neither the frameworks
above it nor Kubernetes below it closes it.

## From prototype to production

Building an agent is a good experience today. You wire up a framework (LangChain,
LlamaIndex, an SDK), point it at a model, add a retriever and a few tools, and it
works on your laptop against test data. Then it has to run for real, and a different
set of questions starts:

- Which GPU does it run on, and what happens when several requests arrive at once?
- Where does the vector index live, and who keeps it current?
- How do you stand up a second and third agent without each one re-implementing model
  access, retrieval, and tool plumbing?
- What is it costing, and which step is slow when an answer takes too long?

None of these are answered by the framework, because they were never its job. The
framework's job ends at "call a model"; everything after that is operations.

## What production actually demands

A handful of agents running for real need, at minimum:

- **Model access that is not one hardcoded endpoint** - local GPUs and hosted APIs
  behind a single abstraction, chosen per call as load and availability shift.
- **GPU scheduling that understands model memory.** A 24 GB card can hold a 7B and an
  8B model at once, or a single larger model; which combination is loaded depends on
  what is being asked right now. That decision belongs at the moment of inference, not
  baked into a Deployment at deploy time.
- **Retrieval as infrastructure** - vector sources that are declared, provisioned, and
  kept fresh, not a Python script someone remembers to run.
- **Tools as a managed service** rather than per-agent glue code.
- **Coordination across more than one agent** - shared context, and a way to surface
  and resolve disagreement instead of trusting a single voice.
- **Cost and observability across a multi-step run**, not just one API call.

These are operational concerns, not library features, and they recur in every project
that gets past the prototype.

## Where today's tools stop

This is not an empty field. Model-serving and MLOps platforms - KServe, Seldon,
BentoML, Ray Serve, vLLM, NVIDIA NIM, Kubeflow - solve a real and adjacent problem:
take a trained model and run it behind a scalable, observable endpoint. They are good
at *serve this model*.

They are not built for the layer above it: a multi-step, multi-model **workflow**.
Serving answers "how do I host one model?" It does not answer "how do I run a crew of
small models that call tools, retrieve from RAG, share GPUs just-in-time, coordinate
with each other, and produce an answer I can measure?" That orchestration is left to
application code, so it gets written again, in Python, for the next project.

## The missing layer

```
  Application / framework code
  (LangChain, LlamaIndex, SDKs)          <- where you build the agent
  ---------------------------------------
  ???  agentic workflow orchestration    <- unfilled
  ---------------------------------------
  Kubernetes + model serving
  (Deployments, KServe, vLLM, GPUs)      <- where a model is hosted
```

Frameworks give you the agent. Kubernetes and the serving platforms give you a hosted
model and a container. Between them, the orchestration of a deliberating, multi-model
workflow as operable infrastructure is missing, so teams build it by hand and rebuild
it the next time.

## What that layer requires

Closing the gap means making the workflow itself declarative and operable, the same
way the rest of a cluster is. Concretely:

- **Model providers as resources** - local GPU and hosted APIs unified, selected per
  inference call rather than pinned at deploy time.
- **GPU scheduling** that places each call on a provider with room for it, the way the
  Kubernetes scheduler bin-packs pods onto nodes.
- **Retrieval as a declared source** that is vectorized and served for you.
- **Tools behind a managed gateway**, discovered rather than wired in.
- **Agents and their prompts as versioned objects**, applied with `kubectl` and
  diffed in `git`, not recompiled and redeployed as code.
- **A crew that coordinates the agents** and resolves disagreement by deliberation.
- **Quality measured over time**, so a change can be shown to help or hurt rather than
  eyeballed in a transcript.

This is the layer Kubemoot provides: cloud-native orchestration for agentic AI
workloads, with crews, agents, prompts, and models as first-class Kubernetes
resources. The same shape of problem appears in image and audio pipelines; Kubemoot's
focus is the agentic, multi-model case.

## Next

- [Why Kubemoot](../why-kubemoot/) - where it fits and how it differs from a single
  model, a code-based framework, or a hosted platform.
- [Overview](../kubemoot-brief/) - what's in the box.
- [Quickstart](../quickstart/) - run a crew and ask it something.
