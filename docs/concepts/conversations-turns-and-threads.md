---
title: "Conversations, Turns & Threads"
weight: 15
description: "The units of interaction: a durable conversation, the turns within it, and the thread each turn's discussion runs on."
---

When you ask a crew a question, four words describe what happens, and they nest.
The docs and the dashboard use them with specific meanings, so this page defines
each one and how it relates to the others.

## The layers

```
  Conversation            durable session with a crew (conversationId)
    |
    +-- Turn              one question and the answer settled on it
    |     |
    |     +-- Thread      where that turn's discussion runs (threadId)
    |           |
    |           +-- Discussion   the deliberation on the thread (phases + signals)
    |
    +-- Turn ...          later questions, each on its own thread
```

A conversation holds many turns; each turn runs on its own thread; the discussion
is what happens on that thread. The two innermost words name the same layer from
different angles, which is explained below.

## Conversation

A **conversation** is a durable session between you and a crew, identified by a
`conversationId` (a UUID). Supply one to continue an existing conversation; omit it
and the crew generates a fresh one and returns it. The crew keeps a bounded history
of the conversation's recent turns as working context for follow-up questions, and
persists it so the session survives a coordinator restart. Every answer carries the
`conversationId` back to you.

## Turn

A **turn** is one exchange within a conversation: your question and the answer the
crew settled on. The conversation records each turn together with the `threadId` that
produced it and a timestamp, and keeps a bounded window of the most recent turns
(older ones age out).

A turn is the conversation-level unit. It is not the same as the iterations a single
agent makes while calling tools and mulling on its way to a contribution - those
happen *inside* one turn's discussion and are not turns themselves.

## Thread

Each turn runs on its own **thread**, identified by a fresh `threadId` (a new UUID per
question). The thread is the channel the deliberation runs on: agents exchange
messages on NATS subjects of the form `kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>`,
bracketed by a `thread_start` and a `thread_close`.

Because a thread is created per question, `conversationId` and `threadId` are always
different values: one conversation spans many threads, one per turn. A concluded
thread can be reopened for a further round of deliberation rather than being treated
as immutable.

## Discussion

**Discussion** and **thread** name the same layer from two angles. The *thread* is the
channel and the identifier you see in streaming events, traces, and the dashboard; the
*discussion* is the deliberation that runs on it - the coordinator convening the crew,
agents contributing findings and consensus signals, and the answer being synthesized.
There is no separate discussion object; a thread advancing through its phases *is* the
discussion.

How that deliberation is organized - its phases, its signal vocabulary, how it
settles, and the fact that the structure is a pluggable archetype - is covered in
[The Moot](../consensus-model/).

## In the API and the dashboard

The crew's discussion endpoint returns a `conversationId` with every answer; echo it
back on your next message to continue the same conversation. Stream the deliberation
with `GET /api/v1/discussions/{crew}/{conversationId}/stream` and the thread is surfaced directly (a `thread_found` event,
followed by per-phase events) as it unfolds. See [Quickstart](../../introduction/quickstart/)
for the request and response shapes, and [Dashboard](../../operating/dashboard/) for
the live `/discussions` view that renders each thread.

The stream survives restarts. The POST reply also carries `requestedAt`; pass it as
`?since=` on the stream so any gateway replica can tell this turn's thread from an
earlier turn's. Each thread event carries an SSE `id`; a client whose connection drops
reconnects with that id as the `Last-Event-ID` header (or the `lastEventId` query
parameter) and receives only what it missed. If the coordinator is replaced while it
works on a question, its successor starts the question again on a new thread, and the
stream announces it with a second `thread_found`; the earlier thread will not close.

## Related

- [The Moot - Consensus Model](../consensus-model/) - how a discussion is organized and settles.
- [Crews & Agents](../crews-and-agents/) - the team a conversation talks to.
- [Signals & Protocol](../signals-and-protocol/) - the messages exchanged on a thread.
- [Discussion Artifact Store](../discussion-artifact-store/) - how large tool outputs are passed by reference within a discussion.
- [Quickstart](../../introduction/quickstart/) - open a conversation and ask a question.
