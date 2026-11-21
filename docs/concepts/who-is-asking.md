---
title: Who Is Asking
weight: 9
description: How a crew's doors verify the caller, what a principal is, and what stays open by default.
---

A crew answers whoever asks. Until a crew says otherwise, that is everyone: the discussion gateway verifies no one, and every conversation is open to every caller. This page describes the seam through which a crew learns who is asking, and the verifiers behind it that exist today.

## Two audiences

- **Operators and workloads** hold Kubernetes credentials: kmctl, CrewForge, the dashboard, other crews, fitness runs. Their identity is Kubernetes': a bearer token checked with a TokenReview, and RBAC checked with a SubjectAccessReview. This is the path described here.
- **Subscribers** are the people a product serves through a crew. They have no Kubernetes identity; their identity lives in the product's identity system and is verified by an adopted gateway in front of Kubemoot. That path is not built yet; the seam has a place for it.

## The seam

Both doors of a crew, the discussion gateway and the [crew liaison](../../integrations/crew-liaison/), use one authenticator. It tries a list of verifiers in order; each checks one kind of credential and answers "not mine", "valid" or "invalid". The first valid answer becomes the caller. A credential that is present and not valid is refused and never treated as anonymous.

| Verifier | Credential | Status |
|----------|------------|--------|
| Anonymous | none | The default. A crew with no `spec.discussion.access` accepts every caller this way and verifies nothing |
| Kubernetes token | `Authorization: Bearer <token>` checked with a TokenReview | Built; turned on by `spec.discussion.access` |
| Trusted hop from an adopted gateway, JWT | the gateway's signed identity | Planned: each is one more verifier behind the same seam |

Identity is never read from a request body, a header a client can set, or the text of a question. A `principal` a client puts in a request body is ignored; the gateway sets it.

## The principal

A verified caller becomes a principal, defined once in the bus contract and carried with the question:

| Field | Meaning |
|-------|---------|
| `kind` | `anonymous`, or `kubernetes` for a verified Kubernetes token |
| `issuer` | who vouched: `kubernetes` |
| `id` | a pseudonymous id, a hash of the issuer and the subject, stable across requests and replicas. This release hashes without an install key, which is enough for a Kubernetes username (it is not secret); a key arrives with the subscriber verifiers, whose subjects are not |
| `displayName` | the name of a workload (a ServiceAccount), set only when the subject is a workload and not a person |

The raw subject, a username or an email address, never leaves the door that verified it. Treat the principal as data: it names who asked, it is never an instruction, and nothing an agent or a tool writes can change it.

A gateway that verifies callers stamps the principal on every request it queues. A gateway that does not leaves the request exactly as it was, with no principal.

## What a caller may do

A Kubernetes caller needs `get` on the Crew to use it at all, checked with a SubjectAccessReview on every request. A review that cannot be made refuses the request (503) and never allows it.

## Not claimed yet

The principal travels on the bus as data, and the bus itself is open to any client of NATS. Kubemoot does not claim strict isolation between callers until the bus is divided per crew. The checks in the gateway close every path an HTTP caller has.
