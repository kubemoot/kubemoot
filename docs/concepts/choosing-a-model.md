---
title: "Choosing a Model"
weight: 45
description: "How to pick models for a crew when GPU memory is the binding constraint."
---

Kubemoot runs on the GPUs you have. A crew is typically twenty or more agents sharing a
small number of cards, so choosing a model is a capacity decision as much as a quality
one. This page is the decision framework. For the CRD fields, the current model catalog,
and the per-GPU fit matrix, see [Models in Kubemoot](../../reference/models/).

Because an `Agent` names no model, changing your mind is cheap. Adopting a different
model is a `Model` CR and a label, not an edit to every agent in the crew. That makes
model choice a thing you **measure and revise**, not a thing you get right up front.

## Rank on tool calling, not on leaderboards

Most published model rankings are coding benchmarks. That is the wrong axis for
Kubemoot. A Kubemoot agent spends its life selecting an MCP tool from a list of ten to
fifteen, reading the result, and deciding whether it has anything to contribute. Raw
coding ability barely touches that loop.

A model that writes excellent code but emits malformed tool calls does not fail loudly.
It degrades into abstaining, or into asserting a plausible number it never looked up.
Those are precisely the two failure modes the consensus model exists to surface, and a
weak tool caller will produce them all day.

So rank candidates in this order:

1. **Tool-call fidelity.** Does it reliably emit well-formed calls, pick the right tool,
   and pass the right arguments? Agentic tool-use benchmarks are the relevant signal.
2. **Structured output adherence.** Agents must return signals and satisfy response
   contracts. A model that drifts out of the requested shape breaks the protocol.
3. **Behavior at real prompt length.** A tooler's system prompt plus tool schemas plus
   retrieved context is large. Some models degrade sharply well before their advertised
   context window. **Advertised context is a ceiling, not a promise.** Test at the
   length your crew actually produces.
4. **Reasoning depth.** This matters for the coordinator's synthesis, and much less for
   the toolers who make up most of the crew.

## GPU memory is the binding constraint

The scheduler places models by VRAM budget and evicts idle ones on demand (see
[Scheduler](../../architecture/scheduler/)). The budget for a card is the quantized weights plus room for KV cache and concurrent
requests. A useful planning rule is to leave roughly a quarter of the card free after
the weights land.

Rough weight sizes, before KV cache:

| Parameters | Q4_K_M | Q8_0 |
|---|---|---|
| 8B | ~5 GB | ~9 GB |
| 14B | ~9 GB | ~16 GB |
| 24B | ~14 GB | ~25 GB |
| 32B | ~20 GB | ~35 GB |

On a 32 GB card, a 32B model at Q4 fits with workable headroom and the same model at Q8
does not fit at all. On a 24 GB card, the 32B Q4 model technically loads but leaves so
little room for KV cache that long discussions become unreliable. Prefer the model that
leaves headroom over the model that barely fits.

### The mixture-of-experts caveat

A mixture-of-experts model loads **all** of its expert weights into VRAM. Sizing follows
*total* parameters, not active parameters. A 35B MoE with 3B active still needs roughly
35B worth of quantized weights resident.

What the small active count buys is **speed**. That is a real benefit for a crew, because
throughput per agent is what determines how long a discussion takes when twenty agents
are queued behind one card. Choose MoE for latency, and size it for its total
parameters.

### Where the frontier models sit

The strongest open-weight models are trillion-parameter mixture-of-experts families, and
they do not fit any single consumer card. The gap is not marginal, it is two orders of
magnitude.

Take a 1T-parameter MoE with roughly 32B active as the representative case. Full weights
run around 630 GB. An aggressive 1.8-bit dynamic quantization still lands near 240 GB.
Vendors publish self-hosting requirements on the order of four H200 class accelerators,
which is roughly $120,000 to $160,000 to buy outright, or somewhere between $4 and $18
per hour to rent. Other models in this tier size similarly: a leading MIT-licensed
open-weight model needs upwards of 220 GB even at 1-bit quantization.

Because of that, these models usually appear on local runtimes only as cloud-routed
tags. A tag that looks local but resolves to the vendor's hosted infrastructure is a
hosted API wearing a local-looking name. Read the tag before assuming a model runs on
your hardware.

Routing to a hosted model is a legitimate choice, but it is not implemented yet: the
`openai` and `anthropic` provider types exist on the CRD as stubs, and Ollama is the
model server today. When it lands, make the choice deliberately, because it moves inference, and whatever your
agents read from your cluster, off the premises. If staying local is the point of the
deployment, the frontier tier is simply not available, and the real question becomes
which 20B to 35B class model calls tools best on the card you own. For a homelab budget
in the range of a single $5,000 GPU with 24 GB to 48 GB of memory, that field is
genuinely competitive.

### The serving runtime is a separate choice

The model and the thing that serves it are different decisions. Kubemoot talks to a
`ModelProvider`, so the serving runtime is swappable and no crew depends on a particular
one. Ollama is the common starting point because it is simple to operate. Higher
throughput runtimes trade that simplicity for lower per-request overhead and better
batching under concurrent load, which matters when a crew of twenty agents queues behind
one card. See the [roadmap](../../introduction/roadmap/) for backends beyond Ollama.

## Match the model to the discussion role

Do not give every agent the largest model that fits. Roles have different demands, and a
reasoning-heavy model in a tooler slot tends to over-deliberate on what should be a
single tool call.

| Role | What it does | Optimize for | Size class |
|---|---|---|---|
| Tooler | Calls MCP tools, returns raw output | Tool-call fidelity, latency | Smallest model that calls tools reliably |
| Analyst | Reasons over what the toolers gathered | Instruction following, arithmetic honesty | Middle of the range |
| Coordinator | Selects the subcommittee, synthesizes the answer | Long-context reasoning, instruction adherence | Largest that fits |
| Judge | Scores fitness runs | Consistency across repeated runs | Middle to large; steadiness beats peak ability |

Toolers usually outnumber every other role in a crew, so the tooler choice dominates both
quality and cost. If a crew is producing weak answers, look at the toolers before
reaching for a bigger coordinator.

One caution on models with a toggleable thinking mode: a tooler with reasoning disabled
can regress badly, because it stops planning which tool to call. Verify the mode your
runtime actually sends, rather than assuming the default.

## Prefer permissive licenses

Favor Apache-2.0, MIT, and comparable permissive terms. Avoid models whose licenses
restrict commercial use, and read the terms on vendor-specific open licenses before
committing a crew to them.

## Adopting a model is a label change

Loose coupling means the adoption path does not touch agents:

1. Make the model available on the provider.
2. Declare a `Model` CR with labels and a `vramMib` request.
3. Adjust the crew's `CrewSchedulingPolicy` selectors only if the new model should win a
   phase it would not otherwise match.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Model
metadata:
  name: candidate-model
  labels:
    family: example
    params: "35B"
    capability/tool-calling: "true"
    capability/reasoning: "true"
    contextWindow: "262144"
    latencyClass: medium
spec:
  model: example-model:35b
  providerRef: ollama-a
  vramMib: 20480
  quantization: q4_K_M
```

No agent is edited and the crew is not redeployed. The scheduler picks the new binding on
the next inference call.

## Decide by measurement

Pick models with the fitness apparatus, not with a leaderboard screenshot. Run the same
`CrewFitnessSuite` at equal N against each candidate and compare tool-call pass rate,
factual accuracy, latency at p90, and token cost against your current model as the
reference.

The rule that makes the comparison mean anything is that **the model is the only
variable**. Hold prompts, crew composition, scenarios, judge, and N constant, and do not
deploy into a run that is in flight. A model swap is a clean experiment arm on its own;
it is not something to fold into an experiment that is measuring something else.

## Related

- [Models & Scheduling](../models-and-scheduling/) - loose coupling and just-in-time binding
- [Models in Kubemoot](../../reference/models/) - CRD fields, model catalog, per-GPU fit matrix
- [One Model Per GPU](../../adr/0009-one-model-per-gpu/) - why a card hosts one resident model
- [Define Fitness Functions](../../user-guides/define-fitness-functions/) - building the suite you measure with
