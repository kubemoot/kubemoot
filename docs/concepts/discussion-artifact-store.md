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

## One artifact per tool call (opt-in)

By default a tooler's contribution is its tool output glued together, one `[tool]`
section per call, and a contribution over about 4 KiB is spilled as one object. A reader
has to guess each section's format back from its first lines, and a failed call sits
among the good ones.

With `artifacts.perCallArtifacts: true` (chart value, `spec.artifacts.perCallArtifacts`
in the KubemootConfig; default `false`) every tooler (`discussRole: tooler`) instead
writes **each tool call's result as its own object** in the same bucket, under the
thread's key prefix:

```
{namespace}/{crew}/{threadId}/{agent}/{nnn}-{tool}
```

`nnn` is the agent's call number in the thread, three digits and counting up
(`001-pods_list`, `002-events_list`). A tool name is cut to letters, digits, `_`, `.` and
`-`. The body is the tool's own result text, with no `[tool]` header: JSON where the
tool returned JSON. A result over the runtime's size cap (1,000,000 characters) is cut
and ends with ` ...[truncated]`. An error result is stored too, and flagged in its
headers. Each object is announced to the materializer like any other, so a compute
sandbox finds it as a file under `/artifacts/{key}`.

### Object headers

The call is described by NATS object headers on the object. Names that the
[OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/)
define use the convention's name; the rest sit in the `kubemoot.` namespace.

| Header | Value |
|--------|-------|
| `gen_ai.operation.name` | Always `execute_tool`. |
| `gen_ai.tool.name` | The tool called. |
| `gen_ai.tool.call.id` | The model's tool call id, or the object's last key segment (`001-pods_list`) when the model sent none. |
| `gen_ai.tool.call.arguments` | The arguments as the model sent them, cut at 1,000 characters. |
| `gen_ai.agent.name` | The agent that made the call. |
| `error.type` | `tool_error`, present only on an error call. |
| `kubemoot.started` | When the call started, RFC 3339 UTC. |
| `kubemoot.duration_ms` | How long the call took. |
| `kubemoot.error` | `true` or `false`. |
| `kubemoot.bytes` | Size of the stored body. |
| `kubemoot.rows` | Items in a JSON list, or in the `items` list of a JSON object; absent when unknown (text). |
| `kubemoot.format` | `json` when the body is a JSON object or list, otherwise `text`. A text body is a `kubectl` table, YAML or prose, and `workspace_query` detects which. |
| `kubemoot.truncated` | `true` only when the result was cut at the size cap. |

### The manifest

The tooler's contribution is then a manifest, built in code from what was stored: a
header line, one line per call, and one line saying where the results are. Nothing large
goes through the bus, and the manifest stays under 4,000 characters so it posts inline.

```
[WORKSPACE agent=k8s-workloads calls=2 errors=1 prefix=default/pilot/t1/k8s-workloads/]
[CALL key=default/pilot/t1/k8s-workloads/001-pods_list tool=pods_list args={"namespace":"kube-system"} format=text-table bytes=15210 rows=unknown error=false columns=NAME,READY,STATUS,RESTARTS,AGE]
[CALL key=default/pilot/t1/k8s-workloads/002-nodes_list tool=nodes_list args={} format=json bytes=2210 rows=3 error=false fields=metadata,spec,status]
The results are stored under these keys and are not shown here. Read a key with workspace_query (SQL; the columns are listed on the call line), the artifact_* tools, or execute_code over the file /artifacts/<key>; the key default/pilot/t1/k8s-workloads/*-<tool> reads every call of one tool as one table.
```

Each call line says what the stored body is, so a reader does not guess it. `format=` is
`json` (a JSON object or list), `text-table` (a `kubectl` style table: a first line of
upper-case column titles separated by two or more spaces) or `text` (anything else, and a
JSON result that was cut at the size cap). For a table, `columns=` lists its column titles;
for a JSON list or object, `fields=` lists the keys of its first row (the `items` list of a
Kubernetes list). At most 12 names are shown, each cut at 30 characters, then `,+Nmore`.
These are the names `workspace_query` reads the table by, so a query can use them as written.
A `workspace_query` that names a column the table does not have fails with `no such column`
and the error lists the actual columns of every table in the query.

Arguments are cut to 60 characters. A manifest that would pass the limit drops the
arguments, then the column names, then replaces the last lines with a count (`[N more calls under the prefix]`).
The tooler's own model keeps its normal behavior: it sees a short stand-in for a result
too large for its window, and that stand-in now names the call's key
(`Stored as workspace artifact {key}.`). If any write to the store fails, the contribution
is the glued output as before, so a storage failure never loses data.

Call numbers keep counting after an agent restarts: the next number is found from the
objects already stored, so a call never overwrites an earlier one. Square brackets in the
summarized arguments show as parentheses so a list cannot end the call line early.

Analysts and the coordinator receive the manifest. The tool-free synthesizer cannot read
the workspace. Counting belongs to the analysts' tools, so the coordinator first gives the
analysts the complete list of calls, computes which of them each analyst read, and delivers to
the synthesizer the analysts' results and only the stored results no analyst read. How is
described next.

### The catalog: the complete list for the analysts

When the review begins, the coordinator builds the **catalog** of the thread in code from the
object headers under the thread's key prefix (not from what any model wrote), and puts it in the
`review_ready` message that wakes the analysts, after the contributions. Nothing stored is
missing from it, and the totals line shows if a line were:

```
[CATALOG calls=11 errors=1 bytes=198940 groups=3 prefix=default/pilot/t1/]
[GROUP agent=proxmox-qm tool=get_vm_rrd_data calls=9 errors=0 bytes=198000 rows=540 pattern=default/pilot/t1/proxmox-qm/*-get_vm_rrd_data]
[CATALOG-CALL key=default/pilot/t1/proxmox-qm/002-get_vm_rrd_data tool=get_vm_rrd_data args={"node":"pve","vmid":101} format=text rows=60 bytes=22000 error=false]
...
Read one call by its key, or every call of a group by its pattern, with workspace_query (SQL) or the artifact_* tools, or with execute_code over the file /artifacts/<key>. A pattern reads the calls as one table with the columns _call (the call's key), _args (its arguments) and _arg_<name> for an argument that differs between the calls (for example _arg_vmid).
```

One `[GROUP ...]` line per agent and tool carries the call, error and byte counts, the sum of the
rows when every call reports them (`unknown` otherwise) and the key pattern that reads the group.
One `[CATALOG-CALL ...]` line per call carries the tool, the arguments (cut at 60 characters),
the format and rows from the headers, the bytes and whether it was an error. A catalog line never
begins with `[CALL key=`, so it is not read as a tooler's manifest. When the call lines do not fit
a quarter of the smallest window any server publishes for the model (about 3 characters a token;
8,000 characters when no window is known), the catalog is rendered one level coarser: the totals
and the group lines only, with a note that `SELECT DISTINCT _call, _args` over a group's pattern
lists its calls. It is never cut: no group line is dropped. The files stay separate and are reachable
by one key, by a pattern through `workspace_query`, and as `/artifacts/<key>` for `execute_code`.

### Coverage: what each analyst read

Which stored calls an analyst read is computed in code, never reported by the model. The agent
runtime keeps the keys that each **successful** tool call of the agent read and puts them on the
agent's `agree` signal as metadata `readKeys`. What is traced:

| Tool | Traced keys |
|------|-------------|
| `workspace_query` | every value of the `artifacts` map (a key or a `*` pattern) |
| `artifact_head`, `artifact_tail`, `artifact_grep`, `artifact_count`, `artifact_rows`, `artifact_select`, `artifact_jq` | the `key` argument |
| `execute_code` | each `/artifacts/<key>` path that the code names as text: a literal key, a directory ending in `/` (every call below it) or a pattern with `*` |

What is **not** traceable: a path that code builds at run time (concatenation, a directory
listing) counts for nothing, and a call that failed counts for nothing. Reading a call is taken as
covering it: a query that read a call may have used only some of its rows, which is not looked into.

The coordinator expands each pattern against the catalog (`*` stands for any characters except `/`)
into the calls it matched. An analyst counts only when it answered with data (not `NO_DATA` or
`TOOL_GAP`), so the reads of an analyst that failed, stood aside or answered no data cover nothing.
A call that is a tool error is never covered: it holds no data, and the synthesis sees it so a
failed source stays visible. The coverage is rebuilt from the signals, so a restarted coordinator
has it back as far as the signals are replayed. The per-analyst lines go at the head of the synthesis
input, followed by the catalog's totals and a few ADL rules (a `[COVERED ...]` line stands for a
result an analyst read; a count or total comes from a tool result only):

```
[COVERAGE analyst=compute read=9 of 11 stored calls: get_vm_rrd_data 9 of 9]
[COVERAGE read by no analyst: 2 calls ({get_node_status=1, list_vms=1}), 200 bytes]
[CATALOG calls=11 errors=1 bytes=198940 tools={get_node_status=1, list_vms=1, get_vm_rrd_data=9}]
```

### Delivery order

The synthesizer is the reduce step over the analysts' map; refinement is only the fallback. The
coordinator decides from state, in this order:

1. **Covered results are never read raw.** Their call lines are replaced by one line,
   `[COVERED key=... tool=... by=<analysts>]`, and the results are not read from the store.
2. **The results no analyst covered** are read (in parallel, up to 8 at once) and delivered whole in the
   room of the model the scheduler picks, as described under [Delivery to the synthesis](#delivery-to-the-synthesis).
   When they all fit, that is one model call.
3. **When they do not fit and the crew has a query-capable analyst** (role `analyst`, with
   `workspace_query` or `execute_code` among its tools in the crew resumes), the coordinator
   convenes it: the thread goes back to review with only the convened analysts, and the new
   `review_ready` names the uncovered key patterns, each with its count of uncovered calls
   (`[ASSIGN analyst=... pattern=... calls=N]`), shared out largest first so several analysts read
   in parallel. The message carries no data; the analysts read with a tool that counts. When they
   have signalled, the synthesis runs again with their reads covered. The coordinator convenes at most once per thread. The test of "does not fit" compares the tokens of the
   uncovered results with the room the smallest window leaves after the rest of the prompt.
4. **What no tool reduced** (the analysts failed, or the crew has no query-capable analyst) goes
   through the notes rounds below, and what is still unread is named `UNKNOWN` with its count.
   A crew with no query-capable analyst keeps this path, with the catalog counts and the rule that
   a count or total comes from a tool result only added to its synthesis input.

### Delivery to the synthesis

The synthesis call is placed by the scheduler like any other, and the room it has depends on
the model the scheduler picks for that call. So the prompt is built after the pick, from the
**context window of the picked model on the picked server**, never the smallest window of any
server: a server that does not hold the model (a CPU embedding server with an 8,192-token
window, say) does not count. The room is that window, less the rest of the prompt and the reply
reserve, counted with the estimator an agent's tool loop uses (digits and symbols count as
dense, so numeric JSON takes about a token per digit). No size is fixed: an install whose
models have an 8K window, a 32K window or a 128K window gets batches sized to its own.

**One round.** When every stored result fits that room, the coordinator replaces each
`[CALL ...]` line with `[tool]` and the stored result (the form the glued output has), drops
the manifest's header and closing lines, and the crew's synthesis prompt writes the answer.
The fill considers every result in manifest order; one that does not fit does not stop the
ones after it.

**Several rounds (refinement).** When the results do not all fit one prompt, the evidence is
read in rounds, the established "refine" pattern:

1. Round 1 sends the question, the analysts' contributions and the first batch of stored
   results that fits. The model writes **working notes**: one line per finding, with exact
   values, names and counts, each tagged with the call key it came from (`- [<call key>]
   <finding>`). It does not round, merge distinct items or count by eye; a number or list an
   analyst computed is copied verbatim with the analyst's name.
2. Rounds 2 to N send the notes so far and the next batch. The model adds findings, corrects
   one only when a new result shows it wrong (with the reason on that line), and never drops
   a finding without a reason.
3. The last round is the crew's normal synthesis prompt, over the question, the contributions
   and the finished notes (under a "WORKING NOTES FROM THE STORED TOOL RESULTS" heading).

Every stored result is read exactly once across the rounds, and the batch of each round is
sized to the window of the model that round was placed on. Rounds can land on different
models: a later round that gets a smaller window takes a smaller batch (it is re-planned
from what is left, never sent over the window), and one that gets a larger window takes more.
A result larger than one round's room is read in consecutive parts, cut at line boundaries
(a single line longer than the room is cut at the character), each marked `part i of n of
<key>`. The same evidence takes more rounds on a small window than on a large one.

**Limits.** The rounds, counting the last, are capped by the coordinator setting
`KUBEMOOT_DISCUSS_SYNTHESIS_EVIDENCE_ROUNDS` (`kubemoot.discuss.synthesis-evidence-rounds`,
default **4**; a value below 1 means the default), and each allowed round gets the synthesis
time limit (`synthesisTimeoutSeconds`) within the thread's hard ceiling. Both are safety
limits, not flow control. A small-window install needs more rounds for the same evidence, so
raise the cap there. The last allowed round takes the results that fit and names every other
result with the same notice an unreadable artifact gets:
`[ARTIFACT UNAVAILABLE key=... Treat it as UNKNOWN ... do NOT infer, guess, or fabricate ...]`.
A result that was not delivered is never described as summarized by the analysts.

**Time.** A notes round writes at most about a fifth of the tokens of its batch (never fewer than
512; the model's own limit still applies), not a flat limit. A notes round begins only when its
predicted cost fits the budget left: the cost is predicted from the calls already made in this
synthesis (their time per token, read and written, times the tokens of the round, plus one more
call for the answer). The first call has nothing to predict from and is allowed. The budget is the
synthesis time limit times the round cap, within the thread's hard ceiling. A round that does not fit
is not started and the results it would have read are named `UNKNOWN`. The scheduler's own latency
predictions are not used; the prediction is from this synthesis's calls.

**Unknown window.** When no window is known for the picked model, there is one round and each
result is filled in up to 24,000 characters (cut with a marker), as before.

The round prompts and the notes format are ADL resources of the runtime
(`agent-runtime/src/main/resources/prompts/synthesis-notes-*.adl`), not Java text.

The coordinator logs one line per synthesis that delivered stored results:

```
synthesis evidence: thread=... windows=[8192, 8192, 32768] rooms=[...] results=7 filled=7 rounds=3 chunks=0 bytes=140000 notDelivered=[]
```

and the `synthesis_done` event carries `rounds` (model calls the synthesis made) and
`filledBytes` (bytes of stored results that reached the model), so a measurement can read
them. A thread with stored per-call results also gets the `delivery` object (the keys covered
and not covered, the analysts convened and the milliseconds of each step, see
[signals and protocol](signals-and-protocol.md)), and the log line ends with
`covered=N uncovered=M convened=[...] stepMillis={catalog=.., read=.., convene=.., round1=..}`.
A thread without a manifest reports one round and no bytes, and synthesizes as before.

The final answer to the user never carries a manifest line. An analyst that needs rows calls
`workspace_query` with a key from a `[CALL ...]` line; see
[Query with SQL](#query-with-sql). Both formats coexist: artifacts written before the
setting was turned on, and the single object a tooler spills with the setting off, are
read as before.

Per-call objects follow the same retention as every other object: they are deleted with
their thread (by the thread's key prefix), and the 48-hour bucket TTL bounds anything
that is missed. See [Garbage collection](#garbage-collection).

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
mode; an MCPServer with `kubemootImage: artifactAccess` runs it without naming a version,
see the [MCPServer guide](../../reference/mcpserver-guide/#run-a-kubemoot-built-server-without-naming-a-version)). That service exposes output-bounded tools that extract only the slice an
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

### Query with SQL

`workspace_query` gives every agent one query language, SQL, for reading the evidence a
discussion has already gathered. Toolers call tools and post the results, and a large
result becomes an artifact in the discussion's workspace. Those artifacts arrive in many
formats: `kubectl` text tables, JSON (`kubectl -o json`, search results), YAML, Markdown
tables, CSV, and a tooler's raw output made of one `[tool]` section per call. With
`workspace_query` an agent counts, filters, groups, sorts and totals any of them without
choosing a tool for the format.

A call works in four steps:

1. The agent names the artifacts as table names and writes one `SELECT`.
2. The read-ops service detects each artifact's format and turns it into a table: columns
   from a `kubectl` table's headers, keys from JSON items, a tooler's sections read one by
   one (see [How each artifact becomes a table](#how-each-artifact-becomes-a-table)).
3. It loads the tables into a new in-memory SQLite database, runs the read-only
   `SELECT`, and returns the rows.
4. It discards the database.

```
artifacts: {"pods": "<key>"}
sql: SELECT namespace, COUNT(*) FROM pods GROUP BY namespace ORDER BY 2 DESC LIMIT 5
```

**Why one language.** Without it, an agent chooses among format-specific read tools
(`select` reads CSV only, `jq` reads JSON only, and so on) or writes code for the
compute sandbox. With it, the counting is done by a deterministic engine instead of the
model, and models write SQL well. The read-ops tools above remain available for
fetching a slice of an object.

**What it does not do.** It reads only artifacts already in the workspace. It does not
reach the cluster, Prometheus or any other source, so it counts exactly what the toolers
gathered, and incomplete data stays incomplete. It runs one `SELECT`, with no writes,
no files outside the named artifacts and no network ([Limits and safety](#limits-and-safety)).

#### What persists

| Thing | Persists? |
|-------|-----------|
| The artifact in the NATS object store (`kubemoot_discussion_artifacts`) | Yes. This is the only persistence for artifacts, until [garbage collection](#garbage-collection) removes it. |
| The files under `/artifacts` | No. A local copy that artifact-access reads from. It is lost with the pod and fetched again from the store on next use ([Workspace directory](#workspace-directory)). |
| The SQLite database | No. It exists for one query. |

SQLite stores nothing. Crew memory is separate and lives in NATS KV; see
[Agent Context](../agent-context/#memory).

#### Configuration

The tool is on by default, backed by SQLite. The operator chart value
`artifacts.queryEngine` chooses the engine: `sqlite` (the default) and `duckdb` offer
`workspace_query`; `none` turns it off and the service offers no query tool. A KubemootConfig written by hand that leaves out
`spec.artifacts.queryEngine`, or the whole `artifacts` object, also gets `sqlite`. To turn
the tool off, set `artifacts.queryEngine: none` in the chart values (or
`spec.artifacts.queryEngine: none` in the KubemootConfig). Turning the engine on changes
no CPU or memory request or limit of the artifact-access pod: an MCPServer that sets no
`spec.resources` keeps the operator's default MCPServer resources (see the
[MCPServer reference](../../reference/mcpserver-guide/)), which hold the growth the memory watch allows for one query. The
only addition is the 1Gi `/artifacts` volume described below. If you set `spec.resources`
yourself, keep the memory limit above the memory setting plus the base process. See
[Query engines](#query-engines) for why and when. The engine sits behind a seam:
another engine can replace these through the chart value without a change to crews or
prompts. The engines speak their
own SQL; the tool description tells the model which one it has.

| Input | Meaning |
|-------|---------|
| `artifacts` | A map from a table name you choose to an artifact key. Up to 8 per query. A name is letters, digits and underscores. The tool also accepts a string that holds that JSON object; any other string is rejected with the expected shape, `{"<table>": "<artifact key>"}`. |
| `sql` | One `SELECT` (or `WITH ... SELECT`) over those table names. |

The result is plain text a small model reads easily: a header line, one `a | b` line per
row, then `(N rows)`. A result longer than the row limit (100 by default) is cut and
says so.

```
artifacts: {"pods": "default/pilot/t1/k8s/pods_list-1"}
sql: SELECT "STATUS", count(*) AS n FROM pods GROUP BY 1 ORDER BY 1

STATUS | n
Pending | 1
Running | 2
(2 rows)
```

#### How each artifact becomes a table

The format is decided from the content (object keys carry no file extension):

| Content | Table |
|---------|-------|
| A JSON list of objects | One row per object, one column per key. |
| A JSON object with an `items` list (`kubectl get -o json`) | One row per item. |
| A `kubectl get` table | One row per line, columns named by the header. |
| YAML (several documents become a list) | Read as JSON. |
| A Markdown pipe table | One row per table row. |
| CSV with a header row | One row per line. |
| Anything else | One row per line, columns `line_no` and `line`. |

With [per-call artifacts](#one-artifact-per-tool-call-opt-in), the key of one call
(`default/pilot/t1/k8s-workloads/001-pods_list`) is one table, and its `kubemoot.format`
header decides how it is read: `json` is read as JSON, `text` is detected as above. A
key with a `*` reads every stored call that matches as one table. `*` stands for any
characters except `/`, and the part before the first `*` must name the namespace, crew and
thread (`ns/crew/thread/`), and the namespace must be the artifact-access pod's own
(`KUBEMOOT_NAMESPACE`). `default/pilot/t1/k8s-workloads/*-pods_list` is every
`pods_list` call of one agent in the thread; `default/pilot/t1/*/*-pods_list` is every
`pods_list` call of every agent. Calls of the same shape are stacked as the rows of one
table, a different shape is `name_2`, and a call that returned an error is left out and
named in a note. At most 100 objects match, and together they stay inside the artifact
size limit.

Every row of a table stacked from a `*` key carries the call that produced it, so rows
can be grouped, joined and counted per call (both engines):

| Column | Value |
|--------|-------|
| `_call` | The object key of the call. |
| `_args` | The call's arguments as the runtime recorded them (JSON text, cut at 1,000 characters); empty when the call has none. |
| `_arg_<name>` | An argument that is a plain value (text, number or flag) in every stacked call and **differs between them**, one column per such argument, at most 6 in name order, with the argument's name made of letters, digits and `_`. For nine `get_vm_rrd_data` calls that differ only in `vmid`, that is `_arg_vmid`. |

```
artifacts: {"rrd": "default/pilot/t1/proxmox-qm/*-get_vm_rrd_data"}
sql: SELECT _arg_vmid, max(cpu) AS peak_cpu FROM rrd GROUP BY _arg_vmid ORDER BY peak_cpu DESC
```

A column the data already has under that name (compared without regard to case) is never
replaced; the call column is then left out. The columns are added to a `*` key even when
it matches one call. A key that names one call is unchanged, and so is every artifact
that is not a stored tool call. An argument that is the same in every call, is missing from
one of them, is a list or object, or comes from arguments cut at the header limit has no
`_arg_` column; `_args` still holds the text, and
`SELECT DISTINCT _call, _args FROM rrd` lists the calls. `artifact_count` on a call whose result is a JSON list returns the list's
item count from the `kubemoot.rows` header.

A tooler agent's contribution is, with per-call artifacts off, its raw tool output, which the agent runtime writes as
one section per tool call: a `[tool_name]` line, the result, then a blank line. A query
reads such an artifact one section at a time, so a kubectl table or a JSON result behind
a `[pods_list]` line is read as the table or the JSON it is. Sections of the same
format and columns (the same tool called twice) are the rows of one table, the name you
chose. A section with other columns is its own table, `name_2`, `name_3` and so on, in
order of appearance. When a table is made of several sections, a note after the rows
names the tools behind it. An artifact whose sections are all prose is read as one text. A
result the runtime cut at its size limit loses its last partial row (a JSON list keeps
its complete items), and a note says so. An artifact that does not start with a
`[tool_name]` line is read as above.

A column whose every value is a number is stored as numbers, so `sum`, `avg` and
`ORDER BY` work on it (`RESTARTS` of a kubectl table, a Markdown count column).
A nested object or list in JSON or YAML is read per engine. In SQLite it is a text
column holding JSON: `status->>'phase'` reads a field, `json_array_length(tags)` counts
a list. In DuckDB it is a struct: `status.phase`. A JSON `true` or `false` is `1` or `0`
in SQLite and a boolean in DuckDB.

Markdown beyond pipe tables is read as lines of text. DuckDB's Markdown reader is a
community extension that downloads at run time, which a server with no network cannot do.

#### Limits and safety

- **Read-only.** The SQL must be exactly one `SELECT` (or `WITH` or `VALUES`). Writes,
  `ATTACH`, `PRAGMA`, `VACUUM`, `COPY`, `INSTALL`, `SET` and a second statement are
  refused before the engine sees them. The query then runs as a sub-select of a wrapper
  query, so no other statement can be spelled around it. SQLite adds a connection set
  to query-only with `ATTACH` switched off; DuckDB locks its configuration.
- **Only the named artifacts.** Each query gets its own in-memory database holding only
  the tables it names. SQLite has extension loading disabled and no file or network
  functions. DuckDB runs in its own short-lived process with external access
  off, so a path outside the named artifacts (`/etc/passwd`, another artifact) and any
  `http`, `https` or `s3` address are refused and extensions cannot load or download.
- **Bounded.** A query that runs past the time limit (10 seconds by default) is stopped.
  An artifact over 32 MiB is refused, and the result is cut at the row limit and at
  the tool output cap. DuckDB's memory is capped at the memory setting (256 MB by
  default). The pure-Go SQLite build has no heap limit of its own, so artifact-access
  watches the size of its process: a query that grows it by more than the memory
  setting is stopped, a single value cannot exceed a quarter of the setting, and at
  most two SQLite queries run at once. The watch reads `/proc`; where that is
  unavailable the time limit and the value ceiling still apply. Give the pod a
  container memory limit as the last bound.
- **Errors say what to fix.** A missing artifact, an unknown column or a refused
  statement comes back as a tool error naming the cause.

The service reads these variables; the chart value sets only the engine.

| Variable | Default | Meaning |
|----------|---------|---------|
| `ARTIFACT_QUERY_ENGINE` | `none` | `none`, `sqlite` or `duckdb`. The service itself defaults to `none` when run on its own; the operator sets it from the KubemootConfig `spec.artifacts.queryEngine` (`sqlite` when unset) unless the MCPServer sets it. |
| `ARTIFACT_DUCKDB_BIN` | `duckdb` | The `duckdb` program. The artifact-access image that serves `duckdb` must carry it; when it is missing the service logs the reason and offers no query tool. |
| `ARTIFACT_QUERY_TIMEOUT` | `10s` | Time limit per query. |
| `ARTIFACT_QUERY_MAX_ROWS` | `100` | Rows per result. |
| `ARTIFACT_QUERY_MEMORY` | `256MB` | DuckDB memory limit. For SQLite, the growth of the process that stops a query, and a quarter of it is the ceiling on any one value. |
| `ARTIFACT_QUERY_MAX_ARTIFACT_BYTES` | `33554432` | Largest artifact a query may read. |
| `ARTIFACT_DIR` | `/artifacts` | Where artifacts are materialized; the server needs a writable volume there. |

An artifact the sandbox sidecar has not materialized yet is fetched from the object store
on first use.

#### Workspace directory

When the engine is `sqlite` or `duckdb`, the operator gives the MCPServer pod an
`emptyDir` mounted at `/artifacts` with a size limit of 1Gi, and sets `TMPDIR=/artifacts`,
so a pod with a read-only root filesystem has a writable place for the materialized
artifacts and the engine's scratch files (the DuckDB program's home and temporary
directory). One query names at most 8 artifacts of at most 32 MiB each and DuckDB may spill
up to 512 MiB, so 1Gi holds the most one query writes; the kubelet evicts a pod that
exceeds it. With `none` nothing is added. A volume the MCPServer already mounts at
`/artifacts`, or a `TMPDIR` it sets, is kept. Raising
`ARTIFACT_QUERY_MAX_ARTIFACT_BYTES` raises what a query writes, so declare a larger
`/artifacts` `emptyDirVolumes` entry with it.

#### Query engines

`sqlite` is the recommended engine and `duckdb` is the option for large data.

**Why SQLite is the recommended engine.**

- It runs inside the artifact-access process. There is no extra image, no extra process
  to start and no program to install, and it needs a few MB of memory.
- Its database is a new in-memory one for each query and holds nothing afterward; see
  [What persists](#what-persists).
- SQLite is public domain and written in C. Kubemoot uses a pure-Go translation of it,
  so artifact-access stays a single static binary on its existing image.
- "Lite" means embedded and serverless, not limited. SQLite is the most widely deployed
  database engine, and it has full SQL: joins, grouping, window functions, common table
  expressions and JSON functions.
- Workspace data is capped in size and is usually kilobytes. For that, both engines
  answer in milliseconds; with DuckDB the start of its process dominates the time.

**When DuckDB fits.**

- DuckDB is a column store, built for analytical scans over large tables, and it reads
  JSON and CSV files directly.
- It is a C library and a separate program, so the artifact-access image must carry the
  `duckdb` program. When `duckdb` is chosen and the program is missing, artifact-access
  logs why and offers no query tool.
- Choose it when artifacts are large (many thousands of rows and up) and queries scan
  them whole.

Measured with the same kind of query on both, a group-by over a JSON list:

| Case | SQLite, in the artifact-access process | DuckDB, separate process |
|------|----------------------------------------|--------------------------|
| `SELECT 1` | about 3 MB more than an idle process, 1 to 2 ms | 42 MiB peak, 0.07 s |
| 50 rows | about 3 MB more, 1 to 2 ms | 45 MiB peak, 0.07 s |
| 200,000 rows, about 14 MB | about 30 MB more, 1.3 s | 127 MiB peak, 0.24 s |

SQLite figures are the added peak resident memory of a process running the query
in-process, over the same process idle (about 9 MB). DuckDB figures are the peak resident
memory of its whole process. SQLite loads the file into a table before it queries, so its
time grows with the size of the artifact. DuckDB scans the file as it queries and is
faster on the large case.

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

The compute sandbox is the [code sandbox](../code-sandbox/)
([`code-sandbox`](https://github.com/kubemoot/kubemoot/blob/main/code-sandbox/README.md) in the repository),
an MCP stdio server that exposes `execute_code` and `validate_code` for Python
(standard library only) or bash. The pod is the sandbox: the container that runs
programs holds no credentials and talks to nothing itself, and the crew's network policy
limits the pod's egress to the message bus. A **materializer sidecar** (the same
`artifact-access` binary in its default mode) is the only part of the pod that reads
NATS. It fetches referenced objects from the store and writes them into a shared local volume under `/artifacts`. The sandbox
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

- [Agent Context](../agent-context/) - where the workspace sits among the other parts of
  an agent's context, and the planned rename from artifact to workspace.
- [Conversations, Turns & Threads](../conversations-turns-and-threads/) - the
  thread whose lifecycle governs artifact GC layer 1.
- [Signals & Protocol](../signals-and-protocol/) - the discussion messages that
  carry reference envelopes.
- [Tooler-Analyst Architecture](../../architecture/tooler-analyst-architecture/) -
  the agent roles that produce and consume artifacts.
- [MCP Tools](../mcp-tools/) - how agents access the read-ops MCP service.
