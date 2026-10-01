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
| `rejudge` | object | | Set at creation, immutable. Judge an earlier run's saved answers against this suite's scripts instead of running iterations. `suite` names the source `CrewFitnessSuite` in the same namespace and `runId` its run. See [Re-judge an earlier run](#re-judge-an-earlier-run). |

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
| `rejudge` | On a re-judge suite: the `source` run, how many `transcripts` were copied, and `notInSource`, the scenarios of this suite the source run has no answer for. |
| `conditions` | On a re-judge suite, `RejudgeSourceReady` reports whether the source could be used. See [Re-judge an earlier run](#re-judge-an-earlier-run). |

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

## Re-judge an earlier run

A judge score is only comparable to another judge score taken against the same references. When the references in the scenarios change (a corrected ground truth, a reworded reference), every earlier run was scored against text that no longer exists. `spec.rejudge` scores an earlier run's saved answers against the current scenarios without asking the crew anything.

A re-judge suite is an ordinary `CrewFitnessSuite` whose `scripts` are the current scenarios, plus a `rejudge` block naming the source run:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: CrewFitnessSuite
metadata:
  name: baseline-rejudged
  namespace: crew-homelab-pilot
spec:
  crewRef: homelab-pilot
  description: "baseline answers judged against the current references"
  iterations: 1
  rejudge:
    suite: baseline-n1        # the source CrewFitnessSuite, same namespace
    runId: 001cd44d           # the source's status.runId
  scripts:
    - testRef: gpu-utilization-all-rigs
      testContent: |
        ...the current scenario text...
```

The simplest way to get the current scripts is to build the suite from the crew's scenario directory, the same way a baseline is built, and add the `rejudge` block.

### What happens

1. **Validate.** The source suite must exist, its `status.runId` must equal `rejudge.runId`, and its phase must be `Completed`. Every script must resolve (inline `testContent`, or `<testRef>.adl` in its `configMapRef`).
2. **Copy.** The operator reads the source run's transcripts from the Object Store and matches them to this suite's scripts by `testRef`. Each matched transcript is copied into this suite's own run, keeping the answer, the events, and the timing, and adding a `rejudgedFrom` field with the source suite, run, and object key.
3. **Re-evaluate the assertions.** The copied transcript carries this suite's assertions, not the source's (see below).
4. **Complete.** The suite moves from `Pending` straight to `Completed`. `iterationsTotal` is the number of transcripts copied, and `passed` and `failed` come from the re-evaluated assertions.
5. **Judge.** The deferred judge pass then scores the copies exactly as it scores a normal run, against this suite's `DEFER` references, one scenario at a time with the same resumable checkpoint. The dashboard shows "judging X/Y" until it finishes, and the XLSX is written once judging is complete.

The crew named in `crewRef` receives no question. `iterations`, `concurrency`, `perIterationTimeout`, `purgeMemory`, and `suspend` do not apply. The source run is not modified, and its own judge scores are not carried over.

### Scenarios that do not match

- A scenario in this suite with no transcript in the source run (added since, or errored in the source) is listed in `status.rejudge.notInSource` and is not scored.
- A scenario in the source run that this suite does not have is ignored.

### Deterministic assertions

The fitness runner and the operator share one assertion engine, so the operator re-evaluates the deterministic `ASSERT` lines itself:

- An assertion whose text is identical to one the source run evaluated keeps the source result exactly as recorded.
- Any other assertion (new, or reworded) is evaluated against the stored answer and events. Its message ends with `(re-evaluated from the source transcript)`. A transcript is written only for an answered run, so the run state the runner had (POST returned 200, a `done` event arrived, no timeout) is recovered from the transcript itself.
- `DEFER` assertions carry this suite's reference, which is what the judge reads.

### When the source cannot be used

The suite goes to `Error` with `status.error` set and the `RejudgeSourceReady` condition `False`:

| Reason | Meaning |
|--------|---------|
| `InvalidRejudge` | `suite` or `runId` is empty, `suite` names this suite, there are no scripts, or a `testRef` appears twice. |
| `SourceNotFound` | No `CrewFitnessSuite` with that name in this namespace. Deleting a suite purges its transcripts, so a deleted run cannot be re-judged. |
| `RunIDMismatch` | The source suite's `status.runId` is not `rejudge.runId`. |
| `SourceNotCompleted` | The source run is not `Completed` (still running, paused, cancelled, or failed). |
| `ScriptUnavailable` | A script of this suite has no content: its ConfigMap or key is missing, or it sets both or neither source. A failure to reach the API server is retried, not reported. |
| `NoTranscripts` | No scenario of this suite has a readable transcript in the source run (for example, the source transcripts passed their Object Store retention). |
| `NoArtifactStore` | The operator has no NATS Object Store configured. |

On success the condition is `True` with reason `SourceReady` and a message naming the copy count and any scenarios not in the source. The `rejudge` block is set when the suite is created and cannot be added, changed, or removed afterwards; the API server rejects the edit. To re-judge again, create a new suite.

Scenarios are matched by `testRef`, so each `testRef` may appear only once in a re-judge suite. The source run's transcripts are mapped to scenarios through the source suite's `spec.scripts`; if those were edited after the run, the mapping follows the edited list.

### With kmctl

`kmctl` has no re-judge flag or command; the `rejudge` block in the manifest is the whole interface.

```bash
kmctl fitness run -f baseline-rejudged.yaml -n crew-homelab-pilot   # apply, wait for Completed
kmctl fitness get baseline-rejudged -n crew-homelab-pilot
kmctl fitness download baseline-rejudged -n crew-homelab-pilot      # once judging has finished
```

A re-judge suite reaches `Completed` as soon as its transcripts are copied, before the judge pass finishes, so `kmctl fitness run` returns early; watch "judging X/Y" on the dashboard Fitness page for the scores. Do not pass `--scenario` for a re-judge suite: it creates a live `CrewFitness` that asks the crew the question.

## Deleting a suite

Deleting the `CrewFitnessSuite` removes everything it owns. Its `CrewFitness` runs, their Jobs, and their pods are garbage-collected through owner references, and the operator's finalizer (`kubemoot.ai/fitness-artifacts`) purges the run's XLSX, transcripts, and judge scores from the Object Store before the resource goes. Delete a suite when you want the run and its results gone; stop it when you want to keep what completed.

## Stopping a single CrewFitness

A standalone `CrewFitness` (one scenario, one run) has no stop field. Deleting it is the stop: its Job and pod are garbage-collected through owner references, and it has no partial result worth keeping.

```bash
kubectl delete crewfitness node-count-check -n crew-my-crew
```
