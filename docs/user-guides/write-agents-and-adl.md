---
title: "Write Agents & ADL"
weight: 20
description: "Define agent behavior with ADL, the Architecture Definition Language."
---

An agent's behavior is its prompt, and in Kubemoot every prompt is written in
**ADL (the Architecture Definition Language)** and stored in `PromptModule` CRs - never
as inline prose in an Agent spec. This guide covers how to write ADL and how modules
compose.

## Why ADL

ADL expresses behavior as structured rules instead of free-flowing prose. The benefits
are practical: a rule-based prompt is **scannable** (you can read what an agent will
and won't do at a glance), **versionable** (`kubectl diff` shows exactly what changed),
and **deployable without recompilation** (a `kubectl apply` changes how an agent
reasons). Prose prompts drift and hide their intent; ADL keeps it explicit.

## The keywords

ADL uses a small, fixed vocabulary:

| Keyword | Use |
|---------|-----|
| `DESCRIPTION` | A short statement of the module's purpose. |
| `DEFINE DOMAIN` / `DEFINE COMPONENT` | Scope a block of rules. |
| `WHEN … THEN …` | A conditional rule - the core of ADL. |
| `ALWAYS` / `NEVER` | Unconditional rules. |
| `ASSERT` | An invariant the agent must hold. |
| `FOREACH … CONTAINED WITHIN …` | Iterate over a set within a boundary. |

Write rules, not paragraphs. Each line should be a behavior a reader (or a reviewer
diffing the file) can evaluate on its own.

```text
DESCRIPTION How this Tooler investigates and reports.

WHEN a query names a logical resource THEN discover its real labels before querying - never assume the label scheme.
ALWAYS report a tool failure as a failure signal, never as a silent stand_aside.
NEVER include internal directives (REMEMBER:, TOOL_GAP:) in the user-facing answer.
```

ADL is the default for **every** agent-prompt surface - not only the main system
module, but also triage summaries, fitness scenarios, and RAG source descriptions.
Free prose anywhere an agent reads is a slip.

## Prose vs ADL: choosing a form

Default to ADL. The rationale is practical, not stylistic. ADL rules are scannable
line by line, produce meaningful `kubectl diff` output (one changed rule is one changed
line, not a re-flowed paragraph), and deploy without recompilation. They also reduce
ambiguity for smaller Tooler and Analyst models: a rule stated as `WHEN X THEN Y` has
one reading; a prose sentence can hedge or imply conditions that a small model misses.

Prose is acceptable in isolation for sections that are genuinely narrative (a worked
example, an explanatory aside), but every behavioral rule - every WHEN, every ALWAYS,
every invariant - should be ADL.

### Side-by-side examples

Each example shows one rule in ADL, as `homelab-pilot-crew` ships it, next to the same
rule written as prose.

**Evaluation Phase rule (discussion-protocol module, order 10)**

The rule in both forms expresses the same intent: when the question is in the
Tooler's domain and the Tooler has tools, use the tools before reporting.

ADL form (`homelab-pilot-crew/templates/promptmodule-discussion.yaml`):

```text
WHEN question touches your domain AND you have tools:
  Use tools first; real data, even partial, beats a gap report.
```

The same rule as prose:

```text
If it does and you have tools, use them first to gather real data, then contribute
facts. Real data, even partial, beats a gap report.
```

The ADL form makes the condition explicit (`WHEN ... AND ...`) and separate from the
action (`Use tools first`). The prose form embeds the condition in a clause ("If it
does"). For a smaller model parsing a long system prompt, the ADL form is easier to
match against: the keywords stand out and the condition boundary is unambiguous.

**Additive-brief rule (coordinator advisory-prompt module, order 5)**

This rule constrains how the coordinator writes its framing brief: suggestions only,
no prohibitions.

ADL form (`homelab-pilot-crew/templates/promptmodule-coordinator.yaml`):

```text
ASSERT: The brief is additive guidance only.
NEVER phrase a prohibition or tell toolers NOT to use a tool.
ALWAYS suggest methods, data sources, and approach angles toolers should consider.
```

The same rule as prose:

```text
The brief must be additive guidance only. Suggest methods, data sources, and approach
angles the Toolers and Analysts should consider. Never phrase a prohibition or tell them not to
use a tool - a Tooler may reach for a richer tool and that is correct.
```

The ADL form separates the invariant (`ASSERT`), the unconditional prohibition
(`NEVER`), and the unconditional positive (`ALWAYS`) onto distinct lines. Each line is
a reviewable, diffable unit. Adding a new constraint is one new `NEVER` or `ALWAYS`
line. In the prose form, changing one constraint means editing a paragraph and
checking that the surrounding sentences still read correctly.

### How to translate prose to ADL

The translation is mechanical once you identify what kind of statement each sentence
is.

| Prose pattern | ADL form |
|---|---|
| "If X, do Y" | `WHEN X THEN Y` or `WHEN X: <action>` |
| "Always do Y" | `ALWAYS Y` |
| "Never do Z" | `NEVER Z` |
| "X is always true / X must hold" | `ASSERT X` |
| "This section covers topic T" | `DEFINE COMPONENT T` followed by `DESCRIPTION ...` |

Keep the meaning identical; only the form changes. A conditional sentence ("If the
query returns empty, do not write the final answer yet") becomes `WHEN tool returns
empty THEN do NOT write the final answer yet`. An imperative ("Report each metric in
a consistent form") becomes `ALWAYS report each metric in a consistent form`. A stated
invariant ("Real data, even partial, beats a gap report") becomes `ASSERT real data,
even partial, beats a gap report`.

Group related rules under a `DEFINE COMPONENT` block to give Tooler and Analyst
models a scannable heading for that cluster of behavior. This also makes the diff
output show which component a changed rule belongs to.

See [Compose a Crew](../compose-a-crew/) for how ADL PromptModules fit together
as a crew design system, and how the coordinator's `advisory-prompt` module uses these
patterns to constrain brief framing.

## How modules compose

`PromptModule`s are concatenated by **order** to form an agent's system prompt, and an
agent lists the ones it uses in `spec.promptRefs`. The order ranges are conventional:

| Order | Module kind |
|-------|-------------|
| 10 | Shared discussion protocol |
| 20 | Shared response style |
| 30 | Per-agent system module (`{agent-name}-system`) |
| 5 / 45 / 50 | Coordinator-only: advisory, decision logic, synthesis |

Shared modules (10, 20) keep crew-wide conventions in one place; the per-agent module
(30) carries that agent's specific behavior. The composed text is mounted into the
agent at runtime.

## Workflow

1. Write each module as a `PromptModule` CR with ADL `content`.
2. Reference them, in order, from the agent's `spec.promptRefs`.
3. `kubectl apply` - the agent picks up the new behavior without a rebuild.
4. Review changes with `kubectl diff` before applying; the rules make the diff
   meaningful.

## Next

- [Compose a Crew](../compose-a-crew/) - how coordinator advisory modules and Tooler/Analyst
  modules fit together as a design system.
- [Agent CRD reference](../../reference/agent/) - the spec fields ADL plugs into.
- [Define Fitness Functions](../define-fitness-functions/) - check the behavior you
  just wrote actually holds.
