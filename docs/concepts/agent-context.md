---
title: "Agent Context"
weight: 21
description: "Where an agent's model context comes from in Kubemoot: what each source provides today, how it is written and read, how long it lives, how it is isolated, and which sources are trusted instructions and which are untrusted data."
---

An agent's answer is only as good as what the model sees when it answers. That
material is the agent's **context**, and it comes from several places with different
owners and lifetimes: some you write in advance, some the crew produces while it
deliberates, and some the runtime adds on its own.

This page is organized by where context comes from, following the sources that LLM
agent context is usually drawn from. It lists the parts of an agent's context in
Kubemoot and says plainly where Kubemoot does not cover a source yet, with the proposed approach labeled as a proposal. Context engineering
in general also names parts such as examples and output format; here those live in the
instructions, written as ADL in PromptModules.

## Sources at a glance

The last column separates **trusted instructions**, which you or your user wrote and
the model should follow, from **untrusted data**, which the model reads but must not
obey. See [Why the split matters in a crew](#why-the-split-matters-in-a-crew).

| Source | Kubemoot covers | Trust |
|--------|-----------------|-------|
| **Static or configured** | | |
| System prompt | `PromptModule` resources written in ADL and the `Agent` spec, rendered into the system prompt at agent start | Trusted instructions |
| Tool definitions | MCP tools through the crew's gateway, narrowed per agent by `enabledTools` | Trusted instructions |
| Few-shot examples | Written inside a PromptModule next to the rules they illustrate; no separate mechanism | Trusted instructions |
| Skills or instruction files loaded on demand | `Skill` resources the coordinator selects and injects for the discussion that needs them | Trusted instructions |
| **Conversational** | | |
| User messages | The question sent to the crew's discussion gateway | Trusted instructions |
| Conversation history | The `kubemoot_conversations` bucket; the last 10 turns, expiring after 24 hours of inactivity (defaults) | Your earlier turns are trusted instructions; the crew's earlier answers are untrusted data |
| Conversation compaction | Not yet. Proposed: an adopted agent harness that compacts long conversations, rather than growing Kubemoot's own runtime | Not applicable |
| **Retrieved** | | |
| RAG | `RAGSource` documents named in `spec.ragSources`, indexed into a vector store and searched per question | Untrusted data |
| Keyword or structured search | `workspace_query` (SQL over the workspace), the coordinator's resume search, and the gateway's optional tool search | Untrusted data |
| Web search and fetch | A web search tool server, when the crew has one | Untrusted data |
| **Tool and environment output** | | |
| Tool results | Returned through the gateway; large ones are stored in the workspace and passed by reference | Untrusted data |
| Environment observations | Through tools, such as `kubectl` output | Untrusted data |
| Errors and feedback | Tool errors, `failure` signals to the table, and the compute contract's re-prompt | Tool errors are untrusted data; the re-prompt is runtime text |
| **Memory** | | |
| Working memory | Within one agent call; across agents, the discussion thread is the crew's shared working state | Untrusted data where it holds peers' words |
| Long-term memory | Crew memory: facts about the domain, written by `REMEMBER:` lines and injected before each query. Not yet for facts about users | Untrusted data |
| Episodic memory | Not yet. Proposed: past discussions as retrievable episodes in the crew memory design, following the Open Memory Protocol | Not applicable |
| **Multi-agent** | | |
| Messages from other agents | The discussion thread, the table: the coordinator's brief and each agent's signals and evidence | Untrusted data |
| **Metadata or runtime** | | |
| Injected runtime context | The discussion frame, the agent's own description and tools, the crew resumes, and the requester's time zone and date passed to tools | Runtime text |
| Token budgets | Not yet. Proposed: a context budget per agent call, set by the operator | Not applicable |
| User preferences or profile | Not by Kubemoot. An application in front of a crew supplies them, for example as runtime metadata | Not applicable |

## Static or configured

This context is written before any question arrives. It is the part you control most
directly, and it is trusted.

**System prompt.** Instructions are `PromptModule` resources written in **ADL**, the
Architecture Definition Language of Mark Richards and Neal Ford applied to agents, and
attached to an `Agent` through `spec.promptRefs`. The `Agent` spec adds the rest of the
declaration: its capabilities, its role, and the tools and knowledge sources it may
use. Prompt text never lives inline in the spec.

Because these are Kubernetes resources, they follow the path of the rest of your
configuration: stored in Git, reviewed in a pull request, applied by GitOps, guarded by
RBAC. The operator assembles the PromptModules into one system prompt and mounts it
into the agent. The agent reads it when it starts, so editing a PromptModule changes the
prompt hash on the pod and rolls the agent. See
[Agent Definition Language](../agent-definition-language/).

**Tool definitions.** An agent is offered the names, descriptions and parameters of the
MCP tools it may call. It reaches them through its crew's MCP gateway, which aggregates
every tool server of the crew, and `spec.enabledTools` and `spec.disabledTools` narrow
the set to the few that fit the agent's job. Keep the set small, because tool-calling
reliability falls as the list grows. If an agent declares tools the gateway does not
offer, it signals `failure` rather than answering without them. Definitions are read
from the gateway each time the agent looks, so a tool server added later is picked up
without a restart. See [MCP Tools](../mcp-tools/).

**Few-shot examples.** Kubemoot has no separate example mechanism. An example is text
you write inside a PromptModule, next to the rules it illustrates.

**Skills.** A `Skill` is a crew-level procedure written in ADL. Its body stays out of
every prompt until a discussion needs it: the coordinator reads each skill's
description when it chooses who joins, selects the skills that fit the question, and
only the selected bodies are loaded and added for that discussion. See the
[Skill reference](../../reference/skill/).

## Conversational

**User messages.** The question you send arrives at the crew's discussion gateway and
opens a turn. It is the one input the crew treats as the request to act on.

**Conversation history.** A conversation is your ongoing exchange with a crew,
identified by a `conversationId`. It holds a history of **turns**, each a question and
the answer the crew settled on. On a follow-up, the coordinator places the recent turns
in front of the discussion so the question is read in context.

The history is bounded and durable. It keeps the most recent turns (ten by default),
expires after a period of inactivity (a day by default), and is stored in the NATS KV
bucket `kubemoot_conversations` under the key `conv.<conversationId>`, so a session
survives a coordinator restart. Supplying the same `conversationId` continues a
conversation; omitting it starts a new one. Old turns drop off the end. Kubemoot does
not compact or summarize them. See
[Conversations, Turns & Threads](../conversations-turns-and-threads/).

## Retrieved

**RAG.** Knowledge is reference material the crew retrieves rather than remembers. A
`RAGSource` points at documents; the operator indexes them into a vector store and runs
a query service for each source. An `Agent` names the sources it may query in
`spec.ragSources`, each with a `topK` (default 5) and a `priority`. The operator
resolves each named source to its query service and passes the endpoints to the agent
as environment variables, and the agent's network policy allows only the services it is
configured to call.

When the agent answers, the runtime searches those sources with the user's question
(in a discussion, the question and not the whole thread) and places the top results in
the system prompt ahead of the model call. Keep `topK` small, because large retrieved
payloads degrade tool calling. See the
[RAGSource guide](../../reference/ragsource-guide/) and the
[Agent reference](../../reference/agent/#specragsources).

**Keyword and structured search.** Three mechanisms answer a precise question without
loading a whole document:

- `workspace_query` runs one read-only SQL `SELECT` over named artifacts in the
  [workspace](#workspace), so counting and totalling are done by a database instead of
  by the model.
- The coordinator has a query service of its own. The operator indexes the crew's
  resumes into a per-crew `RAGSource` and points the coordinator at it, so the
  coordinator can narrow the crew to the agents whose expertise matches the question.
- The MCP gateway can offer a tool search so an agent finds a tool by intent. It is
  optional and off unless the gateway enables it.

**Web search and fetch.** A crew gets web content only when it includes a web search
tool server. The content arrives as tool results and is treated as untrusted data.

## Tool and environment output

**Tool results.** A result returns to the model inside the agent's tool loop and
becomes part of that call's context. A Tooler's findings are then published to the
thread for the other agents. A result above a size threshold goes to the
[workspace](#workspace) and travels by reference.

**Environment observations.** Kubemoot gives an agent no direct view of its
environment. What an agent observes, such as the output of `kubectl`, arrives as a tool
result.

**Errors and feedback.** A failed tool call returns its error to the model. When the
same tool keeps failing, or the error rate climbs, the loop stops and the agent
publishes a `failure` signal that the whole table sees. For a compute agent, the runtime
adds one more kind of feedback: if the agent states a number without running code, the
runtime re-prompts it to compute the result by running the code over the data.

### Workspace

The workspace holds bulk data for one discussion. A tool output or contribution above a
size threshold is stored whole in a NATS object store and passed to the table **by
reference**: the message carries a preview and a reference envelope, and an agent that
needs more reads a bounded slice with `head`, `tail`, `grep`, `select`, `count`, `rows`
or `jq`. The optional `workspace_query` tool uses a SQLite or DuckDB engine. Agents
without network access read the same data as files materialized under `/artifacts`.

Workspace objects are keyed `{namespace}/{crew}/{threadId}/{agent}/{tool}-{seq}` and are
deleted with the discussion: when the thread closes, then by a bucket expiry, then by a
background reaper for anything left.

Today this store is called the **discussion artifact store**, and the tool and docs use
"artifact" for what this page calls the workspace. Renaming it to "workspace" is
planned, so that "artifact" can name generated work that outlives a discussion (see
[Artifacts](#artifacts-planned)). See
[Discussion Artifact Store](../discussion-artifact-store/) for the full mechanism.

## Memory

**Working memory.** Within one agent call, working memory is the model's own context:
the prompt, the tool results so far, and its reasoning. Across the crew, the discussion
thread is the shared working state of the turn (see [Multi-agent](#multi-agent)).

**Long-term memory.** Crew memory holds facts the crew learned about its domain, so it
stops rediscovering them. It records facts about the domain and the environment. Not yet for
facts about users. It belongs to the **crew**, not to a single agent, and is configured by
`spec.memory` on the `Crew` resource (the Agent spec has no memory field).

- **Write.** When an agent resolves a durable fact, it ends a line of its answer with
  `REMEMBER: <topic> | <key> | <value>`. The runtime parses the line, stores the fact,
  and strips the line from the answer the user sees.
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

Because agents write memory from what they read, a stored fact can carry content that
came from a tool or a peer. Treat injected memory as data.

**Episodic memory.** Not yet. Kubemoot does not retrieve past discussions into a new
one. Proposed: past discussions as retrievable episodes in the crew memory design,
following the Open Memory Protocol.

See the [Crew reference](../../reference/crew/#working-memory) for every field and the
duplicate and conflict rules.

## Multi-agent

### Discussion thread (the table)

This source is what is distinctive about a crew. Each turn runs on its own thread, and
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

Most writing on context engineering describes a single agent. The thread exists only
because several agents deliberate together. See [The Moot](../consensus-model/),
[Signals & Protocol](../signals-and-protocol/) and
[The Table and the Harnesses](../the-table-and-the-harnesses/).

## Metadata or runtime

**Injected runtime context.** The runtime adds a short list of items without your
writing them, and only these:

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

The runtime injects no description of the agent's permissions; Kubernetes RBAC and
network policy enforce them around the agent. Kubemoot sets no token budget on any
part of the context. Not yet. Proposed: a context budget per agent call, set by the
operator.

**User preferences or profile.** Not by Kubemoot. An application in front of a crew
supplies them, for example as runtime metadata.

## Why the split matters in a crew

Prompt injection enters through untrusted data: text that reads like an instruction but
arrives as content. Retrieved documents, web pages, tool output and other agents'
messages are all content. Only the static and configured sources and your own messages
are instructions.

A crew widens the path. An agent that reads an injected web page or a poisoned tool
result can restate it in its contribution. The contribution goes onto the discussion
thread, where every peer reads it, and a peer may repeat it again in its own
contribution or store it in crew memory. For that reason other agents' contributions are
untrusted data too, even though they come from agents in your own crew.

What this means in practice:

- Write rules that govern behavior in PromptModules, not in places an outside source can
  edit.
- Give each agent only the tools it needs with `enabledTools`, so an injected
  instruction has little to call.
- Put tools that read external content on agents whose other tools are read-only.
- Treat a surprising instruction inside a contribution, a retrieved passage or a stored
  fact as a finding about the content, not as an order.

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
