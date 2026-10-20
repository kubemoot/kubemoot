# Kubemoot quickstart

From an empty Kubernetes cluster to a crew of agents answering a question, in one
script. It installs the pieces a Kubemoot deployment always has, on the smallest
footprint that still holds a real consensus discussion:

| Step | What | Why |
|---|---|---|
| 1 | NATS with JetStream (release `nats`, namespace `nats`) | every discussion is carried over NATS; the operator bootstraps its streams |
| 2 | Ollama on CPU | the quickstart engine: familiar, and it runs anywhere. An installation on accelerators prefers vLLM (see the installation guide) |
| 3 | The operator Helm chart with `operator-values.yaml`, which also declares a `ModelServer` for that Ollama and one model, `qwen2.5:1.5b`, in the cluster's model catalog | the dashboard is on with no route (the script prints the `kubectl port-forward` that opens it); admission webhooks, Grafana dashboards, the internal MCP servers, the NATS MCP server and the verify runner are off. The `ModelServer` is the endpoint the scheduler binds agents to at inference time |
| 4 | The operator marks the `ModelServer` ready and downloads the model onto it | the model is a catalog entry, not part of the crew; the script waits for the download |
| 5 | The `hello` crew: a coordinator, one Tooler, six `PromptModule`s, and no `Model`s | the smallest crew that deliberates; prompts are ADL rules you can `kubectl apply`. Its agents are scheduled from the catalog |
| 6 | One question through the crew's discussion gateway | the answer comes back as the discussion's synthesis |

## Run it

```bash
git checkout "$(git describe --tags --abbrev=0 --match 'operator-chart-v[0-9]*' --exclude '*-rc.*')"
kind create cluster --name kubemoot     # or any cluster your kubectl points at
./quickstart/quickstart.sh
```

The script installs the published chart of the release it is checked out at, read from the
git tag (`chart-version.sh`; no version is typed in a file), so the manifests next to it
and the chart agree. The `git checkout` above selects the latest release. If the quickstart
files differ from the latest release, as on a branch of `main` that changed them, the script
stops and says so; set `KUBEMOOT_CHART_VERSION` to a chart you know matches to override.

Needs `kubectl`, `helm`, and `python3`, and a node with 2 CPUs and 8 GB free. Expect
several minutes on a laptop: the model pull and the first discussion dominate. Release
images are amd64 only today, so an arm64 machine runs them under emulation or fails to
start them. The script waits on Kubernetes status
fields, never on fixed sleeps; the timeouts are safety nets.

Rerunning on an existing cluster is fine: every step is an upgrade or a re-apply, and the
script applies the chart's CRDs itself because `helm upgrade` never updates them.

CI runs this script on a fresh kind cluster against the exact images a release's chart pins,
with the quickstart files as they were at that release: when a release publishes, on demand,
and weekly against the latest release ([quickstart.yaml](../.github/workflows/quickstart.yaml)).
On every change to the quickstart, the chart, or the CRDs, CI also installs the checkout's own
CRDs on a kind cluster and has the API server validate the manifests the script applies and
the chart renders, so the quickstart and the chart on `main` cannot drift apart. To test
images you built yourself, load them into the cluster under the chart's registry prefix and
run with `KUBEMOOT_CHART=operator/chart/kubemoot-operator KUBEMOOT_IMAGE_TAG=<your tag>`.

## Then

```bash
kubectl get modelservers,models -n kubemoot
kubectl get agents,crew -n hello
kubectl edit promptmodule hello-facts-system -n hello   # change a rule, ask again
```

Ask another question: rerun with `QUESTION="..." EXPECT=""`, or use `kmctl conversation ask hello "..." -n hello`.

## Troubleshooting

- **The answer says no agent contributed.** The 1.5B model sometimes stands aside.
  Rerun the question; the script is idempotent.
- **The NATS pod stays `Pending`.** NATS keeps its stream on a PersistentVolumeClaim, so
  the cluster needs a default StorageClass. `kind` provides one.
- **Ollama restarts or is killed.** The node needs 8 GB free; on a smaller node the
  kernel kills Ollama while it loads the model.

## What it does not cover

RAG sources, MCP tool servers, GPU scheduling across servers, admission webhooks
(they need cert-manager), and publishing the dashboard on a hostname. Each has its own guide in `docs/`.
