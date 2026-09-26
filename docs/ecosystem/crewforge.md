---
title: "CrewForge"
weight: 40
description: "The crew-authoring IDE (desktop today; a VS Code plugin is envisioned)."
---

**CrewForge** is an integrated environment for **authoring crews**. Building a crew
means writing a set of related Kubernetes resources - a `Crew`, its `Agent`s, their
`PromptModule`s, and the models, tools, and knowledge they use - and CrewForge is the
tool that makes editing that set coherent rather than hand-managing a folder of YAML.

## What it is

CrewForge is a desktop application today (built with Tauri and Svelte). A **VS Code
plugin** is envisioned, to bring the same authoring experience into an editor many
developers already live in.

## What it does - and what it doesn't

CrewForge is a **CRD editor**: it creates, reads, updates, and deletes Kubemoot crew
manifests. It deliberately does **not** own lifecycle. Deleting, garbage-collecting,
namespace management, and cascading cleanup all belong to the
[controller](../kubemoot-controller/), which handles them through finalizers and owner
references. CrewForge stays a lightweight authoring surface; the operator stays the
single source of lifecycle truth. That boundary is intentional - it keeps the IDE
simple and avoids two systems disagreeing about what should be cleaned up.

## Where it fits

CrewForge sits at the start of a crew's life - authoring - and hands its output to the
controller to run. For the manual path the same resources can be written by hand and
applied with `kubectl`; CrewForge is the assisted way to do it. To understand what
you're authoring, see [Build a Crew](../../user-guides/build-a-crew/).
