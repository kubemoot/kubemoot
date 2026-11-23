---
title: LibreChat
weight: 70
description: Install LibreChat, connect it to a crew through the crew liaison, and use the crew from an agent.
---

LibreChat is an open-source chat app that speaks MCP. It reaches a crew only through an
agent, so expect more setup than in the apps listed before it. This page takes you from nothing to a chat in which LibreChat's model asks a
crew a question: install LibreChat, point it at your accelerators and at the crew liaison,
then build an agent that uses the crew as its tool.

Two models take part, and they have different jobs:

- **LibreChat's model** is the one you chat with. It reads your question and decides when
  to call a tool. It runs on a model server you give LibreChat as a custom endpoint, for
  example vLLM's OpenAI-compatible API.
- **The crew** is the tool. When the model calls it, the crew deliberates with its own
  models and returns one answer.

For the tools and the ticket flow see [The Crew Liaison](../crew-liaison/); for what the
liaison expects in a request see [Signing in](../signing-in/).

## Install

LibreChat runs from its own Helm chart, `oci://ghcr.io/librechat-ai/librechat-chart/librechat`,
with an external MongoDB. Use the chart and LibreChat image versions that its release page
lists together, and pin the image by digest.

### MongoDB

LibreChat keeps accounts, agents and chats in MongoDB. Run one instance in the LibreChat
namespace, with a persistent volume and a Secret that holds its root password. Two points
matter:

- **MongoDB needs a processor with AVX.** On a cluster whose virtual machines present a
  processor model without it, MongoDB exits at start. Pin its pod to nodes that report AVX,
  here through node-feature-discovery:

  ```yaml
  nodeSelector:
    feature.node.kubernetes.io/cpu-cpuid.AVX: "true"
  ```

- **Let only LibreChat reach it.** A NetworkPolicy that admits port 27017 from the
  LibreChat pods and nothing else is enough.

### Secrets

Create the namespace and one Secret, `librechat-credentials-env` (the name the chart reads
by default), before installing. Generate every value yourself and keep it out of Git:

| Key | Value |
|---|---|
| `CREDS_KEY` | 32 random bytes as 64 hex characters (`openssl rand -hex 32`) |
| `CREDS_IV` | 16 random bytes as 32 hex characters (`openssl rand -hex 16`) |
| `JWT_SECRET` | a long random string |
| `JWT_REFRESH_SECRET` | a different long random string |
| `MONGO_URI` | `mongodb://root:<password>@<mongodb service>.<namespace>.svc.cluster.local:27017/LibreChat?authSource=admin` |

### Chart values

Turn off the chart's bundled stores (you run MongoDB yourself) and the optional ones.
Meilisearch gives conversation search and the RAG API gives file question answering; neither
is needed for crews.

```yaml
updateStrategy:
  type: Recreate
  rollingUpdate: null
mongodb:
  enabled: false
meilisearch:
  enabled: false
redis:
  enabled: false
librechat-rag-api:
  enabled: false
librechat:
  imageVolume:
    enabled: true
    size: 10Gi
```

- **Use the `Recreate` strategy.** The uploads volume is ReadWriteOnce, so a rolling update
  waits forever: the new pod needs a volume the old pod holds. `Recreate` stops the old pod
  first, which leaves a short gap with one replica. Set `rollingUpdate: null` as well, or the
  API server refuses a Deployment that carries both.
- **Keep the pod unprivileged** (`runAsNonRoot`, `allowPrivilegeEscalation: false`, all
  capabilities dropped) and give it a memory limit of about 2 GiB.

### Accounts

LibreChat uses local accounts. The first account registered becomes the admin, so register
yours first, then turn registration off so no one else can create one:

```yaml
librechat:
  configEnv:
    ALLOW_REGISTRATION: "false"   # "true" only until your own account exists
    ALLOW_EMAIL_LOGIN: "true"
    ALLOW_SOCIAL_LOGIN: "false"
    ALLOW_PASSWORD_RESET: "false"
```

### Reaching it

Without a route, use a port-forward and open `http://localhost:3080`:

```bash
kubectl port-forward -n librechat svc/librechat 3080:3080
```

Set `DOMAIN_CLIENT` and `DOMAIN_SERVER` to the address you open (`http://localhost:3080`
for the port-forward, or the public name once you add a route). The sign-in for a crew
returns to this address.

## Configure

Two things go in the configuration: the models LibreChat chats with, and the crew liaison.

### Local model endpoints

Declare each model server as a custom endpoint in `librechat.yaml`, pointing at its
OpenAI-compatible address (the base URL ends in `/v1`):

```yaml
endpoints:
  custom:
    - name: "Cluster model"
      apiKey: "not-needed"
      baseURL: "http://<model server>.<namespace>.svc.cluster.local:8000/v1"
      models:
        default: ["<model name>"]
        fetch: false
      titleConvo: true
      titleModel: "current_model"
      modelDisplayLabel: "<model name>"
```

Pick a model with reliable tool calling. A model that answers instead of calling the
tool is the commonest reason a crew is never asked.

### Enable agents

Set `ENDPOINTS` to include `agents`:

```yaml
librechat:
  configEnv:
    ENDPOINTS: custom,agents
```

MCP tools reach the model only through an agent. A plain chat on a custom endpoint never
calls the crew; it answers from the model's memory. With `ENDPOINTS: custom` alone there is
no way to create an agent.

### Add the crew liaison

The crew liaison is one MCP endpoint, `/mcp`, for every crew in the cluster. Add it under
`mcpServers` in `librechat.yaml`. Which of the two examples fits depends on how the liaison
identifies callers.

**A crew with per-person sign-in.** When the liaison runs a sign-in front (see
[Signing in](../signing-in/#the-sign-in-front)), each person signs in to the service behind
the crew, and the crew then acts with that person's own access. Reach the liaison through
its own hostname (`liaison.expose.hostname`):

```yaml
mcpServers:
  kubemoot:
    type: streamable-http
    url: https://moot.example.org/mcp
    title: Kubemoot crews
    description: Ask a Kubemoot crew a question
    requiresOAuth: true
    timeout: 180000
    initTimeout: 30000
```

With `requiresOAuth: true`, LibreChat discovers the sign-in on its own. The liaison answers
a request without a token with a 401 that points at its protected-resource metadata;
LibreChat reads it, registers itself with the liaison's front, and sends the person to
sign in once. No client id or secret goes into `librechat.yaml`. The liaison's
`signIn.allowedRedirectHosts` must allow LibreChat's address unless LibreChat is on
`localhost`, which is always allowed. [O'guide](https://github.com/kubemoot/crews/tree/main/oguide-crew#use-oguide-from-a-chat-app)
is a worked example.

**A liaison that uses a bearer token.** When the liaison has a shared token, or checks
Kubernetes tokens, the app sends one fixed header and there is no sign-in:

```yaml
mcpServers:
  kubemoot:
    type: streamable-http
    url: https://moot.example.org/mcp
    title: Kubemoot crews
    description: Ask a Kubemoot crew a question
    headers:
      Authorization: "Bearer ${LIAISON_TOKEN}"
    requiresOAuth: false
    timeout: 60000
    chatMenu: true
```

- `${LIAISON_TOKEN}` is replaced from LibreChat's environment. Leave out `headers` when the
  liaison has no token.
- `requiresOAuth: false` stops LibreChat from treating the 401 for a wrong token as a
  sign-in to start.
- The default `timeout` of 60000 milliseconds is above the liaison's 45-second cap on each
  call.

In both cases `url` is the liaison's `/mcp` address, and LibreChat's own server makes the
connection, so the address must be reachable from where LibreChat runs. For a LibreChat in
the same cluster, use `http://<release>-liaison.<namespace>.svc/mcp`. Restart LibreChat after
editing the file.

## Use it

1. **Sign in to the crew (per-person sign-in only).** Sign in to LibreChat, open the MCP
   panel, and click **Authenticate** (or **Connect**) on the crew's server. The sign-in does
   not start by itself from a chat question. LibreChat sends you to the service's sign-in
   page; when your browser is already signed in there, the service approves without showing
   a login page. You sign in once, and LibreChat keeps your token.
2. **Create an agent.** Open the Agent Builder (the newer interface has an Agents
   marketplace). Creating an agent needs the `agents` endpoint from
   [Enable agents](#enable-agents). Choose a model with reliable tool calling, add the crew's
   MCP server and its tools, and write instructions that make the model ask the crew:

   > Answer by asking the crew's tools. Do not answer from your own knowledge. If a tool
   > fails, say so and do not guess.

   If answers carry dates, add: "When you call `ask`, pass the user's IANA time zone as
   `timeZone`."
3. **Chat with the agent.** Select the agent and ask your question:

   > Ask the homelab-pilot crew which nodes have an accelerator.

   The model calls `list_crews` if it needs the name, then `ask`, then `get_answer` until
   the crew has settled, and replies with the answer.

<!-- Exact clicks: confirmed against LibreChat <version> on <date>. The steward fills this in once the agent flow is confirmed. -->

### How to tell the crew was asked

- The reply shows a tool step for the crew's server.
- The Kubemoot dashboard's Moot shows a new thread for that crew. Under Conversations it
  appears under **Any**, owned by the person's identity at the service they signed in to
  (not under **Mine**, which lists conversations owned by your Kubernetes identity).

## Troubleshooting

| What you see | What it means |
|---|---|
| The answer reads well but names authors, titles or facts the crew never returned | The model answered from memory. No tool was called: use an agent, not a plain chat, check that the agent has the crew's tools, and tighten its instructions. Switch to a model with better tool calling if it keeps skipping the tool |
| No way to create an agent | `ENDPOINTS` lacks `agents`. Set it to `custom,agents` and restart LibreChat |
| "OAuth handling failed" in LibreChat's log | LibreChat tried to load the crew's tools before you authenticated. Open the MCP panel, click **Authenticate** on the server, then start a new chat |
| The sign-in page never opens, or returns an error | Check the liaison's log for "sign-in started" and "sign-in completed". The liaison's `signIn.allowedRedirectHosts` must include LibreChat's host unless it is on `localhost`, and `DOMAIN_CLIENT` and `DOMAIN_SERVER` must match the address in your browser |
| The sign-in completes but the crew's tools fail | The liaison log shows the upstream token call and its status. A status other than 200 points at the service behind the crew, not at LibreChat |
| MongoDB pod `CrashLoopBackOff` or never schedules | The node lacks AVX. Pin MongoDB with the `nodeSelector` above |
| A new LibreChat pod stays `Pending` or `ContainerCreating` on update | The strategy is rolling and the uploads volume is ReadWriteOnce. Use `Recreate` |

## Limits

The [limits that apply to every app](/docs/integrations/#limits-that-apply-to-every-app) apply: a
follow-up in the same LibreChat chat starts a new Kubemoot conversation, and the asker's
time zone travels only if the model passes `timeZone`. To have the model pass it, put
that instruction in the agent's instructions.

Also specific to this setup:

- **A plain chat cannot reach a crew.** Only an agent can call MCP tools.
- **The sign-in is a click, not part of the question.** A person who has not authenticated
  gets no crew tools until they do.
- **One replica.** The `Recreate` strategy means an update takes the chat offline briefly.
