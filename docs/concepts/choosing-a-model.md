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

Routing to a hosted model is a legitimate choice, but it is not supported yet. Ollama is
the only provider type today, and the API server rejects `openai` and `anthropic`. When
hosted models are supported, make the choice deliberately, because it moves inference, and whatever your
agents read from your cluster, off the premises. If staying local is the point of the
deployment, the frontier tier is simply not available, and the real question becomes
which 20B to 35B class model calls tools best on the card you own. For a homelab budget
in the range of a single $5,000 GPU with 24 GB to 48 GB of memory, that field is
genuinely competitive.

### The serving runtime is a separate choice

The model and the thing that serves it are different decisions. Kubemoot talks to a
`ModelProvider`, so the serving runtime is swappable and no crew depends on a particular
one. The runtime decides how many calls a card can work on at once, and a moot makes that
number matter: it fans one question out to several agents, so overlapping calls are the
normal case.

| Situation | Recommended engine |
|---|---|
| One model that several agents call at once | vLLM |
| Several models on one accelerator, each busy at times | vLLM with sleep mode, if the host has memory to park weights; otherwise Ollama |
| A quickstart, a CPU-only setup, or a small setup | Ollama |
| Many models on one GPU, each loaded on demand, on a host without spare memory | Ollama |

vLLM batches overlapping requests on one model, so its throughput keeps rising as calls
pile up. Ollama loads models per call, which suits many models sharing a card, but it
serves a fixed number of requests at a time per loaded model and queues the rest. vLLM
serves one model per server and switches models by restart or by sleep and wake. The
measured cost of each switch is in [Switching models](#switching-models).

#### Measured results

The measurement replayed 151 real model calls captured from the Homelab Pilot reference
crew against each engine, with identical settings: a 32768-token context and thinking
off. One engine ran at a time on an otherwise idle cluster. The replay sent 1,476
requests with 0 errors. It ran on 2026-10-18 against the current releases of both
engines at the time.

Terms used below:

- **c1, c4, c8**: 1, 4, or 8 requests in flight at once.
- **Throughput**: output tokens per second, summed across the requests in flight
  (tokens/s). Higher is better.

| Model | GPU | Engine | c1 | c4 | c8 |
|---|---|---|---|---|---|
| Qwen3 14B | RTX 5090 | vLLM | 115.6 | 300.4 | 371.3 |
| Qwen3 14B | RTX 5090 | Ollama | 99.6 | 145.0 | 144.7 |
| Qwen3 14B | RTX 4090 | vLLM | 75.9 | 192.5 | 257.7 |
| Qwen3 14B | RTX 4090 | Ollama | 66.8 | 94.5 | 98.8 |
| Qwen3 32B | RTX 5090 | vLLM | 62.0 | 168.2 | 195.4 |
| Qwen3 32B | RTX 5090 | Ollama | 53.4 | 53.9 | 53.1 |

What the numbers show:

- **One call at a time**, vLLM is about 15% faster.
- **With calls overlapping**, vLLM is 2.0 to 2.6 times faster on the 14B and 3.1 to 3.7
  times faster on the 32B.
- **Ollama's throughput stops at its slot count.** The 14B ran with 2 slots and the 32B
  with 1, because 2 slots do not fit in 32 GB at a 32k context. Calls beyond the slot
  count wait. On the 32B at c8, the median time to the first token of a text answer was
  91 s on Ollama and 5.6 s on vLLM.
- **The engine effect is the same on both GPUs**, within 4%. The RTX 5090 is 1.44 to 1.56
  times the RTX 4090 with either engine.
- **Prompt-prefix cache**: both engines reuse about 71% of prompt tokens at c1. Under load
  vLLM keeps 72 to 75% and Ollama drops to 63%.

Caveats:

- The engines ran different 4-bit builds of the same models: GGUF Q4 in Ollama and AWQ in
  vLLM. Speed compares per token. Answer quality was not judged, and on the 14B the AWQ
  build wrote about 13% fewer output tokens.
- The replay sends each captured conversation as recorded and does not run tools, so both
  engines receive identical input.

#### Switching models

Switch cost is the time from asking a model server for a different model to that model's
first full reply, minus a normal reply's time. Each value is the median of 5 switches per
direction. Each request was a median-sized captured crew call with output capped at 16
tokens and a unique prefix, so no prompt cache helped. There were 0 failed trials. Ollama
ran at its normal settings (one loaded model). vLLM ran with a 16384-token context and 85%
of GPU memory per server. The measurement ran on 2026-10-18 against the current releases
of both engines at the time.

How each engine switches:

- **Ollama** unloads one model and loads the other.
- **vLLM restart** stops one server and starts the other.
- **vLLM sleep mode** keeps every server started with one awake. At level 1 the sleeping
  model's weights are parked in CPU memory. At level 2 they are dropped and reloaded.

Switch cost in seconds:

| Switch | GPU | Ollama | vLLM restart | vLLM sleep level 1 | vLLM sleep level 2 |
|---|---|---|---|---|---|
| 8B to 14B | RTX 5090 | 2.78 | 35.6 | 0.72 | 3.33 |
| 14B to 8B | RTX 5090 | 2.75 | 33.6 | 0.73 | 2.50 |
| 14B to 32B | RTX 5090 | 3.12 | 46.6 | 1.21 | 4.92 |
| 32B to 14B | RTX 5090 | 3.09 | 36.6 | 1.21 | 3.31 |
| 8B to 14B | RTX 4090 | 2.77 | 37.6 | 1.25 | 4.50 |
| 14B to 8B | RTX 4090 | 3.07 | 33.6 | 1.22 | 3.45 |

What the numbers show:

- **vLLM sleep mode switches as fast as Ollama or faster.** Level 1 took 0.7 to 1.3 s,
  level 2 took 2.5 to 4.9 s, and Ollama took 2.8 to 3.1 s.
- **Restarting vLLM is 11 to 15 times slower than Ollama.** Switch vLLM models by sleep
  and wake.
- **Ollama's first load after a node restart is slower.** It took 13 to 51 s, because the
  weights are read from network storage, and about 3 s after that.

Costs of sleep mode:

- Level 1 needs host memory for every sleeping model's weights. A 14B and a 32B together
  needed a 44 GiB container memory limit on a 48 GB node. A memory-backed `/dev/shm`
  counts against that limit.
- Each model's server must be started once. That takes about 35 to 47 s when its weights
  and compile cache are already on the node, and minutes otherwise.

With sleep mode, vLLM keeps fast switching alongside its concurrency advantage. Ollama
remains a fit for CPU-only and quickstart setups and for hosts without memory to park
weights.

Not measured yet: answer quality through vLLM, and more than two models sharing one
accelerator.

To run vLLM on a GPU node, see
[Run vLLM on a GPU node](../../operating/gpu-worker-nodes/#run-vllm-on-a-gpu-node). For
which provider types a `ModelProvider` accepts today, see
[Models in Kubemoot](../../reference/models/).

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
- [Scheduler](../../architecture/scheduler/) - how models are placed on GPUs, several to a card when they fit
- [Define Fitness Functions](../../user-guides/define-fitness-functions/) - building the suite you measure with
