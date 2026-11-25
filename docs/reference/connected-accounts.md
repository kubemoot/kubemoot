---
title: "Connected accounts"
description: "How a crew declares the outside accounts a person connects, how a tool server takes the person's token from one, and the ConnectedAccounts condition that says whether a declaration can work."
weight: 12
---

## Overview

Some crews use a service that bills or entitles each person: a library, a code host, a document store. A crew declares each such service as a **connected account**. The person connects it once, and the crew's tools act as that person at the service. The declaration travels with the crew and names no install: it says what the crew needs and where the service signs people in. The install registers its own client with the service, and an MCP server of the crew takes the person's token from the account.

Nothing about an account is assumed by the door. A crew that declares none behaves as it always has.

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
| `connectedAccounts[].displayName` | string | required | The service's name as a person sees it on the consent page and in an answer that asks them to connect. Up to 64 characters |
| `connectedAccounts[].purpose` | string | | One sentence shown on the consent page: what the crew does with the account. Up to 300 characters |
| `connectedAccounts[].usage` | string | `Required` | `Required`: asking the crew needs the account connected. `Optional`: the crew's tools fall back or skip without it |
| `connectedAccounts[].scopes` | []string | none | The OAuth scopes the crew needs at the service |
| `connectedAccounts[].authorizeURL` | string | required | The service's OAuth authorization endpoint. `https` only |
| `connectedAccounts[].tokenURL` | string | required | The service's OAuth token endpoint. `https` only |
| `connectedAccounts[].revocationURL` | string | | The service's [RFC 7009](https://www.rfc-editor.org/rfc/rfc7009) revocation endpoint. When set, disconnecting the account revokes the token there. `https` only |
| `connectedAccounts[].tokenHosts` | []string | required | The hosts the account's token may be sent to, as bare lower-case host names without scheme, port or path. At least one. A call to any other host is refused, so a tool cannot send the token elsewhere |

The API server refuses a declaration whose name is not a DNS label, whose endpoints are not `https`, or that has no `tokenHosts`.

## Taking the token in an MCPServer

Set `registry.callerCredential.account` to the name of an account the crew of the server's namespace declares. It replaces `verifier` as the way to name where the person's token comes from.

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

The server's other `callerCredential` fields (`onForbidden`, `relayContext`, `accountName`) work as described in the [MCPServer guide](../mcpserver-guide/#calls-with-the-asking-persons-own-token). The tool gateway receives the account name in the server's registration. An `account` that no crew of the namespace declares is not reported anywhere; declare it on a crew first.

## What the install registers

The install registers a client with each service and lists the account names it has registered under `liaison.connectedAccounts.accounts` in the operator chart. The operator reads the names.

```yaml
liaison:
  connectedAccounts:
    accounts:
      library: {}
```

A crew that declares a name missing from this list reports `UnregisteredAccount` until the install registers it.

## The ConnectedAccounts condition

A Crew that declares at least one account carries a `ConnectedAccounts` condition. A Crew that declares none carries no such condition. The condition never affects the crew's `Ready`.

| Status and reason | Meaning |
|-------------------|---------|
| `True`, `Ready` | Every declared account is registered by the install, and no other crew uses its name for a different service |
| `False`, `Conflict` | Another crew, in any namespace, declares the same name with a different `authorizeURL`, `tokenURL`, `revocationURL` or set of `tokenHosts`. One name must mean one service, because the install keeps one registration and one token per person for each name. Both crews report it; rename one of the accounts |
| `False`, `UnregisteredAccount` | The install has not registered a name the crew declares. The message lists the missing names |
| `False`, `NoSingleToolGateway` | A server in the crew's namespace takes one of the crew's accounts, and the namespace has no MCPGateway or several. The servers of a namespace are fronted by its single Kubemoot MCPGateway |

The reasons are checked in this order: `Conflict`, `UnregisteredAccount`, `NoSingleToolGateway`.
