FROM docker.io/library/golang:1.27.4-alpine3.23@sha256:146e3b9c7c1e2ec3d3f5348c8a686151a75e1976e50efac9fc022ebe446a9a82 AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o code-sandbox ./cmd/code-sandbox/

# The programs agents write run on this image: Python 3.14 with the standard
# library only, and bash. Nothing else is installed.
FROM docker.io/library/python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
# tini reaps processes a killed program leaves behind; without it they stay as
# zombies under the server for the life of the pod.
RUN apt-get update && apt-get install -y --no-install-recommends tini && rm -rf /var/lib/apt/lists/*
COPY --from=builder /build/code-sandbox /usr/local/bin/code-sandbox
# uid:gid 1000:1000 matches the artifact-access materializer that writes /artifacts.
USER 1000:1000
WORKDIR /tmp
ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/usr/local/bin/code-sandbox"]
