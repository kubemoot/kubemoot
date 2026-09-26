FROM golang:1.26-alpine AS builder

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
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /build/fitness-runner /fitness-runner

ENTRYPOINT ["/fitness-runner"]
