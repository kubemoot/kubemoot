---
title: "Related Projects"
weight: 75
description: "How Kubemoot relates to nearby projects: Kubernetes-native agent platforms, agent frameworks, model serving, tool gateways, and evaluation tools."
---

A common first question is "how is this different from X?" This page answers it for
the projects people ask about most. Each entry says what the project is, where it
overlaps with Kubemoot, and where the two differ. Several of these are the better
choice for some jobs, and the entries say so. Descriptions of other projects are
summaries of their own documentation; read their sites for the current picture.

For Kubemoot's own position, see [Why Kubemoot](../why-kubemoot/) and the
[Questions](../faq/) page.

## Kubernetes-native agent platforms

### kagent

[kagent](https://github.com/kagent-dev/kagent) describes itself as a Kubernetes native
framework for building AI agents. It is a CNCF project under the Apache 2.0 license.

- **Overlap:** both declare agents as Kubernetes custom resources and manage them with
  `kubectl`. An agent in kagent is a system prompt, a set of tools and agents, and an
  LLM configuration. Both connect agents to tools through MCP servers.
- **Differences:** kagent lists OpenAI, Azure OpenAI, Anthropic, Google Vertex AI, and
  Ollama as model providers and ships MCP tool servers for Kubernetes, Istio, Helm,
  Argo, Prometheus, Grafana, and Cilium, and reports OpenTelemetry tracing. Kubemoot's
  hosted-model providers are not yet implemented (Ollama is the model server today) and
  it does not emit OpenTelemetry traces yet, so kagent is the better fit if you want
  agents backed by hosted models, standard tracing, or a ready set of
  cluster-operations tools.
  Kubemoot adds three things on top of the declarative agent: a
  [consensus protocol](../../concepts/consensus-model/) between agents (each
  specialist can agree, raise a concern, object, or abstain, and a coordinator
  synthesizes the answer), a [scheduler](../../concepts/models-and-scheduling/) that
  picks a model server for each inference call, and
  [fitness functions](../../fitness/) that score a crew over time.

### Agent Sandbox

[Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) is a Kubernetes SIG
Apps project (not a CNCF project) with a stable v1.0 release and a `v1beta1` API. It
provides a `Sandbox` custom resource for isolated, stateful, singleton pods with a
stable identity, persistent storage, and pause and resume. Extensions add
`SandboxTemplate`, `SandboxClaim`, and `SandboxWarmPool` for templated, claimable, and
pre-warmed sandboxes. Isolation comes from gVisor or Kata Containers through a
Kubernetes `RuntimeClass`. It ships Go and Python SDKs and an optional Sandbox Router.

- **Overlap:** Kubemoot's compute agent runs the code it writes in a pod-level
  [code sandbox](../../concepts/discussion-artifact-store/), the `code-sandbox` MCP
  server. Agent Sandbox addresses the same need, a safe place to run untrusted,
  model-written code, with stronger isolation and a standard resource.
- **Differences:** Agent Sandbox is a workload primitive. It does not define agents,
  prompts, models, or how agents work together, so it does not replace crews,
  consensus, ADL governance, or fitness. Kubemoot does not use it today. Evaluating it
  as the isolation layer for the code sandbox is a direction on the
  [Roadmap](../roadmap/#evaluating-agent-sandbox-for-code-execution).
- **Background:** [Kubernetes Podcast episode 268, "Agent Sandbox and Lovable"](https://kubernetespodcast.com/episode/268-lovable/).

### Agent Substrate

[Agent Substrate](https://github.com/agent-substrate/substrate) is a standalone,
Google-originated project (not a CNCF project and not part of Kubernetes SIGs). It is pre-1.0, with no
compatibility guarantees. Because agents are idle most of the time, it multiplexes many
agent "actors" onto a smaller set of worker pods. An actor's full state, memory and
filesystem, is checkpointed when it goes idle and restored in well under a second when
it is needed, which allows high oversubscription. An Envoy-based router parks requests
for a suspended actor until it resumes. `WorkerPool` and `ActorTemplate` resources
declare the pools and actors, isolation is gVisor or a microVM, and it is agnostic to
the agent framework.

- **Overlap:** running very many mostly-idle agents densely. Kubemoot's
  [scale to zero](../roadmap/#scale-to-zero) direction pursues a similar saving for idle
  agents with a different mechanism.
- **Differences:** Agent Substrate targets very large numbers of agents running on far fewer pods.
  Kubemoot runs a handful of crews today. Its bottleneck is GPU model scheduling, placing
  and loading models across a few GPUs, which Substrate does not address. Kubemoot's
  agent runtime is LangChain4j compiled to a GraalVM native image, so agent pods start
  quickly compared with a model load, and pod startup is not the problem Substrate
  solves for Kubemoot today. Substrate does not replace crews, consensus, ADL
  governance, or fitness. It stays here to revisit after it reaches a stable release.
- **Background:** [Kubernetes Podcast episode 272, "Agent Substrate"](https://kubernetespodcast.com/episode/272-agent-substrate/).

Both projects solve for very large numbers of agents: density, isolation, and fast
resume. That is a later concern for Kubemoot, not a current one. Agent Sandbox is covered in [Kubernetes Podcast episode 268](https://kubernetespodcast.com/episode/268-lovable/) and Agent Substrate in [episode 272](https://kubernetespodcast.com/episode/272-agent-substrate/).

## In-process multi-agent frameworks

These are libraries. You write agents in code, and the framework runs them inside
your application process. Kubemoot runs crews as Kubernetes services declared in YAML.
The [Orchestration Gap](../orchestration-gap/) page explains why that distinction
matters. If one application, a notebook, or a prototype is the goal, a library is
usually the right tool.

### CrewAI

[CrewAI](https://github.com/crewAIInc/crewAI) is a standalone Python framework for
orchestrating role-playing, autonomous agents. Its Crews are teams of agents that
delegate tasks to one another, and its Flows are event-driven workflows with
fine-grained control.

- **Overlap:** the "crew" vocabulary and the idea of role-based agents working
  together.
- **Differences:** CrewAI's crew is Python objects in a process that you deploy
  yourself. A Kubemoot `Crew` is a Kubernetes resource that the operator reconciles,
  with agents that deliberate to consensus rather than pass tasks along.

### LangGraph

[LangGraph](https://github.com/langchain-ai/langgraph) is a low-level orchestration
framework for stateful agents. Its stated strengths are durable execution, human
oversight of agent state, and short-term and long-term memory.

- **Overlap:** long-running, stateful multi-step agent work.
- **Differences:** LangGraph gives you a graph you define in code, with fine control of
  the control flow. Kubemoot fixes the control flow (the consensus discussion) and
  declares the participants. LangGraph is the better fit when you need to design a
  bespoke workflow.

### AG2 and AutoGen

[AG2](https://github.com/ag2ai/ag2) is an open-source programming framework for
building agents and getting multiple agents to cooperate, with multi-agent
conversation patterns. It grew out of AutoGen; the classic AutoGen-style code lives in a
separate `ag2-classic` repository. Microsoft's
[AutoGen](https://github.com/microsoft/autogen) is in maintenance mode, and its README
points new users to Microsoft Agent Framework.

- **Overlap:** multi-agent conversation as the way to reach an answer.
- **Differences:** same as the other libraries: code in a process versus resources on
  a cluster.

## Model serving and inference routing

These projects serve models. Kubemoot does not replace them; it sits above a model
server. Today Ollama is the only model server Kubemoot drives. Reading the state of
other servers, and the projects below in particular, is under assessment on the
[Roadmap](../roadmap/) and is not shipped.

### Ollama

[Ollama](https://github.com/ollama/ollama) runs open models locally through a REST API,
powered by the llama.cpp inference engine. It is the model server Kubemoot's reference
crews use. Ollama serves models; Kubemoot decides which model server and model serve
each inference call and coordinates the agents that use them.

### vLLM

[vLLM](https://github.com/vllm-project/vllm) is a library for LLM inference and serving.
Its README lists continuous batching, prefix caching, PagedAttention, distributed
inference, and an OpenAI-compatible API server. Kubemoot cannot drive vLLM today.
Trying it as a substitute for Ollama is part of the planned engine-state work on the
[Roadmap](../roadmap/#assessing-model-servers-beyond-ollama).

### KServe

[KServe](https://github.com/kserve/kserve) is a distributed generative and predictive
AI inference platform for Kubernetes. For LLMs it supports vLLM and llm-d backends,
an OpenAI-compatible protocol, model caching, and request-based autoscaling. If you
need to deploy and scale model servers as Kubernetes resources, use KServe; Kubemoot
does not deploy model servers of that kind. It is an agent layer that would sit above
one.

### llm-d

[llm-d](https://github.com/llm-d/llm-d) is a distributed inference serving stack for
Kubernetes and a CNCF sandbox project. It provides prefix-cache and load-aware
routing, tiered KV-cache management, and prefill/decode disaggregation. It routes and
serves requests for one deployment of a model. Kubemoot decides across models and
crews. Assessing llm-d is on the [Roadmap](../roadmap/).

### Gateway API Inference Extension

The [Gateway API Inference Extension](https://gateway-api-inference-extension.sigs.k8s.io/)
is an official Kubernetes project that makes gateways inference-aware. Its Endpoint
Picker routes requests using model server metrics such as cache status and adapter
availability. Kubemoot's planned engine-state contract aligns with its
[Model Server Protocol](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/docs/proposals/003-model-server-protocol/README.md),
as described on the [Roadmap](../roadmap/#assessing-model-servers-beyond-ollama). That
work is not shipped.

### KAITO

[KAITO](https://github.com/kaito-project/kaito) is an operator suite that automates LLM
inference, fine-tuning, and RAG engine deployment in a Kubernetes cluster, including
automatic GPU node provisioning. It solves how models get deployed onto GPUs. Kubemoot
schedules inference onto model servers that already run and does not provision GPU
nodes.

## MCP gateways and tool infrastructure

### agentgateway

[agentgateway](https://github.com/agentgateway/agentgateway) is an open-source proxy for
agentic AI, a Linux Foundation project. It provides an LLM gateway, an MCP gateway, and
an agent-to-agent (A2A) gateway, and runs on Kubernetes with Gateway API support.

- **Overlap:** both put MCP tools behind a gateway. Kubemoot has its own `MCPGateway`
  and `MCPServer` resources (see the [MCPGateway guide](../../reference/mcpgateway-guide/)
  and [MCP Tools](../../concepts/mcp-tools/)).
- **Differences:** agentgateway is general connectivity infrastructure that any agent
  framework can use. Kubemoot's gateway is built into the crew model and onboards tools
  for the crew's own agents. If you need a shared, security-focused proxy for many
  frameworks, agentgateway is the specialist.

## Evaluation frameworks

Kubemoot's [fitness functions](../../fitness/) score a crew's answers against
references, and the score is tracked as the crew changes. These frameworks evaluate LLM
applications more generally.

- [promptfoo](https://github.com/promptfoo/promptfoo) is a CLI and library for
  evaluating and red-teaming LLM apps, with CI/CD integration and side-by-side model
  comparison.
- [DeepEval](https://github.com/confident-ai/deepeval) is a Pytest-style framework for
  unit testing LLM applications, with metrics such as faithfulness and hallucination
  detection and evaluation of agent trajectories.
- [Inspect](https://github.com/UKGovernmentBEIS/inspect_ai), from the UK AI Security
  Institute, is a framework for model evaluations with more than 200 pre-built
  evaluations.

They are general-purpose and run wherever your code runs. Kubemoot's fitness functions
are specific to crews: they run in the cluster as resources against the deployed crew,
and they score consensus behavior as well as answers. If you already evaluate a single
model or prompt, these tools are the mature choice; they can also be used alongside a
crew.

Next: [Why Kubemoot](../why-kubemoot/) or the [Roadmap](../roadmap/).
