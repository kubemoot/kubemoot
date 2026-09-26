---
title: "Contributor Guide"
weight: 10
description: "Set up a development environment and make your first contribution."
---

Kubemoot is an independent open-source project and contributions are welcome. This
guide covers the repository layout, how to build and test each component, and the
conventions a change is expected to follow.

## Repository layout

Kubemoot is a multi-component project:

| Component | Path | Language / stack |
|-----------|------|------------------|
| Operator | `operator/` | Go, controller-runtime, kubebuilder CRDs |
| Agent runtime | `agent-runtime/` | Java / Quarkus + LangChain4j (GraalVM native) |
| Dashboard | `dashboard/` | SvelteKit |
| MCP components | `mcp-bridge/`, `mcp-gateway/` | Go / Java |
| Indexer | `indexer/` | Java |

## Build and test

Every change ships with tests - tests are part of the change, not a follow-up. Run the
suite for the component you touched:

- **Operator** - `make test` (Go envtest).
- **Agent runtime** - `./gradlew test`. Use plain JUnit 5, not `@QuarkusTest`, so the
  suite doesn't require a running Ollama.

After changing CRDs in the operator, run `make manifests generate`, copy the generated
CRDs to the chart, sync the RBAC into the chart templates, and commit the generated
`zz_generated.deepcopy.go`.

## Conventions

A few project rules a reviewer will look for:

- **Conventional commits.** Commit prefixes drive semantic versioning: `feat:` (minor),
  `fix:` (patch), `feat!:` / `fix!:` / `BREAKING CHANGE` (major), and
  `chore:` / `docs:` / `refactor:` (patch). Versions come from git tags via the
  pipeline - never hand-edit a version in a manifest.
- **Prompts in ADL.** All agent prompt text lives in `PromptModule` CRs written in
  ADL (the Architecture Definition Language), never inline in an Agent spec. See
  [Write Agents & ADL](../../user-guides/write-agents-and-adl/).
- **Code quality gates.** Keep functions under cyclomatic complexity 10; refactor
  rather than adding a case to an already-complex function.
- **Lifecycle belongs to the operator.** Cleanup, garbage collection, and namespace
  management are handled in the operator via finalizers and owner refs - not in
  client-side tools.

## Making a change

1. Open or claim an issue describing the change.
2. Branch, implement, and include tests.
3. Run the component's test suite and confirm it passes.
4. Use a conventional-commit message.
5. Open a pull request describing what changed and why.

## Project direction

Kubemoot is an independent open-source project. Where it's headed is sketched in the
[Roadmap](../../introduction/roadmap/).
