---
title: "Secrets and tools"
weight: 55
description: "Why the model never holds a secret, how credentials reach tools today, and the per-tool grant design that is the top roadmap priority."
---

A tool that sends mail, queries a database, or calls an API needs a credential. Kubemoot
keeps that credential away from the model. The principle:

**The model never holds a secret; only the tool process does.** An agent calls a tool
and receives results. It never receives the token, password, or key behind the tool.

## Why

A model reads prompts and writes text. Anything placed in a prompt, a ConfigMap an agent
reads, or an agent's environment can appear in a response, a log, a discussion
artifact, or a retrieved document. A credential held only by the tool process cannot be
leaked by what a model says, and a prompt-injected agent has nothing to hand over. The
tool chain reinforces this boundary: agents reach tools only through the
[MCPGateway](../mcp-tools/), and the credential lives in the MCPServer pod behind it.

## How it works today

An [MCPServer](../../reference/mcpserver-guide/) takes credentials from Kubernetes
Secrets in its own namespace:

- `env` entries with `valueFrom.secretKeyRef` inject one key as an environment variable.
- `secretRef` injects every key of a Secret as environment variables.
- `secretVolumes` mounts a Secret as files.

Only the MCPServer pod receives the credential. Agent pods do not get it through these
fields, so the principle holds for the prompt path.

What is not enforced yet is who may reference which Secret. The operator mounts any
Secret the MCPServer names, and the MCPServer validation does not check that the author
may read it. A crew author who can create an MCPServer can have the operator mount any
Secret in that namespace into a pod they control. The `serviceAccountName` field works
the same way: the server runs as any ServiceAccount the author names. Agent pods run as
the namespace `default` ServiceAccount unless the Agent sets another.

Inside a namespace, then, anyone who can create an MCPServer is trusted with every
Secret there. Namespaces remain the isolation boundary between crews.

## Target design

Closing the gap uses Kubernetes authorization and admission, with nothing new invented.
This is the top priority on the [roadmap](../../introduction/roadmap/).

1. **Admin-owned Secrets.** An admin creates each Secret in the crew namespace. Crew
   authors have no `get` or `list` on Secrets.
2. **A `use` grant.** The admin grants the custom verb `use`, not read, on one named
   Secret through a Role with `resourceNames`, bound to whoever applies the crew, such as
   the Flux identity for that namespace.
3. **Admission check.** A ValidatingAdmissionPolicy on MCPServer create and update asks
   the Kubernetes authorizer, through the CEL `authorizer`, whether the requester may
   `use` every referenced Secret and ServiceAccount. If not, the MCPServer is rejected.
   This closes the confused-deputy path where the operator, which can mount Secrets,
   acts for someone who cannot.
4. **Per-tool ServiceAccounts.** The operator mounts a Secret only into its own
   MCPServer's pod, under a ServiceAccount for that server. Agent pods get no Secret
   access.
5. **Cluster access only where needed.** Only tools that call the Kubernetes API, such
   as the Kubernetes MCP server, reference an admin-made ServiceAccount, behind the same
   `use` check, and its RBAC bounds the tool. Every other tool runs as a ServiceAccount
   with no permissions and no mounted token.
6. **Egress per tool.** A NetworkPolicy per MCPServer limits where a tool can connect,
   so the mail tool reaches only the mail provider.

Narrow token scopes and human confirmation for actions with side effects complete the
picture. Later steps add a secret store such as OpenBao with External Secrets Operator
for rotation, short-lived credentials, and audit, and per-user OAuth for assistants that
act on behalf of one person.

## What to do until then

- Keep credentials in the namespace of the crew that uses them, and give each crew its
  own namespace.
- Limit who can create MCPServers and Agents in a namespace to people you would trust
  with every Secret in it.
- Scope every token narrowly: read-only where reading is enough, limited to the one
  repository, mailbox, or API the tool needs.
- Never put a secret in a prompt, a `promptHint`, a ConfigMap, or an Agent environment
  variable. Put it in a Secret and reference it from the MCPServer.
