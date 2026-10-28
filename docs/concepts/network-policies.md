---
title: "Network policies"
weight: 56
description: "How the operator generates NetworkPolicy objects for a crew: default-deny ingress, an allow for each tool server, and outbound rules for the workloads that declare them."
---

A tool server does not check who calls it. The MCP bridge in front of a stdio server
accepts any connection, so the network is the only gate. When you turn the feature on, the
operator generates the policies that make the crew's gateway the only caller of each tool
server, and it can limit where a tool server or an agent connects to.

Kubemoot builds no policy engine for this. It writes standard `NetworkPolicy` objects, and
`CiliumNetworkPolicy` objects where the cluster runs Cilium and a rule needs something a
`NetworkPolicy` cannot say.

## Turn it on

The feature is off by default. It is a chart value on the operator chart:

```yaml
security:
  networkPolicies:
    enabled: true
```

The cluster needs a CNI that enforces `NetworkPolicy`. The value is cluster-wide: a crew
author cannot turn it off for one crew, so a crew cannot weaken a platform policy. The full
list of values is in the [KubemootConfig and chart guide](../../reference/kubemootconfig-guide/#network-policies).

With the flag off the operator creates nothing. When you turn it off after it was on, the
operator deletes the objects it generated and nothing else.

## What the operator generates

Every generated object is named `km-...`, carries the label
`kubemoot.ai/generated-by: network-policy`, and has an owner reference to the object it
was generated for, so deleting that object deletes its policies. The operator never changes
or deletes a policy that lacks the label. If a hand-written policy already has the name the
operator wants, the operator reports an error and leaves it alone.

| For | Objects | Why |
|-----|---------|-----|
| Each crew namespace (a namespace that holds at least one `Crew`; the operator labels it `kubemoot.ai/crew`) | `km-default-deny`, `km-crew-internal` | Every pod refuses ingress unless a policy admits it. The pods of the namespace, the operator, and the metrics scraper may reach every pod that is not a tool server or a tool gateway. |
| Each MCPServer with a pod | `km-mcp-<name>-ingress` | Only the crew's gateway and the operator reach the server, on its port (the bridge port for a stdio server). A shared server in the operator namespace admits the gateway of every namespace that holds a `Crew`. |
| Each MCPGateway | the `<gateway>-admin` policy (created for every gateway while the flag is on) | The tool port admits the crew's namespace, the operator, and the metrics scraper instead of every pod. The admin port stays operator only. |
| Each Crew with a discussion gateway | `km-crew-<crew>-gateway-ingress`, and on Cilium `km-crew-<crew>-gateway-entities` | Callers from outside the namespace reach the gateway on port 8080: the namespaces in `spec.network.allowedIngress`, the Gateway API proxy, and the API server's service proxy used by `kubectl ask` and kmctl. |
| Each MCPServer or Agent with `spec.egress` | `km-mcp-<name>-egress` or `km-agent-<name>-egress`, and on Cilium the same name with `-fqdn` | Outbound rules, described below. |

An MCPServer with `externalEndpoint` has no pod and gets no policy.

Every namespace that holds at least one `Crew` gets the baseline, whichever way the `Crew`
was created: from a crew chart, with `kubectl apply`, or by the operator chart (the
scheduling crew in `kubemoot-scheduling`). A namespace without a `Crew` gets nothing, and
the operator removes the baseline it generated when the last `Crew` there is deleted (a
`Crew` that is still being finalized counts until it is gone). The operator watches `Crew`
creation and deletion, so this follows the event and not a timer. Hand-written policies are
never touched.

### Who sets the `kubemoot.ai/crew` namespace label

The label is how a `NetworkPolicy` selects the namespaces that hold a `Crew`: the policies
of shared tool servers in the operator namespace, and anything you write, select on it. With
the feature on, the operator keeps it equal to the set of namespaces that hold a `Crew`:

- When a `Crew` exists in a namespace without the label, the operator sets it (the value is
  the name of the first `Crew`) and records that it did so in the namespace annotation
  `kubemoot.ai/crew-label-by: kubemoot-operator`.
- A namespace that already has the label is left alone, so a crew chart that sets it, or
  several `Crew` objects from different sources, never make the value change back and forth.
- When the last `Crew` is deleted, the operator removes the label only if it set it (the
  annotation, or the `app.kubernetes.io/managed-by: kubemoot-operator` label that earlier
  releases used). A label a person or a crew chart set stays, and a managed-by label that
  belongs to another tool, such as Helm, is not touched.
- The system namespaces and the operator's own namespace are never labeled.

With the feature off the operator labels a namespace only for a `Crew` that asks for
namespace management (`kubemoot.ai/manage-namespace`). The label is bookkeeping: it is never
consent to delete a namespace.

The operator's own namespace is not covered by the generated default-deny. Tool servers
there are protected one by one by their `km-mcp-<name>-ingress` policy.

### What this means for your own pods

`km-default-deny` applies to every pod in a crew namespace, including pods Kubemoot did not
create, such as a web server that fronts the crew. Those pods need their own allow policy
for the callers they expect. The pods of the namespace can reach each other through
`km-crew-internal`, but a caller from another namespace, or from the Gateway API proxy,
needs a policy you write (or, for a crew's discussion gateway, `spec.network.allowedIngress`).

Probes from the kubelet come from the node. Most CNIs, including Cilium, always allow them.
A CNI that blocks node-to-pod traffic needs a rule for the node, which Kubemoot does not
generate.

## Outbound rules

Outbound rules apply only to a workload that sets `spec.egress`, and only when the chart sets
`security.networkPolicies.egress.mode: Declared`. A workload without `spec.egress` is not
restricted, so turning the mode on cannot break a working crew.

A workload with `spec.egress` may reach:

- DNS (the chart value `dns` says where it runs).
- For an Agent: the message bus, the crew's MCP gateway, every model server the agent can be
  placed on, the query service of each RAG source it names, the placement service, and the
  telemetry collector. The operator reads these from the same settings it gives the agent,
  so a crew author never lists them.
- What `spec.egress` declares: `allowedServices` for Services inside the cluster,
  `allowedHosts` and `allowedCIDRs` for destinations outside it, `ports` (default 443) for
  those hosts and ranges, `bus: true` for a tool server that uses the message bus, and
  `apiServer: true` for a tool server that talks to the Kubernetes API server.

Everything else is blocked.

### The message bus for a tool server

An agent always reaches the bus. A tool server reaches it only when its `egress` sets
`bus: true`. The operator does not guess which tool servers use the bus: the bus carries no
limit on subjects at the network layer, so reaching it is a capability, and a tool server
that does not need it (a Proxmox or Prometheus tool) should not have it. The address is the
one the operator itself is configured with (the chart value `nats.url`), so a crew never
writes it and a move of the bus changes one value.

```yaml
spec:
  egress:
    bus: true
```

### Services inside the cluster

A destination inside the cluster is declared as a Service, not as a host name:

```yaml
spec:
  egress:
    allowedServices:
      - namespace: observability
        name: kube-prometheus-stack-prometheus
        ports: [9090]
```

The operator reads the Service and writes a standard `NetworkPolicy` rule for the Service's
namespace and pod selector, on the port its pods listen on (the target port, because
Cilium and kube-proxy translate a Service address to a pod before policy applies, so a rule
on the Service port or on a name would not match). Because the rule is plain
`NetworkPolicy`, it behaves the same on every CNI. `namespace` defaults to the workload's
own; empty `ports` allows every TCP port of the Service.

A Service that does not exist, has no pod selector, does not list a requested port, or has a
named target port with no endpoint yet gets no rule, and the workload reports
`EgressEnforced: False` with reason `ServiceNotResolved` and the reason in the message. The
operator watches Services, so the rule appears when the Service does. A Service without a
pod selector cannot be turned into pods; declare its addresses with `allowedCIDRs`.

The query service and the placement service of an agent are read the same way. The query
service rule uses the port of its real Service (the RAGSource's `spec.queryService.port`,
default 8000); the placement service is optional (chart value `placementService`), so an
agent gets an allow for it only while a Service exists behind the URL in the KubemootConfig.

### Host names and the CNI

A `NetworkPolicy` can name an address range but not a host name. What happens to
`allowedHosts` depends on the CNI:

| | Any CNI that enforces NetworkPolicy | Cilium |
|---|---|---|
| Ingress policies (default-deny, tool server, gateway tool port) | Enforced | Enforced |
| Gateway API proxy and API server proxy to the discussion gateway | Enforced from `trustedIngressNamespaces` and `controlPlaneCIDRs` | Enforced with no setting: entities `ingress`, `kube-apiserver`, `remote-node` |
| Outbound to address ranges, namespaces, and pods | Enforced | Enforced |
| Outbound to the API server | Enforced from the `kubernetes` EndpointSlice | Enforced with the `kube-apiserver` entity |
| Outbound to a host name | Not enforceable | Enforced with `toFQDNs` and the DNS proxy |

When the cluster cannot enforce a host name, the operator still protects the cluster. The
workload may reach every address outside the private ranges (10.0.0.0/8, 172.16.0.0/12,
192.168.0.0/16, 100.64.0.0/10, link local, loopback, and the IPv6 equivalents) and outside the
pod and service ranges you list in `clusterCIDRs`, on the ports it declared. The tool works
and cannot reach other pods or services in the cluster. This is weaker than a host rule: any
internet address on those ports is reachable. The MCPServer or Agent reports it:

```yaml
status:
  conditions:
    - type: EgressEnforced
      status: "False"
      reason: FQDNNotSupported
```

`EgressEnforced` is `True` when every declared rule is enforced, and absent when the
workload declares no egress. Cilium is found by the presence of the `CiliumNetworkPolicy`
kind when the operator starts, so restart the operator after installing Cilium. Set
`security.networkPolicies.cilium` to `on` or `off` to decide yourself.

A URL the operator hands an agent, such as a model server endpoint, is read as a cluster
Service when its host is `name.namespace` or `name.namespace.svc`, and as a name outside the
cluster otherwise. The rule uses the port in the URL; a Service whose port differs from the
port its pods listen on needs that pod port declared.

Hosts are matched as written. Cilium translates a Service to its pods before it applies
policy, so a host rule for a name inside the cluster may not match; declare in-cluster
destinations with `allowedServices` and keep `allowedHosts` for names outside the cluster.

Outbound to the API server needs no address from you. On Cilium the rule is the
`kube-apiserver` entity. On any other CNI it is built from the `kubernetes` EndpointSlice in
the `default` namespace, which lists the control plane node addresses and port (for example
6443), not the Service address 10.96.0.1:443 that clients dial.

## What is not covered

- A mode that denies all outbound traffic unless declared (`Deny`) is a possible later
  option. The only egress mode today is `Declared`.
- The RAG indexer and fitness Jobs are not restricted.
- Control per host and per HTTP path is outside what a `NetworkPolicy` or a host rule can
  express. It belongs to a gateway that sees the request.
- The operator namespace has no generated default-deny.

## Example

A mail tool that may reach one provider:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: mail
spec:
  image: registry.example.com/mail-mcp:1.0
  port: 3000
  egress:
    allowedHosts: ["smtp.example.com"]
    ports: [587]
```

With the flag on, the operator creates `km-mcp-mail-ingress` (only the crew gateway and the
operator reach port 3000) and `km-mcp-mail-egress` (DNS, plus the provider: a host rule on Cilium,
the internet fallback elsewhere).

List what the operator generated:

```bash
kubectl get networkpolicy,ciliumnetworkpolicy -A -l kubemoot.ai/generated-by=network-policy
```
