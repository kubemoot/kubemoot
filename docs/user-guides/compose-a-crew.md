---
title: "Compose a Crew"
weight: 15
description: "Design a crew's Tooler and Analyst lineup, write sharp resumes, ground the coordinator, and wire up knowledge so even small models deliver useful answers."
---

A crew is only as useful as its design. This guide covers the decisions that determine
whether a crew's coordinator routes questions intelligently and whether its Toolers and
Analysts can answer them: which agents to include, how to write resumes the coordinator
can reason over, how to ground the coordinator with broad knowledge and a well-crafted
advisory brief, and how to give each Tooler the narrowly-scoped tools it needs and
each Analyst the focused RAG knowledge it reasons over.

Read [Build a Crew](../build-a-crew/) first for the mechanics of declaring the
Kubernetes resources. This guide is about the design decisions inside those resources.

---

## 1. Crew composition: choosing your Toolers and Analysts

A crew's capability comes entirely from its agents. Kubemoot provides the
orchestration substrate; domain belongs to the crew. Start by mapping the domain to
layers: which distinct areas does this crew need to cover, and which require live data
vs. reasoned interpretation?

**Toolers gather ground truth; Analysts interpret it.** Toolers act in the EVALUATING
phase with thinking OFF and call domain MCP tools. Analysts act in the REVIEW phase
with thinking ON and reason over what Toolers found, using RAG knowledge. A crew can
have Toolers only, or Toolers plus Analysts where interpretive depth adds value.

**One Tooler per question shape.** An infrastructure crew, for example, might
separate workload management, node health, Helm releases, and observability metrics,
because each layer calls different tools and answers different-shaped questions. A
single "do-everything" agent carries too many tools, degrades tool-selection accuracy,
and makes the coordinator's subcommittee selection imprecise (its resume matches too
many questions weakly rather than a few questions strongly).

**Minimum viable crew.** A crew needs at minimum one coordinator and one Tooler.
Start small and add agents as you discover gaps rather than pre-populating for
hypothetical questions. The onboarding system can help: when no Tooler scores
above the confidence threshold on a question, the coordinator signals a capability gap
and an onboarding agent can propose an MCP server manifest for it. See [Onboard MCP Tools](../onboard-mcp-tools/) for that path.

**Avoid redundant Toolers.** Two Toolers that always agree on the same
questions add orchestration overhead without adding information. If two agents
consistently produce overlapping answers, merge them into one agent covering that
domain, or differentiate them: one handles real-time scalars, the other handles
time-series trends. See the cohesion and coupling discussion in the
[Agent CRD reference](../../reference/agent/) for detailed heuristics on when to
split vs. merge.

**Roles:** a crew has exactly one `coordinator` (declared via `spec.discussRole:
coordinator`), any number of Tooler agents (set `discussRole: tooler` explicitly; there is no default role),
optionally `analyst` agents for the REVIEW phase, and optionally one or more
`researcher` agents. Researchers augment synthesis but are excluded from settle
triggers and gap detection: suitable for internet search or other background
enrichment where you want context without requiring a definitive answer. See
[Crews & Agents](../../concepts/crews-and-agents/) for the role model.

---

## 2. Resumes: the capability catalog the coordinator reasons over

Every agent's **resume** is the coordinator's primary decision surface. At deploy time
the operator publishes each crew's full capability catalog to a NATS KV store the
coordinator reads. The catalog entry for each agent is compiled from four fields in
its spec:

- `spec.description` - what this agent does, in plain language
- `spec.triageSummary` - a terse, LLM-optimized description of the agent's scope
- `spec.discussKeywords` - additional terms included in the fallback similarity path
- `spec.enabledTools` (the tool names themselves) - which tools this agent can call

When a question arrives, the coordinator makes one LLM reasoning call over the entire
catalog at once. It reads what every Tooler in the crew can do, and reasons about
which ones the question needs. That same call also produces the framing brief. **One
call returns both the selected subcommittee and the brief.** The selected agents form
the subcommittee (`innerCircle`); agents not selected are never woken, incurring zero
GPU cost.

This is genuine reasoning over a compact representation of every Tooler's
capabilities, not a vector retrieval of the closest matches. The coordinator can
reason across the whole crew even for questions that use different vocabulary from any
individual resume.

The catalog the coordinator reasons over strips the bulky per-agent prompt and keeps
only what it needs for selection: agent name, description, and tools. **This is why
`spec.description` and `spec.enabledTools` are the most important fields to get right.**
The coordinator cannot reason well about an agent it cannot read clearly.

### Fallback path

If the reasoning call fails or returns nothing, the coordinator falls back to semantic
similarity over the per-crew resume index (`crew_<namespace>_<crew>_resumes`) and selects the
agents whose resumes score highest. If that also fails, it broadcasts to all
specialists. `spec.discussKeywords` feeds the similarity fallback, not the primary
reasoning call. Treat it as optional supplemental vocabulary that improves fallback
accuracy, not as the main selection signal.

### Write resumes for the coordinator, not for humans

A vague resume gets a Tooler overlooked or selected for the wrong questions. A crisp,
concrete resume produces reliable subcommittee selection.

**`spec.description`** (shown in the dashboard, included in the catalog the coordinator
reasons over): one sentence, declarative, naming the domain, the resource types, and
what the agent actually does - not what it "can do in general." The coordinator reads
this for every question; it must be precise enough for an LLM to classify fit at a
glance.

**`spec.triageSummary`** (seen only by the coordinator's reasoning logic): a compact
noun-phrase list of the resource types, operations, and data the agent covers. Write
it like the agent's signature line in a directory listing, not a sentence. Brevity and
specificity beat prose here.

**`spec.discussKeywords`**: additional terms included in the similarity fallback index.
Include synonyms, related concepts, and domain vocabulary the description may omit.
These improve fallback accuracy but are not read during the primary reasoning call.

**`spec.enabledTools`**: the names of the tools the agent can call. Tool names are
part of the catalog entry the coordinator reasons over, so naming tools clearly
(`kubectl_get`, `execute_range_query`, `list_topics`) contributes to reliable
selection. An agent with vague tool names (`tool1`, `do_thing`) is harder for the
coordinator to reason about correctly.

### Resume example: before and after

Vague - the coordinator cannot reliably distinguish this agent from a general Kubernetes
Tooler:

```yaml
spec:
  description: "Handles Kubernetes stuff"
  triageSummary: "kubernetes"
  discussKeywords:
    - kubernetes
  enabledTools:
    - do_kubernetes_things
```

Concrete - the coordinator can reliably select this agent for workload-lifecycle questions
and exclude it for observability or node questions:

```yaml
spec:
  description: "Kubernetes workload specialist: pods, deployments, services, replicasets, statefulsets, daemonsets, jobs, cronjobs"
  triageSummary: "Pod, deployment, service, replicaset, statefulset, daemonset, job, cronjob, namespace management via kubectl"
  discussKeywords:
    - pod
    - deployment
    - service
    - replicaset
    - statefulset
    - daemonset
    - job
    - cronjob
    - namespace
  enabledTools:
    - kubectl_get
    - kubectl_describe
    - kubectl_logs
    - list_api_resources
    - explain_resource
    - kubectl_scale
```

The coordinator reasons over the whole Tooler catalog in one pass and commits to its
subcommittee. The quality of that reasoning depends entirely on the quality of the
catalog entries it reads.

---

## 3. Prompting: ADL modules and what to put in them

All prompt text lives in `PromptModule` CRs referenced by `spec.promptRefs`, composed
in `order`. Nothing goes inline. See [Write Agents & ADL](../write-agents-and-adl/)
for the full ADL reference, module composition mechanics, and a side-by-side guide on
translating prose rules to ADL form.

The key design principle: **keep methodology in the PromptModule** (the versionable,
diffable artifact), not hardcoded in application logic. Prefer grounding the model
with good context and letting it reason over the situation rather than writing
exhaustive hand-crafted rule tables that enumerate every possible case. A coordinator
with broad domain knowledge and a well-structured ADL module will route more reliably
than one with a long list of keyword-to-Tooler mappings.

**What to put in a Tooler's module (order 30):** the agent's domain, what it
investigates and how, what signals it emits and when, and any field-specific
conventions (units, naming schemes, tool retry behavior). Keep it focused on this
agent's shape of question. Do not duplicate crew-wide protocol in the per-agent module;
that belongs in the shared `discussion-protocol` module (order 10).

**What to put in the coordinator's modules:** the coordinator uses three modules
stacked in order - `advisory-prompt` (order 5), `coordinator-decision-logic` (order
45), and `synthesis-prompt` (order 50). The advisory module instructs the coordinator
on how to frame its brief for Toolers and Analysts (see [section 4](#4-coordinator-grounding-the-advisory-brief)
below). The decision-logic module covers settling and gap classification. The synthesis
module covers how to compose the final answer from agent contributions.

---

## 4. Coordinator grounding: the advisory brief

**The coordinator is the crew's intelligence.** It is the only agent with visibility
across the entire crew's capability catalog, and it is the one that runs on the
crew's most capable model. Every design decision that makes the coordinator smarter
multiplies the value it delivers to every Tooler and Analyst it selects.

### What the advisory brief is

When the coordinator selects a subcommittee, it posts an **advisory brief** to the
discussion board before Toolers begin their tool calls. The brief is the
coordinator's "words of wisdom" for the Toolers and Analysts it has just woken. They
typically run on smaller, faster models on lesser hardware and receive this brief
as part of the thread context to orient their investigation.

A good brief identifies the relevant technologies, suggests approaches, names useful
data sources, and frames the question in a way that helps a Tooler who may not
have the coordinator's broader context. It reduces the chance that a small model picks
the wrong tool or misses the intent of the question.

### The additive constraint

**The brief must be additive only.** It must not contain prohibitions phrased as
exclusions of specific tools or approaches. A phrase like "no Prometheus required" or
"do not use kubectl_logs" tells a Tooler not to use a resource it may have access
to. If a Tooler finds that Prometheus data is in fact useful, or that logs reveal
the answer, a prohibitive brief would suppress a correct, richer answer. The brief
suggests options and approaches; it does not fence the Tooler's investigation.

Write briefs as expansive framing, not as narrowing constraints:

Correct framing: "Relevant technologies: Helm, StatefulSet, PersistentVolumeClaim.
The question concerns persistence configuration. Deployment manifests and Helm values
are likely to contain the relevant settings."

Incorrect framing: "This is only a Helm question. No kubectl_get calls needed. Do not
query pod state."

The first brief helps a small model find the answer faster. The second brief may
prevent it from finding the answer at all.

### What the coordinator needs: broad grounding

The coordinator's ability to write useful briefs and select the right subcommittee
depends on how much it knows about the domain. A coordinator with broad RAG coverage
can also write better context for Analysts in the REVIEW phase: naming which findings
deserve deeper scrutiny and which data sources Analysts should check. Give the coordinator:

1. **A capable model.** The coordinator's model should have strong reasoning capability.
   Declare `["tool-calling", "reasoning"]` in `spec.capabilities` so the scheduler
   assigns a high-quality-tier model. The coordinator does not call MCP tools itself
   (set `KUBEMOOT_GATEWAY_ENABLED=false`), so its model budget goes entirely to triage
   classification and synthesis.

2. **Broad RAGSources.** Give the coordinator RAGSources that span the crew's domain,
   not just one subdomain. A coordinator for an infrastructure crew should have access
   to Kubernetes docs, Helm docs, and any other technology the crew's Toolers cover.
   This is what lets it name data sources in its briefs and frame the question
   accurately for Toolers and Analysts running on smaller models.

3. **The crew capability catalog.** This is published automatically by the operator at
   deploy time to a NATS KV store - you do not configure it manually. It contains each
   agent's name, description, and tools in a compact form the coordinator can reason
   over. The operator also maintains a per-crew vector index (`crew_<namespace>_<crew>_resumes`)
   used as a fallback if the primary reasoning call returns nothing.

4. **A well-grounded advisory PromptModule.** The `advisory-prompt` module (order 5)
   instructs the coordinator how to write its brief. Write it in ADL, focused on the
   asymmetry the coordinator is bridging: the coordinator has broad context that the
   Toolers and Analysts do not. Its job is to surface the relevant portion of that context in a
   form a smaller model can act on.

Example `advisory-prompt` skeleton in ADL:

```text
DEFINE COMPONENT advisory-brief
DESCRIPTION How the coordinator frames questions for the Tooler and Analyst subcommittee.

ALWAYS identify the relevant technologies and infrastructure layers before waking Toolers and Analysts.
ALWAYS name specific data sources, API surfaces, or configuration locations the Tooler should check.
WHEN the question involves multiple technologies THEN name each and describe how they relate.
NEVER phrase the advisory as a prohibition or exclusion. Frame it as context and suggestions.
ASSERT the advisory is additive: Toolers may use richer tools than the brief suggests,
  and that is correct.
ASSERT Toolers run on smaller models with less context than the coordinator.
  Write briefs that help orient them, not ones that constrain their investigation.
```

---

## 5. Agent knowledge: RAGSources and tool scope

Toolers and Analysts each get their own focused resources: Toolers carry MCP tool
scope; Analysts carry RAG knowledge. Keep both narrow and specific.

### RAGSources for Analysts (and the coordinator)

An Analyst's RAGSources should cover the documentation and reference material for its
reasoning domain. A Kubernetes Analyst benefits from Kubernetes API docs and error
references. A Proxmox Analyst benefits from PVE API docs and hardware behavior docs.
Do not give every agent every RAGSource: the coordinator's broad knowledge belongs on
the coordinator, not replicated across all agents.

Toolers may also carry a small RAGSource to supplement their tool calls (e.g. a
Kubernetes Tooler with a small chunk of API reference), but keep `topK` tight so the
tool-calling prompt stays small.

Declare RAGSources on the `Agent` spec:

```yaml
spec:
  ragSources:
    - name: kubernetes-concepts
      topK: 3
      priority: 1
    - name: helm-reference
      topK: 2
```

Keep `topK` low. The retrieved context is injected into the agent's system prompt on
every call. An Analyst with three RAGSources at `topK: 5` each accumulates 15 chunks
of retrieved text. A `topK` of 2-3 per source is usually enough to give a focused
agent the right context. See [RAGSource Guide](../../reference/ragsource-guide/)
for source configuration and indexing details.

### Tool scope for Toolers

Keep each Tooler's tool set to the tools it actually needs for its question shape:
typically 1-5 tools, all serving the same operation type. A Tooler that answers
"what is the current state of X?" needs read tools. One that answers "how did X trend
over time?" needs range-query tools. Do not share tools between these two Toolers
by putting them in one agent: the tool-selection surface grows and the model has to
reason over irrelevant definitions on every call.

Declare tool scope on `spec.enabledTools`. The agent runtime loads this set at startup.
The coordinator has `KUBEMOOT_GATEWAY_ENABLED=false` and uses no MCP tools itself.
Analysts also carry no MCP tools: their prompt stays focused on reasoning.

The accuracy of a small model's tool selection is strongly correlated with the number
of tools it sees. A Tooler with 3 tools makes obvious choices. A Tooler with 20 tools
makes mistakes. Right-sizing tool scope is one of the most effective ways to improve
a crew without upgrading hardware.

### Matching model tier to agent complexity

Declare what an agent needs via `spec.capabilities`, not by naming a model. A
simple tool-calling Tooler that runs one read tool and formats the result declares:

```yaml
spec:
  capabilities:
    - tool-calling
    - kubernetes
```

A coordinator or an Analyst that must reason across multiple Tooler results and
weigh implications declares:

```yaml
spec:
  capabilities:
    - tool-calling
    - reasoning
```

The scheduler matches capabilities to a concrete model and provider at reconcile time
via `CrewSchedulingPolicy`. No agent names a model - loose coupling is preserved so
the same crew chart runs on a different cluster with different hardware. See
[Models & Scheduling](../../concepts/models-and-scheduling/) for the scheduler.

---

## A worked example: decomposed GPU crew

An undecomposed GPU agent carries five Prometheus tools covering four question shapes
(real-time, trend, discovery, target health). It takes 3+ minutes for a real-time
question because the model reasons over irrelevant trend tools on every iteration.

A decomposed crew splits it into three Toolers:

**`nvidia-gpu-now`** - answers "what is the current state of X?"

```yaml
spec:
  description: "Real-time GPU state: current utilization, temperature, VRAM usage"
  triageSummary: "Current GPU utilization, temperature, VRAM use via Prometheus instant query"
  discussKeywords:
    - utilization
    - temperature
    - vram
    - memory
    - current
    - now
  enabledTools:
    - execute_query
  ragSources:
    - name: prometheus-reference
      topK: 2
  capabilities:
    - tool-calling
    - observability
```

**`nvidia-gpu-history`** - answers "how did X trend?"

```yaml
spec:
  description: "GPU trend analysis: utilization, temperature, and VRAM over a time window"
  triageSummary: "Time-series GPU metrics, trend analysis, spike detection via Prometheus range queries"
  discussKeywords:
    - trend
    - history
    - over time
    - spike
    - range
    - last hour
  enabledTools:
    - execute_range_query
    - get_metric_metadata
  ragSources:
    - name: prometheus-reference
      topK: 2
  capabilities:
    - tool-calling
    - observability
```

**`nvidia-gpu-meta`** - answers "what is available and is the monitoring healthy?"

```yaml
spec:
  description: "GPU monitoring discovery and scrape-target health for Prometheus/DCGM"
  triageSummary: "Available GPU metrics, Prometheus scrape target health, DCGM exporter status"
  discussKeywords:
    - available metrics
    - scrape target
    - dcgm
    - exporter
    - discovery
  enabledTools:
    - list_metrics
    - get_targets
  capabilities:
    - tool-calling
    - observability
```

The coordinator reasons over the catalog and selects cleanly: a real-time question
wakes only `nvidia-gpu-now`; a trend question wakes only `nvidia-gpu-history`. Each
runs fast because its tool set is obvious. The crew gains the same capability as the
original single agent with sharper routing and lower latency. A GPU Analyst could
optionally join the REVIEW phase to interpret the findings against VRAM capacity
documentation.

---

## Checklist

Before deploying a crew:

- [ ] Each Tooler answers one shape of question (one-sentence test: no "and/or" listing multiple shapes).
- [ ] Each agent's `description` and `triageSummary` are concrete and domain-specific, not generic.
- [ ] Each Tooler's `enabledTools` is scoped to its question shape (target: 1-5 tools). Analysts carry no `enabledTools`.
- [ ] The coordinator declares `["tool-calling", "reasoning"]` capabilities and has broad RAGSources covering the crew's domain.
- [ ] The coordinator has an `advisory-prompt` module instructing it on additive briefs.
- [ ] Analyst and coordinator RAGSources have low `topK` (2-3 per source).
- [ ] No model names appear anywhere in Agent specs - capabilities only.
- [ ] No cluster-specific labels, node names, or topology are baked into prompt text.

---

## Next

- [Write Agents & ADL](../write-agents-and-adl/) - ADL syntax, PromptModule composition, and how to translate prose rules to ADL.
- [RAGSource Guide](../../reference/ragsource-guide/) - configure and index knowledge sources for Analysts.
- [Agent CRD reference](../../reference/agent/) - full spec including cohesion and coupling heuristics.
- [Define Fitness Functions](../define-fitness-functions/) - measure whether the crew's routing and answers are correct.
