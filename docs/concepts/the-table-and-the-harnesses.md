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
  across all its agents. Today that is the consensus archetype, the scheduling of
  models across accelerators, ADL governance as resources, crew memory, and evaluation
  through fitness functions. Budgets and cost accounting across a moot,
  approval gates for actions, and an evidence and audit trail are directions, not
  shipped features.
- **Everything else, adopted behind a seam.** Every layer outside the moot is a
  published seam with an open-source project or standard behind it, and where
  practical a second project that fits the same seam, so you choose and Kubemoot is
  never tied to one. A component is adopted when it passes a test: a permissive
  license, healthy governance (a foundation, a SIG, or several active maintainers), a
  standard interface, support for local models, an exit that is an adapter and not a
  rewrite, and fitness at least as good as the alternative on the crew's fitness
  suite. Kubemoot builds a capability only when nothing passes, and puts what it
  builds behind the same seam.
- **The native agent runtime is the minimal reference agent.** It stays small and does
  not grow its own harness (planning, compaction, code execution, approvals, session
  state). A capability beyond it arrives through an adopted harness that sits at the
  table.
- **Conventions in the runtime, adopted.** The open conventions small local models need to
  be useful, in the spirit of the tools people already use: skills, memory, and code as
  action in a sandbox. They are adopted as conventions, never as a dependency on a product,
  and each part passes the adoption test above before more of it is built. Skills are
  instruction-only today; bundled files and runnable scripts are directions.
- **Convened, not built.** The per-agent harness race. The moot
  talks to an agent through a thin contract (the question and the role in; a signal,
  a rationale, and evidence out), so an external harness such as Claude Code, Codex,
  or OpenHands can sit at the table as one voice, with the crew's own tools, under
  the user's own license. Kubemoot does not compete with those harnesses; it gives
  them a table.

The seams beyond the agent harness, each with what sits behind it:

| Layer | Seam | Behind it |
|---|---|---|
| Agent harness | The turn contract and the [Moot Mesh](../../architecture/moot-mesh/) sidecar | The native agent runtime; goose and other harnesses |
| Where agents run | The pod spec | Deployments today; Agent Sandbox is a candidate on the [Roadmap](../../introduction/roadmap/) |
| Model servers | The engine-state contract (aligned with the Model Server Protocol), one adapter per engine | vLLM and Ollama; more are a direction |
| Tools | MCP | Any MCP server |

The same contract works in the other direction. An agent harness can use a whole crew
as one of its tools: Claude Code, for example, asks a crew a question through the
[crew liaison](../../integrations/crew-liaison/) and receives the crew's answer as a
ticket result. See [Claude Code](../../integrations/claude-code/).

Which of these exist today and which are directions is tracked on the
[Roadmap](../../introduction/roadmap/#harness-capabilities-at-the-table).
