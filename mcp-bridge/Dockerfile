FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o kubemoot-mcp-bridge ./cmd/bridge/

FROM scratch
COPY --from=builder /build/kubemoot-mcp-bridge /kubemoot-mcp-bridge
# Run as a non-root uid by default (nobody). The single static binary needs no
# write access; this hardens the scratch image even when the operator injects
# the sidecar in non-strict mode (no runAsUser in the Pod SecurityContext).
USER 65534:65534
ENTRYPOINT ["/kubemoot-mcp-bridge"]
