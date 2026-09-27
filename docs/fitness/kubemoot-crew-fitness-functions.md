---
title: "Kubemoot Crew Fitness Functions"
weight: 1
---

Kubemoot implements architectural fitness functions as first-class Kubernetes resources. The `CrewFitness` CRD allows operators and crew designers to define, execute, and observe fitness tests against deployed agent crews - declaratively, headlessly, and with automatic cleanup.

This document describes how Kubemoot adapts the fitness function concept from Richards and Ford's *Architecture as Code* to the domain of AI agent crew orchestration.

## From ADL to Kubernetes

Richards and Ford define an architectural fitness function as "any mechanism that provides an objective integrity check on some architectural characteristic." In Kubemoot, the architectural characteristic under test is the **crew's discussion behavior** - does the crew answer questions correctly, does it stand aside on irrelevant input, does the coordinator synthesize Tooler and Analyst contributions?

ADL (Architecture Definition Language) provides the pseudo-code format. Kubemoot makes it executable by:

1. **Storing** ADL test definitions in a ConfigMap alongside the crew
2. **Declaring** a `CrewFitness` CR that references a specific test
3. **Running** a Job that POSTs to the crew's discussion endpoint, streams SSE signals, and evaluates assertions
4. **Reporting** per-assertion pass/fail results in the CR's status
5. **Cleaning up** automatically via an optional TTL

## The CrewFitness CRD

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: CrewFitness
metadata:
  name: hello-world-health
  namespace: crew-hello-world
spec:
  crewRef: hello-world                     # Crew CR in same namespace
  testRef: discussion-health               # key in ConfigMap (without .adl)
  configMapRef: hello-world-fitness-tests   # ConfigMap with ADL tests
  ttl: 24h                                 # auto-delete after completion
```

### Status

After the fitness runner Job completes, the controller updates the status:

```yaml
status:
  phase: Passed          # Pending | Running | Passed | Failed | Error
  startedAt: "2026-03-18T22:00:00Z"
  completedAt: "2026-03-18T22:01:30Z"
  durationMs: 90000
  jobRef: cf-hello-world-health
  assertions:
    - raw: "POST to discussion endpoint returns 200 with conversationId"
      passed: true
      message: "POST returned 200"
    - raw: "SSE stream emits \"connected\" event"
      passed: true
      message: "Event \"connected\" received"
    - raw: "synthesis is non-empty"
      passed: true
      message: "Synthesis is non-empty"
```

### Lifecycle

```
kubectl apply → Pending → Running (Job created) → Passed/Failed/Error
                                                      ↓
                                              TTL expires → deleted
```

## Declaring fitness: two forms, one behaviour

A crew **may** ship fitness scenarios; it is **optional but strongly encouraged** - fitness is how a crew states, executably, what "working" means for it. A crew with no `fitness/` directory is valid; it simply has no executable expectations.

Scenarios are declared in one of two forms, chosen per file:

- **ADL** (`.adl`) - the structured `DESCRIPTION` / `DEFINE CONST` / `ASSERT(...)` form below. This is the **stronger, preferred form**: scannable, diffable, and unambiguous.
- **Prose Markdown** (`.md`) - the gentler on-ramp: a heading, a question, a checklist of gates, and a fenced reference block. See [Prose Markdown Form](#prose-markdown-form-md).

The runner **auto-detects** the form from content, and both forms parse to the *same* internal test - identical question, identical inline gates, identical `DEFER`/`REFLECT` deferred assertions. Nothing downstream (the runner, the grade, the report) depends on which form an author chose. A single crew may even mix `.adl` and `.md` files in its `fitness/` directory.

## ADL Test Format

Tests are stored as `.adl` keys in a ConfigMap. The format uses ADL keywords adapted for discussion system fitness:

```
DESCRIPTION Verify discussion system is healthy end-to-end
REQUIRES deployed crew with discussion.enabled

DEFINE CONST QUESTION AS "Hello, are you there?"
DEFINE CONST MAX_DURATION AS 90 seconds

# Gateway is reachable
ASSERT(POST to discussion endpoint returns 200 with conversationId)

# SSE stream connects and finds the thread
ASSERT(SSE stream emits "connected" event)
ASSERT(SSE stream emits "thread_found" event within 30 seconds)

# Discussion lifecycle completes
ASSERT(at least 1 "phase" event with status=triaging is emitted)
ASSERT(discussion completes with "done" event within MAX_DURATION)

# Coordinator produces a response
ASSERT(synthesis is non-empty)
```

### Supported Assertion Types

| Pattern | What it checks |
|---|---|
| `POST to discussion endpoint returns 200` | HTTP POST succeeds |
| `SSE stream emits "X" event` | Named SSE event received |
| `SSE stream emits "X" event within N seconds` | Event received within deadline |
| `discussion completes within MAX_DURATION` | Done event before timeout |
| `at least N specialist(s) contribute with signal=agree` | Specialist participation count |
| `0 specialists contribute with signal=agree` | No specialists engaged |
| `coordinator produces synthesis` | Non-empty synthesis |
| `synthesis CONTAINS "term"` | Content includes expected terms |
| `synthesis does NOT CONTAIN "term"` | Content excludes prohibited terms |
| `synthesis is non-empty` | Synthesis exists |
| `DEFER synthesis <KEYWORD> "<reference>"` | Deferred, crew-judged quality score (see below) |

These are **inline** assertions: cheap, deterministic string/threshold checks the runner evaluates during the run. Custom assertions (not matching a known pattern, and not `DEFER`) are passed through as advisory - manual-review-recommended.

## Prose Markdown Form (.md)

The same scenario can be written as prose Markdown. The convention mirrors ADL one-for-one:

| Markdown | ADL equivalent |
|---|---|
| `# <heading>` | `DESCRIPTION <heading>` |
| first paragraph, or a `**Question:**` line | `DEFINE CONST QUESTION AS "..."` |
| `- <gate>` list item | `ASSERT(<gate>)` - same patterns as the table above |
| a fenced ` ```<keyword> ` block | `ASSERT(DEFER synthesis <KEYWORD> "...")` |

The fenced block's info string **is** the deferred keyword (lower-cased): ` ```reflects ` becomes the `REFLECTS` keyword, resolved post-suite to the crew labelled `kubemoot.ai/adl-keyword: REFLECTS` (see below). The block body is the reference; it may wrap across lines (whitespace is collapsed). A `completes within N seconds` gate seeds the wall-clock deadline, exactly as `DEFINE CONST MAX_DURATION` does in ADL.

The ADL test at the top of this page, written as prose Markdown:

````markdown
# Verify discussion system is healthy end-to-end

Hello, are you there?

- POST to discussion endpoint returns 200 with conversationId
- SSE stream emits "connected" event
- SSE stream emits "thread_found" event within 30 seconds
- discussion completes with "done" event within 90 seconds
- synthesis is non-empty

```reflects
A brief, friendly acknowledgement that the crew is reachable and ready to help
with infrastructure questions. Makes no fabricated cluster claims.
```
````

`README.md` in a `fitness/` directory is treated as documentation, never a scenario. ADL remains the preferred form - reach for `.md` when prose lowers the barrier to authoring a scenario at all.

## Deferred Assertions (DEFER) - crew-judged, extensible

Keyword string-matching can verify structure but not *quality*: `synthesis CONTAINS "release"` passes "No **release**s were found." For answer quality you need judgment against a known-correct answer. That judgment is itself an LLM task, so it must not run inline - a judge call during the run competes with the crew under test for the GPU and perturbs the measurement.

`DEFER` assertions solve both problems. They are **not evaluated during the run** - the runner records them and the operator scores them in a **post-suite pass**, after the whole suite completes.

```
DEFINE CONST QUESTION AS "Which Helm releases are deployed and their versions?"
# inline gates run during the suite …
ASSERT(discussion completes with "done" event within MAX_DURATION)
ASSERT(synthesis is non-empty)
# … and a deferred, reference-grounded quality score runs after the suite:
ASSERT(DEFER synthesis REFLECTS "About a dozen releases exist: cert-manager v1.19.4, ingress-nginx v4.11.2, prometheus v27.3.0, and similar")
```

### How it works - keyword resolves to a crew

A `DEFER synthesis <KEYWORD> "<reference>"` assertion is **decoupled from any specific judge**. The `<KEYWORD>` (e.g. `REFLECTS`) is resolved at post-suite time to **whatever Crew declares it** via the label:

```yaml
kubemoot.ai/adl-keyword: REFLECTS
```

The operator dispatches the generic payload `{KEYWORD, QUESTION, ANSWER, REFERENCE}` to that crew's discussion endpoint and reads back the standard **verdict contract**:

```json
{"score": 0.0, "fabrication": true, "reasoning": "Claims none found when ~32 releases exist."}
```

The `score` (0.0-1.0) becomes the scenario's quality measure in the rubric.

**This is a plugin model.** Nothing - keyword, crew, or endpoint - is hardcoded in the engine. A new evaluation capability is a *deployment*, not a code change: write a crew with a judge prompt for the new keyword, label it `kubemoot.ai/adl-keyword=<KEYWORD>`, and use that keyword in scenarios. For example, a future `DEFER synthesis SECURITY_AUDIT "<policy>"` would route to a crew labelled `kubemoot.ai/adl-keyword=SECURITY_AUDIT`. The built-in inline keywords are the fast path; `DEFER` keywords fan out to specialist judge crews.

### The kubemoot-fitness crew

`kubemoot-fitness` is an internal Kubemoot **control-plane crew** - it is not an end-user crew; its job is to judge other crews' fitness. It is the first `DEFER` plugin: it declares `kubemoot.ai/adl-keyword=REFLECTS` and scores how well an answer reflects the supplied reference (the scenario's expected answer, sourced from the cluster ground-truth baseline). It is deliberately small - a coordinator plus one reasoning-only judge agent (qwen3:32b), no MCP tools - because the reference travels in the payload. Because the pass is post-suite, the judge crew is the only thing running, so it never contends with the crew under test.

### ConfigMap Example

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: hello-world-fitness-tests
  namespace: crew-hello-world
data:
  discussion-health.adl: |
    DESCRIPTION Verify discussion system is healthy end-to-end
    DEFINE CONST QUESTION AS "Hello, are you there?"
    DEFINE CONST MAX_DURATION AS 90 seconds
    ASSERT(POST to discussion endpoint returns 200 with conversationId)
    ASSERT(SSE stream emits "connected" event)
    ASSERT(discussion completes with "done" event within MAX_DURATION)
    ASSERT(synthesis is non-empty)

  general-knowledge.adl: |
    DESCRIPTION Verify crew answers general knowledge questions
    DEFINE CONST QUESTION AS "What are the three states of matter?"
    DEFINE CONST MAX_DURATION AS 120 seconds
    ASSERT(discussion completes within MAX_DURATION)
    ASSERT(at least 1 specialist contributes with signal=agree)
    ASSERT(coordinator produces synthesis)
    ASSERT(synthesis CONTAINS reference to "solid" AND "liquid" AND "gas")

  stand-aside-irrelevant.adl: |
    DESCRIPTION Verify specialist stands aside on nonsense input
    DEFINE CONST QUESTION AS "asdf jkl qwerty"
    DEFINE CONST MAX_DURATION AS 60 seconds
    ASSERT(discussion completes within MAX_DURATION)
    ASSERT(0 specialists contribute with signal=agree)
    ASSERT(coordinator produces synthesis)
```

## Running Fitness Tests

### Single test

```bash
kubectl apply -f - <<EOF
apiVersion: kubemoot.ai/v1alpha1
kind: CrewFitness
metadata:
  name: health-check
  namespace: crew-hello-world
spec:
  crewRef: hello-world
  testRef: discussion-health
  configMapRef: hello-world-fitness-tests
  ttl: 1h
EOF
```

### Watch results

```bash
kubectl get crewfitness -n crew-hello-world -w
# NAME           PHASE     CREW          TEST                DURATION   AGE
# health-check   Passed    hello-world   discussion-health   4200       30s
```

### Inspect assertions

```bash
kubectl describe crewfitness health-check -n crew-hello-world
```

### Run all tests in a ConfigMap

```bash
for key in $(kubectl get cm hello-world-fitness-tests -n crew-hello-world \
  -o jsonpath='{.data}' | jq -r 'keys[]' | sed 's/.adl$//'); do
  kubectl apply -f - <<EOF
apiVersion: kubemoot.ai/v1alpha1
kind: CrewFitness
metadata:
  name: "test-${key}"
  namespace: crew-hello-world
spec:
  crewRef: hello-world
  testRef: "${key}"
  configMapRef: hello-world-fitness-tests
  ttl: 24h
EOF
done
```

## End-to-End Flow

From a crew designer's perspective, running fitness tests looks like this:

1. **Author** ADL or prose fitness files in the crew's `fitness/` directory, alongside its other manifests
2. **Bundle** them into a ConfigMap (`{crew-name}-fitness-tests`), with `kmctl` or a plain `kubectl create configmap --from-file=fitness/`
3. **Apply** a `CrewFitness` CR that references the ConfigMap and a test key
4. **Operator reconciles** - validates the Crew has a discussion endpoint, creates a Job with the fitness-runner image, mounts the ConfigMap
5. **Fitness runner runs** - the Job POSTs a question to the discussion API, opens an SSE stream, and collects signals in real time
6. **Agents respond** - Toolers triage and evaluate with their MCP tools, Analysts reason and agree or stand aside; the coordinator synthesizes
7. **Runner evaluates** - each ASSERT line is checked against the collected signals (HTTP status, event presence, synthesis content, timing)
8. **Results patch back** - the runner writes per-assertion JSON to the Job annotation; the controller reads it and updates the CR status
9. **Watch** with `kubectl get crewfitness -w`, or `kubectl describe` for per-assertion detail
10. **Auto-cleanup** - the CrewFitness CR deletes itself after the TTL (default 1 hour)

CI/CD pipelines create `CrewFitness` CRs the same way, as a post-deploy verification step.

## Architecture

### Controller Flow

1. **CrewFitness created** → controller sets phase to Pending
2. **Validate** - look up Crew CR for `status.discussionEndpoint`, verify ConfigMap contains the test key
3. **Create Job** - fitness-runner image, ConfigMap mounted at `/tests/`, discussion endpoint and test file path passed as env vars
4. **Poll Job** - requeue every 5s until Job completes or fails
5. **Read results** - Job writes assertion results as a JSON annotation (`kubemoot.ai/fitness-result`) on the Job object
6. **Update status** - phase becomes Passed (all assertions pass), Failed (any assertion fails), or Error (Job crashed)
7. **TTL cleanup** - if `spec.ttl` is set, delete the CrewFitness CR after `completedAt + ttl`

### Fitness Runner Job

The fitness runner is a lightweight Go binary that:

1. Reads the ADL file from the mounted ConfigMap
2. Parses `DEFINE CONST` and `ASSERT(...)` lines
3. POSTs to the discussion endpoint with the QUESTION constant
4. Connects to the SSE stream using the returned `conversationId`
5. Collects `SignalEvent`s until a `done` event or timeout
6. Evaluates each assertion against the collected signals
7. Writes results as JSON to the Job annotation via the Kubernetes API

The runner needs minimal resources (50m CPU, 64Mi RAM) - it's an HTTP client, not an inference workload.

## Relationship to CrewForge

[CrewForge](../ecosystem/crewforge/) lists crews and lets you talk to them from the
editor; it does not create, edit, or run `CrewFitness` resources. Fitness tests are
Kubernetes resources like everything else Kubemoot manages, so they stay reachable from
any Kubernetes client, `kubectl`, `kmctl`, or CI/CD, whatever authors them.

## Connection to Richards and Ford

Kubemoot's crew fitness functions are **holistic** fitness functions in the Richards/Ford taxonomy - they test the combined behavior of coordinator, Toolers, Analysts, discussion gateway, NATS messaging, and LLM inference as a single system. They are also **triggered** (run on demand via CR creation) rather than continuous, though nothing prevents scheduling them via a CronJob that creates CrewFitness CRs on a cadence.

The ADL format is intentionally human-readable and LLM-interpretable. As Richards and Ford note, the pseudo-code declaration is the "source of truth about the architecture" - the fitness runner translates it into concrete HTTP assertions. The same ADL test file could be translated by an LLM into a different test framework if needed, maintaining the cross-platform portability that ADL was designed for.

> "Placing a governance rule that specifies intent avoids the need for documentation that no one will read with active code that reveals intent and known limitations."
> - *Architecture as Code*, Chapter 1
