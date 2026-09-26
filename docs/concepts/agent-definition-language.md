---
title: "ADL, the Architecture Definition Language, for agents"
weight: 25
description: "What ADL is, where it comes from, and why Kubemoot writes agent behavior in it."
---

Kubemoot defines what an agent does in **ADL**, a small, structured language of
behavioral rules. Every agent prompt is ADL stored in a `PromptModule` resource, never
free prose in an Agent spec, and the same language expresses a crew's executable fitness
checks. ADL is the surface where a crew's behavior is authored, reviewed, and versioned.

## What ADL is, and where it comes from

ADL is the **Architecture Definition Language**, a pseudo-code introduced by Mark
Richards for describing and governing the structure of a software system, and paired
with the fitness-function idea from Richards and Neal Ford's evolutionary-architecture
work (see their book *Architecture as Code*, and Richards' ADL reference at
developertoarchitect.com). In its original form ADL defines a system's parts (`DEFINE
SYSTEM`, `DEFINE DOMAIN`, `DEFINE COMPONENT`) and asserts the rules between them
(`ASSERT`, `FOREACH ... CONTAINED WITHIN`), making architecture a machine-readable
artifact and enforcing it with executable checks that fail when an implementation
drifts from its intended design. Kubemoot applies that same language, unchanged in
form, to a different subject: instead of governing how a system's components may
depend on each other, its rules govern how an agent behaves. This is a partnership with
ADL's authors and their notation, not a fork or a renaming.

## Why ADL suits agent prompts

The rule forms ADL already has, `DEFINE COMPONENT`, `ASSERT`, `WHEN ... THEN`, `ALWAYS`
/ `NEVER`, `FOREACH ... CONTAINED WITHIN`, map directly onto what an agent's system
prompt actually needs to say: named scopes for related behavior, conditions with
actions, and unconditional obligations or prohibitions. An architectural fitness
function, an executable check that fails when reality drifts from intent, becomes
Kubemoot's [crew fitness functions](../fitness/kubemoot-crew-fitness-functions/) applied
to a crew's discussion behavior instead of a codebase's structure. The same reasons ADL
disciplines an architecture decision record disciplines an agent prompt: declarative
rules instead of a paragraph of prose, one behavior per line, reviewable in a diff.

## Behavior as rules, not prose

A prose prompt is a paragraph an author hopes a model reads the way they meant. It
drifts as it is edited, buries its intent inside clauses, and shows up in a diff as one
re-flowed blob. ADL states behavior as discrete rules instead:

```text
DESCRIPTION How this Tooler investigates and reports.

WHEN a query names a logical resource THEN discover its real labels before querying.
ALWAYS report a tool failure as a failure signal, never as a silent stand-aside.
NEVER include internal directives in the user-facing answer.
```

Each line is one behavior. You can read what an agent will and will not do at a glance,
and a reviewer can evaluate or diff a single rule without re-reading a paragraph.

## Recommended, not required

ADL is not mandatory, and it is not a separate language you have to learn. An agent's
prompt and a crew's fitness scenarios can be written in plain prose; Kubemoot accepts
both forms and handles them the same way once parsed. ADL is a stronger form of that
prose, the same intent tightened into scannable, diffable rules. Reach for it because it
reads more clearly and reviews more cleanly, not because the system demands it. Where a
passage is genuinely narrative, prose is the right choice; where it states a behavior or
a check, ADL usually states it better.

## What makes it ADL

Three properties define the language, and each matches what a prompt actually is:

- **Declarative.** ADL states rules, not procedures. It says what must hold, not the
  steps to make it hold.
- **General.** One rule governs an open-ended class of situations rather than a single
  example. An agent faces unbounded inputs, so its instructions have to generalize.
- **Inside-out.** ADL instructs an actor how to behave, addressed to the agent itself.
  It does not describe a system from the outside the way an acceptance test does.

Those properties are also why ADL is its own language rather than a borrowed one. An
outside-in, example-bound behavior dialect such as Gherkin is the opposite shape; see
[Gherkin, ADL, and Fitness Functions](../fitness/gherkin-adl-and-fitness-functions/)
for that comparison.

## Where ADL is used

ADL is the recommended form across every surface an agent reads:

- **Agent prompts** - the per-agent system module, composed from `PromptModule` CRs in
  order.
- **Shared crew conventions** - the discussion-protocol and response-style modules every
  agent in a crew shares.
- **Triage and knowledge** - the short instruction an agent uses to judge relevance, and
  the descriptions of its RAG knowledge sources.
- **Fitness scenarios** - the executable checks that verify a crew, written in the
  fitness dialect of ADL.

Prose is accepted on any of these surfaces and parsed the same way; ADL is the form to
prefer, not a requirement.

## Why it pays off

- **Scannable** - behavior is a list of rules, not a wall of text.
- **Diffable** - one changed rule is one changed line in `kubectl diff`, not a re-flowed
  paragraph.
- **Deployable without a rebuild** - a `PromptModule` is a Kubernetes resource, so
  `kubectl apply` changes how an agent reasons with no image rebuild.
- **Less ambiguous for smaller models** - `WHEN X THEN Y` has one reading. A prose
  sentence can imply conditions a smaller Tooler or Analyst model misses.
- **Testable** - because behavior is explicit, a crew's fitness functions can check that
  the behavior ADL prescribes actually holds.

## Prescribe and verify

ADL spans two surfaces that mirror each other: prompts **prescribe** behavior, and
fitness scenarios **verify** it, both in the same readable, version-controlled language.
A rule you add to a prompt and the fitness scenario that checks for it are authored the
same way and reviewed in the same diff.

Whether the structured form also steers a model's output better than equivalent prose is
an empirical question, not an assumption. Kubemoot ships its reference crew in both an
ADL form and a prose form and measures the difference with fitness functions rather than
asserting it. The structural benefits above, scannable and diffable and deployable and
testable, hold regardless of that result.

## Next

- [ADL Reference](../reference/adl-reference/) - the complete keyword and assertion vocabulary.
- [Write Agents & ADL](../user-guides/write-agents-and-adl/) - how to author ADL
  `PromptModule`s and compose them into an agent.
- [Kubemoot Crew Fitness Functions](../fitness/kubemoot-crew-fitness-functions/) - the
  fitness dialect of ADL, run against a live crew.
