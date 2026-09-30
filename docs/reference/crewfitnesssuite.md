---
title: "CrewFitnessSuite CRD"
weight: 8
---

## Overview

A `CrewFitnessSuite` runs a set of fitness scripts against a crew many times, collects the result of every iteration, and writes an XLSX report to the NATS Object Store bucket `kubemoot_fitness_artifacts`. The operator creates one `CrewFitness` per script and iteration, serially by default, and owns each one through an owner reference.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: CrewFitnessSuite
metadata:
  name: homelab-baseline
  namespace: crew-homelab-pilot
spec:
  crewRef: homelab-pilot
  description: "READ-query baseline across the homelab layers"
  iterations: 15
  concurrency: 1
  scripts:
    - testRef: gpu-utilization-live
      configMapRef: homelab-pilot-fitness-tests
    - testRef: node-count
      configMapRef: homelab-pilot-fitness-tests
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `crewRef` | string | required | Crew in the same namespace that the suite runs against. |
| `description` | string | | Purpose of the suite. Shown on the dashboard and in the XLSX Overview tab. |
| `iterations` | int | required, at least 1 | How many times each script runs. The suite creates `len(scripts) x iterations` runs. |
| `scripts` | list | required, at least 1 | The scripts to run, in order. Each entry has a `testRef` and exactly one of `testContent` (inline script) or `configMapRef` (ConfigMap holding `<testRef>.adl`). |
| `concurrency` | int | `1` | Maximum runs in flight at once. Keep it at 1 for baselines: parallel runs share the crew and its model providers, so they measure contention. |
| `perIterationTimeout` | duration | `10m` | Wall-clock cap for one iteration. |
| `artifactRetention` | duration | `168h` | Object Store TTL for the XLSX. |
| `purgeMemory` | bool | `true` | Clear the crew's working memory once before the first iteration. |
| `suspend` | bool | `false` | Pause between iterations. See [Pause, resume and stop](#pause-resume-and-stop). |
| `cancel` | bool | `false` | Stop the suite. See [Pause, resume and stop](#pause-resume-and-stop). |

## Status

| Field | Description |
|-------|-------------|
| `phase` | `Pending`, `Running`, `Paused`, `Completed`, `Cancelled`, `Failed`, or `Error`. |
| `runId` | Identifies this execution. Used in iteration names and in the artifact key. |
| `startedAt` | When scheduling began. |
| `completedAt` | When the last iteration finished, or when the suite was cancelled. |
| `iterationsTotal` | `len(scripts) x iterations`. It keeps the planned total after a cancel, so `iterationsCompleted < iterationsTotal` marks a partial run. |
| `iterationsCompleted` | Iterations that reached a terminal phase, whatever the outcome. |
| `passed`, `failed`, `errored` | `iterationsCompleted` broken down by outcome. |
| `artifactRef` | Bucket, object key (`<namespace>/<suite>/<runId>.xlsx`), and size of the XLSX, set once it is written. |
| `error` | Why the suite itself failed to execute (phase `Error`). |

### Phases

| Phase | Terminal | Meaning |
|-------|----------|---------|
| `Pending` | no | Created, not yet started. |
| `Running` | no | Iterations are being scheduled or are in flight. |
| `Paused` | no | `spec.suspend` is true and no iteration is in flight. Nothing runs against the crew. |
| `Completed` | yes | Every iteration reached a terminal phase. Failed or errored iterations are outcomes in the counts, not a suite failure. |
| `Cancelled` | yes | `spec.cancel` stopped the suite. Completed iterations are kept and the XLSX is marked partial. |
| `Failed`, `Error` | yes | The suite itself could not execute, such as an invalid spec. |

## Pause, resume and stop

Two spec fields control a running suite. Edit them with `kubectl patch`, or use the Pause, Resume, and Stop buttons on the dashboard Fitness page.

**Pause** sets `spec.suspend: true`. The iteration in flight finishes and its result is kept; no new iteration starts. The phase stays `Running` until that iteration finishes, then becomes `Paused`. A `Paused` suite puts no load on the crew, which makes it safe to change the crew or the cluster between iterations.

**Resume** sets `spec.suspend: false`. The phase returns to `Running` and the suite continues at the next iteration it has not yet run. Completed iterations are not repeated.

**Stop** sets `spec.cancel: true`. The operator deletes any iteration in flight (its Job and pod follow through owner references), starts no new iteration, sets `completedAt`, and moves the suite to `Cancelled`. Stop works from `Pending`, `Running`, or `Paused`, takes precedence over `suspend`, and cannot be undone: clearing `cancel` afterwards does not restart the suite. Both fields are ignored once the suite is terminal.

```bash
# Pause
kubectl patch crewfitnesssuite homelab-baseline -n crew-homelab-pilot \
  --type merge -p '{"spec":{"suspend":true}}'

# Resume
kubectl patch crewfitnesssuite homelab-baseline -n crew-homelab-pilot \
  --type merge -p '{"spec":{"suspend":false}}'

# Stop
kubectl patch crewfitnesssuite homelab-baseline -n crew-homelab-pilot \
  --type merge -p '{"spec":{"cancel":true}}'
```

### The report of a cancelled suite

A cancelled suite still writes its XLSX. It holds only the iterations that finished before the stop; the iteration that was in flight is dropped. The Overview tab shows `Status: Cancelled` and a `Partial` row with how many iterations completed out of the planned total.

### Deferred judging

Scores from `DEFER` assertions are produced by a judge pass that runs only after the suite reaches a terminal phase. A `Running` or `Paused` suite is never judged, so pausing cannot start judging early.

A `Cancelled` suite is not judged at all. Stop means no further work on the GPUs, and a judge pass over a partial run would score scenarios from fewer iterations than planned. The quality columns of a cancelled suite's XLSX stay unjudged. To score a run, let it reach `Completed`.

## Deleting a suite

Deleting the `CrewFitnessSuite` removes everything it owns. Its `CrewFitness` runs, their Jobs, and their pods are garbage-collected through owner references, and the operator's finalizer (`kubemoot.ai/fitness-artifacts`) purges the run's XLSX, transcripts, and judge scores from the Object Store before the resource goes. Delete a suite when you want the run and its results gone; stop it when you want to keep what completed.

## Stopping a single CrewFitness

A standalone `CrewFitness` (one scenario, one run) has no stop field. Deleting it is the stop: its Job and pod are garbage-collected through owner references, and it has no partial result worth keeping.

```bash
kubectl delete crewfitness node-count-check -n crew-my-crew
```
