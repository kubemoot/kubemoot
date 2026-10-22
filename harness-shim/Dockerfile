# The harness-goose image: the harness shim plus the upstream goose release binary.
# Build context: this directory.
FROM docker.io/library/golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079 AS builder

WORKDIR /build
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o harness-shim ./cmd/harness-shim/

# goose 1.55.0, Apache-2.0, the statically linked (musl) linux amd64 release. The sha256 is
# of the release asset goose-x86_64-unknown-linux-musl.tar.gz: it equals the digest GitHub
# reports for the asset and the one computed on a download of it. Raise both values together.
FROM docker.io/library/alpine:3.23@sha256:21f3e19bafab57a9ff7aa9dd571dac72f868370472c10f450cc0416cc1aca553 AS goose
# The release asset is amd64 only, so the stage refuses any other architecture.
ARG GOOSE_VERSION=1.55.0
ARG GOOSE_SHA256=fade147bd5ebc020ecea05453b0905397f1ebb01ee3de2f3678570c5c3502ed7
RUN [ "$(uname -m)" = "x86_64" ] || { echo "the goose release asset is amd64 only" >&2; exit 1; } \
 && wget -q -O /tmp/goose.tar.gz "https://github.com/aaif-goose/goose/releases/download/v${GOOSE_VERSION}/goose-x86_64-unknown-linux-musl.tar.gz" \
 && echo "${GOOSE_SHA256}  /tmp/goose.tar.gz" | sha256sum -c - \
 && mkdir /out && tar -xzf /tmp/goose.tar.gz -C /out goose \
 && /out/goose --version

FROM docker.io/library/alpine:3.23@sha256:21f3e19bafab57a9ff7aa9dd571dac72f868370472c10f450cc0416cc1aca553
# tzdata: the turn carries the requester's IANA time zone, which the command reads from TZ.
RUN apk add --no-cache tzdata ca-certificates
COPY --from=goose /out/goose /usr/local/bin/goose
COPY --from=builder /build/harness-shim /usr/local/bin/harness-shim
COPY goose/config.json /etc/harness-shim/goose.json
LABEL org.opencontainers.image.licenses="Apache-2.0"
# A numeric user: the pod runs as non-root.
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/harness-shim", "-config", "/etc/harness-shim/goose.json"]
