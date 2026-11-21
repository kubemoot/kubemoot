---
title: Signing in
weight: 15
description: What a chat app presents to the crew liaison today, and what is planned for signing in a person.
---

A chat app that reaches a crew has two places where identity can matter: the
liaison's door, and the services a crew's tools call. This page states what is built.

## Today

- **The liaison has no sign-in of its own.** It does not run a login page, an OAuth
  flow, or an identity provider. It accepts one of three things, set in the chart:
  - No token and no Kubernetes token check: every caller is accepted, as anonymous.
  - A shared token (`liaison.auth.token`): the app sends
    `Authorization: Bearer <token>`. Everyone who holds the token is the same caller.
  - `liaison.auth.kubernetesTokens: true`: the liaison checks a Kubernetes bearer
    token with a TokenReview, so the caller is a Kubernetes identity (a user or a
    ServiceAccount). Each ticket belongs to the caller who asked.
  A request that presents a token that is not valid is refused. See
  [Knowing who is asking](../crew-liaison/#knowing-who-is-asking).
- **The discussion gateway verifies Kubernetes tokens per crew.** With
  `spec.discussion.access` on a crew, the gateway checks the caller's Kubernetes
  token, authorizes it against the Crew, and records the caller as the owner of the
  conversations it starts. The liaison passes the caller's token on to the gateway.
  See [Who is asking](../../concepts/who-is-asking/).
- **A chat app sends one static header.** Every app on these pages can attach a fixed
  `Authorization` header to its connection. The app holds the token; the person
  typing does not present their own. Two people using the same app instance with a
  shared token are one caller to Kubemoot.

Per-user placeholders some apps offer in headers (a user id or email in a header) are
sent to the liaison but are not read: identity comes from a verified token, never
from a header a client can set.

## What a crew's tools sign in to

A crew's tools may call services that have their own accounts, such as a search
service. Those credentials belong to the crew's installer and are kept in the
crew's tool gateway, not in the chat app. The asker's own account at such a service
is not used today: the tool uses the credential the installer configured, whoever asks.

## Planned

Per-person sign-in to a crew's service is planned: a person would connect their own
account at the service a crew's tool calls (through that service's own sign-in page),
and the tool would use that person's access for their question only. It is not
built, and the chat apps on these pages have nothing to configure for it. Until it
exists, treat a crew's tool credentials as the installer's, and restrict who can reach
the liaison to people the installer is willing to have use them.
