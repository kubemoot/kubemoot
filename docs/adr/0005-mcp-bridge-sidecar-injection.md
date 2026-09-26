---
title: "5. MCP Bridge Sidecar Injection"
weight: 5
---

Date: 2025-12-15

## Status

Accepted

## Context

Most MCP server images support only stdio transport - stdin/stdout pipes. The MCP protocol as consumed by Kubemoot's MCPGateway requires HTTP/SSE. The two ends need a translator. Most MCP server images are distroless or scratch-based, so no shell is available for an inline wrapper script.

The translator must:
- Survive main-container restarts (a sidecar pattern, not an embedded entrypoint)
- Work with images that have no shell (distroless, scratch)
- Have zero external dependencies (the translator itself must compile to a single static binary)
- Start before the main container so the HTTP/SSE endpoint is ready when the Gateway connects

Kubernetes 1.28+ supports native sidecars via init containers with `restartPolicy: Always`. Combined with a startup probe on the sidecar, the main container starts only after the bridge is ready.

## Decision

We will inject a static Go binary (`mcp-bridge`) as a Kubernetes native sidecar for every MCPServer that uses `transport: stdio`. The bridge runs as an init container with `restartPolicy: Always`, communicates with the main container via named pipes (FIFOs) in a shared `emptyDir` mounted at `/pipes/`, and exposes an HTTP/SSE endpoint that the MCPGateway connects to. The bridge operates in **shell mode** (`sh -c "exec <cmd> < /pipes/stdin > /pipes/stdout"`) for images with a shell, or **exec mode** (copies itself into the shared volume, uses `dup2` + `syscall.Exec`) for distroless/scratch images. A TCP startup probe on the sidecar gates main-container start.

## Consequences

- Stdio-only MCP servers participate in Kubemoot without modification to their image.
- Distroless and scratch images are supported via the exec mode; no `sh` is required in the target image.
- The bridge is a single static Go binary with no transitive dependencies; image bumps are simple.
- A failure in the bridge does not require an MCPServer pod restart - the native sidecar `restartPolicy: Always` restarts the bridge in place.
- Every stdio MCPServer pod gains an extra container and an `emptyDir` volume. Memory overhead is small (the bridge binary is ~10MB resident); operational complexity is higher than HTTP-native MCP servers.
- The bridge does not buffer indefinitely. Pipe-buffer overflow is handled with a 16MB buffer and graceful overflow recovery.

## References

- `kubemoot/mcp-bridge/` - bridge source
- `kubemoot/operator/internal/controller/mcpserver_controller.go` - sidecar injection logic
- [Kubernetes 1.28 Native Sidecars](https://kubernetes.io/blog/2023/08/25/native-sidecar-containers/)
