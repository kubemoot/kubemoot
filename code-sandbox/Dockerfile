FROM golang:1.27-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o code-sandbox ./cmd/code-sandbox/

# The programs agents write run on this image: Python 3.12 with the standard
# library only, and bash. Nothing else is installed.
FROM python:3.12-slim
COPY --from=builder /build/code-sandbox /usr/local/bin/code-sandbox
# uid:gid 1000:1000 matches the artifact-access materializer that writes /artifacts.
USER 1000:1000
WORKDIR /tmp
ENTRYPOINT ["/usr/local/bin/code-sandbox"]
