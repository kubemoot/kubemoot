---
title: "The Table and the Harnesses"
weight: 22
description: "A crew as a harness made of harnesses: what Kubemoot builds at the table, what it adopts, and what it convenes."
---

A harness is everything around a model: the guides that steer an agent before it acts
and the sensors that let it correct itself afterwards, in the sense of Martin Fowler's
[Harness Engineering](https://martinfowler.com/articles/harness-engineering.html).

A crew is a harness made of harnesses. Each agent has its own loop: its prompt, its
tools, its retries, its memory. The moot is the harness one level up, and its guides
and sensors act between agents:

- **Guides at the table:** ADL rules in `PromptModule` resources, the consensus
  archetype that shapes a discussion, and skills the coordinator injects on demand.
- **Sensors at the table:** the signals agents publish (agree, concern, block, stand
  aside, failure), gap detection when a discussion lacks a tool or a specialist, and
  fitness functions that score a crew against ground truth.

## What Kubemoot builds, adopts, and convenes

That layering decides what belongs in Kubemoot and what does not.

- **Native to the table, built here.** What only a crew can do, or what a moot needs
  across all its agents: declared archetypes, budgets and cost accounting across a
  moot, approval gates for actions, durable crew memory, an evidence and audit trail,
  and evaluation.
- **Native to the runtime, adopted.** The open conventions small local models need to
  be useful, in the spirit of the tools people already use: skills, memory, and code
  as action in a sandbox. Adopted as conventions, never as a dependency on a product.
- **Convened, not built.** Everything else in the per-agent harness race. The moot
  talks to an agent through a thin contract (the question and the role in; a signal,
  a rationale, and evidence out), so an external harness such as Claude Code, Codex,
  or OpenHands can sit at the table as one voice, with the crew's own tools, under
  the user's own license. Kubemoot does not compete with those harnesses; it gives
  them a table.

The same contract works in the other direction. An agent harness can use a whole crew
as one of its tools: Claude Code, for example, asks a crew a question through the
[crew liaison](../../integrations/crew-liaison/) and receives the crew's answer as a
ticket result. See [Claude Code](../../integrations/claude-code/).

Which of these exist today and which are directions is tracked on the
[Roadmap](../../introduction/roadmap/#harness-capabilities-at-the-table).
