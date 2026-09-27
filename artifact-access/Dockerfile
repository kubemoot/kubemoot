FROM golang:1.27-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
# Copy only the Go source the build needs, not the whole context (no .git, Dockerfile,
# docs, or stray files). The module is exactly cmd/ + internal/; this also keeps the
# build-cache layer from busting on unrelated file changes.
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o artifact-access ./cmd/artifact-access/

FROM scratch
COPY --from=builder /build/artifact-access /artifact-access
# Run as non-root (uid:gid 1000:1000) instead of scratch's default root. The binary
# is a static CGO_ENABLED=0 build that never resolves a user by name, so a numeric
# USER is sufficient - no /etc/passwd needed. 1000 matches the code-sandbox reader
# uid and the operator's strict-mode uid, and the shared /artifacts emptyDir is
# world-writable (0777, the k8s default), so the materializer writes fine as 1000.
USER 1000:1000
ENTRYPOINT ["/artifact-access"]
