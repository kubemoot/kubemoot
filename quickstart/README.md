# Kubemoot quickstart

From an empty Kubernetes cluster to a crew of agents answering a question, in one
script. It installs the pieces a Kubemoot deployment always has, on the smallest
footprint that still holds a real consensus discussion:

| Step | What | Why |
|---|---|---|
| 1 | NATS with JetStream (release `nats`, namespace `nats`) | every discussion is carried over NATS; the operator bootstraps its streams |
| 2 | Ollama on CPU, pulling `qwen2.5:1.5b` | a small model that runs anywhere; swap for a GPU-backed Ollama later |
| 3 | The operator Helm chart with `operator-values.yaml` | the dashboard is on with no route (the script prints the `kubectl port-forward` that opens it); admission webhooks, Grafana dashboards, the internal MCP servers, the NATS MCP server and the verify runner are off |
| 4 | A `ModelProvider` pointing at that Ollama | the endpoint the scheduler binds agents to at inference time |
| 5 | The `hello` crew: a coordinator, one Tooler, one `Model`, six `PromptModule`s | the smallest crew that deliberates; prompts are ADL rules you can `kubectl apply` |
| 6 | One question through the crew's discussion gateway | the answer comes back as the discussion's synthesis |

## Run it

```bash
kind create cluster --name kubemoot     # or any cluster your kubectl points at
./quickstart/quickstart.sh
```

Needs `kubectl`, `helm`, and `python3`, and a node with 2 CPUs and 8 GB free. Expect
several minutes on a laptop: the model pull and the first discussion dominate. Release
images are amd64 only today, so an arm64 machine runs them under emulation or fails to
start them. The script waits on Kubernetes status
fields, never on fixed sleeps; the timeouts are safety nets.

Rerunning on an existing cluster is fine: every step is an upgrade or a re-apply, and the
script applies the chart's CRDs itself because `helm upgrade` never updates them.

CI runs this script on a fresh kind cluster every time a release publishes a new
operator, `agent-runtime`, or `discussion-gateway` image, against the exact images the
chart pins ([quickstart.yaml](../.github/workflows/quickstart.yaml)). To test images you
built yourself, load them into the cluster under the chart's registry prefix and run with
`KUBEMOOT_CHART=operator/chart/kubemoot-operator KUBEMOOT_IMAGE_TAG=<your tag>`.

## Then

```bash
kubectl get agents,models,crew -n hello
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

RAG sources, MCP tool servers, GPU scheduling across providers, admission webhooks
(they need cert-manager), and publishing the dashboard on a hostname. Each has its own guide in `docs/`.
