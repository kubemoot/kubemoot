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
| `scripts` | list | required, at least 1, unless `tier` is set | The scripts to run, in order. Each entry has a `testRef` and exactly one of `testContent` (inline script) or `configMapRef` (ConfigMap holding `<testRef>.adl`). With `tier` set, leave it out: the operator writes the scripts it resolves here when the suite starts. |
| `tier` | `smoke`, `standard` or `full` | | Run a named part of the crew's own scenarios instead of listing scripts. Set at creation, immutable. See [Tiers](#tiers). |
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
| `passed`, `failed`, `errored` | `iterationsCompleted` broken down by outcome of the gates (the deterministic assertions). Not the judge's score: see [Pass and score](#pass-and-score). |
| `artifactRef` | Bucket, object key (`<namespace>/<suite>/<runId>.xlsx`), and size of the XLSX, set once it is written. |
| `error` | Why the suite itself failed to execute (phase `Error`), in one sentence. A tool smoke failure is in the `ToolSmoke` condition message instead. See [Pass and score](#pass-and-score). |
| `toolSmoke` | The table the tool smoke produced: `passed`, `failures`, and one row per tool (`server`, `tool`, `invoked`, `passed`, `error`, `durationMs`), failures first. See [Tool smoke](#tool-smoke). |
| `tier` | On a suite with `spec.tier`: the tier, the ConfigMap it was read from, the scenarios it chose, and how many scenarios the crew has. See [Tiers](#tiers). |
| `pathCoverage` | Which call paths the run exercised, read from its own transcripts, and the paths it missed. See [Path coverage](#path-coverage). |
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
| `Completed` | yes | Every iteration reached a terminal phase. Failed or errored iterations are outcomes in the counts and in `failureReason`, not a failure of the suite itself; a client that wants a pass or fail verdict reads `failed` and `errored`. See [Pass and score](#pass-and-score). |
| `Cancelled` | yes | `spec.cancel` stopped the suite. Completed iterations are kept and the XLSX is marked partial. |
| `ToolSmokeFailed` | yes | The tool smoke found a failing MCP tool, so the suite stopped before any model ran. No iteration was created and no artifact is written. See [Tool smoke](#tool-smoke). |
| `Failed`, `Error` | yes | The suite itself could not execute, such as an invalid spec. |

## Tiers

A crew's fitness suite is as large as its scenarios, and most changes do not need all of them. A tier names how much of the suite a run covers, so one set of scenarios serves all three:

| Tier | Covers | For |
|------|--------|-----|
| `smoke` | A few scenarios chosen so that together they take every call path: triage, the tool loop, simple calls, thinking on and off, each model and provider the crew uses, one question with a trap. Minutes, not hours. | Call plumbing, client libraries, engine adapters, operator wiring, image or chart changes. It shows that the paths work, not that the answers are good. |
| `standard` | The scenarios a change to crew behavior is gated on. | Features that change what the crew does. |
| `full` | Every scenario the crew ships, labelled or not. Use several iterations when two arms are compared. | Prompt, model and scheduling decisions, and measurements of record. |

Compare results only within a tier, on the same judge references. A tier is fixed by what the scenarios say, not picked by hand for each change, so two runs of one tier cover the same scenarios until a scenario's label changes.

A scenario names the tiers it belongs to in an ADL constant:

```text
DEFINE CONST TIERS AS "smoke standard"
```

The words are `smoke` and `standard`, separated by spaces or commas. A scenario with no `TIERS` constant belongs to no named tier and runs only in `full`. The label lives in the scenario, so a tier is reviewed in the same change that edits the scenario, and a crew author chooses its own tiers. Only ADL scenarios carry the label; a prose Markdown scenario runs in `full` only.

To run a tier, set `spec.tier` and leave out `spec.scripts`:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: CrewFitnessSuite
metadata:
  name: homelab-smoke
  namespace: crew-homelab-pilot
spec:
  crewRef: homelab-pilot
  iterations: 1
  tier: smoke
```

When the suite starts, the operator reads the ConfigMap `<crewRef>-fitness` in the suite's namespace, which the crew chart ships (one key per scenario, `<name>.adl`), keeps the scenarios that name the tier (every scenario for `full`), orders them by name, and writes them into `spec.scripts` with their text inline. The suite then keeps the text it ran, even if the crew is upgraded and its scenarios change while the suite runs, and every consumer of a suite (the report, the re-judge, `kmctl fitness scenarios`) reads the scripts as usual. `status.tier` records what was resolved:

```yaml
status:
  tier:
    name: smoke
    source: homelab-pilot-fitness
    available: 76
    scenarios: [k8s-pod-status, proxmox-vm-list, smoke-cluster-namespaces]
    resolvedAt: "2026-11-23T14:02:11Z"
```

The suite ends in phase `Error`, with the reason in `status.error` and no model asked, when:

- the ConfigMap `<crewRef>-fitness` does not exist in the namespace;
- the ConfigMap holds no scenario;
- no scenario carries the tier (a tier that selects nothing would measure nothing);
- a scenario names a tier the operator does not know (a typo such as `smok` must not shrink a tier without a word), or names `full`, which needs no label;
- `spec.scripts` is also set (the tier chooses the scripts; set one or the other).

The API server refuses a suite at creation when `spec.tier` is not `smoke`, `standard` or `full`, when it is empty, when neither `spec.scripts` nor `spec.tier` is set, when `spec.tier` is combined with `spec.rejudge`, and any later change to `spec.tier`.

## Path coverage

A smoke run exists to take every call path, so the suite says which paths its run took. Once the run is terminal, `status.pathCoverage` counts the run's placed model calls, read from the scheduler's records in the run's own transcripts (the receipt of each discussion), not from what the suite was meant to do:

```yaml
status:
  pathCoverage:
    calls: 87
    transcripts: 8
    kinds:     [{name: tool-turn, calls: 52}, {name: triage, calls: 21}, {name: simple, calls: 14}]
    phases:    [{name: evaluation, calls: 52}, {name: triage, calls: 21}, {name: synthesis, calls: 8}, ...]
    models:    [{name: qwen3:8b, calls: 61}, {name: qwen3:32b, calls: 26}]
    providers: [{name: vllm, calls: 70}, {name: ollama, calls: 17}]
    thinking:  [{name: "off", calls: 66}, {name: "on", calls: 21}]
    gaps:      ["no call on provider ollama"]
```

| Field | Meaning |
|-------|---------|
| `calls`, `transcripts` | The placed calls counted, and the iterations whose transcript held at least one. `calls: 0` means the transcripts held none (a run from before the receipt was kept in the transcript) and the other fields are empty. |
| `kinds` | The path that sent the call: `triage`, `tool-turn` (a turn of the tool loop) or `simple` (a call with no tools). |
| `phases` | Where in the thread the call happened: `advisory`, `selection`, `triage`, `evaluation` or `synthesis`. |
| `models`, `providers` | The model the call ran on, and the engine type of the model server it ran on (the `engine` of the realm's model servers). A name the operator cannot resolve counts as `unknown`. |
| `thinking` | The thinking setting of the agent that made the call: `on` or `off` when the Agent sets `spec.think`, `default` when it leaves the model's own default, `unknown` when no agent of the crew has that name. The setting is the agent's configuration, not a flag on the call. |
| `gaps` | What the crew has that the run never took: a call kind or phase with no call, an engine type among the crew's model servers with no call, a thinking setting some agent declares with no call. Empty means every declared path was exercised. |

The operator writes the table for every suite, tiered or not. An empty `gaps` on a smoke run is the evidence that the plumbing was covered; a non-empty one says which path to add a scenario for, or which one the run could not reach. Models have no declared list to compare with, because the scheduler picks the model at call time, so the table reports the models used and leaves the comparison to the reader.

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

## Pass and score

A suite reports two separate things about every scenario. They are measured by different parts of the system and they can disagree.

| | Pass (the gates) | Score (the judge) |
|---|---|---|
| Question it answers | Did the run satisfy the scenario's deterministic `ASSERT` lines? | How close is the crew's answer to the scenario's reference? |
| Where it is | `passed`, `failed` and `errored` on the suite and on each row of `status.scenarios`, plus `passRate`, `allPassed` and `passAllK` | `status.judge.scores`, `status.judge.mean` (the `Quality` column) |
| Computed by | The assertion engine, inline, while each iteration runs | The deferred judge crew, once the suite is terminal |
| Scale | Counts of iterations | 0 to 100 per scenario |

**Pass.** An iteration is `Passed` when every assertion it evaluated passed, `Failed` when any assertion failed, and `Errored` when it could not run or recorded no assertions. The gates are mechanical checks: the discussion endpoint returned 200, the agree floor was met, a term is present or absent in the answer. A `DEFER` assertion does not gate. It records a placeholder that passes ("deferred to post-suite judge crew") and leaves its judgment to the score. An assertion kind the engine has no check for also passes, with a note that manual review is recommended.

**Score.** The score is the judge's reading of the answers against the reference in the `DEFER` assertion, averaged over the scenario's iterations, 0 to 100: the judge scores every iteration. Each iteration's score, fabrication flag and gated state are in the judge checkpoint, the object `deferred-scores-v2.json` under `<namespace>/<suite>/<runId>/` in the artifact bucket, in its `answers` map (scenario to a list of `{iteration, score, fabrication, gated, reason}`); status keeps only the counts and means, to stay small. An iteration that failed its consensus floor or returned an empty answer counts as 0 without a judge call. The judge reads the answer text and the evidence behind it; it does not read the gates.

**Why they disagree.** The two measure different things, so neither overrides the other:

- *Passed with a low score.* Every gate held (the crew agreed, answered, and used the required terms) but the answer is far from the reference, for example it states cluster facts no tool result held. The gates cannot see that. Read the score and its `reason`.
- *Failed with a high score.* The answer matches the reference but a gate failed: a forbidden term appeared, a required term was missing from the exact wording, or the agree floor was not met. The judge does not see the gate. Read `failureReason` on the scenario row.

The dashboard, CrewForge and `kmctl fitness get` show them in separate columns, gates passed and failed beside the judge score. Neither number is wrong. Neither is changed to match the other: the scores are the judge's verdict against the references as written, and the gates are the script's own checks. A suite is *clean* when no iteration failed or errored (the gates) and its scores meet your bar (the judge); a client that reports a single verdict should say which of the two it means.

**Why a suite did not pass.** The status says it in plain words, by case:

| Case | Where the reason is |
|------|---------------------|
| The suite could not execute (phase `Error`) | `status.error` |
| The tool smoke failed (phase `ToolSmokeFailed`) | The `ToolSmoke` condition message, for example `9 tool(s) failed the smoke, no model ran; proxmox-mcp/list_nodes: ...`, and the table in `status.toolSmoke` |
| The suite completed with failed or errored iterations (phase `Completed`) | `failureReason` on each scenario row of `status.scenarios` (the first failed gate), with the counts above it. The full assertion messages of every iteration are in the XLSX and the transcripts |
| The judge could not score a scenario | `status.judge.unjudged` |

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
    judgedAnswers: 30
    answerMean: 44
    fabrications: 1
    completedAt: "2026-10-02T14:31:07Z"
    scores:
      - scenario: gpu-utilization-live
        score: 0
        reason: all iterations gated (consensus failed or empty synthesis)
        answers: 15
        gated: 15
      - scenario: node-count
        score: 87
        reason: Correct node count and roles; omits the control-plane taint the reference names.
        answers: 15
        fabrications: 1
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
| `passed`, `failed`, `errored` | `iterations` broken down by outcome of the gates. A passed iteration can still score low with the judge. |
| `failureReason` | One line, at most 160 characters: the message of the first failed assertion of the first failed iteration, or the error of the first errored one. Absent when every finished iteration passed. |
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
| `judgedAnswers` | Iterations scored so far over every judged scenario, consensus-gated iterations included (they score 0). `judged` counts scenarios; this counts the answers behind them. Absent for a run judged before answers were kept. |
| `answerMean` | Mean score of the judged iterations, 0 to 100, rounded: every iteration counts once. `mean` is the mean of the scenario scores, so the two differ when scenarios have different iteration counts. Absent when any judged scenario has no per-answer record (a run judged before answers were kept). Compare two runs on `answerMean`, or on the per-iteration scores in the judge checkpoint (see below). |
| `fabrications` | Judged answers the judge flagged as fabricated. The flag is advisory: it does not change the score, which already carries the rubric's deduction. |
| `completedAt` | When the operator recorded the pass as complete. |
| `scores` | Each judged scenario in spec order: `scenario`, `score` (the mean of its iteration scores, 0 to 100, rounded), `reason` (the judge's rationale for the lowest-scoring judged answer, on one line, cut to 200 characters), `answers` (iterations scored), `gated` (iterations scored 0 without a judge call) and `fabrications` (answers the judge flagged). The fields after `reason` are absent on a run judged before answers were kept. |
| `unjudged` | Each scenario the judge gave up on, in spec order: `scenario` and `reason`, one line cut to 200 characters. Absent when there are none. |

The judge writes `status.judge` when its pass starts, after each scenario it scores, and when it finishes, so `judged` out of `total` shows progress while the pass runs. Once `phase` is `Complete` or `Skipped` the field is final: it is not rewritten, even after the judge checkpoint passes its object store retention. While judging, `judged` never goes down: if the checkpoint expires before the pass completes, status keeps the scores it recorded. `status.judge` is absent when the operator has no NATS Object Store configured, since there is no judge.

**Fitting the document to the judge's window.** The judge receives each scenario's comparison document: the question, the reference, and every answer's synthesis with the evidence behind it. The operator finds the window the judge crew's agents have: for each model their `mulling` rule admits (the judge crew's rules are the same for every phase), the window each request gets from its model server, and the smallest of these. A server's status gives it in this order: the window of the loaded model, the window calibration measured, the server-wide setting, and only then the catalog's `contextLength`, which for Ollama is the model's trained context, not the one the server serves. The document is kept within that window, less a reserve for the judge's reply (the larger of 1024 tokens and an eighth of the window) and a quarter of the window for the judge crew's own prompts, counting 3 bytes of JSON to a token as agent-runtime does for dense text.

**Scoring every iteration.** A scenario's comparison document holds one answer for each iteration that passed its consensus floor and returned an answer, in iteration order; each answer in the document carries an `index`, and the judge returns one score and one fabrication flag for each, echoing that index. An iteration that failed the floor or returned nothing is scored 0 without a judge call and counted as `gated`. When the answers do not fit the window together with their evidence whole, the operator splits them over several documents, packed in iteration order, and dispatches them one after another (the judge contends for the same accelerators as the crews, so it runs only after the suite completes and one document at a time); each part counts only its own answers. The indexes are used only when they are exactly the document's (one score per answer, every index present, none repeated or out of range). Otherwise the scores are read by position, but only when there is one score per answer; when no score can be tied to an answer, every answer counts 0. The scenario score is the mean of the iteration scores. A scenario is judged once every part has a verdict. Each part's scores are kept in the checkpoint as it finishes, so a failed part is retried alone.

When one answer is larger than the window by itself, the question, reference and synthesis stay whole and the evidence is cut: items shorter than a shared cap are kept, longer ones keep their start and end with a note of how many characters (not bytes) were left out, and the document carries `evidenceTrimmed` with the number of items cut and characters omitted. When the claims alone are larger than the budget, all evidence is reduced to its omission note, the document is sent anyway, the operator logs its size and the budget, and the judge's own `CONTEXT_EXCEEDED` report marks the scenario unjudged. When no server declares a window, the document is sent whole.

**Unjudged scenarios.** The judge never retries a scenario without bound. Each judge pass makes one attempt per scenario still pending, and the attempt count is kept in the judge checkpoint, so an operator restart does not reset it. A scenario becomes unjudged in two cases:

- The judge reports that the comparison document does not fit its context window (a `failure` signal of type `CONTEXT_EXCEEDED`, or a response with no verdict that names it). The same document would fail the same way, so the scenario is unjudged at once.
- Any other failure (no verdict in the response, a transient error, no crew declaring the scenario's keyword) is retried until the scenario has used 3 attempts, then the scenario is unjudged.

When every scenario is judged or unjudged, `phase` becomes `Complete`: a partly judged run reads as done with a visible gap (`judged` below `total`, and the scenarios and reasons in `unjudged`), not as failed. Unjudged scenarios have no score in the XLSX. The bound counts attempts, not elapsed time.

`kubectl get crewfitnesssuites` shows the judge phase and the mean score in the `Judge` and `Quality` columns.

### What stays in the object store

The status carries summaries. The per-iteration transcripts (question, answer, events, assertion messages), the full judge reasons, the judge checkpoint, and the XLSX report stay in the NATS Object Store bucket `kubemoot_fitness_artifacts`, under `<namespace>/<suite>/<runId>/`, and `status.artifactRef` names the XLSX.

### Size bound

Status is stored in etcd with the rest of the object, so both lists are capped at 300 entries (the first 300 distinct `testRef` values in spec order), scenario names at 253 bytes, and reasons at 200 characters (failure reasons at 160). The API server rejects a longer list. `judged`, `total`, `mean`, and `zeros` always cover every scenario, so `judged` greater than the length of `scores` means the list was cut. A `testRef` longer than 253 bytes is cut in status, so keep names shorter than that to keep them distinct. For 300 scenarios with 30-character names and full reasons, the two lists take about 125 KiB (failure reasons add up to 190 KiB if every scenario has one); at the field maximums (253-byte names, reasons of 200 four-byte characters, failure reasons of 160) they take under 650 KiB, well below the 1 MiB an object should stay under. A suite with inline `testContent` also stores its scripts in the spec, so keep very large script sets in a ConfigMap.

## Pause, resume and stop

Two spec fields control a running suite. `kmctl fitness pause SUITE`, `kmctl fitness resume SUITE` and `kmctl fitness stop SUITE` set them with your own credentials (each needs `patch` on `crewfitnesssuites`, and `stop` asks first unless you pass `--yes`); `kubectl patch` sets the same fields, the dashboard Fitness page shows the `kubectl patch` command for each action, and CrewForge has the same controls.

**Pause** sets `spec.suspend: true`. The iteration in flight finishes and its result is kept; no new iteration starts. The phase stays `Running` until that iteration finishes, then becomes `Paused`. A `Paused` suite puts no load on the crew, which makes it safe to change the crew or the cluster between iterations.

**Resume** sets `spec.suspend: false`. The phase returns to `Running` and the suite continues at the next iteration it has not yet run. Completed iterations are not repeated.

**Stop** sets `spec.cancel: true`. The operator deletes any iteration in flight (its Job and pod follow through owner references), starts no new iteration, sets `completedAt`, and moves the suite to `Cancelled`. Stop works from `Pending`, `Running`, or `Paused`, takes precedence over `suspend`, and cannot be undone: clearing `cancel` afterwards does not restart the suite. Both fields are ignored once the suite is terminal.

```bash
# Pause, resume and stop with kmctl
kmctl fitness pause homelab-baseline -n crew-homelab-pilot
kmctl fitness resume homelab-baseline -n crew-homelab-pilot
kmctl fitness stop homelab-baseline -n crew-homelab-pilot

# The same with kubectl: pause
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
