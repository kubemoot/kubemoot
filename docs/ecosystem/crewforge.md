---
title: "CrewForge"
weight: 40
description: "Authoring crews: your editor and kmctl today; a VS Code extension is the planned surface."
---

**CrewForge** is the name for the crew-authoring experience: building a crew means
writing a set of related Kubernetes resources, a `Crew`, its `Agent`s, their
`PromptModule`s, and the models, tools, and knowledge they use, and keeping that set
coherent as it grows.

## Authoring today

A crew is a folder of YAML, and the best tool for a folder of YAML is the editor you
already use. VS Code with the Kubernetes and YAML extensions gives you completion,
validation against the CRD schemas, and diffs; [kmctl](../kmctl/) scaffolds, applies,
and runs fitness from the terminal. The [crews](../crews/) repository holds packaged
examples to copy from. See [Build a Crew](../../user-guides/build-a-crew/) for the
resources themselves.

## The planned surface

An earlier standalone desktop application (Tauri and Svelte) proved the shape: a
CRD-aware editor with templates, a deploy action, and a fitness runner. Its future is a
**VS Code extension** with the same goal, so the authoring experience lives where the
editing already happens rather than duplicating an editor. Until then, nothing in
Kubemoot depends on it.

## The boundary that stays

Whatever the surface, CrewForge is a **CRD editor**: it creates, reads, updates, and
deletes crew manifests and deliberately does not own lifecycle. Deleting,
garbage-collecting, namespace management, and cascading cleanup belong to the
[controller](../kubemoot-controller/), through finalizers and owner references. The
authoring tool stays light; the operator stays the single source of lifecycle truth.
