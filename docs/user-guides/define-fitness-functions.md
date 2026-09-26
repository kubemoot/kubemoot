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
ASSERT(at least 1 Tooler contributes with signal=agree)
ASSERT(coordinator produces synthesis)
ASSERT(synthesis CONTAINS reference to a node count)
```

Common assertion patterns include: the discussion endpoint returns 200; a named SSE
event is emitted (optionally within a deadline); the discussion completes within a time
budget; at least N Toolers agree (or, for an out-of-scope smoke test, *zero* agree);
the coordinator produces a synthesis; and the synthesis contains or does **not** contain
specific terms.

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

## Holistic by design

These are **holistic** fitness functions: each run exercises the whole crew at once -
coordinator, Toolers, Analysts, tool gateway, message bus, and live model inference - against
real, measurable behavior, not a mocked unit. That is what makes "does the crew still do
its job?" an objective, repeatable check instead of a transcript you eyeball.

For the deeper treatment, see
[Kubemoot Crew Fitness Functions](../../fitness/kubemoot-crew-fitness-functions/).
