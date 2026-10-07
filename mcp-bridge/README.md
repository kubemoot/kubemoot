# Kubemoot MCP Bridge

A static Go binary that bridges stdio-based MCP servers to HTTP/SSE endpoints.
Runs as a Kubernetes **native sidecar** alongside the MCP server container,
communicating via named pipes (FIFOs) in a shared emptyDir volume.

## Why

Most MCP servers use **stdio** transport (read JSON-RPC from stdin, write to stdout).
Kubernetes services need HTTP endpoints for routing, health checks, and gateway integration.
The bridge solves this without modifying the MCP server image or injecting code into its container.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  Pod                                                │
│                                                     │
│  mcp-bridge (native sidecar)    mcp-server          │
│  ┌───────────────────────┐      ┌────────────────┐  │
│  │ Creates FIFOs         │      │ Original image │  │
│  │ HTTP/SSE on :8080     │◄────►│ stdin/stdout   │  │
│  │ /message -> stdin     │ pipes│ via /pipes/    │  │
│  │ stdout -> /sse        │      │                │  │
│  └───────────────────────┘      └────────────────┘  │
│           ▲                                         │
│      Port 8080                                      │
└─────────────────────────────────────────────────────┘
```

## Modes

### Sidecar Mode (default)

Runs as a Kubernetes native sidecar (`initContainer` with `restartPolicy: Always`).
Creates FIFOs at `/pipes/stdin` and `/pipes/stdout`, starts an HTTP server, and
waits for the MCP server to connect.

```
kubemoot-mcp-bridge --port 8080 --healthz /healthz --pipe-dir /pipes
```

On startup, the bridge copies itself to the pipe directory (`/pipes/kubemoot-mcp-bridge`)
so the main container can use exec mode. The copy is written to a temporary file and
renamed into place, so a restarted sidecar can replace it while the main container is
executing the previous copy.

### Several sessions, one server

Every SSE client gets its own `sessionId`, and several clients (for example the
gateways of several crews) can share one stdio server. The bridge gives each request
it forwards an id unique within the bridge and sends the server's reply only to the
session that asked, with the client's own id restored, so two sessions that both send
id 1 each receive their own answer. Notifications and requests from the server go to
every session.

### Exec Mode

Replaces `sh -c "exec <cmd> < /pipes/stdin > /pipes/stdout"` for images that have no
shell (scratch, distroless). Uses `dup2` to redirect stdin/stdout to the named pipes,
then `syscall.Exec` to replace the process with the MCP server binary.

```
/pipes/kubemoot-mcp-bridge exec --pipe-dir /pipes -- /server/github-mcp-server stdio
```

The operator sets this as the main container's command. The bridge binary is available
at `/pipes/` because the sidecar copied it there on startup.

## HTTP Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/sse` | GET | SSE stream of MCP server responses (stdout) |
| `/message` | POST | Send JSON-RPC request to MCP server (stdin) |
| `/healthz` | GET | `200` when MCP server connected, `503` when waiting |

## Probes

The operator configures three probes on the sidecar:

- **Startup**: TCP on bridge port. Gates main container start (binary copied, FIFOs ready).
- **Readiness**: HTTP GET `/healthz`. Returns 200 only when MCP server has connected to pipes.
- **Liveness**: TCP on bridge port. Ensures HTTP server is responsive.

## Reconnection

If the MCP server container restarts (crash, OOM, upgrade), the sidecar:
1. Detects EOF on the stdout pipe
2. Sets readiness to 503 (disconnected)
3. Waits for the new MCP server process to open the pipes
4. Resumes bridging (readiness returns to 200)
5. Sends `notifications/tools/list_changed` to every connected SSE client once the new
   process has completed its handshake, because the new process may offer other tools

No sidecar restart required. The bridge survives MCP server restarts, and SSE clients
stay connected across them. A client whose event buffer is full when the notification is
sent misses it; the gateway's periodic re-list (`mcp.gateway.tool-list-max-age`) catches that.

## Building

The image is built by CI/CD on push to `main` (see `.github/workflows/ci-mcp-bridge.yaml`).
The workflow builds the static binary on the runner after the tests
(`.github/scripts/go-build-static.sh`: `CGO_ENABLED=0`, `-trimpath`), and Paketo buildpacks
package it onto the Ubuntu Noble `run-static` image (`.github/workflows/build-go-binary-image.yaml`).
The binary is `/workspace/kubemoot-mcp-bridge`, started by the image entrypoint
`/cnb/process/web`. The image runs as uid 1002, gid 1001 unless the pod sets a user.

## Configuration

The bridge image version is centrally managed via **KubemootConfig**:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: KubemootConfig
metadata:
  name: default
spec:
  images:
    mcpBridge: ghcr.io/kubemoot/mcp-bridge:0.55.0
```

Individual MCPServer resources can override the image:

```yaml
spec:
  proxyInjection:
    image: custom/bridge:1.0
    port: 9090
```
