---
title: "Pilot - a Reference Crew"
weight: 30
description: "Homelab Pilot: one reference implementation of a canonical crew."
---

**Homelab Pilot** is one reference crew - an example of what a Kubemoot crew looks like
when it's fully built out. It is not the product; the product is the
[controller](../kubemoot-controller/). Pilot exists to demonstrate the controller with
a complete, real crew you can read and learn from.

## What it demonstrates

Pilot is an **infrastructure-operations** crew: its Toolers carry MCP tools for
Kubernetes, Helm, Proxmox, and Prometheus, and its Analysts carry domain knowledge
from Kubernetes, Proxmox, and Talos documentation. Ask it an operational question and
the coordinator convenes the relevant Toolers, they investigate with their tools,
Analysts reason over the findings in the REVIEW phase, and the crew settles on an answer.

It also shows the **shape** of a canonical crew end to end: a coordinator plus domain
Toolers and Analysts, prompts written in ADL, loosely-coupled models bound by the
scheduler, tools reached through the gateway, RAG knowledge sources, working memory
learned per cluster, and fitness functions that measure it. Pilot ships with its own
web/chat application as the front end users talk to.

## Why "one reference"

Pilot is deliberately framed as *a* reference crew, not *the* crew. Because the domain
lives in the crew rather than the controller, there can be many crews for many domains -
Kafka operations, research, security - all on the same machinery. Pilot is the worked
example for infrastructure ops; the [crews catalog](../crews/) is where others live.

## Learn from it

Read Pilot as a working template when you build your own crew. Start with
[Build a Crew](../../user-guides/build-a-crew/), then compare each piece against Pilot's
chart.
