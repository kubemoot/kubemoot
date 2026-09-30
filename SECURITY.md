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
Whether the dashboard chart publishes a Gateway route is a chart value
(`gateway.enabled`); the application has no authentication either way.

### NATS has no authentication

The NATS bus that carries discussions, memory, and scheduler state runs without
authentication in this release. Namespaces separate subject and key names (discussion,
request, artifact, and memory subjects and keys carry the namespace and crew name after a
fixed prefix; some operator subjects carry no namespace at all), but a client that can
reach NATS can read and write any of them. Namespaces are naming separation, not a security boundary today.
Per-crew NATS accounts and a NetworkPolicy that limits ingress to NATS are directions,
not shipped features.

### Creating Kubemoot resources is equivalent to creating pods

An `Agent`, `MCPServer`, or `RAGSource` selects a container image and a service account
(`spec.deployment.image` and `spec.deployment.serviceAccountName` on an `Agent`;
`spec.image` and `spec.serviceAccountName` on an `MCPServer`; `spec.indexer.image` and
`spec.indexer.serviceAccountName` on a `RAGSource`).
Anyone who can create these resources can run arbitrary images with those service
accounts. Grant create access to them as you would grant it for `Pod`.

### Internal MCP servers and cluster credentials

The operator chart can deploy internal MCP servers (`internalAgents.enabled`). When
enabled, they run behind the `kubemoot-mcpserver-creator` `ClusterRole`, which creates and
updates Kubemoot `mcpservers`, `ragsources`, `agents`, and `agentpolicies` cluster-wide and
reads pods, namespaces, services, ConfigMaps, workloads, and jobs. Inspect the rendered role
(`kubectl get clusterrole kubemoot-mcpserver-creator -o yaml`) and remove any rule that
grants `secrets` before you rely on it. Check the chart's `values.yaml` for the current
default of `internalAgents.enabled`; the quickstart disables it. Leave it disabled unless
you use the internal MCP servers, and bind any Kubernetes-facing MCP server to a narrow
`Role` in the crew's namespace.

The chart's `verify` value, when enabled, creates the `kubemoot-verify-runner` service
account with a `ClusterRole` that has full access to Kubemoot resources, cluster-wide read
of Secrets, and create and delete on Jobs. Leave it disabled unless you run the verify jobs.

The reference crews in the `crews` repository bind tools with cluster-wide read access,
including Secrets, plus pod exec and workload patch and scale, for demonstration. Review
each chart's RBAC before installing it, and give a crew read-only credentials wherever it
does not need to change anything.

### Agents follow text they read

An agent acts on text from its tools, documents, and the web. A prompt injected through
any of them can steer a tool-calling agent. Limit each agent to the tools its role needs
(`spec.enabledTools`) and to read-only credentials.

### The code sandbox relies on NetworkPolicy enforcement

Kubemoot ships no NetworkPolicy for the code sandbox. A crew chart that deploys the sandbox
can add one that limits it to NATS and DNS; a cluster CNI that does not enforce
NetworkPolicy (plain flannel, for example) ignores it, and the sandbox then has
unrestricted network access. Inside the pod there is no process-count limit and the
program runs as the sandbox server's user (uid 1000). The operator applies a
`RuntimeDefault` seccomp profile and drops all capabilities; CPU and memory limits are
whatever the `MCPServer` declares. Use a CNI that enforces NetworkPolicy and add a policy
before enabling the sandbox.

### API stability and platforms

- The API group `kubemoot.ai/v1alpha1` can change between releases.
- Release images are `amd64` only today. See the
  [Roadmap](https://github.com/kubemoot/kubemoot/blob/main/docs/introduction/roadmap.md).
- Base and third-party images are referenced by tag, some of them `latest`; not all are pinned by digest.
