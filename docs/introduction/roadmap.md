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

## Credentials for tools, granted per tool

This is the top priority on this page, and the gap it closes is a security gap you
should know about before you run Kubemoot with real credentials.

**Today:** an MCPServer takes credentials from Secrets in its own namespace, through
`env` entries with `secretKeyRef`, `secretRef`, and `secretVolumes`. The operator mounts
whatever Secret the MCPServer names, and nothing checks that the person who created the
MCPServer is allowed to read that Secret. A crew author who can create an MCPServer can
therefore have the operator mount any Secret in that namespace into a pod they control.
The same holds for `serviceAccountName`: the server runs as any ServiceAccount the
author names. Agent pods run as the namespace `default` ServiceAccount unless the Agent
sets another, with the ServiceAccount token mounted. The model itself never sees a
Secret value in the prompt path, but the boundary that keeps one tool's credential away
from another crew author is not enforced yet. Until it is, treat every namespace as one
trust domain and follow the guidance in [Secrets and tools](../../concepts/secrets-and-tools/).

**Direction:** the principle is that the model never holds a secret; only the tool
process does. The design uses Kubernetes mechanisms and invents nothing new:

- An admin owns each Secret in the crew namespace. Crew authors have no read access to
  Secrets.
- The admin grants a `use` verb, not `get`, on one named Secret to whoever applies the
  crew, such as a Flux identity per crew namespace.
- A ValidatingAdmissionPolicy on MCPServer create and update uses the CEL `authorizer`
  to check that the requester may `use` every Secret and ServiceAccount the MCPServer
  references, and rejects the request otherwise.
- The operator mounts a Secret only into its own MCPServer's pod, under a per-tool
  ServiceAccount. Agent pods get no Secret access.
- Only tools that call the Kubernetes API reference an admin-made ServiceAccount, behind
  the same `use` check. Every other tool runs as a ServiceAccount with no permissions
  and no mounted token.
- An egress policy per tool, so the mail tool reaches only the mail provider.

Later steps add a secret store such as OpenBao with External Secrets Operator for
rotation, short-lived credentials, and audit, and per-user OAuth for assistants that act
for one person. See [Secrets and tools](../../concepts/secrets-and-tools/) for the full
picture.

## Assessing model servers beyond Ollama

**Today:** Ollama is the only self-hosted model server Kubemoot drives.

**Direction:** we will soon assess other model servers and inference serving platforms,
such as vLLM, llama.cpp, and llm-d, in the interest of giving crews the best GPU and
model resources in the shortest time. Beyond the cluster, rented cloud GPUs reached as
model providers, so a crew can borrow capacity it does not own. Agents never name a
model server or where it runs, so a crew does not change when the server beneath it
does.

A spike is planned soon on an engine-state contract for the scheduler. Instead of
probing Ollama directly, the scheduler will read each model server's live state through
one contract: requests running and queued, KV cache use, loaded models with their
context and parallel capacity, and load and unload events. The contract aligns with the
Kubernetes Gateway API Inference Extension
[Model Server Protocol](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/docs/proposals/003-model-server-protocol/README.md),
a proposal in the Gateway API Inference Extension project that vLLM implements natively. Each model server gets an adapter
that fills the contract, Ollama's first.

The spike also tries vLLM as a substitute for Ollama on the tool-calling roles, where
many agents call the same model at once and vLLM's continuous batching should help. It
is measured against Ollama before any default changes. The payoff: scheduler decisions
improve as it sees more state, and changing the model server under a crew becomes a new
adapter, not a rework.

## MCP sources from public and private registries

**Today:** an `MCPCatalog` resource ([reference](../../reference/mcpgateway-guide/#mcpcatalog))
points one gateway at one external source, such as the official MCP Registry, Smithery,
Glama, Docker, or npm, and a quality policy filters what it finds. Each catalog type is
handled by its own code, catalogs are referenced per gateway, and the feature is off by
default until quality filtering is robust enough to keep broken community servers out.
Private sources are not covered.

**Direction:** Kubemoot inspects and retrieves MCP servers from many public and private
registries through one interface. Admins declare, once and cluster-wide rather than per
crew or per gateway, the list of MCP sources Kubemoot may draw from, private sources
included, and crews and onboarding agents see only servers from that list. A source
declaration names the registry endpoint, the credentials to reach it, and the quality
policy that applies. This is a direction, not built behavior.

A standard API exists to build it on. The official MCP Registry publishes
[`server.json`](https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/server-json/draft/server.schema.json)
metadata (name, packages or remote URLs, run arguments) and a REST API described by an
[OpenAPI spec](https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/api/openapi.yaml):
`GET /v0.1/servers` lists servers with cursor pagination and an `updated_since` filter,
and `GET /v0.1/servers/{serverName}/versions/{version}` returns one version, where
`latest` selects the newest. The registry documents that other registries, including
private ones, can implement the same spec so that hosts need one client for all of them
([registry overview](https://modelcontextprotocol.io/registry/about),
[aggregators guide](https://modelcontextprotocol.io/registry/registry-aggregators)),
with a `_meta` field for registry-specific additions such as ratings. The public
registry does not accept private servers and recommends a separate private registry for
them, and its own codebase is not designed for self-hosting. The registry API is still in
preview. Other registries already implement it: for example, Azure API Center exposes an
MCP registry endpoint of the form `.../v0.1/servers` for the servers in an organization's
inventory ([Microsoft documentation](https://learn.microsoft.com/en-us/azure/api-center/register-discover-mcp-server)).
Marketplaces such as Smithery and Glama do not necessarily follow the spec, so a source
declaration keeps a type, with the standard API as the default.

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

## Harness capabilities at the table

**Today:** a crew is a harness made of harnesses (see
[The Table and the Harnesses](../../concepts/the-table-and-the-harnesses/)). Its guides
are ADL rules, the consensus archetype (`consent-3`), and skills; its sensors are signals, gap
detection, and fitness functions. A compute agent runs code it writes in a sandbox for
counting, sorting, and filtering. In the other direction, an agent harness such as
Claude Code can already use a whole crew as one tool through the
[crew liaison](../../integrations/crew-liaison/).

**Direction:** the rest of what a moot needs across its agents: a planning phase for
multi-step questions, budgets and cost accounting across a discussion, approval gates
for actions, and an evidence and audit trail. And external harnesses convened as
voices at the table, not only as clients of it. What these should change, stated as
hypotheses the fitness harness will test before anything is claimed:

- **Quality.** A planning phase stops multi-step questions being answered from the
  first tool call; an external harness at the table raises the ceiling on hard
  questions while local agents keep it grounded. Expect fewer "could not be
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

## Agent backends behind the moot

**Today:** the Kubemoot agent runtime, a native thin agent, is the only backend that runs
an agent's turn.

**Direction:** the moot is Kubemoot's differentiator: the coordinator convenes a
subcommittee, agents deliberate by signal, the coordinator synthesizes. What runs one
agent's turn (call a model, call a tool, produce a signal) is a separate concern that
capable runtimes already handle well. The moot should talk to an agent through a thin,
backend-agnostic contract: in comes the question, the agent's role, and the available
tools; out comes a signal, a rationale, and evidence. Behind that contract could sit the
native agent (the default, open source, minimal VRAM and startup cost), an external agent
process supplied and licensed by the user, or a raw local model reached through a model
provider. The principle is the same one applied to tools: build natively against open
standards, adopt open standards such as MCP directly, and keep any specific product
swappable behind a contract rather than load-bearing. The core must run with no
dependency on a proprietary agent product, and an external backend stays optional and
never bundled. The contract itself, a Go interface and an adapter registry in the
operator, is not built.

## Cloud and frontier models at the table

**Today:** every agent in a crew runs on a model served inside the cluster.

**Direction:** an agent backed by a hosted or frontier model sits in the same moot as
the local agents, publishing the same signals. Local agents ground it in the cluster's
facts and tools; it brings reasoning depth. The operator decides per phase and per
crew what may leave the premises, which makes data handling for outbound context, cost
accounting, and a circuit breaker the prerequisites, not afterthoughts.

## Consensus archetypes declared, not coded

**Today:** the `MootArchetype` CRD ([reference](../../reference/mootarchetype/)) declares the
scheduling phases and signals that `CrewSchedulingPolicy` rules use to pick a model per phase, and the consensus archetype (`consent-3`) ships with the operator. How the coordinator
convenes, who may speak in which phase, when a discussion settles, and the rules of
engagement between agents are still fixed in code and in prompts.

**Direction:** the whole orchestration declared in the archetype manifest associated
with a crew: convening rules, turn order, objection handling, settlement criteria, and
the coordination pattern (consent, debate, expert panel, an orchestrator with workers
and a planning phase). Changing how a crew deliberates becomes a manifest edit, the
way changing what an agent believes is already a `PromptModule` edit.

## Crews that evolve

**Today:** the coordinator signals gaps in a discussion (a missing tool, a missing
specialist), and an agent in onboarding mode can propose an `MCPServer` manifest with a human's
consent. Wiring the new server to a Tooler agent is manual, and the chart does not
deploy the onboarding agents.

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

## Generated work that outlives the discussion

**Today:** artifacts a crew passes between its agents live in the
[discussion artifact store](../../concepts/discussion-artifact-store/) and are deleted
with their discussion. What a crew generates for you, a quiz, a report, a design,
arrives in its answer, and keeping it is up to the client that asked.

**Direction:** generated work retained as a first-class result: stored with its
provenance (the question, the agents that contributed, the sources they used),
versioned when a crew revises it, kept under a retention policy set per crew, and
retrievable later through the liaison, kmctl, or CrewForge.

## Crews talking to crews

**Today:** crews are isolated by namespace and do not exchange messages; the message
bus itself is open inside the cluster.

**Direction:** crews with explicit permissions to consult other crews, ask them
questions through the same liaison contract a human client uses, and decline
questions they are not allowed to answer. Per-crew message-bus accounts are the
security foundation this rests on.

From there, crews compose: a crew delegating a sub-question to another crew, crews
working the parts of a question in parallel, crews arranged in a hierarchy, and a
table of crews deliberating as a moot of their own, one more
[consensus archetype](#consensus-archetypes-declared-not-coded).

## Crews that act

**Today:** crews answer and generate, but they do not change systems. Their tools
query systems and documents, and their output is a finding, a synthesis, or new
material such as a quiz written from a course's reading or a game invented and played
in chat. Nothing a crew does changes the state of anything outside the discussion.
That is not a technical limit: an MCP server whose tools write plugs into the same
gateway as one whose tools read. It is a matter of testing and guardrails, which have
to come first.

**Direction:** crews that change state: applying a manifest, running a remediation,
opening a change. The consent mechanics the protocol already has (concern, block,
stand aside) extend to actions, with an approval gate a human or a policy holds, an
audit trail of what was done and why, permissions scoped to the change allowed, and
the fitness harness scoring outcomes rather than answers. Similarity-gated
auto-remediation, where a crew may act only on a situation it has seen resolved
before, is the first cautious slice. Generation grows alongside: deliberation that
ends in a reviewed artifact, a document, a design, or a change proposal, handed back
through the same doors.

## Crew skills

**Today:** skills exist in their first form: a crew-level `Skill` resource holding
instructions that the coordinator selects on demand and injects just in time, beside
the agent's PromptModules and MCP tools.

Code as action exists in its first form too: a compute agent writes short programs and
runs them in the crew's code sandbox for deterministic work over shared artifacts.

**Direction:** the rest of the convention, in the spirit of Claude Code skills: bundled
files a skill carries, and a bundled script a skill may run in the sandbox, so any
agent can reach for code as a tool rather than being limited to predefined read-only
functions.

## Evaluating Agent Sandbox for code execution

**Today:** the compute agent runs the code it writes in Kubemoot's own `code-sandbox`
MCP server, where the pod is the sandbox: no credentials, network limited by a network
policy where the crew ships one (the reference crew charts do).

**Direction:** evaluate [Agent Sandbox](../related-projects/#agent-sandbox), now stable at
v1.x, as the isolation layer for code execution in place of Kubemoot's own sandbox
setup. This is a direction under evaluation, not a commitment. Background: [Kubernetes Podcast episode 272, "Agent Substrate"](https://kubernetespodcast.com/episode/272-agent-substrate/).

## The liaison for every agent harness

**Today:** any MCP client can use a crew as one agent through the crew liaison; the
documented paths are Claude Code and Claude Desktop, and the answer arrives by ticket.

**Direction:** documented, tested connections for other agent harnesses (Codex,
OpenHands, Strands, Pi, and the ones that come next), native MCP Tasks once clients
implement them so a long deliberation needs no polling, and an agent card per crew for
protocols that speak agent to agent.

## Scale to zero

**Today:** agent pods run at their configured replica count whether or not a
discussion is active. The pieces on the agent side exist: an agent that starts from zero
publishes `waking` and `ready` signals so the coordinator does not settle a discussion
before it can take part. The operator chart carries a `scaleToZero` toggle, off by
default. The operator does not yet create any autoscaling resources, so nothing scales
agents to zero.

**Direction:** [KEDA](https://keda.sh) (Kubernetes Event-Driven Autoscaling) as the
mechanism. Idle agents scale to zero on message-bus consumer lag through KEDA's
[NATS JetStream scaler](https://keda.sh/docs/scalers/nats-jetstream/) and wake when a
discussion starts, and the operator creates and owns a `ScaledObject` beside each agent
Deployment. The reasoning:

- CPU and memory metrics do not fit these workloads. Agents are message-bus subscribers,
  so consumer lag is the right signal, and tool servers can sit idle for hours and still
  need to scale up when a discussion begins.
- Hand-written replica logic in the operator would reimplement what an autoscaling
  controller already does. KEDA layers on the standard HPA, handles scale to zero, and
  covers both the message-bus trigger for agents and request-rate triggers for MCP
  servers and gateways with one installation.
- Waking an existing Deployment is faster than creating a Job per discussion, which is
  too slow for the settle window of a discussion. Only the replica count changes.
- The cost is a cold start on the first discussion after an idle period (pod start and
  message-bus connect, not a model load when the model server is warm), and KEDA as a
  cluster dependency.

The coordinator's subcommittee selection already limits inference to the agents it
convenes; this extends the saving to the pods themselves.

## Tracing and logs

**Today:** the dashboard infers discussion spans from signal timestamps; logs are per
pod.

**Direction:** an OpenTelemetry trace per discussion, with each agent step and tool
call a real span carrying the GenAI semantic conventions, exported to any OTel
backend, and centrally aggregated logs correlated to those traces.

## Images built with Cloud Native Buildpacks

**Today:** the Java, GraalVM native, and Go images (the operator, agent runtime, indexer,
MCP gateway, MCP bridge, and the other Go services) are built with
[Cloud Native Buildpacks](https://buildpacks.io/) and the [Paketo](https://paketo.io/)
buildpacks. The lifecycle runs as a non-root user with no daemon in a short-lived pod
inside the project's own cluster, writes a software bill of materials into every image,
and uses builder and run images pinned by digest.

The images that keep a Dockerfile (the dashboard, the RAG query service, the code
sandbox, the test runner, and the CI runner image) are built by
[Buildah](https://buildah.io/) in the same kind of pod. The pod runs in its own user
namespace, so the build is root only inside it, with no privileged container and the
Buildah image pinned by digest.

**Direction:** move the dashboard to the Node.js buildpack and the RAG query service to
the Python buildpack, so only the images whose operating-system contents are the product
(the code sandbox, the test runner, and the CI runner image) keep a Dockerfile. Signing
by digest and the provenance on every release stay as they are.

## Images for arm64

**Today:** release images are amd64 only.

**Direction:** multi-architecture images, so the quickstart and the operator run on
Apple silicon and arm64 nodes without emulation.

## Suggest a direction

Something missing, or a direction you would take differently? Argue for it in
[Discussions](https://github.com/orgs/kubemoot/discussions), open an
[issue](https://github.com/kubemoot/kubemoot/issues), or write to
**moot@kubemoot.org**. Items on this page move when someone makes the case.
