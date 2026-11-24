---
title: "Define Fitness Functions"
weight: 30
description: "Write executable fitness scenarios to measure a crew."
---

A crew's behavior is only as trustworthy as your ability to check it. Kubemoot
implements **architectural fitness functions** as Kubernetes resources: you write a
short scenario that asks the crew a real question and states what a good answer must
satisfy, apply it, and the operator runs the crew live and scores each assertion pass
or fail.

## The shape of a scenario

A fitness scenario is ADL pseudo-code - a question plus assertions:

```text
DESCRIPTION Verify the crew answers a basic infrastructure question.

DEFINE CONST QUESTION AS "How many nodes are in the cluster?"
DEFINE CONST MAX_DURATION AS 120 seconds

ASSERT(discussion completes within MAX_DURATION)
ASSERT(at least 1 specialist agrees)
ASSERT(coordinator produces synthesis)
ASSERT(synthesis CONTAINS reference to a node count)
```

Common assertion patterns include: the discussion endpoint returns 200; a named SSE
event is emitted (optionally within a deadline); the discussion completes within a time
budget; at least N specialists (Toolers) agree (or, for an out-of-scope smoke test, *zero* agree);
the coordinator produces a synthesis; and the synthesis contains or does **not** contain
specific terms. In an assertion, write "specialist" for a Tooler: the runner's agree-floor assertion matches that word, even though the docs call them Toolers.

## Run one scenario

Store scenarios as a ConfigMap alongside the crew and declare a `CrewFitness` resource
that references one:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: CrewFitness
metadata:
  name: node-count-check
  namespace: crew-my-crew
spec:
  crewRef: my-crew
  testRef: node-count        # key in the ConfigMap
  configMapRef: my-crew-fitness-tests
  ttl: 1h                    # auto-delete after completion
```

The operator validates the crew has a discussion endpoint, runs a job that asks the
question and streams the discussion, evaluates every `ASSERT` against the collected
signals, and writes per-assertion pass/fail into the resource's status. With `ttl` set,
the `CrewFitness` cleans itself up after it finishes.

```bash
kubectl get crewfitness -n crew-my-crew -w
kubectl describe crewfitness node-count-check -n crew-my-crew   # per-assertion results
```

## Run many - suites and baselines

To measure stability rather than a single pass, a `CrewFitnessSuite` runs a scenario
(or several) many times and writes a spreadsheet of results - pass rates and p50/p90
timings per scenario - so you can see a crew hold steady or drift across releases.

A long suite can be paused, resumed, or stopped while it runs, with `kmctl fitness pause`,
`resume` and `stop` (or `kubectl patch`, which the dashboard Fitness page shows):

- **Pause** (`spec.suspend: true`): the iteration in flight finishes and is kept, no new one
  starts, and the suite reads `Paused` once nothing is running.
- **Resume** (`spec.suspend: false`): the suite continues at the next iteration it has not run.
- **Stop** (`spec.cancel: true`): the iteration in flight is ended, the suite reads `Cancelled`,
  and the spreadsheet is written for the iterations that completed, marked partial. A stopped
  suite is not quality-judged.

```bash
kmctl fitness pause my-baseline -n crew-my-crew

# The same with kubectl
kubectl patch crewfitnesssuite my-baseline -n crew-my-crew --type merge -p '{"spec":{"suspend":true}}'
```

**Run part of a suite.** Most changes do not need every scenario. A scenario can name the
tiers it belongs to, `smoke` and `standard`, so the same scenarios serve three sizes of run:

```text
DEFINE CONST TIERS AS "smoke standard"
```

Smoke is a few scenarios chosen so that together they take every call path (triage, the tool
loop, simple calls, thinking on and off, each model and provider the crew uses): it checks
plumbing in minutes after a change to a client library, an engine adapter, the operator or a
chart. Standard is what a change to crew behavior is gated on. Full is every scenario, for
decisions about prompts, models and scheduling. Start one by naming the tier; the operator
reads the crew's scenarios and fixes the list when the suite starts:

```bash
kmctl fitness run --tier smoke --crew my-crew --name my-crew-smoke -n crew-my-crew
```

A smoke run reports which call paths it exercised and which it missed in
`status.pathCoverage`, so a gap shows instead of being assumed. Compare results only within
one tier. See [Tiers](../../reference/crewfitnesssuite/#tiers) and
[Path coverage](../../reference/crewfitnesssuite/#path-coverage).

When you change a scenario's reference, earlier scores were taken against the old text.
A suite with `spec.rejudge` scores an earlier run's saved answers against the current
scenarios without asking the crew again, so old and new runs compare on the same footing.

To stop a single `CrewFitness`, delete it. See the
[CrewFitnessSuite reference](../../reference/crewfitnesssuite/) for every field and phase.

## Holistic by design

These are **holistic** fitness functions: each run exercises the whole crew at once -
coordinator, Toolers, Analysts, tool gateway, message bus, and live model inference - against
real, measurable behavior, not a mocked unit. That is what makes "does the crew still do
its job?" an objective, repeatable check instead of a transcript you eyeball.

For the deeper treatment, see
[Kubemoot Crew Fitness Functions](../../fitness/kubemoot-crew-fitness-functions/).
