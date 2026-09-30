# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities **privately** to security@kubemoot.org. Do not open a
public issue for a security report.

We will acknowledge your report within a reasonable period and coordinate disclosure
with you before any public announcement.

## Supported versions

Kubemoot is under active development; security fixes target the latest `main`.

## Security model and known limitations

Kubemoot is a `v1alpha1` platform to evaluate and shape. Read this section before you
install it anywhere that holds data or credentials you care about.

### In-cluster endpoints are unauthenticated

Most in-cluster endpoints accept requests without authentication: the NATS message bus,
the dashboard, the MCP bridge in front of every MCP server, the MCP gateway admin API,
an agent's chat endpoint, the discussion gateway, and the operator's report server. The
crew liaison accepts an optional bearer token; with no token set it is open to the
cluster. No NetworkPolicy ships for these endpoints, so the effective boundary is
"any pod in the cluster". Put an authenticating proxy in front of the dashboard and the
discussion gateway, and restrict who can run pods in namespaces that host Kubemoot.

### NATS has no authentication

The NATS bus that carries discussions, memory, and scheduler state runs without
authentication in this release. Namespaces separate subject and key names (every subject
starts with the namespace and crew name), but a client that can reach NATS can read and
write any of them. Namespaces are naming separation, not a security boundary today.
Per-crew NATS accounts and a NetworkPolicy that limits ingress to NATS are directions,
not shipped features.

### Creating Kubemoot resources is equivalent to creating pods

An `Agent`, `MCPServer`, or `RAGSource` selects a container image and a service account.
Anyone who can create these resources can run arbitrary images with those service
accounts. Grant create access to them as you would grant it for `Pod`.

### Internal MCP servers and cluster credentials

The operator chart can deploy internal MCP servers (`internalAgents.enabled`, on by
default) behind a `ClusterRole` that reads Secrets cluster-wide and creates Kubemoot
resources. The quickstart disables them. Set `internalAgents.enabled: false` unless you
use them, and bind any Kubernetes-facing MCP server to a narrow `Role` in the crew's
namespace.

The reference crews in the `crews` repository bind tools with broad Kubernetes access
for demonstration. Review each chart's RBAC before installing it, and give a crew
read-only credentials wherever it does not need to change anything.

### Agents follow text they read

An agent acts on text from its tools, documents, and the web. A prompt injected through
any of them can steer a tool-calling agent. Limit each agent to the tools its role needs
(`spec.enabledTools`) and to read-only credentials.

### The code sandbox relies on NetworkPolicy enforcement

The code sandbox restricts egress with a NetworkPolicy. A cluster CNI that does not
enforce NetworkPolicy (plain flannel, for example) ignores it, and the sandbox then has
unrestricted network access. Inside the pod there is no process-count limit and the
program runs as the same user as the sandbox server; CPU and memory limits bound a
runaway program. Use a CNI that enforces NetworkPolicy before enabling the sandbox.

### API stability and platforms

- The API group `kubemoot.ai/v1alpha1` can change between releases.
- Release images are `amd64` only today. See the
  [Roadmap](https://github.com/kubemoot/kubemoot/blob/main/docs/introduction/roadmap.md).
- Base images are pinned by tag, not by digest.
