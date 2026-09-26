---
title: "About the Name"
weight: 90
description: "Where \"Kubemoot\" comes from - a moot is an assembly that deliberates to a decision."
---

In Anglo-Saxon England, a **moot** (or *gemōt*) was an assembly gathered to deliberate
and reach a decision. From village moots to the great Witenagemot that advised kings,
these assemblies embodied a principle: better decisions emerge when diverse
perspectives contribute to a shared deliberation.

Readers of Tolkien know another one. In *The Two Towers*, the Ents hold an
[Entmoot](https://tolkiengateway.net/wiki/Entmoot) to decide whether to march on
Isengard: a gathering in a hidden dell, unhurried, where every Ent speaks and nothing
is decided until all have. It takes three days. A Kubemoot crew takes minutes, which
is still slower than one model asserting an answer, and for the same reason: the
answer is not one voice's.

**Kubemoot** brings that principle to Kubernetes. The operator orchestrates AI agents,
each carrying domain expertise, into a discussion where a question is settled by
**deliberation** rather than dictated by one model. Agents communicate over a message
bus using a vocabulary of consensus signals (`agree`, `concern`, `stand_aside`,
`block`).

The name maps directly to the architecture:

- **Kube** - Kubernetes-native, built on CRDs and controllers
- **moot** - an assembly that deliberates toward a decision

The metaphor fixes the **goal** - reach an answer through structured deliberation, not
by a single authority asserting it - not one rigid structure. *How* a crew organizes
that deliberation (who convenes whom, who may object, how it settles) is a declared
**consensus archetype**. The archetype that ships today uses a facilitating
coordinator that convenes the relevant agents; a hierarchy is itself a valid
archetype, and the design deliberately leaves the door open for others. See
[Concepts](../../concepts/consensus-model/) for the consensus model.

**An assembly of AI agents, deliberating on Kubernetes. Every voice, one answer.**
