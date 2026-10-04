<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/kubemoot-horizontal-white-text.png">
  <img src=".github/assets/kubemoot-horizontal-color.png" alt="Kubemoot" height="64">
</picture>

[![Latest release](https://img.shields.io/github/v/release/kubemoot/kubemoot?sort=semver&filter=operator-chart-v*)](https://github.com/kubemoot/kubemoot/releases/latest) [![Build status](https://github.com/kubemoot/kubemoot/actions/workflows/release.yaml/badge.svg?branch=main)](https://github.com/kubemoot/kubemoot/actions/workflows/release.yaml?query=branch%3Amain) [![License: Apache 2.0](https://img.shields.io/github/license/kubemoot/kubemoot)](https://github.com/kubemoot/kubemoot/blob/main/LICENSE) [![OpenSSF Scorecard](https://img.shields.io/ossf-scorecard/github.com/kubemoot/kubemoot?label=openssf%20scorecard)](https://scorecard.dev/viewer/?uri=github.com/kubemoot/kubemoot)

Kubemoot is a Kubernetes operator and runtime for **multi-agent AI consensus**. A
question is answered not by one model but by a **crew** of small, specialized agents
that deliberate over a message bus and settle by signal. The crew, its agents, their
prompts, and the models they use are all Kubernetes resources you declare and version
like any other workload.

## Why Kubemoot

Agent frameworks are good for prototyping a single agent; Kubernetes runs a container.
Neither operates a *multi-agent* AI workflow as infrastructure - that gap is Kubemoot's
lane. Instead of routing every question through one large hosted model, Kubemoot
composes capability **horizontally** from many small models, scheduled just in time
across the GPUs you have, and settles an answer by deliberation rather than by one
model asserting it. Crew quality is measured by executable fitness functions, not
eyeballed from transcripts.

See [Why Kubemoot](docs/introduction/why-kubemoot.md) for who it's for and how it
differs from a single-model agent or a hosted agent platform.

## The mental model

A **moot** is an assembly that reaches a decision by deliberation. In Kubemoot:

- A **coordinator** receives a question and convenes the Toolers whose expertise fits
  it.
- Each **Tooler** investigates with its domain MCP tools and contributes a finding
  carrying a consensus **signal** - `agree`, `concern`, `stand_aside`, `block`, or `failure`.
- **Analysts**, if the crew has them, reason over the Toolers' findings before
  synthesis.
- The coordinator watches the signals and, once the discussion settles,
  **synthesizes** the answer. No single model dictates it; consensus emerges from the
  crew.

A `MootArchetype` names a discussion's phase vocabulary and signals. The consensus
flow above is the one archetype that ships; its orchestration is fixed in the
runtime today (see the [Roadmap](docs/introduction/roadmap.md)).

## Building blocks (all Kubernetes resources)

| Resource | What it is |
|----------|------------|
| `Crew` | A group of agents that deliberate together, plus the discussion gateway clients talk to. |
| `Agent` | One participant: a coordinator, a Tooler, or an Analyst, with declared capabilities and tools - never a hardcoded model name. |
| `PromptModule` | An agent's behavior, written in ADL (the Architecture Definition Language) and composed by reference. |
| `MootArchetype` / `CrewSchedulingPolicy` | How a discussion is run: its phases, and the rules that decide who speaks in each. |
| `Model` / `ModelProvider` | A model, addressed by capability, and the GPU-backed endpoint that serves it. |
| `RAGSource` | A knowledge source that's indexed and made queryable for agents. |
| `MCPServer` / `MCPGateway` | Tools an agent can call, discovered through a gateway (Model Context Protocol). |
| `CrewFitness` / `CrewFitnessSuite` | Executable tests that score a crew against ground truth. |
| `KubemootConfig` | Cluster-scoped defaults: images, pull secrets, the vector store, the embedding model. |

Because these are CRDs, you author crews with `kubectl apply`, review changes with
`git diff`, and never recompile to change behavior. Full CRD reference:
[docs/reference/](docs/reference/).

## Quick start

A CPU trial you can run on a laptop, no GPU required: one script installs everything
on an empty cluster (`kind` works) and has a two-agent crew answer a question over
NATS, a CPU Ollama with a small model, the operator chart, a `ModelProvider`, and the
`hello` crew.

```bash
kind create cluster --name kubemoot
./quickstart/quickstart.sh
```

The same script runs in CI against every release's images
([quickstart.yaml](.github/workflows/quickstart.yaml)), so if it breaks, the build is
red before you see it. Details, options, and what each step creates:
[quickstart/README.md](quickstart/README.md). For the narrated walkthrough and the CPU
trial's model/agent/latency profile, see the
[Quickstart doc](docs/introduction/quickstart.md); for installing the operator on your
own cluster with a GPU-backed model provider, see
[Installation](docs/introduction/installation.md).

## Documentation

- [Why Kubemoot](docs/introduction/why-kubemoot.md), [Overview](docs/introduction/kubemoot-brief.md),
  and [The Orchestration Gap](docs/introduction/orchestration-gap.md) - what Kubemoot
  is and where it fits.
- [Concepts](docs/concepts/) - the consensus model, crews and agents, ADL, scheduling,
  and the conversation/turn/discussion/thread vocabulary.
- [User guides](docs/user-guides/) - build a crew, write agents in ADL, define fitness
  functions, onboard MCP tools, use `kmctl`.
- [Integrations](docs/integrations/) - a crew as one agent for Claude Code, Claude
  Desktop, or any MCP client, through the crew liaison.
- [Reference](docs/reference/) - full CRD specs (`Crew`, `Agent`, `KubemootConfig`,
  `MCPServer`, `MCPGateway`, `RAGSource`, models) and the `kmctl` CLI.
- [Architecture](docs/architecture/) - the scheduler, the Tooler/Analyst split,
  scheduling of reminders and follow-ups, and consensus internals.
- [Questions](docs/introduction/faq.md) and [Roadmap](docs/introduction/roadmap.md):
  the short answers, and the major directions.
- [Community](https://kubemoot.org/docs/community/) - contributing, governance, code of conduct, security, and releases.

## Ecosystem

Kubemoot is the operator at the center of a small ecosystem: [Homelab
Pilot](docs/ecosystem/pilot.md) is the reference crew, [`kmctl`](docs/ecosystem/kmctl.md) is the CLI,
[CrewForge](https://kubemoot.org/docs/ecosystem/crewforge/) is the VS Code extension for browsing crews and
talking to them from the editor, and [crews](docs/ecosystem/crews.md) covers other
packaged crews. See [docs/ecosystem/](docs/ecosystem/) for the full picture.

## Development and community

Kubemoot is an independent open-source project under the Apache License 2.0. Contributing, support, governance, the code of conduct, security reporting, and releases are documented in one place: the [Community section of kubemoot.org](https://kubemoot.org/docs/community/). Ask questions and share ideas in [GitHub Discussions](https://github.com/orgs/kubemoot/discussions). Write to moot@kubemoot.org for anything else. Use security@kubemoot.org only to report a vulnerability, privately.

The [Development Guide](docs/community/development.md) covers the repository layout, prerequisites, and how to build and test each component. Run `./quickstart/quickstart.sh` on an empty `kind` cluster as a smoke test before opening a pull request. Security details, including the platform's known limitations, are in [SECURITY.md](SECURITY.md).

## License

Apache License, Version 2.0. See [LICENSE](LICENSE).
