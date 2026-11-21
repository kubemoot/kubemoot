---
title: goose
weight: 60
description: Add the crew liaison to goose as a remote extension and ask a crew from a goose session.
---

goose is an open-source agent that runs in a terminal or a desktop app and treats MCP
servers as extensions. The crew liaison is a remote extension over Streamable HTTP. For
the tools and the ticket flow see [The Crew Liaison](../crew-liaison/); for what the
liaison expects in a request see [Signing in](../signing-in/).

## Configuration

Try it for one session with the flag:

```bash
kubectl -n kubemoot port-forward svc/<release>-liaison 18080:80
goose session --with-streamable-http-extension "http://localhost:18080/mcp"
```

The extension is enabled for that session only. To keep it, run `goose configure`,
choose **Add Extension**, then **Remote Extension (Streamable HTTP)**, and give the
URL; or add it to goose's `config.yaml`:

```yaml
extensions:
  kubemoot:
    name: Kubemoot
    type: streamable_http
    uri: http://localhost:18080/mcp
    enabled: true
    timeout: 300
```

`timeout` is in seconds and is the longest goose waits for one extension call. The
liaison returns within 45 seconds on every call, so 300 is generous.

goose's documentation lists no setting for a fixed `Authorization` header on a remote
extension. This page was tested against a liaison with no token set. For a liaison that
requires a token, check goose's current extension settings, or reach it through a
route whose edge needs no header from goose.

## Use it

Ask in plain language, in a session or from a script. A script runs one question
with `goose run`, naming the session with `-n` and giving the question with `-t`; a
second run with `--resume` continues that goose session:

```bash
goose run -n crew-question -t "Ask the homelab-pilot crew how many namespaces the cluster has."
goose run -n crew-question --resume -t "Which of those were created most recently?"
```

goose's model calls `ask`. A quick crew answers in that call; otherwise the ticket is
`pending`, and the model calls `get_answer` with a wait of 45 seconds, reads the
answer when the crew settles, and does not ask again. In a tested run a first
question was answered inside `ask`, and a second needed one `get_answer` poll of 45 seconds
before the answer arrived, with no repeated question.

## Limits

The [limits that apply to every app](/docs/integrations/#limits-that-apply-to-every-app) apply, and
goose shows them clearly:

- **A resumed goose session is not a resumed Kubemoot conversation.** The second
  command above continues goose's own session, so the model knows what "those" refers
  to and rewrites the question for the crew in full. Kubemoot records two
  conversations, each with one turn, because `ask` takes no conversation id.
- **goose sends no time zone.** It does not put the person's zone in the model's
  context, so `timeZone` is absent unless you tell goose's model to pass it, for example
  in a recipe or instructions file.
- **goose shows no progress.** The liaison sends no progress notifications; goose waits
  on `get_answer` polls.
