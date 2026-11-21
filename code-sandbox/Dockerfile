FROM docker.io/library/golang:1.27.4-alpine3.23@sha256:f70850ef6ce341e8b6427f677049145d63c0b7ce002d4d7e3d464828b1b96dcd AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o code-sandbox ./cmd/code-sandbox/

# The programs agents write run on this image: Python 3.14 with the standard
# library only, bash, and GNU date with the zone database, which agents use for
# time-zone conversion and date arithmetic. Nothing else is installed.
FROM docker.io/library/python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
# tini reaps processes a killed program leaves behind; without it they stay as
# zombies under the server for the life of the pod. tzdata is named so a future base
# image that drops it cannot take the zone database away.
RUN apt-get update && apt-get install -y --no-install-recommends tini tzdata && rm -rf /var/lib/apt/lists/*
# The build fails if the image cannot convert a zone or do date arithmetic.
RUN test "$(TZ=Asia/Tokyo date -d '2026-11-20 15:00 EST' '+%F %R %Z')" = "2026-11-21 05:00 JST" \
    && test "$(date -d '2026-11-20 + 45 days' +%F)" = "2027-01-04"
COPY --from=builder /build/code-sandbox /usr/local/bin/code-sandbox
# uid:gid 1000:1000 matches the workspace-access materializer that writes /kubemoot/workspace.
USER 1000:1000
WORKDIR /tmp
ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/usr/local/bin/code-sandbox"]
