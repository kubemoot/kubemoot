---
title: "CrewForge"
weight: 40
description: "The VS Code extension for browsing Kubemoot crews and talking to them from your editor."
---

**CrewForge** is a VS Code extension that lists the Kubemoot crews your kubeconfig can
read and opens a chat with any of them, without leaving the editor. It reaches a crew
the same way [`kmctl`](../kmctl/) does: through the Kubernetes API server's service
proxy, using your kubeconfig, so no crew needs a public address.

## Install

Install the `.vsix` from the
[vscode-crewforge releases](https://github.com/kubemoot/vscode-crewforge/releases):
in VS Code, **Extensions: Install from VSIX...**, or
`code --install-extension crewforge-<version>.vsix`.

## Use it

1. Click the Kubemoot mark in the activity bar. The **Crews** view lists every crew
   your kubeconfig can read, grouped by namespace, with a mark on the ones that are
   ready.
2. Click a crew to open a chat beside the view. Type a question and press Enter.
3. While a turn runs, each agent has a card: queued, analyzing (with the GPU it landed
   on), then its finding or that it stood aside. The crew's answer follows, rendered as
   Markdown. Ask again in the same panel and the crew keeps the conversation's context.

CrewForge reads the kubeconfig from the `crewforge.kubeconfig` setting, else every file
in `KUBECONFIG` (merged the way `kubectl` merges them), else `~/.kube/config`. Use
**CrewForge: Select Kubeconfig File** to point at a different file, and **CrewForge:
Select Kubernetes Context** to change cluster.

## Conversations

Conversations are saved, so you can pick up where you left off or hand one to someone
else. **CrewForge: Continue a Conversation** reopens any saved conversation. The copy
and download buttons in a chat's header copy or save it as Markdown, and **CrewForge:
Open Conversations Folder** shows where they're kept on disk.

**Powered by Kubemoot** in a chat opens the Kubemoot dashboard, if `crewforge.dashboardUrl`
is set.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `crewforge.kubeconfig` | empty | Path to a kubeconfig. Empty uses `KUBECONFIG`, then `~/.kube/config`. |
| `crewforge.context` | empty | Context to use. Empty uses the kubeconfig's current context. |
| `crewforge.namespaces` | `[]` | Show crews only in these namespaces. Set it when your account can read only some namespaces. |
| `crewforge.streamTimeoutSeconds` | `600` | Longest a single turn may stream. |
| `crewforge.dashboardUrl` | empty | The Kubemoot dashboard for this cluster, opened from **Powered by Kubemoot**. |

## What your account needs

- `list` on `crews.kubemoot.ai`, cluster-wide or in each namespace of
  `crewforge.namespaces`.
- `get` and `create` on `services/proxy` in the crew's namespace, to ask a question and
  to stream its answer.

## Not yet

CrewForge does not view or edit crew manifests, apply changes, or run fitness suites.
Those stay a `kubectl`/`kmctl`/editor workflow (see [Build a Crew](../../user-guides/build-a-crew/)
and [Fitness Functions](../../fitness/kubemoot-crew-fitness-functions/)) until authoring
lands in the extension. It also is not yet on the VS Code Marketplace or Open VSX;
install it from the `.vsix` attached to each
[release](https://github.com/kubemoot/vscode-crewforge/releases).

## The boundary that stays

CrewForge reads Crews and talks to their discussion gateways; it creates and deletes
nothing. Whatever CrewForge grows into, it stays a **CRD editor** at most: it never
deletes namespaces, manages Jobs, touches non-CRD cluster resources, or implements
cleanup or lifecycle logic. The [controller](../kubemoot-controller/) owns namespace
lifecycle, garbage collection, Job management, and cascading cleanup, through
finalizers and owner references.
