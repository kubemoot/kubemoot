---
title: "Development Guide"
weight: 15
description: "Set up a development environment: repository layout, prerequisites, and how to build and test each component."
aliases:
  - /docs/contributing/contributor-guide/
---

This guide covers the repository layout and how to build and test each component. The
process for issues, ideas, and pull requests is on the [Contributing](../contributing/)
page; the conventions a change is expected to follow are summarized at the end of this
one.

## Repositories

| Repository | What it holds |
|------------|---------------|
| [kubemoot](https://github.com/kubemoot/kubemoot) | The operator, agent runtime, dashboard, MCP components, and the component docs |
| [crews](https://github.com/kubemoot/crews) | Packaged crews as Helm charts |
| [kmctl](https://github.com/kubemoot/kmctl) | The command-line tool |
| [vscode-crewforge](https://github.com/kubemoot/vscode-crewforge) | CrewForge for VS Code |
| [kubemoot-docs](https://github.com/kubemoot/kubemoot-docs) | This site: the Hugo and Docsy shell and the cross-cutting chapters |

## Layout of the kubemoot repository

| Component | Path | Language / stack |
|-----------|------|------------------|
| Operator | `operator/` | Go, controller-runtime, kubebuilder CRDs |
| Agent runtime | `agent-runtime/` | Java / Quarkus + LangChain4j (GraalVM native) |
| Dashboard | `dashboard/` | SvelteKit |
| MCP bridge, scheduling and tool servers | `mcp-bridge/`, `scheduling-mcp/`, `artifact-access/`, `code-sandbox/` | Go |
| MCP gateway | `mcp-gateway/` | Java |
| Discussion gateway, crew liaison, fitness runner | `discussion-gateway/`, `crew-liaison/`, `fitness-runner/` | Go |
| Indexer | `indexer/` | Java |
| Query service | `query-service/` | Python |
| Quickstart | `quickstart/` | Shell |
| Integration tests | `k8s/tests/` | Shell |

## Prerequisites

- Go 1.27+ (operator, Go components)
- Java 25+ with Gradle (agent runtime, indexer, MCP gateway)
- Node.js 26+ (dashboard)
- `kubectl` and `helm` (v3.8+, for OCI charts)
- Access to a Kubernetes cluster; `kind` works for the quickstart

## Build and test

Every change ships with tests. Run the suite for the component you touched.

**Operator** (`operator/`):

```bash
cd operator
make generate manifests   # generate code and CRD manifests
make build                # build the operator
make test                 # unit tests (Go envtest)
make lint                 # golangci-lint
```

To run the operator against your current kubeconfig context:

```bash
make install   # install the CRDs
make run       # run the operator locally
```

After changing the API types or RBAC markers, run `make manifests generate` (it also
copies the CRDs and the manager role into the chart) and commit the generated files.
CI regenerates them and fails when they differ from what is committed.

**Agent runtime and other Java components** (`agent-runtime/`, `indexer/`,
`mcp-gateway/`):

```bash
./gradlew build   # tests plus Checkstyle (cyclomatic complexity <= 10)
```

Checkstyle reads the shared ruleset in `config/checkstyle/checkstyle.xml`.
Use plain JUnit 5, not `@QuarkusTest`, so the suite does not require a running Ollama.

**Go components** (`mcp-bridge/`, `scheduling-mcp/`, `artifact-access/`, `code-sandbox/`,
`discussion-gateway/`, `crew-liaison/`, `fitness-runner/`):

```bash
go test ./...
../.github/scripts/golangci-lint.sh   # golangci-lint
```

Every Go module, the operator included, lints with the shared `.golangci.yml` at the
repository root (cyclomatic complexity <= 10, duplication, and the rest) at the
`GOLANGCI_LINT_VERSION` pinned in `operator/Makefile`.

**Query service** (`query-service/`):

```bash
pip install --require-hashes -r requirements.txt -r requirements-lint.txt
ruff check .
python -m unittest discover -s . -p 'test_*.py'
```

**Dashboard** (`dashboard/`):

```bash
npm install
npm test          # vitest
npm run lint      # eslint
npm run check     # svelte-check
```

**Quickstart smoke test.** For any change to the kubemoot repo, run the quickstart on an
empty `kind` cluster. It installs the operator, a small CPU model, and a two-agent
crew, asks the crew a question, and checks that an answer comes back. CI runs the same
script against every release's images.

```bash
kind create cluster --name kubemoot
./quickstart/quickstart.sh
```

**Integration tests** run against a cluster with Kubemoot installed:

```bash
cd k8s/tests
./test-smoke.sh     # fast subset
./test-all.sh       # full suite
```

See `k8s/tests/README.md` for what each test covers.

## Other repositories

**kmctl** requires Go 1.27+:

```bash
make build      # build the ./kmctl binary
make test       # unit tests with coverage
make test-race  # unit tests with the race detector (needs CGO)
make lint       # golangci-lint
make hooks      # install the pre-commit gate (gofmt and lint)
```

**crews.** Each subdirectory is an independently versioned Helm chart. To add a crew,
create `<your-crew>/` with `Chart.yaml`, `values.yaml`, `templates/`, and a `fitness/`
directory of scenarios, then add a release workflow modeled on an existing one under
`.github/workflows/`. A merge to main builds a release candidate and pushes it to the maintainers' registry; the Promote Release workflow publishes the final chart to GHCR and creates the GitHub Release.

**Documentation.** Each component's reference docs live in that component's `docs/`
directory. The site is built from the `kubemoot-docs` repo with Hugo; `npm install`
followed by `hugo server` previews it.

## Conventions

A few project rules a reviewer looks for:

- **Conventional commits.** Commit prefixes drive semantic versioning. Versions come
  from git tags via the pipeline; never hand-edit a version in a manifest. See
  [Releases](../releases/).
- **Prompts in ADL.** All agent prompt text lives in `PromptModule` resources written
  in ADL (the Architecture Definition Language), never inline in an Agent spec. See
  [Write Agents & ADL](../../user-guides/write-agents-and-adl/).
- **Code quality.** Keep functions at cyclomatic complexity 10 or less; refactor rather
  than adding a case to an already complex function.
- **Lifecycle belongs to the operator.** Cleanup, garbage collection, and namespace
  management are handled in the operator with finalizers and owner references, not in
  client-side tools.

## Naming

| Item | Convention | Example |
|------|------------|---------|
| API group | `kubemoot.ai` | `apiVersion: kubemoot.ai/v1alpha1` |
| CRD kinds | Singular, CamelCase | `ModelProvider`, `Agent`, `CrewSchedulingPolicy` |
| Short names | Lowercase abbreviations, chosen when the CRD is designed | `mdlp`, `mdl`, `emb`, `mcp`, `mcpgw`, `rag`, `csp`, `pm` |
| Brand names in prose and UI | CrewForge, Homelab Pilot, Kubemoot | "Install Kubemoot" |
| Names in code and config | Kebab-case identifiers | `vscode-crewforge`, `homelab-pilot`, `kubemoot` |

Using the same kebab-case identifiers in Helm charts, manifests, and the dashboard keeps
copy-paste across layers working, and short names keep `kubectl get` output scannable.

## Project direction

Where the project is headed is sketched in the [Roadmap](../../introduction/roadmap/).
