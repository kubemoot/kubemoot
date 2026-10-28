---
title: "Agent Context"
weight: 21
description: "Everything that goes into an agent's model context in Kubemoot: where each part comes from, how it is written and read, how long it lives, and how it is isolated."
---

An agent's answer is only as good as what the model sees when it answers. That
material is the agent's **context**, and in Kubemoot it comes from several places with
different owners and different lifetimes: some of it you write in advance, some of it
the crew produces while it deliberates, and some of it the runtime adds on its own.

This page lists the parts of an agent's context in Kubemoot. Context engineering in
general also names parts such as examples and output format; here those live in the
instructions, written as ADL in PromptModules. Each part below says where it comes
from, how it is written and read, how long it lives, and how it is kept apart from other
crews.

| Part | Comes from | Written and read | Lives | Isolation |
|------|------------|------------------|-------|-----------|
| [Instructions](#instructions) | `PromptModule` and `Agent` resources you declare | Written in advance as Kubernetes resources; rendered into the system prompt when the agent starts | Until you change the resources; a change rolls the agent | Kubernetes namespace and RBAC on the resources |
| [Environment](#environment) | The runtime | Added by the runtime to the prompt and to tool calls | One discussion or one call | Built per agent and per thread |
| [Conversation](#conversation) | You and the crew's answers | Each settled turn is appended to the conversation and read back as context for follow-ups | A bounded window of recent turns, expiring after a day of inactivity by default | Keyed by `conversationId` |
| [Discussion thread](#discussion-thread-the-table) | The coordinator and every agent at the table | Published on the message bus; every agent at the table reads the thread | One turn; the thread closes when the answer settles | Subject carries namespace and crew |
| [Tools](#tools) | MCP servers through the crew's MCP gateway | Definitions are offered to the model; results come back from each call | The call or the discussion | Per-crew gateway; per-agent `enabledTools` |
| [Workspace](#workspace) | Large tool outputs and contributions | Written to an object store; passed by reference; read as small slices | Deleted with the discussion | Keys carry namespace, crew and thread |
| [Memory](#memory) | What agents learn during discussions | Written by `REMEMBER:` lines; injected automatically before each query | Used facts persist; unused facts age out | Keys carry namespace and crew |
| [Knowledge](#knowledge) | `RAGSource` documents | Indexed in advance into a vector store; queried per question | Until re-indexed or deleted | Per `RAGSource`; an agent names the sources it may query |
| [Artifacts](#artifacts-planned) (planned) | Generated work | Not shipped | Not shipped | Not shipped |

## Instructions

Instructions are everything you tell an agent before any question arrives. They are
written as `PromptModule` resources in **ADL**, the Architecture Definition Language of
Mark Richards and Neal Ford applied to agents, and attached to an `Agent` through
`spec.promptRefs`. The `Agent` spec adds the rest of the declaration: its capabilities,
its role, the tools and knowledge sources it may use. Prompt text never lives inline in
the spec.

Because instructions are Kubernetes resources, they follow the same path as the rest of
your configuration: stored in Git, reviewed in a pull request, applied by GitOps, and
guarded by RBAC. The operator assembles the PromptModules into one system prompt and
mounts it into the agent. The agent reads it when it starts, so editing a PromptModule
changes the prompt hash on the pod and rolls the agent. The coordinator can also inject
skills into a discussion on demand (see
[The Table and the Harnesses](../the-table-and-the-harnesses/)).

See [Agent Definition Language](../agent-definition-language/) for the language and
[Crews & Agents](../crews-and-agents/) for how an agent composes its parts.

## Environment

The environment is what the runtime adds to an agent's context without your writing it.
It is a short list, and only these items are injected:

- **The discussion frame.** In a discussion, the agent's prompt opens with a line that
  it is taking part in a team discussion on a named thread, and the system prompt ends
  with the thread's identifier so prompt modules and tools can refer to it.
- **The agent's own card.** For the quick "should I contribute?" check, the agent is
  shown its own name, its description, the tools it has, and the technologies the
  coordinator named for the question.
- **The crew roster, for the coordinator.** The coordinator reasons over the crew's
  resumes, a catalog of what each agent can do, to choose which agents to convene. The
  operator compiles the resumes from the Agent specs.
- **Where and when the requester is.** A client may send the requester's time zone and
  local date and time with a question. The runtime passes them on the thread and hands
  them to tools as MCP metadata, so a tool that needs "today" uses the requester's
  date rather than the server's clock. They are not written into the prompt text.

The runtime injects no description of the agent's permissions. Permissions are enforced
by Kubernetes RBAC and network policy around the agent, not described to the model.

## Conversation

A conversation is your ongoing exchange with a crew, identified by a `conversationId`.
It holds a history of **turns**, each a question and the answer the crew settled on.
When you ask a follow-up, the coordinator places the recent turns in front of the
discussion so the question is read in context.

The history is bounded and durable. It keeps the most recent turns (ten by default),
expires after a period of inactivity (a day by default), and is stored in the NATS KV
bucket `kubemoot_conversations` under the key `conv.<conversationId>`, so a session
survives a coordinator restart. Supplying the same `conversationId` continues a
conversation; omitting it starts a new one.

The conversation is the thread of your dialogue with the crew. What happens inside a
single turn is a different part, covered next. See
[Conversations, Turns & Threads](../conversations-turns-and-threads/) for the
vocabulary.

## Discussion thread (the table)

This part is what is distinctive about a crew. Each turn runs on its own thread, and
the thread is the shared context for everyone at the table. Agents do not each receive
a private copy of the question; they read the same running record and reason over what
their peers have contributed.

The thread lives on the message bus on the subject
`kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>`. It carries, in order:

- **The question**, as the user asked it, and the previous turns when it is a follow-up.
- **The coordinator's brief.** The coordinator selects the subcommittee for the question
  and posts a framing brief that tells the selected agents what the question is about
  and where to look.
- **Each agent's signals and evidence.** Every contribution carries a consensus signal
  (`agree`, `concern`, `stand_aside`, `block`, `failure`) and the finding behind it,
  including the data a Tooler retrieved. Analysts read the Toolers' findings in the
  review phase before they weigh in.
- **Workspace references.** A contribution too large to carry inline appears as a short
  preview plus a reference to the object in the [workspace](#workspace).

The runtime formats the thread for each agent as a labelled transcript (user question,
advisory, responses, concerns, stand-asides, synthesis), so an agent sees which peer said
what. The thread is one turn long: it closes when the answer settles, and a reply
reopens it for another round. Subjects carry the namespace and crew name, so two crews
never see each other's threads. That is naming separation, not an access boundary; see
[Isolation](../isolation/) and
[SECURITY.md](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md).

Most writing on context engineering describes a single agent. The thread is the part
that exists only because several agents deliberate together. See
[The Moot](../consensus-model/), [Signals & Protocol](../signals-and-protocol/) and
[The Table and the Harnesses](../the-table-and-the-harnesses/).

## Tools

An agent is offered **tool definitions**: the names, descriptions and parameters of the
MCP tools it may call. It reaches them through its crew's MCP gateway, which aggregates
every tool server of the crew, and `spec.enabledTools` and `spec.disabledTools` narrow
the set to the few that fit the agent's job. Keep the set small, because tool-calling
reliability falls as the list grows. If an agent declares tools the gateway does not
offer, it signals `failure` rather than answering without them.

**Tool results** return to the model inside the agent's tool loop and become part of
that call's context. A Tooler's findings are then published to the thread for the other
agents. Results that are too large go to the workspace instead of the bus.

Tool definitions are read from the gateway each time the agent looks, so a tool server
added later is picked up without restarting the agent. See [MCP Tools](../mcp-tools/).

## Workspace

The workspace holds bulk data for one discussion. A tool output or contribution above a
size threshold is stored whole in a NATS object store and passed to the table **by
reference**: the message carries a preview and a reference envelope, and an agent that
needs more reads a bounded slice with `head`, `tail`, `grep`, `select`, `count`, `rows`
or `jq`. The optional `workspace_query` tool runs one read-only SQL `SELECT` over named
artifacts, using a SQLite or DuckDB engine, so counting and totalling are done by a
database instead of by the model. Agents without network access read the same data as
files materialized under `/artifacts`.

Workspace objects are keyed `{namespace}/{crew}/{threadId}/{agent}/{tool}-{seq}` and are
deleted with the discussion: when the thread closes, then by a bucket expiry, then by a
background reaper for anything left.

Today this store is called the **discussion artifact store**, and the tool and docs use
"artifact" for what this page calls the workspace. Renaming it to "workspace" is
planned, so that "artifact" can name generated work that outlives a discussion (see
[Artifacts](#artifacts-planned)). See
[Discussion Artifact Store](../discussion-artifact-store/) for the full mechanism.

## Memory

Crew memory holds facts the crew learned about its environment, so it stops
rediscovering them. It belongs to the **crew**, not to a single agent, and is configured
by `spec.memory` on the `Crew` resource (the Agent spec has no memory field).

- **Source and write.** When an agent resolves a durable fact, it ends a line of its
  answer with `REMEMBER: <topic> | <key> | <value>`. The runtime parses the line,
  stores the fact, and strips the line from the answer the user sees.
- **Storage.** Facts live in the NATS KV bucket `kubemoot_crew_memory` under the key
  `<namespace>.<crew>.<topic>.<key>`. Each value is a JSON record with `value`,
  `learnedBy`, `learnedAt` and `usedAt`.
- **Read.** Before each discussion query, the runtime injects the crew's relevant facts
  into the agent's system prompt under a "Crew Working Memory" heading. Facts that share
  words with the question rank first, then the most recently used. The agent does not
  have to decide to look anything up.
- **Caps and lifetime.** Injection is capped (`injectLimit`, eight by default) and
  storage is capped (`maxFacts`, 5000 by default, least recently used evicted). A fact
  that is read is refreshed, and a fact unused for `ttlDays` is pruned. Deleting the
  Crew removes its facts; updating it keeps them.

See the [Crew reference](../../reference/crew/#working-memory) for every field and the
duplicate and conflict rules.

## Knowledge

Knowledge is reference material the crew retrieves rather than remembers. A `RAGSource`
points at documents; the operator indexes them into a vector store and runs a query
service for each source. An `Agent` names the sources it may query in `spec.ragSources`,
each with a `topK` (default 5) and a `priority`. The operator resolves each named source
to its query service and passes the endpoints to the agent as environment variables, and
the agent's network policy allows only the services it is configured to call.

When the agent answers, the runtime searches those sources with the user's question and
places the top results in the system prompt ahead of the model call. In a discussion
the search uses the user's question, not the whole thread. Keep `topK` small, because
large retrieved payloads degrade tool calling.

The coordinator also gets a query service of its own: the operator indexes the crew's
resumes into a per-crew `RAGSource` and points the coordinator at it, so the coordinator
can narrow the crew to the agents whose expertise matches the question.

See the [RAGSource guide](../../reference/ragsource-guide/) and the
[Agent reference](../../reference/agent/#specragsources).

## Artifacts (planned)

Artifacts are generated work that outlives the discussion that produced it: a report, a
manifest, a dataset a crew builds and hands to the user. This is the sense the A2A
protocol uses for the word. Artifacts are planned and not shipped; nothing in Kubemoot
today keeps generated work beyond the discussion's lifetime. The discussion artifact
store described under [Workspace](#workspace) is temporary by design and is the reason
for the planned rename.

## Open standards

Crew memory is planned to follow the Open Memory Protocol (OMP), published by the Open
Memory Protocol Initiative. Today memory uses Kubemoot's own JSON record and no OMP
format.

## Related

- [Crews & Agents](../crews-and-agents/) - how an agent composes instructions, tools,
  knowledge and memory.
- [Agent Definition Language](../agent-definition-language/) - the language the
  instructions are written in.
- [Conversations, Turns & Threads](../conversations-turns-and-threads/) - the
  conversation and thread vocabulary.
- [The Table and the Harnesses](../the-table-and-the-harnesses/) - the layer above the
  single-agent loop.
- [Discussion Artifact Store](../discussion-artifact-store/) - the workspace mechanism.
- [Crew reference](../../reference/crew/) - the `spec.memory` fields.
