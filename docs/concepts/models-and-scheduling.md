---
title: "Models & Scheduling"
weight: 40
description: "Loose model coupling and just-in-time GPU scheduling."
---

Kubemoot composes capability from **many small models** rather than one large one,
and it binds an agent to a model **as late as possible**. Two ideas make that work:
loose model coupling and just-in-time scheduling.

## Loose model coupling

An `Agent` spec names no model. It declares abstract **capabilities** -
`tool-calling`, `reasoning`, `kubernetes`, and so on. Concrete models are separate
resources: a `Model` (an LLM, by capability labels, not a hardcoded name) served by a
`ModelProvider` (a GPU-backed inference endpoint such as Ollama on a particular GPU).

The match between the two is made by a `CrewSchedulingPolicy`, whose rules select
candidate `Model` CRs by their labels. Because the agent never names
`qwen3:8b` (or any model), you can swap models, add a provider, or re-tier a crew by
changing labels and policy - not by editing and redeploying every agent. The same crew
runs unchanged on different hardware.

## Just-in-time scheduling

Scheduling happens in two steps. When an agent reconciles, the operator ranks the
`Model`s its phase may use. At each inference call, the agent runtime reads live
provider state (which models are loaded, which GPUs have free slots and free memory)
from a shared store and decides which model and which GPU serve that call. This
matters because GPU inference, model loading, and pod scheduling all have unpredictable
latency: a static binding made at deploy time would be wrong as soon as load shifted.

Cold start is treated the same as warm start: if a model isn't resident yet, the
system waits on the state transition rather than failing or requiring a manual warm-up.

## How a model is chosen

**The operator ranks candidates.** For each phase of an agent (mulling and triage),
the operator matches the crew's `CrewSchedulingPolicy` against the `Model` labels. A
rule's `require` selector decides which models are allowed at all. Its `prefer` weights,
or the policy's `qualityBias` when the rule has no `prefer` block, give each allowed
model a **quality score**. The best model becomes the agent's **preferred model**, and
the operator hands the agent the whole ranked list, preferred model first.

**The runtime picks per call.** At each call the runtime works down this order and
takes the first that succeeds:

1. The preferred model, if a GPU already has it loaded with a free slot.
2. Another candidate that a GPU already has loaded with a free slot, if its quality
   score is within the **quality tolerance** of the preferred model's score. Using it
   costs no model load and evicts nothing, so it is preferred over loading the
   preferred model from scratch.
3. A candidate within the quality tolerance that another agent is loading right now:
   the call joins that copy instead of loading a second model.
4. The preferred model under the normal cost ranking: queue behind a loaded copy, or
   load it onto a GPU with enough free memory.

If none of these has room, the agent waits (see below). The model a call actually ran
on is recorded on its signal as `metadata.model`, next to `metadata.provider`.

### The quality tolerance

The tolerance is **10 quality-score points** by default. A candidate qualifies when its
score is at most 10 points below the preferred model's score. How far apart models
score depends on the crew's policy:

- With `qualityBias`, each model's `latencyClass` label contributes `high` = bias x 100,
  `low` = (1 - bias) x 100, and `medium` = 100 - |bias - 0.5| x 200. At a bias of `0.7`,
  that is high 70, medium 60, low 30: an agent whose preferred model is `high` may use a
  loaded `medium` model (10 points below) but never a `low` one (40 below). At `0.9` it
  is high 90, medium 20, low 10, so the agent stays on `high`. A bias of `0.3` mirrors
  this toward the fast models.
- With explicit `prefer` weights, the gap is the difference between the weights. Weights
  of 100 and 50 are 50 points apart, so the agent keeps the preferred model.
- Models that score the same (for example, no `prefer` block, no `qualityBias`, and no
  `latencyClass` label) are interchangeable.

To keep an agent on a particular model size, do one of these:

- Narrow the rule's `require` selector to that size (for example, `params: "32B"`).
  Models outside the selector are never candidates.
- Give the size an explicit `prefer` weight more than 10 points above the others, or set
  the capability's `qualityBias` strongly toward quality (0.8 or higher) or speed (0.2
  or lower).
- Set `KUBEMOOT_MODEL_CANDIDATE_TOLERANCE` on the agent (through
  `spec.deployment.env`). `0` allows only candidates that score the same as the
  preferred model.

## When every GPU is busy

A GPU has room for a call when the model is loaded there with a free slot, or when the
model fits in the GPU's free memory next to the models already loaded. When no GPU has
room, the scheduler compares two ways forward and takes the cheaper one:

- **Wait for a loaded copy.** If the requested model, or a candidate within the quality
  tolerance, is already loaded somewhere but busy, the scheduler estimates when that
  copy frees up, from the calls running on it and how long calls there usually take.
  Waiting needs no load and unloads nothing.
- **Unload idle models and load.** On a GPU that could hold the model, the scheduler
  can unload models that are loaded but not in use. The cost is the time to load the
  requested model, plus, for each model unloaded, the time it would take to bring it
  back, weighted by how likely it is to be needed again soon. A large model is slow to
  reload, so unloading one that is still wanted is expensive.

The scheduler prefers waiting a short time for a model already loaded over unloading a
valuable one. It unloads only when the wait would cost more: a long queue on the busy
copy, or a model to unload that is small or that nobody needs.

### How it judges what is needed soon

Every crew's agents share one view of demand for each model:

- **Waiting**: agents waiting for a GPU for that model right now. A model agents wait
  for is never unloaded unless every choice has agents waiting for it.
- **About to request**: agents a coordinator just selected for a discussion, which name
  the model among their candidates. They will call within seconds.
- **Recent use**: how often the model was called lately (older calls count less), and
  when it was last used.
- **Usual subcommittees**: which agents each crew tends to select together, and which
  models those agents use. This is a forecast; it only affects what to keep.

When it must unload, the scheduler picks the fewest models that make room, and among
those the least needed: no agents waiting first, then fewest about to request, then
least recent use, then longest unused. A model with a call in progress is never
unloaded.

### Waiting

When neither way forward helps right away, or waiting is the cheaper one, the agent
**waits for capacity**:

- It publishes a `waiting` signal once, naming the model it waits for. CrewForge, Homelab
  Pilot, and the discussion stream show it as "waiting for a GPU with room for
  `<model>`".
- The wait is driven by state, not a timer. The agent tries again each time provider
  state or the in-flight calls change: a call finishes and frees a slot, or another
  agent's unload frees memory. When a retry succeeds, the agent publishes `evaluating`
  and carries on as usual.
- A **safety limit** ends a wait that never gets capacity: the discussion's synthesis
  timeout by default (90 seconds), or `KUBEMOOT_DISCUSS_GPU_WAIT_LIMIT_SECONDS` on the
  agent. A wait also ends when the discussion itself ends. Either way the agent stands
  aside with the reason `gpu-busy`. The limit is a safety net; the normal end of a wait
  is a GPU with room.

If no GPU in the cluster has enough memory to ever hold the model, waiting cannot help.
The agent stands aside at once with the reason `model-too-large`.

## Planning when an agent is selected

Kubemoot plans an agent's model the moment the coordinator selects it for a
discussion, not when the agent finally calls. The selected agent will call within
seconds, so the scheduler places its first call right away: it picks the model and the
GPU, reserves the capacity, and if the model is not loaded yet, starts loading it. The
load then runs while the agent triages the question and builds its prompt. When the
agent calls, it uses the planned GPU, or plans again if the cluster changed meanwhile.
If the agent stands aside or the discussion ends first, the reservation is released.

A discussion's agents are placed together. Agents that can use the same model converge
on one loaded (or loading) copy instead of each loading their own, and this holds
across crews asking at the same time, because every plan and every reservation is
visible to all of them.

### No preloading or pinning

Kubemoot loads a model only for an agent that a coordinator has selected for a live
discussion. It never preloads a model because it might be needed, and it never pins a
model to a GPU. Forecasts of demand only decide what to keep and where to load when an
agent actually needs a GPU. A GPU held for a guess starves the agent that actually asks
next.

## What the messages mean

| What you see | Meaning | What to do |
|---|---|---|
| `waiting` signal, "waiting for a GPU with room for `<model>`" | Every GPU that can hold the model is busy. The agent is queued on cluster state. | Nothing. It proceeds when capacity frees up. |
| `stand_aside` with reason `gpu-busy` | The agent waited but no GPU had room before the discussion ended or the safety limit passed. | Ask again when the cluster is quieter, add GPU capacity, or add a smaller `Model` the policy can pick. |
| `stand_aside` with reason `model-too-large` | No GPU in the cluster can hold the model. | Add a smaller `Model` that satisfies the rule, or a GPU with more memory. |
| `stand_aside` with reason `prompt-too-large` | The prompt is larger than the per-request context every suitable provider gives the model. | Raise `OLLAMA_CONTEXT_LENGTH` on a provider (trading against `OLLAMA_NUM_PARALLEL`), or use a model with a larger context. |
| `stand_aside` with no reason | The agent had nothing to add. | Normal crew behavior. |

When no agent contributed and at least one of them could not get a GPU, the crew's
answer says so instead of suggesting the crew lacks coverage:

> The crew's agents could not get a GPU: every GPU was busy with other work, so none of
> them could answer in time. This is the cluster's capacity, not the crew's design; ask
> again in a moment.

For `model-too-large` the answer names the model: "No GPU in this cluster can hold the
model `<model>` the agents need; add a smaller Model or a larger GPU." Either answer is
about cluster capacity. It does not mean the crew's agents, prompts, or tools are wrong.

## Several crews at once

All crews in a cluster share the same provider state, reservations, and demand view,
so their calls account for each other.

- **Warm reuse across crews is expected.** A model one crew loaded serves another
  crew's calls on the same GPU when that model is a candidate for both.
- **Unloading respects every crew.** A model another crew is waiting for, about to use,
  or uses often is the last to be unloaded.
- **Waits grow with load.** With more concurrent calls, slots fill and agents wait
  longer. A burst that outlasts the safety limit ends in `gpu-busy` stand-asides.
- Fuller prediction across concurrent crews is ongoing work. The behavior above is what
  happens today; there is no cross-crew priority or fairness beyond the per-call
  choice, the demand view, and the wait.

### Example: two crews ask at once

A cluster has two GPUs. GPU 1 (32 GiB) has `qwen3:14b` loaded with one free slot.
GPU 2 (24 GiB) has `qwen3:8b` loaded and is busy with another crew's call. Two crews
ask a question at the same moment, and each coordinator selects its agents.

1. Crew A's analyst prefers `qwen3:32b` (bias 0.7: `qwen3:32b` scores 70, `qwen3:14b`
   60, `qwen3:8b` 30). `qwen3:32b` is not loaded anywhere. `qwen3:14b` is loaded on
   GPU 1 with a free slot and scores within 10 points, so the analyst's plan takes that
   slot the moment it is selected. No model loads.
2. Crew B's rules-keeper prefers `qwen3:14b` and has no candidate within tolerance.
   `qwen3:14b` is loaded on GPU 1, so its call queues behind the analyst's call on that
   copy instead of loading a second copy.
3. Crew B's researcher prefers `qwen3:32b` with a strong quality bias (0.9), so
   `qwen3:14b` (20 points below) is out of tolerance. `qwen3:32b` is not loaded, GPU 1
   has no free memory beside `qwen3:14b`, and GPU 2 is busy. The scheduler weighs its
   options on GPU 1:
   - While the analyst and the rules-keeper are using `qwen3:14b`, it cannot be
     unloaded. The researcher publishes `waiting`.
   - When their calls finish and no agent is waiting for or about to use `qwen3:14b`,
     unloading it is cheap: the researcher's retry unloads the idle `qwen3:14b` and
     loads `qwen3:32b` onto GPU 1.
   - If other agents are waiting for `qwen3:14b`, or were just selected to use it, it
     stays loaded, and the researcher keeps waiting until memory frees up elsewhere or
     the safety limit passes and it stands aside with `gpu-busy`.

If no GPU in the cluster could hold `qwen3:32b` at all, the researcher would have stood
aside with `model-too-large` without waiting.

The same comparison protects a large model from a small one's rush. If the researcher
had instead wanted `qwen3:14b` while GPU 1 held a warm `qwen3:32b` that crews use
regularly, and `qwen3:14b` were loaded on GPU 2 with one call about to finish, the
researcher would wait a few seconds for GPU 2 rather than unload `qwen3:32b`.

## Which models to actually pick

Loose coupling decides *how* a model is bound. It does not decide *which* model belongs
in which role, or what fits the GPU memory you have. For the selection framework - why
tool-calling fidelity outranks coding benchmarks, how to budget VRAM, why mixture-of-experts
models size by total parameters, and how to match model size to discussion role - see
[Choosing a Model](../choosing-a-model/).
