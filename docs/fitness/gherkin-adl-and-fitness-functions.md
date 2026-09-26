---
title: "Gherkin, ADL, and Fitness Functions"
weight: 2
---

## Purpose

Kubemoot uses two structured languages: **ADL** (the Architecture Definition Language, applied to agents) to compose agent prompts, and a **fitness-function** format to specify executable acceptance checks for a crew. A natural question - especially for anyone arriving from a Behavior-Driven Development (BDD) background - is: *why not use Gherkin (Cucumber's Given/When/Then) for either of these?*

This document answers that. It is a general compare-and-contrast, not a proposal. It explains where Gherkin **overlaps** with what Kubemoot already does, where it **diverges**, and why Kubemoot settled on ADL plus a purpose-built fitness format. Gherkin remains a credible alternative substrate for one of the two surfaces; the reasoning below makes clear which, and why.

## What Gherkin is

Gherkin is the structured natural-language dialect used by Cucumber (and ports such as `behave` for Python and `godog` for Go). A `.feature` file reads:

```gherkin
Feature: GPU metrics reporting
  Scenario: A crew reports live GPU utilization
    Given a crew with GPU-metrics tools
    When asked "What is the current GPU utilization?"
    Then the answer cites real DCGM metrics
    And the answer invents no values
```

Each step (`Given` / `When` / `Then` / `And`) is bound at run time to a **step definition** - code that executes the action or evaluates the assertion. Gherkin's defining property is that a scenario is *simultaneously* a human-readable specification and an executable test - "living documentation." It is, in spirit, a form of Spec-Driven Development (SDD): the specification and the verification are the same artifact.

Two properties matter for the comparison that follows:

1. **It is outside-in.** Gherkin describes the *expected behavior of a system under test*, observed from the outside.
2. **Its value is the binding.** A bare `.feature` file does nothing; the power comes from steps being wired to executable definitions.

## Kubemoot's two structured surfaces

- **ADL - prompts.** ADL keywords (`WHEN`/`THEN`, `ALWAYS`/`NEVER`, `ASSERT`, `DEFINE`, `FOREACH`) compose an agent's system prompt from PromptModules. The agent *reads* ADL as behavioral directives. ADL is declarative, general (a rule covers a class of situations), and instruction-oriented - it tells an actor how to behave.
- **Fitness functions - tests.** A fitness scenario pairs a question with inline gates (the request succeeds, the discussion completes, a synthesis is produced, …) and a deferred, reference-grounded quality judgment. It runs against a live crew and produces a graded report. It is executable, example-oriented (one concrete question per scenario), and verification-oriented - it checks what a crew actually did.

The two surfaces are deliberately different in kind: one *prescribes* behavior, the other *verifies* it. Gherkin's fit is very different for each.

## Gherkin vs ADL (prompts)

### Overlap

- Both are **structured natural language**: scannable, diffable, version-controlled, deployable without recompilation.
- Both use a **situational When/Then shape**. ADL's `WHEN … THEN …` is close to Gherkin's `When … Then …`; Gherkin adds `Given` for context.
- Both aim to be readable by non-programmers yet precise enough to drive behavior.

### Divergence

- **Direction is inverted.** ADL prescribes behavior to an actor from the inside (imperative directives). Gherkin describes the expected behavior of a system under test from the outside (acceptance criteria). Using Gherkin to *instruct* an agent runs against the grain of what it was designed for.
- **Generality vs. concreteness.** ADL rules are general - one rule governs an open-ended class of inputs. Gherkin scenarios are concrete examples - one situation each. An agent faces unbounded inputs; enumerating a scenario per situation does not scale, and generalizing the `Given`s far enough to cover them converges on "ADL with Given/When/Then keywords."
- **The binding disappears.** Gherkin's core value is the step-definition binding. A prompt is *read by a model*, not executed, so Gherkin-as-prompt discards exactly the feature that makes Gherkin worthwhile, leaving only its surface syntax.

### Net

As a prompt language, Gherkin reduces to a stylistic variant of ADL. The one plausible upside is empirical, not structural: Given/When/Then is concrete and example-shaped, and language models follow concrete examples well, so a scenario-style prompt *might* steer a model better than abstract rules for some behaviors. That is a question to measure, not an inherent advantage. Kubemoot uses ADL here because ADL is purpose-built for inside-out, general instruction - the thing a prompt actually is.

## Gherkin vs fitness functions (tests)

### Overlap (strong)

- Both **are executable acceptance specifications** run against a system.
- Both **bind natural-language phrases to evaluators.** Kubemoot's assertion classifier (which maps a phrase like "completes within N seconds" to a concrete check) plays the same role as Cucumber's step definitions.
- Both produce pass/fail with reporting and double as **living documentation** of expected behavior.
- The **Given/When/Then mapping is direct**: `Given` = the crew and context, `When` = the question/request, `Then` = the assertions. A Kubemoot fitness runner is, conceptually, a BDD runner.

A Gherkin front-end for the fitness layer would therefore be a small conceptual leap - a `.feature` parser compiling to the runner's existing internal model. The ideas align.

### Divergence

- **Determinism assumption.** BDD assumes a `Then` reliably holds - scenarios are repeatable. Crews are non-deterministic: stochastic models and variable scheduling mean the same question can yield different runs. Kubemoot addresses this with **N iterations and statistical measures** (reliability as a pass-rate, self-consistency across iterations). Classic Gherkin models a single deterministic pass/fail per scenario and has no native notion of "run this 15 times and score the distribution."
- **Graded vs. binary outcomes.** Gherkin steps are binary. Kubemoot's most informative check is a **graded, reference-grounded quality judgment** - a score, not a boolean - produced by a deferred model-based judge rather than a string match. Expressing that as a binary `Then` requires choosing a threshold and discards the granularity.
- **Evaluation substrate.** Gherkin step definitions are typically deterministic code, often bound by regular expressions or Cucumber expressions. The **mechanical** gates (an HTTP request succeeds, a completion event arrives, a quorum agrees) fit that model perfectly. The **semantic** gate does not: judging whether an answer is correct against ground truth - and whether it fabricated - is deliberately routed to a model, because keyword and regex matching are blind to meaning (a string match for "release" passes "no **release**s were found"). The deterministic steps suit step-definitions; the semantic step needs a different kind of binding.

### Net

Gherkin and the Kubemoot fitness format solve the same problem, and Gherkin brings a mature ecosystem (reporting, IDE support, parameterized scenarios) and an *enforced vocabulary* - a fixed set of recognized steps, which is more disciplined than fuzzy phrase matching. The friction is that Gherkin's binary, deterministic, code-bound model fits the mechanical gates cleanly but fits neither non-deterministic statistical scoring nor graded model-based judgment without extension. Kubemoot's bespoke format exists to make those two things first-class.

## The unifying idea, and why it is only partial

BDD's deepest promise is that **one artifact is both the specification and the test**. Applied to a crew, a single Gherkin scenario could in principle seed the prompt (as a concrete behavioral example the agent reads) *and* serve as the fitness test that verifies the crew honored it - write the behavior once; it teaches the agent and grades it.

The obstacle is the tension already noted: the prompt use wants **generality** (cover open-ended inputs), while the test use wants **concreteness** (a specific input with a checkable outcome). A single artifact cannot maximize both. It works well for *covered* behaviors - concrete scenarios that simultaneously act as few-shot examples and as tests - but it does not generalize the prompt to *uncovered* situations. The unified-artifact ideal is real and attractive, but partial: excellent for "here are the behaviors we care about, taught and tested," not a complete prompt on its own.

## Summary

| Dimension | Gherkin for prompts | Gherkin for fitness |
|---|---|---|
| Conceptual direction | Misfit - describes from outside; prompts prescribe from inside | Fit - fitness *is* outside-in acceptance testing |
| Generality needed | Poor - scenarios are concrete; prompts must generalize | Fine - fitness scenarios are concrete by design |
| Use of Gherkin's binding | Lost - prompts are read, not executed | Fit - gates map to step definitions |
| Determinism | n/a | Friction - crews are non-deterministic; needs N-iteration framing on top |
| Graded judgment | n/a | Friction - binary `Then` vs. a 0-1 model-judged score |
| Tooling/ecosystem benefit | Low (only syntax remains) | High (runners, reporting, vocabulary) |
| Overall | Stylistic variant of ADL | Credible alternative substrate |

**Why Kubemoot uses ADL and a purpose-built fitness format today.** ADL is built for what a prompt is - inside-out, general instruction - where Gherkin's outside-in, example-bound, execution-dependent nature does not fit. The fitness layer needs two things Gherkin does not model natively: statistical handling of non-deterministic runs, and graded model-based judgment of answer quality against a reference. The overlap on the fitness side is genuine, though: a Gherkin-fronted fitness runner is a coherent design, and anyone preferring `.feature` files over the native format would find the concepts map cleanly, provided the non-deterministic and graded-judgment extensions are carried over.

Gherkin, then, is not a missing capability so much as a different point in the same design space - strongest where Kubemoot's fitness functions already live, weakest where its prompts already live.
