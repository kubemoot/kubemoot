FROM docker.io/library/golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o code-sandbox ./cmd/code-sandbox/

# The programs agents write run on this image: Python 3.12 with the standard
# library only, and bash. Nothing else is installed.
FROM docker.io/library/python:3.12-slim@sha256:02108f5d322dd89f1c9e552442c25acb0543dfdbc455693a5599624f20d9155d
# tini reaps processes a killed program leaves behind; without it they stay as
# zombies under the server for the life of the pod.
RUN apt-get update && apt-get install -y --no-install-recommends tini && rm -rf /var/lib/apt/lists/*
COPY --from=builder /build/code-sandbox /usr/local/bin/code-sandbox
# uid:gid 1000:1000 matches the artifact-access materializer that writes /artifacts.
USER 1000:1000
WORKDIR /tmp
ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/usr/local/bin/code-sandbox"]
