---
title: "Connected accounts"
description: "How a crew declares the outside accounts it needs, how an MCPServer names one, and the ConnectedAccounts condition that says whether a declaration is consistent."
weight: 12
---

## Overview

Some crews use a service that bills or entitles each person: a library, a code host, a document store. A crew declares each such service as a **connected account**. The declaration travels with the crew and names no install: it says what the crew needs and where the service signs people in. The install registers its own client with each service, and an MCPServer of the crew names the account it takes the person's token from.

A crew that declares none behaves as it always has.

## Declaring an account on a Crew

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Crew
metadata:
  name: library
spec:
  connectedAccounts:
    - name: library
      displayName: Example Library
      purpose: Search the library with your own subscription.
      usage: Required
      scopes: ["openid", "offline_access", "search"]
      authorizeURL: https://auth.example.org/oauth2/authorize
      tokenURL: https://auth.example.org/oauth2/token
      revocationURL: https://auth.example.org/oauth2/revoke
      tokenHosts: ["mcp.example.org"]
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `connectedAccounts[].name` | string | required | The account's name across the install, a DNS label. A crew and an MCPServer name it, and two crews that declare one name declare one service. Names are unique within a crew |
| `connectedAccounts[].displayName` | string | required | The service's name as a person sees it. Up to 64 characters |
| `connectedAccounts[].purpose` | string | | One sentence saying what the crew does with the account. Up to 300 characters |
| `connectedAccounts[].usage` | string | `Required` | `Required`: the crew needs the account. `Optional`: the crew's tools can fall back or skip without it |
| `connectedAccounts[].scopes` | []string | none | The OAuth scopes the crew needs at the service |
| `connectedAccounts[].authorizeURL` | string | required | The service's OAuth authorization endpoint. `https` with a host |
| `connectedAccounts[].tokenURL` | string | required | The service's OAuth token endpoint. `https` with a host |
| `connectedAccounts[].revocationURL` | string | | The service's [RFC 7009](https://www.rfc-editor.org/rfc/rfc7009) revocation endpoint. `https` with a host |
| `connectedAccounts[].tokenHosts` | []string | required | The hosts the account's token may be sent to, as bare lower-case host names without scheme, port or path. At least one |

The API server refuses a declaration whose name is not a DNS label, whose endpoints are not `https` with a host, or that has no `tokenHosts`.

## Naming an account in an MCPServer

Set `registry.callerCredential.account` to the name of an account a Crew of the server's namespace declares. It replaces `verifier` as the way to name where the person's token comes from.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: library-search
spec:
  externalEndpoint: https://mcp.example.org/mcp
  registry:
    callerCredential:
      mode: Required
      account: library
```

| Rule | Enforced by |
|------|-------------|
| `account` needs `mode: Required` | the API server |
| `account` and `verifier` are mutually exclusive | the API server |
| `mode: Required` needs one of `verifier` and `account` | the API server |
| `account` is declared by a Crew of the namespace | the `ConnectedAccounts` condition, reason `UnknownAccount` |

The tool gateway receives the account name in the server's registration.

## What the install registers

The install registers a client at each service under `liaison.connectedAccounts.accounts` in the operator chart, and turns
the feature on. It is off by default and nothing is kept when it is off. Each name must be a DNS label, or the chart refuses to render. The operator reads the names.

```yaml
liaison:
  auth:
    hashKeySecret: {name: install-hash-key}
  connectedAccounts:
    enabled: true
    sealKeySecret: {name: liaison-vault-keys}
    accounts:
      library:
        clientId: "<the id registered at the service for <publicURL>/oauth/callback>"
        clientSecret: {name: library-client-secret}   # optional; a public client uses PKCE alone
        hosts: []             # pin the service: declared endpoints must be on these hosts
        issuer: ""            # the service's issuer (RFC 9207); empty accepts the host of its authorization endpoint
        allowedCrews: []      # empty: every crew that declares it; else names or namespace/name
        maxLifetimeHours: 0   # the longest the service lets a connection stand; 0 for none known
```

| Value | Default | Description |
|-------|---------|-------------|
| `liaison.connectedAccounts.enabled` | `false` | Turns on the connect pages, the account store and the broker. Needs the door's own identity, `sealKeySecret`, `liaison.auth.hashKeySecret`, and `liaison.state.backend` `nats` or `openbao` with more than one replica |
| `liaison.connectedAccounts.sealKeySecret` | required when enabled | A Secret you create with `id:base64key` lines (32 random bytes each) that seal what is kept; apart from the door's own keys |
| `liaison.connectedAccounts.brokerPort` | `8081` | The internal port of the account broker and its Service |
| `liaison.connectedAccounts.accounts.<name>.clientId` | required | The install's client id at the service |
| `liaison.connectedAccounts.accounts.<name>.clientSecret` | none | A Secret (`name`, `key` default `client-secret`) mounted as a file. Never in the environment |
| `liaison.connectedAccounts.accounts.<name>.hosts` | `[]` | Hosts the declared authorization, token and revocation endpoints must be on. A declaration elsewhere is not offered. Set it, with `allowedCrews`, so no crew can point the install's client at a host the install did not choose |
| `liaison.connectedAccounts.accounts.<name>.issuer` | empty | What the `iss` of the service's redirect must equal |
| `liaison.connectedAccounts.accounts.<name>.allowedCrews` | `[]` | Limits the account to crews, by name or `namespace/name` |
| `liaison.connectedAccounts.accounts.<name>.maxLifetimeHours` | `0` | The longest a connection is kept from the day it was made, for a service that limits it without saying so in its token answer |
| `liaison.state.backend` | empty | Where the liaison keeps state: `memory`, `nats` or `openbao`; empty picks `nats` with the door on |

A crew that declares a name missing from this list reports `UnregisteredAccount`. Only crews the registration allows supply the endpoints the install's client is used against, and allowed crews that declare one name for different services get no offer ([details](../../integrations/signing-in/#connected-accounts)). The retention rules, the pages and the store are described in [Connected accounts](../../integrations/signing-in/#connected-accounts) and [Data ownership](../../operating/data-ownership/#a-persons-connected-accounts).

## Who may ask: the `connectedAccount` rule

A crew whose `spec.discussion.access.askers` is `connectedAccount: <name>` admits a person only while they hold a live connection to that
account for this crew. The crew's gateway asks the liaison's account broker, which the operator configures (the broker's address, and a
projected ServiceAccount token made for the broker alone) when the install has connected accounts on. A gateway with no broker, and a broker that does not
answer, refuse. The rule needs `hashKeySecretRef` and a trusted hop, like every askers rule other than `kubernetesRBAC`.

## The ConnectedAccounts condition

A Crew carries a `ConnectedAccounts` condition when it declares at least one account, or when a server in its namespace names an account that no Crew of the namespace declares. Otherwise it carries none. The condition never affects the crew's `Ready`.

| Status and reason | Meaning |
|-------------------|---------|
| `True`, `Ready` | Every declared account is registered by the install, every account a server names is declared, and no other crew uses a declared name for a different service |
| `False`, `Conflict` | Another crew, in any namespace, declares the same name with a different `authorizeURL`, `tokenURL`, `revocationURL` or set of `tokenHosts`. One name must mean one service, so give one of the accounts another name. Both crews report it |
| `False`, `UnregisteredAccount` | The install has not registered a name the crew declares. The message lists the missing names |
| `False`, `UnknownAccount` | A server in the namespace takes an account that no Crew of the namespace declares. The message lists each server and account. Every Crew of the namespace reports it |
| `False`, `NoSingleToolGateway` | A server in the crew's namespace takes one of the crew's accounts, and the namespace has no MCPGateway or several |

The reasons are checked in this order: `Conflict`, `UnregisteredAccount`, `UnknownAccount`, `NoSingleToolGateway`.
