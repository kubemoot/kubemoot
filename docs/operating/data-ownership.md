---
title: "Data Ownership"
description: "NATS is where Kubemoot's data lives and where you, the installer, own it: every store Kubemoot creates, what it can hold, how long it is kept, how to encrypt it, and the other places data appears."
weight: 5
---

NATS is where data ownership happens in Kubemoot. Every question a person asks, every answer, every tool result and every fact a crew learns passes through or rests in NATS JetStream. Kubemoot runs on your cluster, so you are the one who keeps people's data, or does not keep it. The Kubemoot project does not hold it, and Kubemoot sends none of it anywhere.

You decide:

- what is kept, and for how long;
- whether crews remember facts and keep earlier turns of a conversation at all;
- where the NATS disks live, whether they are encrypted, and where backups go;
- how you answer a person who asks for their data to be erased.

Kubemoot gives you the switches for each of these. It does not make the decisions for you, and it does not make an install compliant with any law or standard. It gives you what you need to meet your own obligations. This page lists what exists and how to change it. For how retention policies work, see [Data retention and limits](../data-retention/).

## Every store Kubemoot creates

The operator chart's NATS bootstrap job creates the stores below on install and brings an existing store to these limits on every upgrade. A KV bucket or object store that reaches its size limit refuses new writes until entries expire; a stream drops its oldest messages. "People's data" means questions, answers, tool output, or notes and facts derived from them. "System learning" means state about models, servers and crews, with no question text.

### Streams

| Stream | What it holds | Can it hold people's data | Default retention and size limit | Setting that changes it |
|--------|---------------|---------------------------|----------------------------------|-------------------------|
| `KUBEMOOT_DISCUSS` | Every message of every discussion: the question, each agent's finding and reason, the synthesis, the closing receipt | Yes | 24 hours, 5000 messages, 1 GiB | Chart values `nats.streams.discuss.maxAge`, `maxMessages`, `maxBytes` (`-1` for no limit); a crew may shorten it with `retention.other.window` |
| `KUBEMOOT_COORDINATOR` | Each coordinator's decision log per thread, including the question, the brief and the synthesis | Yes | 24 hours, 100000 messages, 512 per thread, 256 MiB | Chart value `coordinator.eventLog` (`false` leaves it empty; it also turns off coordinator resume); a crew may shorten it with `retention.other.window`; the limits are in the bootstrap job |
| `KUBEMOOT_REQUEST` | Questions waiting for a crew to pick them up, with the requester's time zone and local time | Yes | 24 hours, 1000 messages, 64 MiB | A crew may shorten it with `retention.other.window`; the limits are in the bootstrap job |
| `KUBEMOOT_CHAT` | Direct chat exchanges: the question cut to 500 characters, the answer cut to 2000 | Yes | 168 hours, 5000 messages, 64 MiB | The limits are in the bootstrap job |
| `KUBEMOOT_OPERATOR` | Onboarding progress of MCP servers | No, system learning | 168 hours, 10000 messages, 64 MiB | The limits are in the bootstrap job |
| `KUBEMOOT_CHRONICLE` | MCP server verdicts and trials | No, system learning | 720 hours, 10000 messages, 128 MiB | The limits are in the bootstrap job |
| `KUBEMOOT_QUALITY` | Reserved for quality events; nothing writes to it today | No | 720 hours, 10000 messages, 16 MiB | The limits are in the bootstrap job |

### KV buckets

| Bucket | What it holds | Can it hold people's data | Default retention and size limit | Setting that changes it |
|--------|---------------|---------------------------|----------------------------------|-------------------------|
| `kubemoot_conversations` | Earlier turns of each conversation (question and answer per turn), for follow-up questions | Yes | A crew keeps a conversation 30 days after its last turn; the bucket's own 5 year expiry is a backstop; 256 MiB | Crew `retention.conversations.window` and `retention.purgeUnused` |
| `kubemoot_crew_memory` | Facts a crew learned, written by a model from conversations and tool results | Yes, derived | A crew removes a fact not written or recalled for 90 days; at most 5000 facts per crew; the bucket has no expiry of its own; 64 MiB | Crew `retention.memory.purgeUnused` (`never` keeps facts), `retention.purgeUnused`, `memory.maxFacts`, `memory.enabled` (`false` turns memory off) |
| `kubemoot_scheduled` | Reminders and follow-ups an agent scheduled, with their text; a record is deleted when it fires | Yes | No record outlives 5 years; 16 MiB | The limits are in the bootstrap job |
| `kubemoot_scheduler_performance` | What the scheduler observed per model server, model and agent | No, system learning | No expiry; removed when its ModelServer is deleted; 64 MiB | The limits are in the bootstrap job |
| `kubemoot_crew_resumes` | What each crew's agents and skills offer | No, system learning | Deleted with its Crew; 64 MiB | The limits are in the bootstrap job |
| `kubemoot_server_state` | A ModelServer's probed capacity | No, system learning | Deleted with its ModelServer; 4 MiB | The limits are in the bootstrap job |
| `kubemoot_latency` | Evaluation latencies per server | No, system learning | No expiry; 1 MiB | The limits are in the bootstrap job |
| `kubemoot_model_demand` | Which models crews wait for or used recently | No, system learning | 6 hours; 8 MiB | The limits are in the bootstrap job |
| `kubemoot_server_tickets` | Per-call reservations of accelerator memory | No, system learning | 10 minutes; 4 MiB | The limits are in the bootstrap job |
| `kubemoot_agent_state` | Agent heartbeats | No | 300 seconds; 8 MiB | The limits are in the bootstrap job |
| `kubemoot_coordinator_leases` | Which coordinator owns a thread (created only with `coordinator.lease`) | No | 15 seconds; 1 MiB | The limits are in the bootstrap job |
| `kubemoot_placement_leader` | The placement service's leader lease | No | `placementService.leaseSeconds` (15 seconds); 1 MiB | Chart value `placementService.leaseSeconds` |

### Object stores

| Object store | What it holds | Can it hold people's data | Default retention and size limit | Setting that changes it |
|--------------|---------------|---------------------------|----------------------------------|-------------------------|
| `kubemoot_workspace` | Tool results and contributions agents spill during a thread, stored as the tool returned them | Yes: a tool can return anything | Deleted when the thread closes; 48 hours is the backstop; 2 GiB | Chart value `nats.stores.workspace.maxBytes`; the 48 hour age is in the bootstrap job |
| `kubemoot_fitness_artifacts` | Fitness result spreadsheets and per-iteration transcripts | Test questions written by the crew's author; people's questions only if a suite replays real ones | 168 hours; 1 GiB | Chart values `nats.stores.fitnessArtifacts.maxAge` and `maxBytes`; a suite's `spec.artifactRetention` applies only when the suite creates the store first |

The [dashboard's NATS page](../dashboard/#nats) shows each of these stores live, with its use against its limit and each crew's share. Its list of stores, what each holds and whether it can hold people's data is one file in the dashboard that a test holds to these tables, so a change here is made in that file too.

A limit marked "in the bootstrap job" lives in `templates/nats-streams-job.yaml` of the operator chart and has no chart value. To change one, edit that template and upgrade, or alter the stream or bucket with the `nats` command line after the install; the next upgrade restores the chart's limit.

## What keeps people's data by default, and for how long

With no settings changed:

- **Conversations: 30 days** after the last turn, kept per crew so a follow-up question can see the earlier turns.
- **Crew memory: facts unused for 90 days are removed.** Memory is on for every crew. A fact is text a model wrote from a conversation or a tool result, and it is not screened on write.
- **Discussion messages: 24 hours**, up to 5000 messages and 1 GiB, shared by all crews. The coordinator's log and queued questions follow the same 24 hours; direct chat keeps its shortened exchanges for 168 hours.
- **Tool results in the workspace:** until the thread closes, and never past 48 hours.
- **Reminders:** until they fire.

### Shorten, extend or turn off

- **One crew's conversations and memory.** Set `spec.retention` on the Crew. A shorter `retention.conversations.window` keeps less; `retention.memory.purgeUnused` shortens or, set to `never`, extends how long facts live; `memory.enabled: false` stops the crew learning and recalling facts. A crew's policy can shorten what a shared stream keeps and never extend it past the stream's own limit. See [the Crew reference](../../reference/crew/#retention).
- **The discussion stream.** Set `nats.streams.discuss.maxAge`, `maxMessages` and `maxBytes` in the chart values and upgrade. A shorter `maxAge` keeps people's questions for less time, and the stream still has to hold a thread's messages for as long as a restarted coordinator needs to resume it.
- **The coordinator's log.** Set `coordinator.eventLog: false` to stop writing the second copy of each thread. A coordinator then cannot resume a thread after a restart.
- **The workspace and fitness artifacts.** Set `nats.stores.workspace.maxBytes`, `nats.stores.fitnessArtifacts.maxAge` and `nats.stores.fitnessArtifacts.maxBytes`.
- **Everything else.** Edit the limit in the bootstrap job as described above.

To keep nothing of a person's conversation beyond the thread, set `memory.enabled: false` on the crew, give `retention.conversations.window` a short value, and shorten the streams as above. Each store still holds data for its own age before it expires.

## Taking a conversation away

A person can take away everything Kubemoot still holds about a conversation as one zip. The operator builds it on request from the stores, sends it as it builds it, and keeps no copy. It follows the same pattern as the crew memory export: the operator's report service answers `GET /conversation-export/{namespace}/{crew}?id=<conversationId>`, behind the same bearer token check (a TokenReview, then a SubjectAccessReview that the caller may `get` the Crew). The file is named `<crew>-conversation-<id>.zip`.

| Part | What it holds |
|---|---|
| `conversation.md` | The turns, questions and answers, readable, with times in the asker's zone when the request names one (`timeZone=America/New_York`), else UTC |
| `conversation.json` | The same, structured: ids, the crew, each time in the asker's zone and in UTC |
| `receipts/` | The receipt of each answer, as the stream held it, for the threads whose closing message is still stored |
| `artifacts/` | The workspace files of the conversation's threads that are still stored |
| `memory/` | The crew memory facts learned in this conversation, in the same OMPI files `kmctl memory export` writes, screened the same way: a fact that holds a credential is left out and listed by key or opaque id, never by its text. Facts from other conversations are never included, and neither is a fact whose thread was not recorded |
| `manifest.json` | What is included, what had expired or is not kept, what the screening left out, the time zone and the export's time |

Each part that is not in the zip is listed in the manifest with its status: `expired` (the workspace of a thread is deleted when the thread closes), `not-kept` (receipts the stream no longer holds), `empty` (no memory fact records this conversation as where it was learned) or `unavailable` (a store could not be read). A conversation whose history has expired answers 404.

Repeat `id` to export several conversations. The answer is then a zip of zips named `<crew>-conversations.zip`: one conversation zip per conversation, each exactly the zip a single export returns, plus a top-level `manifest.json` that lists every id asked for as `included`, `refused`, `expired` or `failed`. An id is never dropped silently. At most 50 ids are accepted, and an id is 1 to 128 letters, digits, `-` or `_`. Add `report=true` for the manifest as JSON without the zip.

Who may export: on a crew that verifies no one (no `spec.discussion.access`), conversations have no owners, so the rule is the access the report service already checks for the crew, `get` on the Crew in that namespace; whoever holds it can export any conversation of the crew, as they could already read any of them. On a crew that verifies callers, a conversation belongs to the caller who started it ([who owns a conversation](../../concepts/conversations-turns-and-threads/#who-owns-a-conversation)): only that caller may export it, by presenting their own Kubernetes token to the report service. An operator who may `get` `crews/conversations` may export another caller's conversation by giving a reason in the `Kubemoot-Access-Reason` header, and the export is audited. Anyone else is refused, and so is every export when the owner cannot be read. A conversation with no owner record belongs to no one, so only an operator may export it. The endpoint stays the same. The crew liaison's `export_conversation` presents the caller's own Kubernetes token when the liaison verifies tokens, so the owner check applies to that caller; a caller with no token of their own is served as the liaison's service account, which is not an owner and is refused on such a crew. The crew liaison's `export_conversation` tool calls the same endpoint with the liaison's own service account, and `kmctl conversation export` is planned to match `kmctl memory export`.

Memory facts are tied to a conversation by the thread each was learned in, which the agent runtime records on every fact it writes. A fact written before that was recorded is not attributable to any conversation, so it is never in a conversation export; take those with `kmctl memory export`.

## Answering an erasure request

A person's data has no account key in NATS; it is keyed by namespace, crew, and conversation or thread. The discussion gateway erases one conversation from every store that holds it: `DELETE /api/v1/discussions/{crew}/{conversationId}` ([the endpoint](../../concepts/conversations-turns-and-threads/#stopping-pausing-resuming-and-deleting-a-conversation)). The conversation's owner may call it; so may an operator who holds `delete` on `crews/conversations` (the ClusterRole `kubemoot-conversation-controller`), with a reason, and the use is audited. It finds the conversation's threads from the discussion stream and the history's turns, then removes:

| Store | What is removed |
|-------|-----------------|
| `kubemoot_conversations` | The history, `conv.<namespace>.<crew>.<id>`, and the owner record, `own.<namespace>.<crew>.<id>`, every revision of each |
| `KUBEMOOT_DISCUSS` | Every message of each thread, `kubemoot.discuss.<namespace>.<crew>.*.<threadId>` |
| `KUBEMOOT_COORDINATOR` | The coordinator's decision log of each thread, `kubemoot.coordinator.<namespace>.<crew>.<threadId>` |
| `kubemoot_workspace` (and the bucket it replaced) | Each thread's objects, `<namespace>/<crew>/<threadId>/...` |
| `kubemoot_scheduled` | Reminders and follow-ups scheduled from the conversation's threads, so none fires into a deleted conversation |
| `kubemoot_crew_memory` | The facts learned in the conversation's threads, as the conversation export attributes them |

The answer lists the threads found and how many entries each store gave up, so you can show the person what was removed. It also lists what it cannot remove for one conversation: a question still waiting in `KUBEMOOT_REQUEST` (it expires within its stream's age) and your logs and backups. A conversation still running is refused until it is stopped (or `force=true`), since the coordinator would write its answer again. Facts written before the agent runtime recorded the thread cannot be tied to a conversation; find them with `kmctl memory export <crew>`. A thread whose messages the stream has already expired is still found through the history, but one that is in neither is not.

When the person asks for everything about them across conversations, or the endpoint is not reachable, remove the stores by hand:

1. Find the crew and the time window of the person's questions.
2. Remove the conversation keys from `kubemoot_conversations` (`conv.<namespace>.<crew>.<id>`, and the owner record `own.<namespace>.<crew>.<id>`) and the facts from `kubemoot_crew_memory` (`<namespace>.<crew>.<topic>.<key>`) with the `nats kv` command line. `kmctl memory export <crew>` lists a crew's facts so you can find those that mention the person.
3. Messages in the streams cannot be edited. They expire with the stream's age. If the request cannot wait, purge the stream by subject (`nats stream purge KUBEMOOT_DISCUSS --subject 'kubemoot.discuss.<namespace>.<crew>.>'`), which removes that crew's messages for everyone.
4. Check your backups of the NATS volumes and your log backend: erasing from NATS does not erase from them.

Shorter retention is the stronger answer: data that has already expired needs no erasure.

## Encryption at rest

Kubemoot does not configure the NATS server. The chart creates streams and buckets on a NATS you installed, and the server's disk, encryption and backups are yours. Two ways to encrypt what NATS writes:

**Filesystem encryption.** Run NATS on encrypted volumes, or on nodes whose disks are encrypted (Talos supports disk encryption, as do the major clouds' block storage). NATS recommends this route. It covers everything on the volume and needs no change to the server.

**JetStream encryption.** The server encrypts every stream it stores when the `jetstream` block of its configuration carries a `key`, read from an environment variable so the key lives in a Kubernetes Secret and not in the config:

```
jetstream {
  store_dir: "/data/jetstream"
  cipher: chachapoly   # or aes
  key: $JS_KEY
}
```

Kubemoot's chart does not deploy NATS, it points at the NATS you installed (`nats.url`), so this is set on your NATS install and not on the Kubemoot chart. With the upstream NATS Helm chart, put the `jetstream` block in its configuration and take `JS_KEY` from a Secret through its environment values; follow that chart's documentation for where each goes.

It applies to the whole server, not to a single crew or stream, and it is separate from TLS. Behavior of the server:

- Enable it on a fresh store when you can. Turning it on over a store that already holds data makes the server read the existing streams and rewrite them encrypted. Take a backup first, and check that your stream still holds its messages afterward.
- The key is then required. Restarting without it, or with another key, leaves the streams unreadable (they are not found) until the right key returns. Keep the key where you keep the volume's backups.
- To rotate the key, restart once with the new key as `key` and the old one as `prev_key` (`prev_encryption_key` is the same option), then remove `prev_key` on the next restart.

Read the NATS documentation on [encryption at rest](https://docs.nats.io/running-a-nats-service/nats_admin/jetstream_admin/encryption_at_rest) before you enable it.

Whichever you choose, encrypt before the install keeps people's data, and treat the NATS volume's snapshots and backups as holding the same data.

## In transit

NATS traffic between the operator, agents, gateways and the dashboard is plain by default: the quickstart and chart point at `nats://nats.nats.svc.cluster.local:4222`, and every component connects without authentication. [SECURITY.md](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md) states this. Anyone who can reach the NATS port can read and write every subject and key, so namespaces separate names and do not protect data.

- **TLS and credentials.** Configure TLS and an account on your NATS server, then set `nats.url` to a `tls://` address and the chart values `nats.tls` (a CA bundle, and a client certificate for mutual TLS) and `nats.auth` (a creds file, a user and password, or a token), each from a Secret. Every Kubemoot component that dials NATS reads them, and the operator passes them to the agents, gateways and jobs it creates. The options, the components they cover and the ones they do not are in [Connecting to NATS with TLS and credentials](../nats-connection/).
- **Limit who reaches NATS** with a NetworkPolicy on the NATS namespace that admits only the Kubemoot namespaces and the crews' namespaces.
- **A service mesh or CNI-level encryption** also encrypts the pod-to-pod path, with no change to Kubemoot.
- **Per-crew accounts** that would give each crew its own view of NATS are a direction, not a shipped feature. One shared credential gives every crew the same view.

## Other places data appears

- **Logs** carry no content by default: no question, answer, prompt, tool argument or tool result, at any log level. `KUBEMOOT_LOG_CONTENT=true` on the agent runtime, the MCP gateway or the operator, with debug logging, writes content for a debugging session. Leave it unset on any cluster whose logs go to a shared backend, and your log backend then holds no question text. See [Observability](../observability/#2-logs-content-free-structured-stdout-aggregation-is-yours).
- **Traces.** Kubemoot does not emit traces today. The discussion span graph is rebuilt from NATS messages. If you add tracing around Kubemoot, check what your collector captures.
- **The dashboard** reads NATS and shows discussions, so whoever reaches it can read questions and answers. It is read-only and has no login: keep it off any public route, or put your own authentication in front of it. See [Dashboard](../dashboard/).
- **Fitness evidence** is in `kubemoot_fitness_artifacts` (above) and in the CrewFitness and CrewFitnessSuite status, which lives in Kubernetes, under the cluster's retention. A suite of test questions carries no people's data; a suite that replays real questions does.
- **The workspace** (`kubemoot_workspace`) holds tool results whole. A tool that returns a token or personal record leaves it there until the thread closes. Choose tools and their scopes with that in mind; see [Secrets and tools](../../concepts/secrets-and-tools/) and the [crew threat model](../../concepts/crew-threat-model/).
- **Crew memory export.** `kmctl memory export <crew>` writes a crew's facts out. The file is yours to protect and to delete.
- **Your model servers.** A model server you run, or a hosted one a crew reaches, sees every prompt sent to it. Its logging and retention are outside Kubemoot.

## Keeping question text out

To keep a person's question out of everything beyond the answer:

1. Leave `KUBEMOOT_LOG_CONTENT` unset.
2. Set `memory.enabled: false` and a short `retention.conversations.window` on crews that should not remember.
3. Set `coordinator.eventLog: false` if you accept that a coordinator cannot resume a thread, and shorten `nats.streams.discuss.maxAge`.
4. Keep the dashboard behind your own authentication.
5. Encrypt the NATS volume and its backups.

Question text still passes through NATS while a thread runs: a crew cannot answer a question it does not receive.

## Related

- [Dashboard](../dashboard/#the-operator-report-service) - the operator report service that serves the conversation export.
- [Data retention and limits](../data-retention/) - how retention policies and store limits work.
- [Installation](../../introduction/installation/) - installing the operator and NATS.
- [Observability](../observability/) - logs, metrics and traces.
- [Crew threat model](../../concepts/crew-threat-model/) - what a crew's tools and memory can leak.
- [SECURITY.md](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md) - reporting vulnerabilities and the security notes.
