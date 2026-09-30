---
title: "ADL Reference"
weight: 10
description: "The complete ADL vocabulary: prompt keywords and fitness assertions."
---

ADL has two dialects that share a style. The **prompt** dialect composes an agent's
behavior; the **fitness** dialect specifies executable checks against a live crew. This
page is the complete vocabulary for both; see
[ADL, the Architecture Definition Language, for agents](../concepts/agent-definition-language.md)
for where the notation comes from and how Kubemoot applies it. For how to write and run
it, see [Write Agents & ADL](../user-guides/write-agents-and-adl.md) and
[Kubemoot Crew Fitness Functions](../fitness/kubemoot-crew-fitness-functions.md).

## Prompt ADL

Prompt ADL is stored in the `content` of a `PromptModule` CR. Modules are concatenated
by `order` to form an agent's system prompt.

| Keyword | Form | Meaning |
|---|---|---|
| `DESCRIPTION` | `DESCRIPTION <text>` | A one-line statement of the module's purpose. |
| `DEFINE DOMAIN` | `DEFINE DOMAIN <name>` | Open a top-level scope for a block of rules. |
| `DEFINE COMPONENT` | `DEFINE COMPONENT <name>` | Open a named sub-scope; give Tooler and Analyst models a scannable heading for a cluster of rules. |
| `WHEN ... THEN ...` | `WHEN <condition> THEN <action>` | A conditional rule, the core of ADL. The block form `WHEN <condition>:` followed by indented actions is equivalent. |
| `ALWAYS` | `ALWAYS <action>` | An unconditional positive rule. |
| `NEVER` | `NEVER <action>` | An unconditional prohibition. |
| `ASSERT` | `ASSERT <invariant>` | An invariant the agent must hold. |
| `FOREACH ... CONTAINED WITHIN ...` | `FOREACH <item> CONTAINED WITHIN <set>` | Iterate over a set within a boundary. |

Conditions may combine with `AND`. Write one behavior per line so each rule is a
reviewable, diffable unit.

```text
DESCRIPTION How this Tooler investigates and reports.

DEFINE COMPONENT investigation

WHEN question touches your domain AND you have tools:
  USE tools first, then report facts. Real data, even partial, beats a gap report.

ALWAYS report a tool failure as a failure signal, never as a silent stand-aside.
NEVER include internal directives in the user-facing answer.
ASSERT the brief is additive guidance only.
```

### Memory directives

An agent may emit a `REMEMBER:` line to write a fact to its working memory (for example
`REMEMBER: gpu-topology | gpu-a | label exported_namespace="ollama-a"`). These
directives are internal: a `NEVER include internal directives in the user-facing answer`
rule keeps them out of responses.

## Fitness ADL

A fitness scenario pairs a question with inline gates and an optional deferred quality
judgment. Scenarios live in a crew's `fitness/` directory as `.adl` files; the runner
also accepts the same scenario as prose Markdown and auto-detects the form.

| Keyword | Form | Meaning |
|---|---|---|
| `DESCRIPTION` | `DESCRIPTION <text>` | What the scenario verifies. |
| `REQUIRES` | `REQUIRES <precondition>` | A precondition for the run, for example `deployed crew with discussion.enabled`. |
| `DEFINE CONST` | `DEFINE CONST <NAME> AS <value>` | A named constant. `QUESTION` is the prompt sent to the crew; `MAX_DURATION` seeds the wall-clock deadline. |
| `ASSERT(...)` | `ASSERT(<gate>)` | An assertion the runner evaluates. Inline gates are checked during the run; `DEFER` gates are scored afterward. |
| `#` | `# <text>` | A comment. |

### Inline assertion patterns

`ASSERT(...)` recognizes a fixed set of inline patterns, evaluated deterministically by
the runner:

| Pattern | Checks |
|---|---|
| `POST to discussion endpoint returns 200 with conversationId` | The request succeeds and returns a conversation id. |
| `SSE stream emits "X" event` | A named Server-Sent Event is received. |
| `SSE stream emits "X" event within N seconds` | The event arrives before a deadline. |
| `discussion completes with "done" event within MAX_DURATION` | The run finishes before the deadline. |
| `at least N specialist(s) contribute with signal=agree` | Minimum specialist participation. |
| `0 specialists contribute with signal=agree` | No specialists engage (a stand-aside scenario). |
| `coordinator produces synthesis` | A synthesis is produced. |
| `synthesis is non-empty` | The synthesis has content. |
| `synthesis CONTAINS "term"` | The synthesis includes a term. |
| `synthesis does NOT CONTAIN "term"` | The synthesis excludes a term. |

A custom assertion that matches no known pattern and is not a `DEFER` is passed through
as advisory: recorded but flagged for manual review.

### Deferred, graded judgment

```text
ASSERT(DEFER synthesis REFLECTS "<reference answer>")
```

`DEFER` defers an assertion to a post-run, reference-grounded judge. `synthesis REFLECTS
"..."` asks a judge crew to score the synthesis 0.0 to 1.0 against the reference answer,
fabrication-aware, rather than matching a string. This is the most informative check: it
judges meaning, not keywords.

### A complete scenario

```text
DESCRIPTION List nodes with roles, capacity, and Ready condition; real identities, no invented nodes.

DEFINE CONST QUESTION AS "List the Kubernetes nodes with their roles, CPU and memory capacity,
  and current Ready condition."
DEFINE CONST MAX_DURATION AS 480 seconds

ASSERT(POST to discussion endpoint returns 200 with conversationId)
ASSERT(SSE stream emits "thread_found" event within 30 seconds)
ASSERT(discussion completes with "done" event within MAX_DURATION)
ASSERT(at least 1 specialist contributes with signal=agree)
ASSERT(synthesis is non-empty)
ASSERT(synthesis CONTAINS "node")

# Reference-grounded quality, scored 0.0-1.0 by the deferred judge.
ASSERT(DEFER synthesis REFLECTS "Lists the cluster's nodes with their roles, CPU/memory capacity,
  and Ready condition, discovered not assumed. Real node identities; no invented nodes.")
```

## Next

- [ADL, the Architecture Definition Language, for agents](../concepts/agent-definition-language.md) - what ADL is and why.
- [Write Agents & ADL](../user-guides/write-agents-and-adl.md) - authoring prompt
  modules and composing them.
- [Kubemoot Crew Fitness Functions](../fitness/kubemoot-crew-fitness-functions.md) - the
  fitness CRD, run model, and reporting.
