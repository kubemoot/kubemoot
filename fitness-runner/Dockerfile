FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build

# Operator module (local dependency for the shared SSE reader)
COPY operator/pkg/ /operator/pkg/
COPY operator/go.mod operator/go.sum /operator/

COPY fitness-runner/go.mod fitness-runner/go.sum* ./
RUN go mod download 2>/dev/null || true
COPY fitness-runner/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o fitness-runner .

# Use distroless/nonroot so the binary runs as uid 65532 by default.
# The operator sets runAsUser: 1000 explicitly via SecurityContext, so the
# distroless user is overridden in the Pod spec. Either way, we never run as root.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=builder /build/fitness-runner /fitness-runner

ENTRYPOINT ["/fitness-runner"]
