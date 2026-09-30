---
title: "Develop Crews in VS Code"
weight: 60
description: "Scaffold, deploy, compare, and run fitness against a crew from the Crew Sources view in CrewForge."
---

[CrewForge](../../ecosystem/crewforge/) is a VS Code extension. Its **Crew Sources**
view turns the resource-level workflow in [Build a Crew](../build-a-crew/) into
something you drive from the editor: it finds the crew source in your workspace, shows
where it's deployed, tells you what has drifted, and deploys, updates, rolls back, and
removes it through your own `helm` and `kubectl`. This guide walks through that
workflow end to end. Install CrewForge first (see the
[install steps](../../ecosystem/crewforge/#install)).

## 1. Scaffold a crew

Open the folder you want the crew in, then run **CrewForge: Create Crew** from the
Command Palette (or the `+` icon in the Crew Sources view). Answer three questions:
a name, how many specialists to start with, and a model family. CrewForge runs
`kmctl create <name> --chart` for you and opens the generated `README.md`.

`kmctl` must be on your PATH for this; see the [kmctl User Guide](../kmctl/#install) if
it isn't yet. The result is a Helm chart: `Chart.yaml`, `templates/` with the Crew,
Agents, PromptModules, and Models, and a `fitness/` folder with a starter
`CrewFitnessSuite` kept outside `templates/` so installing the chart doesn't start a
run.

If you'd rather start from plain manifests (a bundle) instead of a chart, scaffold with
`kmctl create <name>` (no `--chart`) from a terminal, or hand-write a folder with a
`Crew` manifest in it; CrewForge finds either kind of source.

## 2. Find it in Crew Sources

Open the Crew Sources view. CrewForge scans your workspace for crew charts (a
`Chart.yaml` whose templates declare a `kubemoot.ai` Crew) and bundles (a folder with a
Crew manifest), renders each one, and lists it by the crew name it renders. Under it,
every namespace with a live Crew of that name appears as a deployment node - there may
be none yet, or several if the same crew runs in more than one place.

## 3. Deploy it

Right-click the source (or use the cloud-upload icon) and choose **Deploy Crew to a
Namespace**. Name a namespace; it doesn't need to exist yet. Then pick a channel:

- **Helm** runs `helm upgrade --install --create-namespace`. Only offered for a chart
  source.
- **Bundle** runs `kubectl apply --server-side --field-manager=crewforge`.
- **Flux** isn't run by CrewForge at all: commit and push the source to the repository
  your GitOps setup watches, and Flux applies it.

Both `helm` and `kubectl` run from your own PATH, against CrewForge's kubeconfig and
context, so the RBAC and tool versions on your machine are what apply. If the target
namespace already has a crew of this name from another source, another developer, or
outside CrewForge entirely, you're asked to confirm before it's replaced.

Once deployed, the Crew carries a handful of annotations recording where it came from:
its source, who deployed it, the git revision, and the channel. These are what let
CrewForge warn you later if someone else's deploy would replace yours; see
[What your account needs](../../ecosystem/crewforge/#what-your-account-needs) for the
full annotation list.

A crew keeps whatever channel it was deployed through: once it's a Helm release, only
Helm changes it; once it's a bundle, only `kubectl apply` does. This keeps two channels
from fighting over the same objects.

## 4. Compare with what's running

Click a deployment's **Compare with Live** action (or expand it) to see how the source
compares with the cluster, object by object: **in sync**, **changed in source**, **in
source, not deployed**, or **deployed, not in source**. Only the fields the source
itself sets are compared, so server defaults and build-stamped labels never show up as
noise. Click a changed or missing object to open a diff editor with the live object on
one side and the source's render on the other.

If the deployment is Flux-managed, the render uses the HelmRelease's own inline
`values`, so the comparison reflects what Flux actually installs, and the deployment
node shows the HelmRelease's own state (ready at a revision, reconciling, failed, or
suspended).

## 5. Make a change and update the deployment

Edit the source (a template, a PromptModule, an Agent), save, and use **Update
Deployment from Source** on the deployment. For a bundle, only the changed and missing
objects are applied (the Crew is always included, to keep its annotations current); for
Helm, this is an upgrade. A single changed object can also be applied on its own with
**Apply This Object** from the drift list. A Flux-managed deployment can't be updated
this way; commit and push the change instead, and optionally use **Follow GitOps
Rollout** to watch it land (see step 7).

## 6. Roll back, or remove it

**Deploy a Revision (Roll Back or Forward)** lists the commits that touched the source
and deploys the one you pick, through the deployment's own channel, without touching
your working tree. For a Flux-managed deployment, roll back by reverting the commit in
git instead.

**Remove Deployment** uninstalls the Helm release or deletes the bundle's objects. It
leaves the namespace itself alone, unless the crew is annotated
`kubemoot.ai/manage-namespace` and the namespace carries the label
`kubemoot.ai/managed-namespace: "true"`, in which case removing it also has the operator
delete the namespace (system namespaces and the operator's own are never deleted); the confirmation dialog tells you which is about to happen.

## 7. Run fitness and follow a rollout

Expand a deployment's **Fitness** node to see its past `CrewFitness` and
`CrewFitnessSuite` runs, newest first. **Run Fitness** offers the fitness definitions
the source renders and any it keeps in a `fitness/` folder, and starts a new run under a
timestamped name. If another run is already in progress anywhere you can see, CrewForge
warns first, since crews share GPUs and a second run at the same time measures
contention rather than the crew. Opening a run shows its report as Markdown. See
[Define Fitness Functions](../define-fitness-functions/) for how to write scenarios.

For a Flux-managed deployment, **Follow GitOps Rollout** watches from the push landing
as a new chart revision through the HelmRelease's reconcile to the crew coming Ready,
and stops on success, failure, suspension, or if you cancel it.

## Related pages

- [CrewForge](../../ecosystem/crewforge/) - the extension reference: settings, RBAC,
  and the boundary it keeps with the operator.
- [Build a Crew](../build-a-crew/) - the resource-level workflow this view wraps.
- [kmctl User Guide](../kmctl/) - the CLI counterpart, including `kmctl create --chart`.
- [Define Fitness Functions](../define-fitness-functions/) - writing the scenarios
  Run Fitness executes.
