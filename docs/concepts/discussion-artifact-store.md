---
title: "Discussion Artifact Store"
weight: 35
description: "How Kubemoot stores every tool result as its own file, puts the file's description in the discussion, and posts a result as well when it is small enough."
---

When a tooler fetches data from a live system, the result can be large: a full
namespace inventory, a Prometheus time-series dump, a JSON response carrying
hundreds of rows. The NATS discussion bus and the LLM context window were not
designed to carry datasets. Dumping bulk data onto the bus truncates it, floods
downstream agents' context, and degrades the quality of reasoning that follows.

Kubemoot treats this as plain mechanics, with no setting to turn on:

1. **Every tool result is written to a file**, always, one file per call.
2. **The discussion carries the file's description**: the tool, its arguments, rows, bytes,
   format and columns.
3. **A result small enough to read is posted in the discussion as well**, whole, after its
   description. A larger result is only in its file. How small is computed from the context
   window of the models that read the discussion ([Small results go in the
   discussion](#small-results-go-in-the-discussion)). A result is never cut: it is posted whole
   or not at all.
4. **Agents read files by key** (`workspace_query`, the `artifact_*` tools, `execute_code`)
   and **add the facts they find** to the discussion. Keys are short and relative to the
   thread ([Short keys](#short-keys)).
5. **The synthesizer answers from the discussion only.** A fact no agent added and no posted
   result holds is reported as unknown, and the file descriptions in the discussion let the
   answer say which data existed. When tools ran and the discussion holds no fact at all, the
   coordinator does not call the model and says that no data was obtained
   ([No facts, no model](../signals-and-protocol/#no-facts-no-model)).

The remedy for a result that is too large is a tool call that asks for less (fewer fields, a
filter, a limit, a shorter time range). Storing the result as a file keeps the bus and the
context lean; it does not replace asking for less.

## The producer pattern

Every agent with tools writes **each tool call's result as its own object** in the NATS
JetStream Object Store bucket `kubemoot_discussion_artifacts`, under the thread's key prefix:

```
{namespace}/{crew}/{threadId}/{agent}/{nnn}-{tool}
```

`nnn` is the agent's call number in the thread, three digits and counting up
(`001-pods_list`, `002-events_list`). A tool name is cut to letters, digits, `_`, `.` and
`-`. The body is the tool's own result text, with no `[tool]` header: JSON where the
tool returned JSON. A result over the runtime's size cap (1,000,000 characters) is cut
and ends with ` ...[truncated]`. An error result is stored too, and flagged in its
headers. Each object is announced to the materializer, so a compute
sandbox finds it as a file under `/artifacts/{key}`.

Call numbers keep counting after an agent restarts: the next number is found from the
objects already stored, so a call never overwrites an earlier one.

A loop with no discussion thread (a direct chat to an agent) has nowhere to store files and
works as a plain chat.

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

### What an agent posts: the descriptions of its files, and its small results

A contribution is what the agent states, followed by the description of each file it
produced, built in code from what was stored. A raw-output tooler (`discussRole: tooler`)
states nothing: its contribution is the descriptions alone. Any other agent with tools posts
its own words first, then the descriptions after a blank line. The descriptions stay under
4,000 characters. A result small enough for the discussion follows its description whole,
between `[RESULT]` and `[END RESULT]`; a larger result is only in its file, so nothing large
goes through the bus.

```
Pod a-1 uses the most CPU, 0.9 cores (workspace_query over 001-pods_list).

[WORKSPACE agent=k8s-workloads calls=3 errors=0 prefix=k8s-workloads/]
[CALL key=k8s-workloads/001-pods_list tool=pods_list args={"namespace":"kube-system"} format=text-table bytes=15210 rows=unknown error=false columns=NAME,READY,STATUS,RESTARTS,AGE]
[CALL key=k8s-workloads/002-nodes_list tool=nodes_list args={} format=json bytes=2210 rows=3 error=false fields=metadata,spec,status]
[CALL key=k8s-workloads/003-events_list tool=events_list args={} format=text bytes=15 rows=unknown error=false]
[RESULT]
No events found
[END RESULT]
The results are stored under these keys; a result not shown above is only in its file. Read a key with workspace_query (SQL; the columns are listed on the call line), the artifact_* tools, or execute_code over the file /artifacts/<key>; the key k8s-workloads/*-<tool> reads every call of one tool as one table.
```

The keys in the lines are relative to the thread: `{agent}/{nnn}-{tool}` ([Short
keys](#short-keys)).

Each call line says what the stored body is, so a reader does not guess it. `format=` is
`json` (a JSON object or list), `text-table` (a `kubectl` style table: a first line of
upper-case column titles separated by two or more spaces) or `text` (anything else, and a
JSON result that was cut at the size cap). For a table, `columns=` lists its column titles;
for a JSON list or object, `fields=` lists the keys of its first row (the `items` list of a
Kubernetes list). At most 12 names are shown, each cut at 30 characters, then `,+Nmore`.
These are the names `workspace_query` reads the table by, so a query can use them as written.
A `workspace_query` that names a column the table does not have fails with `no such column`
and the error lists the actual columns of every table in the query.

Arguments are cut to 60 characters. Descriptions that would pass the limit drop the
arguments, then the column names, then replace the last lines with a count
(`[N more calls under the prefix]`). Square brackets in the summarized arguments show as
parentheses so a list cannot end the call line early.

A statement is cut at 24,000 characters; the descriptions and posted results after it are never cut. An agent that answers `NOTHING_TO_ADD` or `TOOL_GAP:` posts no descriptions (the files are still stored and still listed in the catalog).

An agent's own model keeps its normal behavior while it works: a result too large for its
window is shown to a tooler's model as a short stand-in that names the call's key
(`Stored as workspace artifact {key}.`), and to any other agent as an error asking for a
narrower call. In both cases the whole result is in the file.

**A write that fails** is not hidden. The call gets a line
`[NOT STORED tool=<tool> args=... error=... - the result could not be written to the workspace and is not available]`
in place of its `[CALL ...]` line, and the synthesizer treats that data as unknown. The
result is not posted in the contribution in its place: nothing backs it, so it is not offered
as data.

### Small results go in the discussion

A tool result is posted after its description when it fits in whole. Both limits follow from the
context window of the models that read the discussion. The window used is the smallest one among
the servers that hold the agent's model, so the reader with the least room can still take the
discussion in; when no window is published, the room is a fixed 8,000 characters.

| | Rule | 32,768-token window | 131,072-token window | No window known |
|---|---|---|---|---|
| Room for results in the discussion | A quarter of the window, at 3 characters a token | 24,576 | 98,304 | 8,000 |
| One result | An eighth of the room | 3,072 | 12,288 | 1,000 |
| All the results of one contribution | A quarter of the room, at most 20,000 | 6,144 | 20,000 | 2,000 |

The 20,000 cap keeps a contribution (descriptions of at most 4,000 characters plus results) inside
the 24,000 characters an agent's statement may take on the bus. The catalog uses the same room.

Results are admitted in call order. A result that is over its share, or does not fit what is left
of the contribution's, stays in its file only, and a smaller result after it can still go in.
Rules that keep the posted text trustworthy:

- A result is posted exactly as the tool returned it, or not at all. It is never cut, and a
  result that would have been cut at the storage cap (1,000,000 characters) is not posted.
- A result that holds the text `[END RESULT]` itself, an empty result, and a result that could
  not be stored are not posted.
- A failed call posts its error text between `[FAILED RESULT]` and `[END RESULT]` when it fits.
  That text says why the call failed; it is not data about the system and does not count as a
  fact.
- A long manifest that has to drop lines drops the calls without a posted result first, so a
  posted result keeps the line that describes it.

The example from a Kubernetes crew: a CronJobs table of 856 bytes and an events result of 15 bytes are
posted, and a Jobs table of 25,662 bytes stays in its file on a 32,768-token window (and on a
131,072-token window, where one result may take 12,288 characters). The agents that read files can
still query the Jobs table; the synthesizer reads the CronJobs and the events from the discussion
and reports the Jobs as unknown.

### Short keys

Agents see and type keys relative to the thread: `{agent}/{nnn}-{tool}`, for example
`k8s-workloads/002-resources_list`. The long key still exists in the store and in the reference
envelope, but no agent has to copy a 36-character thread id, which models mistype.

The agent runtime names the thread in the `_meta` of each call to a tool that reads stored results
(`workspace_query`, the `artifact_*` tools, `execute_code`), as the entry `workspacePrefix`
(`{namespace}/{crew}/{threadId}/`). It is the same MCP `_meta` that carries the requester's time zone
and date, and the [MCP gateway](../../reference/mcpgateway-guide/) forwards it after checking it
is three plain name parts. The tool resolves each key inside that thread:

| Key given | Result |
|-----------|--------|
| `k8s-workloads/002-resources_list` | Read as `{namespace}/{crew}/{threadId}/k8s-workloads/002-resources_list`. |
| `k8s-workloads/*-resources_list` | The pattern, inside the thread. `*/*-pods_list` is every `pods_list` call of every agent. |
| The full key of this thread | Read as given. |
| A key of another thread, or a full key with a mistyped thread id | Refused with a message that gives the short form. |
| A key with an empty, `.` or `..` part | Refused, so a key cannot climb out of the thread. |

Without a thread in `_meta` (a call from outside a discussion), keys are used as given. `execute_code`
runs in a sandbox that knows no thread, so the runtime writes the thread into each
`/artifacts/{agent}/{nnn}-{tool}` path in the code before the call; a path that already names a
thread is left alone. Short keys are resolved for the files of the thread the call runs in only; code
that builds a path to another thread's file by hand can still read it from the sandbox's mount.

### The catalog: the complete list of files

When the review begins, the coordinator builds the **catalog** of the thread in code from the
object headers under the thread's key prefix (not from what any model wrote), and puts it in the
`review_ready` message that wakes the agents that can read files, after the contributions. Nothing
stored is missing from it, and the totals line shows if a line were:

```
[CATALOG calls=11 errors=1 bytes=198940 groups=3]
[GROUP agent=proxmox-qm tool=get_vm_rrd_data calls=9 errors=0 bytes=198000 rows=540 pattern=proxmox-qm/*-get_vm_rrd_data]
[CATALOG-CALL key=proxmox-qm/002-get_vm_rrd_data tool=get_vm_rrd_data args={"node":"pve","vmid":101} format=text rows=60 bytes=22000 error=false]
...
Read one call by its key, or every call of a group by its pattern, with workspace_query (SQL) or the artifact_* tools, or with execute_code over the file /artifacts/<key>. A pattern reads the calls as one table with the columns _call (the call's key), _args (its arguments) and _arg_<name> for an argument that differs between the calls (for example _arg_vmid).
```

One `[GROUP ...]` line per agent and tool carries the call, error and byte counts, the sum of the
rows when every call reports them (`unknown` otherwise) and the key pattern that reads the group.
One `[CATALOG-CALL ...]` line per call carries the tool, the arguments (cut at 60 characters),
the format and rows from the headers, the bytes and whether it was an error. A catalog line never
begins with `[CALL key=`, so it is not read as an agent's file description. When the call lines do
not fit a quarter of the smallest window any server publishes for the model (about 3 characters a
token; 8,000 characters when no window is known), the catalog is rendered one level coarser: the
totals and the group lines only, with a note that `SELECT DISTINCT _call, _args` over a group's
pattern lists its calls. It is never cut: no group line is dropped.

An analyst reads files by key or pattern and puts the facts it finds into its own contribution,
for example "pod a-1 uses the most CPU, 0.9 cores". Counting, summing and ranking belong to the
analysts' tools (`workspace_query`, `execute_code`), never to the synthesizer.

### The synthesis: the discussion only

The tool-free synthesizer cannot read the workspace and is not given a file. Its input is:

1. the signal counts and the original question,
2. a few ADL rules for file descriptions (`agent-runtime/src/main/resources/prompts/synthesis-files.adl`),
   present only when a contribution describes a file,
3. the thread: every contribution, each with the descriptions of its files and the results posted
   with them, with the `[WORKSPACE ...]` header and the closing "stored under these keys" line
   removed (they speak to an agent that can read files).

The rules say that a `[CALL ...]` line describes a file and carries no values; that a `[RESULT]`
block is the whole result of the call above it and is data; that a count, total or list is stated as
the agent stated it; that nothing is derived from rows and bytes; and that a value no agent stated and
no posted result holds is reported as **unknown**, naming the files that existed for it (tool,
arguments, rows). A `[NOT STORED ...]` line is unavailable data and also unknown. Because the
synthesizer sees the discussion only, the cost of an answer is bounded by the room computed above, not
by the size of the files.

When tools ran and the discussion holds no fact, the synthesizer is not called ([No facts, no
model](../signals-and-protocol/#no-facts-no-model)).

The final answer to the user never carries a file description line or a posted result block.

Per-call objects are deleted with their thread (by the thread's key prefix), and the 48-hour
bucket TTL bounds anything that is missed. See [Garbage collection](#garbage-collection).

## The reference envelope

Each stored file is announced on the artifact subject
(`kubemoot.artifacts.<namespace>.<crew>.<threadId>`) with a small JSON structure so the
materializer can put it on disk and a consumer can decide before opening it:

| Field | Purpose |
|-------|---------|
| `bucket` | Object store bucket name. |
| `key` | Full object key (`namespace/crew/thread/agent/nnn-tool`). |
| `contentType` | MIME type of the stored object (`application/json` for a JSON body). |
| `bytes` | Total object size in bytes. |
| `rows` | Row count when known. |
| `tool` | Name of the MCP tool that produced the data. |
| `agent` | Name of the agent that ran the tool. |
| `createdAt` | ISO-8601 timestamp of the write. |

The rows and byte counts let many consumers answer without fetching at all.

## Consumer paths

Not every agent can reach the object store directly, so there are two consume paths
depending on the agent's network posture.

### Network-capable agents (sip from the bus)

Toolers, analysts, and the coordinator can reach NATS directly. They read a file's
description (tool, arguments, rows, bytes, columns), decide which slice they need, and
issue a targeted read through the **read-ops MCP service** (`artifact-access` in `mcp`
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
discussion has already gathered. Agents call tools, and every result becomes a file in
the discussion's workspace. Those files arrive in many formats: `kubectl` text tables,
JSON (`kubectl -o json`, search results), YAML, Markdown tables, CSV, and the sectioned
output older Kubemoot versions wrote. With
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

The key of one call
(`k8s-workloads/001-pods_list`, or the full `default/pilot/t1/k8s-workloads/001-pods_list`) is one table, and its `kubemoot.format`
header decides how it is read: `json` is read as JSON, `text` is detected as above. A
key with a `*` reads every stored call that matches as one table. `*` stands for any
characters except `/`. A short key (`k8s-workloads/*-pods_list`) is read inside the current thread
([Short keys](#short-keys)); a full key must name the namespace, crew and thread
(`ns/crew/thread/`), and the namespace must be the artifact-access pod's own
(`KUBEMOOT_NAMESPACE`). `k8s-workloads/*-pods_list` is every
`pods_list` call of one agent in the thread; `*/*-pods_list` is every
`pods_list` call of every agent. Calls of the same shape are stacked as the rows of one
table, a different shape is `name_2`, and a call that returned an error is left out and
named in a note. At most 100 objects match, and together they stay inside the artifact
size limit.

Every row of a table stacked from a `*` key carries the call that produced it, so rows
can be grouped, joined and counted per call (both engines):

| Column | Value |
|--------|-------|
| `_call` | The object key of the call (the full key). |
| `_args` | The call's arguments as the runtime recorded them (JSON text, cut at 1,000 characters); empty when the call has none. |
| `_arg_<name>` | An argument that is a plain value (text, number or flag) in every stacked call and **differs between them**, one column per such argument, at most 6 in name order, with the argument's name made of letters, digits and `_`. For nine `get_vm_rrd_data` calls that differ only in `vmid`, that is `_arg_vmid`. |

```
artifacts: {"rrd": "proxmox-qm/*-get_vm_rrd_data"}
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

An artifact written by an older Kubemoot version holds a tooler's raw output as
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
