---
title: "Roadmap"
weight: 80
description: "The major capabilities being considered, so you can see where Kubemoot is going before you judge what it lacks today."
---

Kubemoot is young. If a capability you expect is missing, it is more likely on this
page than rejected. This page lists the significant directions under consideration or
in progress: the ones that change what a crew can be. Small features, conveniences,
and fixes live on the project board, not here. Each item says what exists today, so
nothing below is mistaken for something already shipped.

Ideas become work in the open: propose or argue for one in
[Discussions](https://github.com/orgs/kubemoot/discussions).

## Next: assessing model servers beyond Ollama

**Today:** Ollama is the only self-hosted model server Kubemoot drives.

**Direction:** we will soon assess other model servers and inference serving platforms,
such as vLLM, llama.cpp, and llm-d, in the interest of giving crews the best GPU and
model resources in the shortest time. Agents never name a model server, so a crew does
not change when the server beneath it does.

## Model choices for the reference crews

**Today:** the reference crews run open-weight Qwen3 models served by Ollama: an
8B-class model for specialists and triage on a 24 GB GPU, and a 32B model for
coordination and synthesis on a 32 GB GPU. Kubemoot itself is model-agnostic: an agent
declares a capability, and the scheduler matches it to a Model resource, so no agent
spec names a model, and changing models is a change to Model resources, not to crews.
Any model Ollama serves with tool calling can be used. Qwen was chosen for the
reference crews for strong tool calling at sizes that fit 24 GB and 32 GB consumer
GPUs, and its Apache 2.0 license.

**Direction:** the reference crews are moving to newer Qwen generations: Qwen3.5 9B for
specialists, Qwen3.8 27B for coordination. Each candidate is compared on the crews' own
fitness suite against the current reference, not chosen from leaderboards. The same
comparison runs for permissively licensed alternatives:

- Gemma 4 (Google, Apache 2.0)
- Muse Glimmer 30B (Meta, Apache 2.0)
- GLM-4.7-Flash (Z.ai, MIT)

If an alternative does better on the suite than the current reference, the reference
crews switch to it, and the results will be published.

## The table and the harnesses

A harness is everything around the model: the guides that steer an agent before it
acts and the sensors that let it correct itself afterwards, in the sense of Martin
Fowler's [Harness Engineering](https://martinfowler.com/articles/harness-engineering.html).
A crew is a harness made of harnesses: each agent has its own loop (prompt, tools,
retries, memory), and the moot is the harness one level up, whose guides (ADL rules,
the archetype, skills) and sensors (signals, gap detection, fitness functions) act
between agents. That shapes what Kubemoot builds and what it does not.

- **Native to the table:** what only a crew can do, or what a moot needs across all its
  agents. Declared archetypes with a planning phase, budgets and cost accounting across
  a moot, approval gates for actions, durable crew memory, an evidence and audit trail,
  and evaluation. These are ours to build.
- **Native to the runtime, adopted:** the open conventions small local models need to
  be useful, in the spirit of the tools people already use: skills, memory, and code as
  action in a sandbox. Adopted as conventions, never as a dependency on a product.
- **Convened, not built:** everything else in the per-agent harness race. The moot
  talks to an agent through a thin contract (the question and the role in; a signal,
  a rationale, and evidence out), so an external harness such as Claude Code, Codex,
  or OpenHands can sit at the table as one voice, with the crew's own tools, under the
  user's own license. Kubemoot does not compete with those harnesses; it gives them a
  table.

What these capabilities should change, stated as hypotheses the fitness harness will
test before anything is claimed:

- **Quality.** A planning phase stops multi-step questions being answered from the
  first tool call; an external harness at the table raises the ceiling on hard
  questions while local agents keep it grounded; code as action moves counting,
  sorting, and filtering out of a small model's head. Expect fewer "could not be
  determined" answers and fewer confident wrong ones.
- **Speed on subsequent questions.** Durable crew memory skips rediscovery the second
  time a topic comes up; result reuse with a freshness policy turns a repeated question
  from minutes into seconds; skills and a context budget shrink prompts, which on small
  models is speed directly.
- **What does not get faster.** The first answer to a new question has a floor: model
  inference times the number of phases, plus the tool calls. Consensus costs a round of
  contributions and a synthesis. The levers on that floor are the models and the
  serving engine, and convening fewer agents. An external harness at the table makes a
  first answer slower and better, not faster.

## Cloud and frontier models at the table

**Today:** every agent in a crew runs on a model served inside the cluster.

**Direction:** an agent backed by a hosted or frontier model sits in the same moot as
the local agents, publishing the same signals. Local agents ground it in the cluster's
facts and tools; it brings reasoning depth. The operator decides per phase and per
crew what may leave the premises, which makes data handling for outbound context, cost
accounting, and a circuit breaker the prerequisites, not afterthoughts.

## Consensus archetypes declared, not coded

**Today:** the `MootArchetype` CRD names a discussion's phases and its signal
vocabulary, and the consent archetype ships with the operator. How the coordinator
convenes, who may speak in which phase, when a discussion settles, and the rules of
engagement between agents are still fixed in code and in prompts.

**Direction:** the whole orchestration declared in the archetype manifest associated
with a crew: convening rules, turn order, objection handling, settlement criteria, and
the coordination pattern (consent, debate, expert panel, an orchestrator with workers
and a planning phase). Changing how a crew deliberates becomes a manifest edit, the
way changing what an agent believes is already a `PromptModule` edit.

## Crews that evolve

**Today:** the operator detects gaps in a discussion (a missing tool, a missing
specialist) and can onboard an MCP server from a catalog with a human's consent.

**Direction:** crews that find, acquire, rate, and adopt MCP tools on their own, and
add agents equipped with them, under a quality policy that decides what a crew may
trust. A crew's membership then grows with its domain instead of being fixed at
authoring time.

## Durable crew memory

**Today:** a crew keeps working memory, facts it learns at runtime such as label and
topology mappings, in NATS KV, with caps on storage and on how much is injected into
an agent's context.

**Direction:** long-lived, curated memory across discussions with progressive recall,
an index loaded cheaply and detail fetched on relevance, so a crew improves with use
without its prompts growing.

## Crews talking to crews

**Today:** crews are isolated by namespace and do not exchange messages; the message
bus itself is open inside the cluster.

**Direction:** crews with explicit permissions to consult other crews, ask them
questions through the same liaison contract a human client uses, and decline
questions they are not allowed to answer. Per-crew message-bus accounts are the
security foundation this rests on.

## Crews that act

**Today:** every example crew is read-only. Its tools query systems and documents, and
its output is a finding or a synthesis; nothing a crew does changes the state of
anything outside the discussion.

**Direction:** two steps beyond answering. Generative crews, whose deliberation ends
in an artifact rather than a verdict: a document, a set of exam questions, a design, a
change proposal, produced and reviewed by the crew and handed back through the same
doors. And crews that change state: applying a manifest, running a remediation,
opening a change, with the consent mechanics the protocol already has (concern, block,
stand aside) extended to actions, an approval gate a human or a policy holds, an audit
trail of what was done and why, and the fitness harness scoring outcomes rather than
answers. Similarity-gated auto-remediation, where a crew may act only on a situation
it has seen resolved before, is the first cautious slice.

## Crew skills

**Today:** skills exist in their first form: a crew-level `Skill` resource holding
instructions that the coordinator selects on demand and injects just in time, beside
the agent's PromptModules and MCP tools.

**Direction:** the rest of the convention, in the spirit of Claude Code skills: bundled
files a skill carries, and a bundled script a skill may run in a sandbox. That last part is the door to code as action: agents that write and
execute code in a sandbox as a tool, rather than being limited to predefined read-only
functions. A sandboxed compute member for deterministic work over shared artifacts is
the first slice.

## The liaison for every agent harness

**Today:** any MCP client can use a crew as one agent through the crew liaison; the
documented paths are Claude Code and Claude Desktop, and the answer arrives by ticket.

**Direction:** documented, tested connections for other agent harnesses (Codex,
OpenHands, Strands, Pi, and the ones that come next), native MCP Tasks once clients
implement them so a long deliberation needs no polling, and an agent card per crew for
protocols that speak agent to agent.

## Scale to zero

**Today:** agent pods run at their configured replica count whether or not a
discussion is active; the chart carries a `scaleToZero` toggle, off by default, and
the operator does not yet wire it.

**Direction:** [ADR 0010](../../adr/0010-keda-for-autoscaling/): idle agents scale to
zero on message-bus consumer lag and wake for a discussion. The coordinator's
subcommittee selection already limits inference to the agents it convenes; this
extends the saving to the pods themselves.

## Tracing and logs

**Today:** the dashboard infers discussion spans from signal timestamps; logs are per
pod.

**Direction:** an OpenTelemetry trace per discussion, with each agent turn and tool
call a real span carrying the GenAI semantic conventions, exported to any OTel
backend, and centrally aggregated logs correlated to those traces.

## Images for arm64

**Today:** release images are amd64 only.

**Direction:** multi-architecture images, so the quickstart and the operator run on
Apple silicon and arm64 nodes without emulation.
