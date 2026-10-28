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

Counting, filtering, grouping and totalling are exact work that a model does badly by
eye. The optional `workspace_query` tool of the same read-ops service runs one
read-only SQL `SELECT` over the artifacts an agent names, so the database does the
arithmetic and the agent reads a short table of rows.

The tool is off by default. The operator chart value `artifacts.queryEngine` chooses the
engine: `none` (the default) offers no `workspace_query` and the service behaves as
before; `sqlite` (the recommended engine) and `duckdb` offer it. See
[Query engines](#query-engines) for why and when. The engine sits behind a seam: the
artifacts stay in the NATS object store, artifact-access materializes them as files
under `/artifacts`, and the engine reads those files. Another engine can replace these
through the chart value without a change to crews or prompts. The engines speak their
own SQL; the tool description tells the model which one it has.

| Input | Meaning |
|-------|---------|
| `artifacts` | A map from a table name you choose to an artifact key. Up to 8 per query. A name is letters, digits and underscores. |
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

How each artifact becomes a table, decided from its content (object keys carry no file
extension):

| Content | Table |
|---------|-------|
| A JSON list of objects | One row per object, one column per key. |
| A JSON object with an `items` list (`kubectl get -o json`) | One row per item. |
| A `kubectl get` table | One row per line, columns named by the header. |
| YAML (several documents become a list) | Read as JSON. |
| A Markdown pipe table | One row per table row. |
| CSV with a header row | One row per line. |
| Anything else | One row per line, columns `line_no` and `line`. |

A column whose every value is a number is stored as numbers, so `sum`, `avg` and
`ORDER BY` work on it (`RESTARTS` of a kubectl table, a Markdown count column).
A nested object or list in JSON or YAML is read per engine. In SQLite it is a text
column holding JSON: `status->>'phase'` reads a field, `json_array_length(tags)` counts
a list. In DuckDB it is a struct: `status.phase`. A JSON `true` or `false` is `1` or `0`
in SQLite and a boolean in DuckDB.

Markdown beyond pipe tables is read as lines of text. DuckDB's Markdown reader is a
community extension that downloads at run time, which a server with no network cannot do.

Limits and safety:

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
| `ARTIFACT_QUERY_ENGINE` | `none` | `none`, `sqlite` or `duckdb`. The operator sets it from the KubemootConfig `spec.artifacts.queryEngine` unless the MCPServer sets it. |
| `ARTIFACT_DUCKDB_BIN` | `duckdb` | The `duckdb` program. The artifact-access image that serves `duckdb` must carry it; when it is missing the service logs the reason and offers no query tool. |
| `ARTIFACT_QUERY_TIMEOUT` | `10s` | Time limit per query. |
| `ARTIFACT_QUERY_MAX_ROWS` | `100` | Rows per result. |
| `ARTIFACT_QUERY_MEMORY` | `256MB` | DuckDB memory limit. For SQLite, the growth of the process that stops a query, and a quarter of it is the ceiling on any one value. |
| `ARTIFACT_QUERY_MAX_ARTIFACT_BYTES` | `33554432` | Largest artifact a query may read. |
| `ARTIFACT_DIR` | `/artifacts` | Where artifacts are materialized; the server needs a writable volume there. |

An artifact the sandbox sidecar has not materialized yet is fetched from the object store
on first use.

#### Query engines

`sqlite` is the recommended engine and `duckdb` is the option for large data.

**Why SQLite is the recommended engine.**

- It runs inside the artifact-access process. There is no extra image, no extra process
  to start and no program to install, and it needs a few MB of memory.
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
