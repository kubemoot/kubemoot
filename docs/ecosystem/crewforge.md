---
title: "CrewForge"
weight: 40
description: "The VS Code extension for browsing, authoring, and deploying Kubemoot crews from your editor."
---

**CrewForge** is a VS Code extension for working with Kubemoot crews without leaving
the editor. Its **Crews** view lists every crew your kubeconfig can read and opens a
chat with any of them. Its **Crew Sources** view finds the crew charts and manifest
bundles in your workspace, shows every namespace each one is deployed to, compares the
source with what is actually running, and deploys, updates, rolls back, and removes
crews through your own `helm` and `kubectl`. It reaches a crew's discussion gateway the
same way [`kmctl`](../kmctl/) does: through the Kubernetes API server's service proxy,
using your kubeconfig, so no crew needs a public address.

## Install

Install the `.vsix` from the
[vscode-crewforge releases](https://github.com/kubemoot/vscode-crewforge/releases):
in VS Code, **Extensions: Install from VSIX...**, or
`code --install-extension crewforge-<version>.vsix`.

## Crews: browse and chat

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

Conversations are saved, so you can pick up where you left off or hand one to someone
else. **CrewForge: Continue a Conversation** reopens any saved conversation. The copy
and download buttons in a chat's header copy or save it as Markdown, and **CrewForge:
Open Conversations Folder** shows where they're kept on disk.

**Powered by Kubemoot** in a chat opens the Kubemoot dashboard, if `crewforge.dashboardUrl`
is set.

## Crew Sources: author and deploy

The **Crew Sources** view finds crew sources in your open workspace folders: a Helm
chart whose templates declare a `kubemoot.ai` Crew (a `kmctl create --chart` scaffold,
for example), or a folder of plain manifests that includes a Crew (a bundle, such as a
[Build a Crew](../../user-guides/build-a-crew/) workspace). For each source, CrewForge
renders it (`helm template`, from your PATH, for a chart; the manifests as they are for
a bundle) to learn the crew's name, then lists every namespace with a live Crew of that
name. A deployment whose Crew does not name this source as its own is marked, since a
crew of the same name can come from a different copy of the source.

### Compare with the cluster

**Compare with Live** renders the source for a deployment's namespace and compares its
Kubemoot objects against what is running, field by field, but only on the fields the
source sets: server defaults and the labels a build stamps on are ignored, so a rebuild
never looks like drift. Each object is one of:

- **In sync** - the live object matches what the source renders.
- **Changed in source** - the source now renders something different from what is live.
- **In source, not deployed** - the source renders it, but the cluster does not have it.
- **Deployed, not in source** - it is live, but the source no longer renders it.

An object owned by a controller (the operator's resume `RAGSource`, for example) is
never reported; it isn't the source's to manage. Selecting a changed or missing object
opens a diff editor, live on one side and the source's render on the other.

A Flux-managed deployment renders with the HelmRelease's own inline `values` applied,
so the comparison matches what Flux actually installs (`valuesFrom` - ConfigMaps and
Secrets - is not read). Its node also shows the HelmRelease's state: ready at a given
revision, reconciling, failed, or suspended.

### Create a crew

**Create Crew** asks for a name, how many specialists to start with, and a model
family, then runs `kmctl create <name> --chart` into the workspace folder you choose
and opens the generated `README.md`. `kmctl` must be on your PATH; see the
[kmctl User Guide](../../user-guides/kmctl/) to install it. The result is a normal crew
chart, so it shows up in Crew Sources like any other.

### Deploy to a namespace

**Deploy Crew to a Namespace** asks for a namespace (any name; it need not exist yet)
and a channel:

- **Helm** - `helm upgrade --install --create-namespace`. Only available for a chart
  source.
- **Bundle** - `kubectl apply --server-side --field-manager=crewforge`.
- **Flux** - CrewForge does not run this one. Commit and push the source to the
  repository your GitOps setup watches; Flux applies it.

`helm` and `kubectl` run from your own PATH, using CrewForge's kubeconfig and context.
A crew already in a namespace keeps the channel it came through: a Flux-managed crew
changes only through git, a Helm release only through Helm, a bundle only through
`kubectl`, so two channels never fight over the same objects. Channels that don't apply
are shown disabled with the reason why.

Before deploying, CrewForge warns and asks you to confirm when:

- the namespace's crew came from a different source,
- it was deployed by a different developer,
- it wasn't deployed by CrewForge at all (its origin is unknown), or
- the namespace already holds other crews.

On deploy, CrewForge records on the Crew:

| Annotation | Holds |
|---|---|
| `crewforge.kubemoot.ai/source` | The source's identity: `<repository>//<path>`, or `local:<folder>` outside git. |
| `crewforge.kubemoot.ai/owner` | The developer who deployed it, from git's `user.email`. |
| `crewforge.kubemoot.ai/revision` | The last commit that touched the source, with `-dirty` if it had uncommitted changes. |
| `crewforge.kubemoot.ai/channel` | `helm`, `bundle`, or `flux`. |
| `crewforge.kubemoot.ai/deployed-at` | When this deploy ran. |

### Update, apply, and remove

**Update Deployment from Source** brings a deployment back in line with its source
through the channel it already came through: a bundle applies only the changed and
missing objects (the Crew is always included, so its annotations stay current), a Helm
release is upgraded, and a Flux deployment gets guidance instead, since only a change in
git moves it. **Apply This Object** does the same for a single object of a bundle
deployment, from the drift list.

**Remove Deployment** uninstalls a Helm release or `kubectl delete`s a bundle's objects.
It never deletes the namespace itself, unless the crew carries the
`kubemoot.ai/manage-namespace` annotation, in which case the operator deletes the
namespace and everything in it. The confirmation dialog says so before you proceed. A
Flux-managed deployment is removed by deleting it from your GitOps repository.

### Deploy a revision

**Deploy a Revision (Roll Back or Forward)** lists the commits that touched the source
and lets you pick one. CrewForge reads that commit from git without touching your
working tree, then deploys it through the deployment's existing channel. A Flux-managed
deployment rolls back by reverting the commit in git instead; CrewForge points you at
that.

### Fitness

Each deployment has a **Fitness** node listing its `CrewFitness` and `CrewFitnessSuite`
runs, newest first, with their results. Opening a run shows its report as Markdown.

**Run Fitness** offers the fitness definitions the source renders, plus any it keeps in
a `fitness/` folder (inside a chart, or beside a bundle), and starts a run under a
timestamped name so every run is kept. If a fitness run is already in progress anywhere
CrewForge can see, it warns first: crews share GPUs, so two runs at once measure
contention, not the crew. See
[Fitness Functions](../../fitness/kubemoot-crew-fitness-functions/) for how to write
scenarios.

### Follow a GitOps rollout

For a Flux-managed deployment, **Follow GitOps Rollout** watches from a push landing as
a new chart revision through Flux's upgrade to the crew coming Ready, reporting each
step. It ends on a failure, a suspended HelmRelease, or if you cancel it.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `crewforge.kubeconfig` | empty | Path to a kubeconfig. Empty uses `KUBECONFIG`, then `~/.kube/config`. |
| `crewforge.context` | empty | Context to use. Empty uses the kubeconfig's current context. |
| `crewforge.namespaces` | `[]` | Show crews only in these namespaces. Set it when your account can read only some namespaces. |
| `crewforge.streamTimeoutSeconds` | `600` | Longest a single turn may stream. |
| `crewforge.dashboardUrl` | empty | The Kubemoot dashboard for this cluster, opened from **Powered by Kubemoot**. |

## What your account needs

For the Crews view:

- `list` on `crews.kubemoot.ai`, cluster-wide or in each namespace of
  `crewforge.namespaces`.
- `get` and `create` on `services/proxy` in the crew's namespace, to ask a question and
  to stream its answer.

For the Crew Sources view:

- `get` and `list` on the Kubemoot kinds it compares (Crew, Agent, PromptModule, Model,
  and the rest of the kinds a source renders).
- `patch` on `crews.kubemoot.ai`, to record the deploy annotations.
- `create` on `crewfitnesses.kubemoot.ai` and `crewfitnesssuites.kubemoot.ai`, to start
  a fitness run.
- `get` on `helmreleases.helm.toolkit.fluxcd.io`, to show a Flux deployment's state.
  Optional: without it, a Flux deployment still shows, just without HelmRelease status.
- Whatever your `helm` and `kubectl` need to deploy: typically `create`, `patch`, and
  `delete` on the objects a source renders, in the namespaces you deploy to.

## The boundary that stays

CrewForge asks `kmctl`, `helm`, and `kubectl` to do the actual writes; whatever it
authors and deploys stays within Kubemoot's custom resources and the objects a source
renders. It never deletes a namespace itself, manages Jobs, touches non-CRD cluster
resources on its own account, or implements cleanup or lifecycle logic. The
[controller](../kubemoot-controller/) owns namespace lifecycle, garbage collection, Job
management, and cascading cleanup, through finalizers and owner references, even when
CrewForge is the one that triggered a delete.

## Related pages

- [Develop Crews in VS Code](../../user-guides/develop-crews-in-vscode/) - a walkthrough
  of the Crew Sources workflow.
- [kmctl - the CLI](../kmctl/) - the scriptable counterpart CrewForge calls to scaffold
  a chart.
- [Build a Crew](../../user-guides/build-a-crew/) - the resource-level authoring
  CrewForge's Crew Sources view works with.
