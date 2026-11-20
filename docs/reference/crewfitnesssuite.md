---
title: "CrewFitnessSuite CRD"
description: "Run fitness scripts against a crew many times with a CrewFitnessSuite and collect every iteration into an XLSX report stored in the NATS Object Store."
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
| `concurrency` | int | `1` | Maximum runs in flight at once. Keep it at 1 for baselines: parallel runs share the crew and its model servers, so they measure contention. |
| `perIterationTimeout` | duration | `10m` | Wall-clock cap for one iteration. |
| `artifactRetention` | duration | `168h` | Object Store TTL for the XLSX, used only when the suite creates the shared bucket. The install creates it with the limit of the chart value `nats.stores.fitnessArtifacts`, and an existing bucket keeps that limit. |
| `purgeMemory` | bool | `true` | Clear the crew's working memory before the run. Set it to `false` to leave the crew's memory alone for the whole run. |
| `trialIsolation` | `PerTrial` or `PerSuite` | `PerTrial` | `PerTrial` clears the crew's working memory before every trial and runs the trials one at a time, so a trial cannot use what an earlier one taught the crew. `PerSuite` clears it once before the first trial: use it to measure learning. See [Independent trials](#independent-trials). |
| `purpose` | `Capability` or `Regression` | | What the suite is for. A capability suite starts low and leaves room to improve; a regression suite sits near 100% and catches breakage. Optional. |
| `suspend` | bool | `false` | Pause between iterations. See [Pause, resume and stop](#pause-resume-and-stop). |
| `cancel` | bool | `false` | Stop the suite. See [Pause, resume and stop](#pause-resume-and-stop). |
| `skipToolSmoke` | bool | `false` | Skip the MCP tool smoke that runs first. See [Tool smoke](#tool-smoke). |
| `rejudge` | object | | Set at creation, immutable. Judge an earlier run's saved answers against this suite's scripts instead of running iterations. `suite` names the source `CrewFitnessSuite` in the same namespace and `runId` its run. See [Re-judge an earlier run](#re-judge-an-earlier-run). |

## Status

| Field | Description |
|-------|-------------|
| `phase` | `Pending`, `Running`, `Paused`, `Completed`, `Cancelled`, `ToolSmokeFailed`, `Failed`, or `Error`. |
| `runId` | Identifies this execution. Used in iteration names and in the artifact key. |
| `startedAt` | When scheduling began. |
| `completedAt` | When the last iteration finished, or when the suite was cancelled. |
| `iterationsTotal` | `len(scripts) x iterations`. It keeps the planned total after a cancel, so `iterationsCompleted < iterationsTotal` marks a partial run. |
| `iterationsCompleted` | Iterations that reached a terminal phase, whatever the outcome. |
| `passed`, `failed`, `errored` | `iterationsCompleted` broken down by outcome. |
| `artifactRef` | Bucket, object key (`<namespace>/<suite>/<runId>.xlsx`), and size of the XLSX, set once it is written. |
| `error` | Why the suite itself failed to execute (phase `Error`). |
| `toolSmoke` | The table the tool smoke produced: `passed`, `failures`, and one row per tool (`server`, `tool`, `invoked`, `passed`, `error`, `durationMs`), failures first. See [Tool smoke](#tool-smoke). |
| `rejudge` | On a re-judge suite: the `source` run, how many `transcripts` were copied, and `notInSource`, the scenarios of this suite the source run has no answer for. |
| `scenarios` | Per-scenario rollup of the finished iterations, in spec order. See [Results in status](#results-in-status). |
| `consistency` | How reliably the scenarios pass: the mean pass^k, the scenarios to check, and for a regression suite the scenarios below 100%. See [Consistency](#consistency-pass-and-pass-k). |
| `realm` | What the run ran on: model servers and models, accelerator capability, versions, agent pod resources, and concurrent activity, at the start and the end. See [Realm](#realm-what-a-run-ran-on). |
| `judge` | Progress and scores of the deferred judge pass. See [Results in status](#results-in-status). |
| `conditions` | `ToolSmoke` says how the tool smoke ended. On a re-judge suite, `RejudgeSourceReady` reports whether the source could be used. See [Re-judge an earlier run](#re-judge-an-earlier-run). |

### Phases

| Phase | Terminal | Meaning |
|-------|----------|---------|
| `Pending` | no | Created, not yet started. |
| `Running` | no | Iterations are being scheduled or are in flight. |
| `Paused` | no | `spec.suspend` is true and no iteration is in flight. Nothing runs against the crew. |
| `Completed` | yes | Every iteration reached a terminal phase. Failed or errored iterations are outcomes in the counts, not a suite failure. |
| `Cancelled` | yes | `spec.cancel` stopped the suite. Completed iterations are kept and the XLSX is marked partial. |
| `ToolSmokeFailed` | yes | The tool smoke found a failing MCP tool, so the suite stopped before any model ran. No iteration was created and no artifact is written. See [Tool smoke](#tool-smoke). |
| `Failed`, `Error` | yes | The suite itself could not execute, such as an invalid spec. |

## Tool smoke

A broken tool server makes agents stand aside or invent answers, and those look like agent
failures. The first step of every suite, the Smoke tier included, therefore checks the tools
before any model runs. It never calls a model.

1. The operator finds the crew's MCP gateway (the namespace's `MCPGateway`) and lists its tools
   with `GET /tools`, the listing agents use.
2. For each `MCPServer` in the suite's namespace it takes the tools that server lists. A server
   that lists none fails, and its status phase and message are in the error.
3. A tool with a declared test input (`spec.smokeInputs` on the `MCPServer`) is called with
   `POST /tools/{tool}/call` and those arguments. It passes when the gateway answers with no
   error inside the call's timeout (5s unless the input sets `timeout`). A declared tool the
   gateway does not list fails without a call.
4. A listed tool with no declared input is only checked for being listed. It is never called.

Calls run a few at a time. The whole smoke is bounded to three minutes. A namespace with no `MCPServer` passes with the `ToolSmokeNoServers`
reason.

When every tool passes, the suite goes on as before. When any tool fails, the suite ends at
once in the terminal phase `ToolSmokeFailed`: no memory is cleared, no iteration is created, no
artifact is written, and the `ToolSmoke` condition is `False` with reason `ToolSmokeFailed`
naming the first failures. `status.toolSmoke.results` has the whole table:

```yaml
status:
  phase: ToolSmokeFailed
  toolSmoke:
    passed: false
    failures: 1
    results:
      - {server: proxmox-mcp, tool: list_nodes, invoked: true, passed: false,
         error: "tool returned an error: proxmox-secret not found", durationMs: 212}
      - {server: proxmox-mcp, tool: list_vms, invoked: true, passed: true, durationMs: 180}
```

The phase is final, and so is a gateway the operator cannot reach (the smoke reports it as a failure of every server). Fix the tool, then create a new suite. Set `spec.skipToolSmoke: true` to skip
the step (the `ToolSmoke` condition then reads `True` with reason `ToolSmokeSkipped`).

### Declaring test inputs

Test inputs belong to the `MCPServer` that owns the tool, so they version with the server's
configuration and need no extra resource:

```yaml
kind: MCPServer
spec:
  smokeInputs:
    - name: list_nodes        # a tool the server lists
      arguments:              # optional, text values by argument name,
        node: pve             # as an agent's call sends them
      timeout: 5s             # optional, default 5s, at most 60s
```

Declare only read-only tools with read-only arguments: the smoke calls them on every suite. A
tool that changes anything, runs a model backend, or needs a scope the call does not carry should
stay without an input, and is then only checked for being listed. A crew chart keeps its inputs
beside the chart in `mcp-smoke/<server>.yaml` and renders them into the `MCPServer`.

## Results in status

The operator writes a run's results to the suite's status, so any client with read access to `crewfitnesssuites` gets them through the Kubernetes API: `kubectl`, `kmctl`, CrewForge, or a script. The status outlives the per-iteration `CrewFitness` objects, which the operator removes after the run, and the object store retention of the transcripts and judge checkpoint.

```yaml
status:
  phase: Completed
  runId: 7f3c2a1b
  iterationsTotal: 30
  iterationsCompleted: 30
  passed: 26
  failed: 3
  errored: 1
  scenarios:
    - name: gpu-utilization-live
      iterations: 15
      passed: 14
      failed: 1
      meanDurationMs: 84210
      assertionsPassed: 59
      assertionsTotal: 60
    - name: node-count
      iterations: 15
      passed: 12
      failed: 2
      errored: 1
      meanDurationMs: 40112
      assertionsPassed: 52
      assertionsTotal: 60
  judge:
    phase: Complete
    judged: 2
    total: 2
    mean: 44
    zeros: 1
    completedAt: "2026-10-02T14:31:07Z"
    scores:
      - scenario: gpu-utilization-live
        score: 0
        reason: all iterations gated (consensus failed or empty synthesis)
      - scenario: node-count
        score: 87
        reason: Correct node count and roles; omits the control-plane taint the reference names.
  artifactRef:
    bucket: kubemoot_fitness_artifacts
    objectKey: crew-homelab-pilot/homelab-baseline/7f3c2a1b.xlsx
    sizeBytes: 48211
```

### status.scenarios

One row per distinct `testRef`, in the order of `spec.scripts`. A script with no finished iteration yet has a row with only its `name`. The rows are refreshed on every reconcile of a running suite and are final once the suite is terminal.

| Field | Description |
|-------|-------------|
| `name` | The script's `testRef`. |
| `iterations` | Finished iterations of this scenario. |
| `passed`, `failed`, `errored` | `iterations` broken down by outcome. |
| `meanDurationMs` | Mean wall-clock duration of the finished iterations. |
| `assertionsPassed`, `assertionsTotal` | Assertion results summed over the finished iterations. |
| `passRate` | Share of the finished iterations that passed, 0 to 100, rounded. |
| `passAllK` | The chance that all k iterations pass, 0 to 100, rounded. See [Consistency](#consistency-pass-and-pass-k). |
| `checkScenario` | `true` when at least three iterations finished and none passed. See [Consistency](#consistency-pass-and-pass-k). |
| `inventedNameRuns` | Finished iterations whose answer named a resource that no tool result of the run held (see [Invented names](#invented-names)). Counted from the run's transcripts when the suite is terminal; absent when none. |

On a re-judge suite the rows come from the copied transcripts, with the durations of the source run.

### Consistency: pass rate and pass^k

The mean quality in `status.judge` says how good the answers are on average. It does not say whether every trial passed. For a crew people rely on, that is the number that matters, so each scenario row also carries:

- `passRate`: the share of finished trials that passed.
- `allPassed`: observed, true when every finished trial passed.
- `passAllK`: an estimate of the chance that all k trials pass, from the per-trial pass rate raised to the power k, where k is the number of finished trials of that scenario. It falls fast as k grows: a scenario that passes 75% of the time passes three trials in a row about 42% of the time.
- `checkScenario`: true when at least three trials finished and none passed. A scenario that never passes usually has a broken script or reference, not an incapable crew, so read its transcripts before judging the crew. Fewer than three trials are too few to call a pattern, so a scenario is not flagged before then.

`status.consistency` rolls the rows up: `meanPassAllK` is the mean of the scenarios' `passAllK` over the scenarios with a finished trial, `scenariosToCheck` counts the flagged scenarios, and on a suite with `spec.purpose: Regression`, `regressions` counts the scenarios below a 100% pass rate. A trial passes when its assertions pass (phase `Passed`); a failed or errored trial does not. `kubectl get crewfitnesssuites` shows the mean in the `PassAllK` column.

The XLSX carries the same figures: the Scenarios sheet has `pass_all_k_estimated`, `check_scenario` and `all_passed` columns after `fabrication_pct`, and the Overview tab lists the trial isolation, the purpose, the estimated mean pass^k, and the scenarios to check. The Overview tab reads them from the suite status, so they are absent on a suite that finished before the fields existed.

Balanced scenarios keep these numbers honest. Include scenarios where the right answer is to stand aside or to say the data is unavailable beside the ones where the crew should act, so tuning toward one side shows up as a drop in the other.

### Independent trials

> **Changed default.** A suite without `trialIsolation` now clears the crew's memory before every trial and runs the trials one at a time, ignoring `spec.concurrency`. Earlier runs shared memory across trials, so their scores are not comparable with new ones: treat them as `PerSuite`. Set `trialIsolation: PerSuite` to restore the earlier behavior.

A trial should start from a clean environment. State shared between trials makes failures correlated and can leak an answer from one trial to the next: a fact the crew stores in the first trial would help the second trial of the same scenario.

With the default `trialIsolation: PerTrial`, the operator clears the crew's working memory before every trial, and runs trials one at a time whatever `spec.concurrency` says (crew memory is per crew, so two trials in flight would share it). If the clear fails, the trial is not created, the `TrialsIndependent` condition reads `False` with reason `PurgeFailed`, and the reconcile retries (the condition returns to `True` once a clear succeeds), because a trial on unclear memory would not be independent. Set `trialIsolation: PerSuite` to clear once before the first trial and let later trials build on earlier ones, which measures learning. Setting `purgeMemory: false` turns clearing off entirely.

The suite's `TrialsIndependent` condition says what held, from the moment the suite starts: `True` with reason `PerTrialPurge`, or `False` with reason `SharedAcrossTrials` (`PerSuite`), `PurgeDisabled` (`purgeMemory: false`), `NoMemoryStore` (the operator has no message bus to clear memory with), or `PurgeFailed` (a clear failed and the next trial is held). The XLSX Overview tab repeats it, so a result says whether its trials were independent.

Crew memory is not the only state a trial can leave behind. The harness pods are per iteration, and the thread's workspace is deleted when the iteration ends.
### Realm: what a run ran on

Infrastructure alone moves agentic eval scores by several points, more than the gap between many models. A score is only comparable to another that ran on the same realm, so every run records its realm in `status.realm`, and the XLSX carries it.

The operator takes one snapshot when the run starts (`status.realm.start`) and one when it reaches a terminal phase (`status.realm.end`). A snapshot holds:

| Field | What it records |
|-------|-----------------|
| `operatorVersion`, `crewVersion` | The operator build version, and the crew chart version from the crew's `kubemoot.ai/crew-version` label. |
| `trialIsolation` | How the run's trials were isolated: `PerTrial`, `PerSuite`, or `None` (see [Independent trials](#independent-trials)). |
| `modelServers[]` | The model servers the crew can use: those hosting a model that a rule of a `CrewSchedulingPolicy` for the crew can select; every model server when no policy applies to the crew or no rule matches a model. Each model server: engine, engine version, readiness, and its `accelerator` capability: `memoryMiB`, `memoryUsedMiB`, `maxParallel`, `contextLength`. The accelerator is described by capability, never by vendor or device name. |
| `modelServers[].models[]` | Each model on the server: name, `digest`, `quantization`, `contextLength`, and `resident`, whether it was loaded on the accelerator at that moment. At the start, `resident` tells a cold run from a warm one. |
| `agents[]` | One row per distinct container shape across the crew's running pods: container, image, pod count, CPU and memory requests and limits. The fitness runner's own pods are left out. |
| `resourceNotes` | Agent resources likely to throttle an agent: a request that is unset (no guaranteed allocation), or a limit equal to the request (no headroom). Guidance is a guaranteed allocation plus a separate, higher hard limit. |
| `concurrentSuites`, `concurrentRuns` | Other suites in the `Running` phase, and standalone `CrewFitness` runs in flight. A measurement expects none. |

The operator reads model server and model status as the model server controller last probed it, so residency and memory in use can lag the engine by one probe interval. Which model serves each call is chosen per call by the scheduler, so the realm records what was available and resident, not a per-call log. Other discussions on the crew are not recorded: they are not Kubernetes objects.

`status.realm.digests` hashes the parts of the start snapshot that decide comparability, one hash each: `servers` (name, engine, engine version, accelerator capability), `models` (name, digest, quantization, context length), `versions` (operator, crew chart, container images), `resources` (requests and limits), and `isolation` (the trial isolation). Time, residency, memory in use, pod counts, and concurrent activity are recorded but left out of the digests, because they change between runs on an unchanged system.

**Comparing runs.** Two runs are comparable when their digests are equal. When a digest differs, the part that differs names what to look at, and a score gap between the runs may be the realm rather than the crew. The dashboard Fitness Suites page shows each run's realm and warns when it differs from the previous run of the same crew, naming the parts. A run with no realm (it ran before this field existed, or the operator could not read the cluster) cannot be compared and the page says so. The comparison rules live in the `pkg/fitnessrealm` package, which any client of the status can import: `DigestsOf`, `DifferingParts`, `Differences`, `Drift`, and `ResourceNotes`.

**Drift.** `status.realm.drift` lists what changed between the start and the end of the run: an engine upgraded, a model replaced, an agent pod rescheduled with other limits, another suite started. A run with drift measured a moving system. Models loading during the run is expected and is not drift.

**Re-judge suites.** The answers of a re-judge suite ran on the source run's realm, so `status.realm` is a copy of the source's, with `copiedFrom` naming the source run.

If the start snapshot cannot be read (an API server error, or the crew cannot be read), the operator retries it on each reconcile until the first iteration finishes, and then records no realm rather than a partial one. A crew without the `kubemoot.ai/crew-version` label is hand-applied and has no version; that is not a failure. A failed end snapshot is retried on each terminal reconcile until it is saved. a snapshot with a part missing would differ from a complete one and raise a false warning. An end snapshot is recorded only for a run that has a start.

The XLSX has a `Realm` tab with the digests, the drift, and both snapshots, one row per fact. The Overview tab shows the digests and the drift count, or says the run recorded no realm.

### Invented names

A run is flagged when its answer names a resource that none of the run's tool results contained. The check is deterministic and changes no score or pass/fail: it is a flag, shown in the `invented_names` column of the XLSX Runs sheet (the names, comma separated) and as the count `inventedNameRuns` in `status.scenarios`.

How it works:

1. When an iteration ends, the fitness runner reads the thread's stored tool calls from the discussion workspace, before the workspace is deleted. A call is an object under `<namespace>/<crew>/<threadId>/` whose `gen_ai.operation.name` header is `execute_tool`. Its tokens are the evidence: the result, an error result included (the run was told "pod x not found"), and the call's arguments (the run asked for it).
2. The check runs only when the evidence covers the run. It is skipped, and the transcript records no names and `evidenceCaptured` false, when no call could be read, a list or read failed (only a bucket that was never created is skipped; any other bucket error skips the check), a result was cut at the size cap, a call that the answering thread's findings name (`[CALL key=...]`) is still not stored after the runner has listed again until all named calls are stored (a 30-second safety net ends the wait), or the token set outgrew its bound. A run that waited the full 30 seconds for a named call records `evidenceStragglerTimeout` in its transcript, so a systematic gap shows when the transcripts of a suite are counted. A skipped run is not flagged.
3. The runner reads the names the answer asserts from name positions only: the first token of a list item, bold text, a kind word (`node`, `pod`, `namespace`, `deployment` and similar) followed by a token, a table's first cell, a code span that is one token, both sides of a `namespace/name` pair, and one-token lines of a fenced block. Placeholders such as `<node-name>` are skipped.
4. A name is reported when it is in none of the run's tool calls and not in the question, the scenario's assertions (its reference and terms), or the crew's own agent and accelerator names. The runner stores only the reported names, `inventedNames`, in the transcript; the operator copies them to the Runs sheet and the status count.

To keep false positives low, a token counts as a name only when it is a lowercase DNS-1123 label with a letter. In a weak position (a list lead, bold text, the token after a kind word) it also needs a digit, because English compounds such as `high-memory`, `out-of-memory` and `in-flight` are hyphenated, unless the clause that follows the name gives it a pod or node state, where a hyphen is enough (`- **nginx-ingress**: ImagePullBackOff`). A state counts in kubectl's spelling only: `ImagePullBackOff`, `ErrImagePull`, `CrashLoopBackOff`, `OOMKilled`, `CreateContainerConfigError`, `NotReady` and `Unschedulable` anywhere in the clause; `Pending`, `Terminating` and `Evicted` only as a status (right after the name's colon, after `status:`, or as a table cell), because "the fix is pending review" and "keep terminating gracefully" are English. A clause ends at a semicolon, a sentence end or the next bold name, so a state word lends strength to the name before it and to no other; in a strong position (a table's first cell, a code span, a pair of two hyphenated sides) a hyphen is enough. Versions, sizes, IP addresses, dates, dotted host names, capitalized words, plain words, `kube-system`, `kube-public`, `kube-node-lease`, `x86-64` and a short list of Kubernetes terms (`control-plane`, `ephemeral-storage`) are never reported. Matching is exact on whole tokens, so `node-1` is not found in `node-12345`. A name also counts as found when a token of the run is that name followed by one or two generated segments, the owner name of a pod, job or ReplicaSet. A generated segment is digits only (a StatefulSet ordinal, a CronJob's scheduled time) or 5 to 10 characters from Kubernetes' generated-name alphabet, which has no vowels and no `0`, `1` or `3` (a pod suffix, a ReplicaSet hash). `gpu-swap-monitor` is found in `gpu-swap-monitor-29914110-vmnfv`, but `kube-scheduler` is not found in `kube-scheduler-extra-config`, `web-front` is not found in `web-front-end-7d9f`, and `node-1` is not found in `node-12345`. JSON results are read as JSON, so an escape such as `\n` separates words. An invented name that is a plain word, or hyphenated without a digit in a weak position, goes unflagged. The flag is for review, not a verdict. It is separate from the Runs sheet's `fabricated` column, which is true when a `NOT CONTAIN` forbidden-claim assertion failed.

The runner reads the workspace buckets (`kubemoot_workspace` and `kubemoot_discussion_artifacts`) over the same anonymous NATS connection it uses to write the transcript. On an install that authenticates NATS, wire the same credentials into the runner Job and grant it read on both buckets; until then the connection fails, the run is not checked, and no name is flagged.

### status.judge

| Field | Description |
|-------|-------------|
| `phase` | `Pending`: the run has not finished, and the judge waits for it. `Judging`: the run finished and the judge is scoring it. `Complete`: the pass is over; every judgeable scenario has a score or is listed in `unjudged`. `Skipped`: the suite was cancelled, so the judge does not run. |
| `judged` | Scenarios scored so far. Unjudged scenarios are not counted. |
| `total` | Scenarios the judge has to score: those with a `DEFER` assertion and at least one transcript. It is set when the pass starts, and counts judged and unjudged scenarios. |
| `mean` | Mean scenario score over the judged scenarios only, 0 to 100, rounded. Absent when no scenario was judged. |
| `zeros` | Judged scenarios that scored 0. A scenario whose every iteration failed its consensus floor scores 0 without a judge call. |
| `completedAt` | When the operator recorded the pass as complete. |
| `scores` | Each judged scenario in spec order: `scenario`, `score` (0 to 100, rounded), and `reason`, the judge's rationale on one line, cut to 200 characters. |
| `unjudged` | Each scenario the judge gave up on, in spec order: `scenario` and `reason`, one line cut to 200 characters. Absent when there are none. |

The judge writes `status.judge` when its pass starts, after each scenario it scores, and when it finishes, so `judged` out of `total` shows progress while the pass runs. Once `phase` is `Complete` or `Skipped` the field is final: it is not rewritten, even after the judge checkpoint passes its object store retention. While judging, `judged` never goes down: if the checkpoint expires before the pass completes, status keeps the scores it recorded. `status.judge` is absent when the operator has no NATS Object Store configured, since there is no judge.

**Fitting the document to the judge's window.** The judge receives each scenario's comparison document: the question, the reference, and every answer's synthesis with the evidence behind it. The operator finds the window the judge crew's agents have: for each model their `mulling` rule admits (the judge crew's rules are the same for every phase), the window each request gets from its model server, and the smallest of these. A server's status gives it in this order: the window of the loaded model, the window calibration measured, the server-wide setting, and only then the catalog's `contextLength`, which for Ollama is the model's trained context, not the one the server serves. The document is kept within that window, less a reserve for the judge's reply (the larger of 1024 tokens and an eighth of the window) and a quarter of the window for the judge crew's own prompts, counting 3 bytes of JSON to a token as agent-runtime does for dense text.

When the document is larger, the question, reference and syntheses stay whole and the evidence is cut: items shorter than a shared cap are kept, longer ones keep their start and end with a note of how many characters (not bytes) were left out, and the document carries `evidenceTrimmed` with the number of items cut and characters omitted. When the claims alone are larger than the budget, all evidence is reduced to its omission note, the document is sent anyway, the operator logs its size and the budget, and the judge's own `CONTEXT_EXCEEDED` report marks the scenario unjudged. When no server declares a window, the document is sent whole.

**Unjudged scenarios.** The judge never retries a scenario without bound. Each judge pass makes one attempt per scenario still pending, and the attempt count is kept in the judge checkpoint, so an operator restart does not reset it. A scenario becomes unjudged in two cases:

- The judge reports that the comparison document does not fit its context window (a `failure` signal of type `CONTEXT_EXCEEDED`, or a response with no verdict that names it). The same document would fail the same way, so the scenario is unjudged at once.
- Any other failure (no verdict in the response, a transient error, no crew declaring the scenario's keyword) is retried until the scenario has used 3 attempts, then the scenario is unjudged.

When every scenario is judged or unjudged, `phase` becomes `Complete`: a partly judged run reads as done with a visible gap (`judged` below `total`, and the scenarios and reasons in `unjudged`), not as failed. Unjudged scenarios have no score in the XLSX. The bound counts attempts, not elapsed time.

`kubectl get crewfitnesssuites` shows the judge phase and the mean score in the `Judge` and `Quality` columns.

### What stays in the object store

The status carries summaries. The per-iteration transcripts (question, answer, events, assertion messages), the full judge reasons, the judge checkpoint, and the XLSX report stay in the NATS Object Store bucket `kubemoot_fitness_artifacts`, under `<namespace>/<suite>/<runId>/`, and `status.artifactRef` names the XLSX.

### Size bound

Status is stored in etcd with the rest of the object, so both lists are capped at 300 entries (the first 300 distinct `testRef` values in spec order), scenario names at 253 bytes, and reasons at 200 characters. The API server rejects a longer list. `judged`, `total`, `mean`, and `zeros` always cover every scenario, so `judged` greater than the length of `scores` means the list was cut. A `testRef` longer than 253 bytes is cut in status, so keep names shorter than that to keep them distinct. For 300 scenarios with 30-character names and full reasons, the two lists take about 125 KiB; at the field maximums (253-byte names, reasons of 200 four-byte characters) they take under 450 KiB, well below the 1 MiB an object should stay under. A suite with inline `testContent` also stores its scripts in the spec, so keep very large script sets in a ConfigMap.

## Pause, resume and stop

Two spec fields control a running suite. Edit them with `kubectl patch`; the dashboard Fitness page shows the command for each action, and CrewForge has the same controls.

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

A `Cancelled` suite is not judged at all. Stop means no further work on the accelerators, and a judge pass over a partial run would score scenarios from fewer iterations than planned. The quality columns of a cancelled suite's XLSX stay unjudged. To score a run, let it reach `Completed`.

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
5. **Judge.** The deferred judge pass then scores the copies exactly as it scores a normal run, against this suite's `DEFER` references, one scenario at a time with the same resumable checkpoint. `status.judge` reports `judged` out of `total` until it finishes, and the XLSX is written once judging is complete.

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

Any suite, re-judge or not, reaches `Completed` before its judge pass finishes, so `kmctl fitness run` returns before the scores exist. A re-judge suite reaches `Completed` as soon as its transcripts are copied. Until judging ends, the XLSX served is provisional with quality 0; the operator rewrites it with the final scores. `kmctl fitness get` shows the judge progress and scores from `status.judge`. Do not pass `--scenario` for a re-judge suite: it creates a live `CrewFitness` that asks the crew the question.

## Deleting a suite

Deleting the `CrewFitnessSuite` removes everything it owns. Its `CrewFitness` runs, their Jobs, and their pods are garbage-collected through owner references, and the operator's finalizer (`kubemoot.ai/fitness-artifacts`) purges the run's XLSX, transcripts, and judge scores from the Object Store before the resource goes. Delete a suite when you want the run and its results gone; stop it when you want to keep what completed.

## Stopping a single CrewFitness

A standalone `CrewFitness` (one scenario, one run) has no stop field. Deleting it is the stop: its Job and pod are garbage-collected through owner references, and it has no partial result worth keeping.

```bash
kubectl delete crewfitness node-count-check -n crew-my-crew
```
