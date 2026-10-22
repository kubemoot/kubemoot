---
title: "The Harness Seam and the Shim"
weight: 7
description: "How an external harness such as goose takes a seat at the table through the Moot Mesh sidecar, why a thin shim gives command-line harnesses the A2A face the sidecar calls, and how another harness reuses it."
---

> **Audience:** crew authors who choose a harness; developers who add one; anyone comparing harnesses on the same crew.

## The seam

Every agent at the table is a harness behind the same seam. The harness does the
reasoning: it builds the prompt, runs the tool loop, and decides what to say. The
[Moot Mesh](../moot-mesh/) sidecar does everything else: the bus, the signals, the
placement of each model call on an accelerator, and the tool allow-list.

The agent-runtime that ships with Kubemoot is the default harness. An agent with no
`spec.harness` block is a legacy agent-runtime pod. An external harness takes a seat
by declaring `spec.harness.mode: sidecar`. The operator then adds the sidecar to
the pod, and the sidecar hands the harness one turn at a time over A2A on the
loopback interface. The harness calls a model and tools through two more loopback
doors. It never learns the bus, the model servers, or the tool gateway.

## Why a shim

The sidecar speaks one protocol to every harness: A2A over HTTP+JSON. Most
harnesses do not. They are command-line programs that take a prompt and print an
answer, or they run a server with a protocol of their own. The shim is the one
adapter that gives any of them the A2A face the sidecar calls. It serves the agent
card and `SendMessage`, runs the configured command for the turn, and maps what the
command prints to the reply.

Because the command is configuration, changing harness is a configuration change,
not a rewrite. Nothing in the sidecar, the operator, or the bus changes when the
command changes.

## Why goose is the first external seat

- It is Apache-2.0 licensed.
- It is governed by the Agentic AI Foundation, beside MCP and A2A, the two standards
  the doors use.
- It uses MCP for tools, so the tools door is its native tool interface.
- It works with any OpenAI-compatible model server, so the model door is its
  native provider interface, and the sidecar places the call.
- It runs headless, one task per invocation.

## How the shim runs goose

The shim runs one `goose run` per turn. goose also has server modes (`goose serve`
speaks the Agent Client Protocol over HTTP and WebSocket), and the shim does not use
them for two reasons. The Agent Client Protocol transport over HTTP is still a draft
in the protocol's own proposals. And a goose server holds its model provider
settings in the process, while each Kubemoot turn carries its own key on the model
door so the sidecar can attribute every call to its turn; one process per turn
gives each turn its own key, its own home directory, and its own tool set.

What this costs:

- **Start-up per turn.** About half a second for goose to start with the shim's
  defaults (0.47 s measured against a local stub model; 0.59 s in an earlier
  measurement). Model time is not part of that.
- **No progress during a turn.** The shim returns when goose finishes. The
  sidecar's heartbeats show that the pod is alive, but the dashboard shows no
  intermediate activity from the harness.
- **Cancel is a kill.** When the sidecar stops waiting, or the turn deadline
  passes, the shim kills goose's process group. goose gets no chance to clean up,
  which is acceptable because each turn runs with no saved session and a private
  home directory that the shim removes afterward.

If goose's server mode becomes stable and carries a per-session provider key, the
shim can drive it for progress and cheaper turns without changing what the sidecar
sees.

## How the shim treats a turn

For each `SendMessage` the shim:

1. reads the question, the agent's instructions, the thread so far, the turn key,
   the deadline, the current time, and the requester's time zone;
2. starts the command in its own process group, in a private home directory, with
   `KUBEMOOT_MODEL_URL`, `KUBEMOOT_TOOLS_URL`, `KUBEMOOT_TURN_KEY`,
   `KUBEMOOT_TIME_ZONE`, `KUBEMOOT_NOW`, and `TZ` set;
3. takes the answer from the command's output;
4. replies with a completed task, or a failed, canceled, or rejected one.

A failed command, an empty answer, and a passed deadline are failed tasks, which the
sidecar publishes as a `failure` signal. They are never silence. Turns run
concurrently; one stuck turn does not hold up a second, and a turn over the
configured limit is rejected so the sidecar can stand aside.

## Reusing the shim for another harness

The shim reads a JSON configuration file. For goose the image ships this:

```json
{
  "argv": ["/usr/local/bin/goose", "run", "--no-session", "[none]--no-profile",
           "--output-format", "json", "--with-streamable-http-extension", "{{toolsURL}}",
           "-t", "{{prompt}}"],
  "env": ["GOOSE_PROVIDER=openai", "OPENAI_HOST={{modelHost}}", "OPENAI_API_KEY={{turnKey}}"],
  "output": "goose-json"
}
```

- `argv` and `env` take the placeholders `{{prompt}}`, `{{question}}`,
  `{{instructions}}`, `{{modelURL}}`, `{{modelHost}}`, `{{toolsURL}}`, `{{turnKey}}`,
  `{{timeZone}}`, and `{{now}}`.
- An `argv` element that starts with `[none]` or `[declared,all]` is kept only when
  `nativeTools` is one of the listed modes. This is how a harness image turns its
  built-in tools off for `nativeTools: none`.
- `output` is `text` (the command's whole output) or `goose-json`.

To seat another command-line harness, such as opencode or `claude -p`, build an
image with that program and the shim, write a configuration with its command line,
and set `spec.harness.image` to it. The harness must accept an OpenAI-compatible
base URL for the model door, and should accept an MCP URL for the tools door.
A harness that already serves A2A, or that has its own A2A wrapper, does not need
the shim.
