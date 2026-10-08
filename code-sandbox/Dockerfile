FROM docker.io/library/golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o code-sandbox ./cmd/code-sandbox/

# The programs agents write run on this image: Python 3.14 with the standard
# library only, and bash. Nothing else is installed.
FROM docker.io/library/python:3.14-slim@sha256:f85c5697265c178cc6887276c55fe16cf3d14ca35c3df6a5eab3b360534a55d2
# tini reaps processes a killed program leaves behind; without it they stay as
# zombies under the server for the life of the pod.
RUN apt-get update && apt-get install -y --no-install-recommends tini && rm -rf /var/lib/apt/lists/*
COPY --from=builder /build/code-sandbox /usr/local/bin/code-sandbox
# uid:gid 1000:1000 matches the artifact-access materializer that writes /artifacts.
USER 1000:1000
WORKDIR /tmp
ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/usr/local/bin/code-sandbox"]
