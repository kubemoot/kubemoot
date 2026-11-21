---
title: "Connecting to NATS with TLS and credentials"
description: "The options every Kubemoot component reads to reach NATS over TLS, with a client certificate and with credentials, set once on the operator chart, and what each component covers."
weight: 6
---

By default every Kubemoot component connects to NATS plainly and without credentials. Set the options below and every component that dials NATS connects over TLS and presents a credential, with no change to a crew. With none set, nothing changes.

Kubemoot does not install NATS; you do, and you configure the server's TLS and accounts on that install. This page is the client side: what Kubemoot's components need to reach a NATS that demands TLS or credentials.

## The options

Every component reads the same eight names. Files are paths where a Secret or ConfigMap is mounted.

| Name | Meaning |
|------|---------|
| `NATS_URL` | The server. A `tls://` URL turns TLS on. |
| `NATS_CA_FILE` | PEM bundle of the CA that signed the server certificate. Needed when the CA is not in the image's trust store. |
| `NATS_CLIENT_CERT_FILE`, `NATS_CLIENT_KEY_FILE` | Client certificate and key for mutual TLS. Set together. Java components need the key as PKCS#8 (`BEGIN PRIVATE KEY`). |
| `NATS_CREDS_FILE` | A NATS `.creds` file (a user JWT and its seed). |
| `NATS_USER`, `NATS_PASSWORD` | A user and password. Set together. |
| `NATS_TOKEN` | A token. |

Set at most one kind of credential: a creds file, a user with a password, or a token. A client certificate without its key, a user without a password, or two kinds of credential is refused when the component starts, naming the variables. A file that is missing or empty stops the component at startup naming its variable, not as a handshake error later. The Go and Java components also check that the CA bundle and the client certificate and key are PEM that loads; the dashboard reads them when it connects.

## Set it once on the chart

The operator chart takes the options as Secret names and passes them to its own pods and to the workloads the operator creates:

```yaml
nats:
  url: "tls://nats.nats.svc.cluster.local:4222"
  tls:
    caSecret: nats-ca          # Secret holding the CA bundle
    caKey: ca.crt              # the key in that Secret
    clientSecret: nats-client  # kubernetes.io/tls Secret (tls.crt, tls.key), for mutual TLS
  auth:
    secret: nats-auth          # one Secret, and exactly one of:
    credsKey: app.creds        #   a .creds file
    # userKey: user            #   a user and password
    # passwordKey: password
    # tokenKey: token          #   a token
```

Create the Secrets in the operator's namespace. The chart mounts them into the operator, the placement service, the accelerator MCP server and the stream bootstrap job. The operator copies them into the namespace of every crew and gives them to the pods it creates there: agents (the runtime and the Moot Mesh sidecar), each crew's discussion gateway, fitness runners that write transcripts, and indexing jobs that read a NATS key. The CA, the client certificate with its private key and the credential are copied into every crew namespace, so anyone who can read Secrets in a crew namespace can read them; give that access accordingly. The operator never overwrites a Secret of the same name it did not create, so a Secret named like one of yours in a crew namespace is used instead of the copy. Rotating a Secret is a change to its content; the operator refreshes its copies, and a pod reads the files when it starts, so restart the workloads to pick it up.

The dashboard subchart takes the same shape under `dashboard.nats` (`url`, `tls`, `auth`), and its Secrets are in the dashboard's namespace.

A Moot Mesh sidecar whose agent has its own Secret `<agent>-nats` uses that credential and CA in place of the shared ones. The harness container never sees either.

## What is covered

| Component | Reads the options | Notes |
|-----------|-------------------|-------|
| Operator | Yes | Also `ompi-export` and `coordinator-chaos`, from the same variables; `ompi-export --creds` still overrides the credential. |
| Discussion gateway | Yes | Passed by the operator to every crew's gateway. |
| Agent sidecar | Yes | Shared options, replaced by the agent's own Secret when it has one. |
| Agent runtime (Java) | Yes | Passed by the operator to classic-mode agents. |
| Placement service (Java) | Yes | `placementService.nats.existingSecret` still supplies a user and password of its own, which replace a shared credential. |
| Accelerator MCP server | Yes | The chart marks it with the annotation `kubemoot.ai/nats-connection: "true"`. Put the same annotation on any MCPServer you run that dials NATS with a Kubemoot component. |
| Scheduling MCP server, workspace access | Yes | They read the variables. Give an MCPServer that runs them the annotation above. |
| Fitness runner | Yes | Transcript and evidence writes. |
| Indexer (Java) | Yes | Reads NATS KV for a `natsKV` RAG source. |
| Dashboard server | Yes | Not its NATS monitoring page request (see below). |
| Stream bootstrap job | Yes | The `nats` command line reads them. It has no token option, so a token is placed in the URL it is given and shows in the job's process arguments; prefer a creds file or a user and password for this job. |
| MCP gateway, crew liaison, query service | Do not dial NATS | Nothing to set. |
| `nats-mcp` (sinadarbouy/mcp-nats) | No | A third-party image the chart runs with `NATS_NO_AUTHENTICATION`. Against a NATS that demands credentials, run it yourself with the options its own documentation names. |

The dashboard's NATS page also reads the server's HTTP monitoring port (8222) with a plain HTTP request. If your server protects or encrypts that port, the page shows the monitoring fields as unavailable and the rest of the page still works; set `NATS_MONITOR_URL` to another address for it.

## Tested behavior

The Go clients are tested against an in-process NATS server with TLS and authentication (a generated CA, a server certificate, a client certificate, and each credential kind including a `.creds` file under an operator and account). The Java and TypeScript clients are tested against a real `nats-server` process started with TLS, client-certificate verification and a user. In each, a client with the options connects, and a client missing the CA, the client certificate or the credential is refused with an error that names the server. The Java and TypeScript server tests run when `NATS_SERVER_BIN` points at a `nats-server` binary and `openssl` is installed; otherwise they are skipped.

## Not covered

- NATS accounts per crew are not created for you. A shared credential gives every crew the same view of NATS; per-crew accounts are a direction (see [Data ownership](../data-ownership/#in-transit)).
- Java components accept only PKCS#8 client keys. Convert an older key with `openssl pkcs8 -topk8 -nocrypt -in key.pem -out key.pkcs8.pem`, or have cert-manager issue it with `privateKey.encoding: PKCS8`.
- The CA bundle and client certificate come from a Secret for the workloads the operator creates; a ConfigMap is not copied across namespaces.
