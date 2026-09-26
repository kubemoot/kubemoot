---
title: "Crews"
weight: 20
description: "The crews repository: internal and example crews, a growing catalog."
---

If the [controller](../kubemoot-controller/) is the substrate, **crews** are what runs
on it. The crews repository is a home for many crews - both the ones Kubemoot needs to
operate and example crews that show how to build your own.

## Internal and example crews

- **Internal crews** are part of how Kubemoot works. The fitness judge crew, for
  instance, scores other crews' fitness runs. These ship with the project.
- **Example crews** demonstrate patterns you can copy - the clearest being
  [Homelab Pilot](../pilot/), an infrastructure-operations crew that doubles as the
  reference implementation.

## A growing catalog

The intent is an **ecosystem** of crews, not a fixed set. A crew is a portable Helm
chart - agents, prompts, tools, and knowledge bundled together - so crews are meant to
be shared and reused across clusters. As the catalog grows, crews are expected to be
published to a registry such as **ArtifactHub.io**, the same way Helm charts and other
cloud-native artifacts are distributed.

## Build your own

A crew is the unit you author when you want Kubemoot to serve a new domain. Start from
[Build a Crew](../../user-guides/build-a-crew/), and look at the example crews in the
repository as working references.
