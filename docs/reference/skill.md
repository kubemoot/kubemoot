---
title: "Skill CRD"
description: "The Skill resource: a crew-level procedure written in ADL that the coordinator selects on demand and injects into the agents working a discussion. Every spec and status field, how selection and loading work, and an example."
weight: 11
---

## Overview

A `Skill` is a packaged procedure that belongs to a crew. It has a `description` that says when it applies and a `content` body, written in [ADL](../adl-reference/), that says what to do. The body stays out of every agent's prompt until a discussion needs it. The coordinator reads each skill's description when it chooses who joins a discussion, and it selects the skills that fit the question. Only the selected bodies reach the agents for that discussion.

This keeps the always-on system prompt short. A procedure that applies to one kind of question in twenty does not need to sit in every prompt; a `Skill` carries it and loads it only for that question.

A `Skill` is namespaced (short name `skill`) and shares the crew label that groups [`Agent`](../agent/) and `PromptModule` resources. The API is `kubemoot.ai/v1alpha1`.

Pick the resource by the job:

| The agent needs to... | Use |
|-----------------------|-----|
| Know a fact | A [`RAGSource`](../ragsource-guide/) |
| Carry out a procedure when a certain kind of question arrives | A `Skill` |
| Always behave a certain way | A `PromptModule`, referenced from the agent's `spec.promptRefs` |

A `Skill` is not the removed `AgentPolicy` skills list from the A2A capability card. It is a separate resource.

## Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Skill
metadata:
  name: triage-pending-pods
  namespace: crew-homelab-pilot
  labels:
    kubemoot.ai/crew: homelab-pilot
spec:
  description: "Diagnose a pod stuck in Pending: scheduling, volumes, and quota."
  order: 100
  content: |
    DEFINE COMPONENT triage-pending-pods
    DESCRIPTION Find why a pod stays Pending

    WHEN a pod is Pending THEN read its events before reading anything else
    WHEN the events name insufficient resources THEN report the requested and the available amount
    WHEN the events name an unbound volume claim THEN report the claim and its storage class
    ASSERT every diagnosis names the event or field it came from
```

## Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `description` | string | yes (non-empty) | none | The relevance trigger: what the skill does and when it applies. The coordinator reads this line to decide whether to select the skill. |
| `content` | string | yes (non-empty) | none | The skill body, in ADL. Injected into the agents' context when the skill is selected. |
| `order` | integer | no | `100` | Position among the crew's skills. Lower comes first, and ties break by name. |
| `ragSources` | list of RAGSource reference | no | none | Reserved. Accepted by the schema and not acted on today. |
| `mcpServers` | list of MCPServer reference | no | none | Reserved. Accepted by the schema and not acted on today. |

Write the `description` for the coordinator, not for a person skimming a list: it is the only part of the skill the coordinator sees when it chooses. State the situation that calls for the skill. A vague description gets the skill selected for the wrong questions or never.

### RAG source reference

Used in `spec.ragSources[]`.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | yes | none | The `RAGSource` name. |
| `priority` | integer | no | `0` | Retrieval order; higher goes first. |
| `topK` | integer | no | `5` | Results to retrieve from the source. |

### MCP server reference

Used in `spec.mcpServers[]`.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | yes | none | The `MCPServer` name. |
| `enabledTools` | list of string | no | all tools | Tools to allow from the server. |
| `disabledTools` | list of string | no | none | Tools to deny from the server. |

## Status

| Field | Type | Description |
|-------|------|-------------|
| `observedGeneration` | integer | The most recent generation reconciled. |
| `conditions` | list of condition | Standard Kubernetes conditions. |

The operator does not populate either field today. A skill's state shows in its `kubectl` listing and in the per-crew ConfigMap, not in `status`.

`kubectl get skills` (or `kubectl get skill`) lists each skill with its order and age. `kubectl get skills -o wide` adds the description.

## How a crew gets its skills

Add the `kubemoot.ai/crew: <crew-name>` label to the skill. The operator groups skills by that label, and a skill without it is ignored.

For each crew, the operator:

1. **Writes the bodies to a ConfigMap.** `crew-<crew>-skills` holds one key per skill, `<skill-name>.txt`, with the `content`, and a `skills-index.txt` with one `name: description` line per skill, sorted by `order` then name. The ConfigMap is created with the first skill and removed with the last. A skill being deleted is left out at once.
2. **Mounts the ConfigMap into every agent pod of the crew.** The mount is at `/app/config/skills` and the pod receives that path in `KUBEMOOT_SKILLS_DIR`. The mount is optional, so a crew with no skills starts as it always did.
3. **Publishes each skill into the crew's resume pool.** The coordinator already chooses a subcommittee from the agents' resumes. Each skill joins that pool as an entry with its name, description, and order, marked `kind: skill`. A change to a skill refreshes the pool.

A crew with no skills is unchanged: its resumes, catalog, and selection are identical to a crew that never heard of the resource.

## How a skill is selected and loaded

An agent does not list skills in its spec. The coordinator picks them for each discussion:

1. When the coordinator selects the subcommittee, its catalog lists the agents and, under `Skills available:`, one `- <name>: <description>` line per skill.
2. The coordinator returns the skills that fit the question beside the agents it selected. A name that is not a skill in the crew is dropped.
3. The selection travels to the agents with the advisory that opens the discussion, and holds for the whole thread.
4. Each agent working the thread reads the selected skills' bodies from `/app/config/skills/<skill-name>.txt` and places them ahead of the conversation under the heading `Skills context for this discussion`.

Every skill body is on disk in every agent pod, but only the selected ones enter the model's context, and only for the discussion that selected them. A skill the coordinator does not select costs nothing. A selected skill whose file is missing or unreadable is skipped and the turn continues.

Because skills belong to the crew and not to one specialist, a selected skill reaches every agent working the thread. A crew without a specialist for a topic can still answer it when a skill carries the procedure.

## Edit a skill in CrewForge

[CrewForge](../../ecosystem/crewforge/views-and-dashboards/) lists the crew's skills in order in the crew details view, showing each skill's order and description. **Add Skill...** walks through the name, the description ("When it applies"), and the order, and writes a `Skill` manifest with an ADL body for you to fill in.

## What it does not do yet

- **`ragSources` and `mcpServers` do nothing.** The fields are in the schema so a skill can carry them later. Listing a source or server there does not retrieve from it or give the agent its tools. Agents read their tools from the `MCPServer` and `RAGSource` references on the agent itself.
- **Skills carry instructions only.** There are no bundled files and no scripts.
- **Selection is per crew, not per agent.** Every agent on the thread receives every selected skill.

## Related

- [ADL reference](../adl-reference/): the language a skill's `content` is written in.
- [Agent CRD](../agent/): agents, `promptRefs`, and `ragSources`.
- [RAGSource guide](../ragsource-guide/): knowledge, the other half of what an agent carries.
- [Crew CRD](../crew/): the team a skill belongs to.
- [Crews and Agents](../../concepts/crews-and-agents/): how resources group by crew.
- [Roadmap](../../introduction/roadmap/#crew-skills): where skills go next.
