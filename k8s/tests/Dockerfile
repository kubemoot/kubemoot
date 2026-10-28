# Kubemoot Test Runner
# Container image with all test scripts baked in.
#
# kubectl comes from the Kubernetes project's own image (registry.k8s.io/kubectl). That
# image is distroless, with no shell, so the scripts run on Debian slim, which adds bash,
# curl, jq and the GNU tools they call. Bump the kubectl tag with the cluster's minor.
FROM registry.k8s.io/kubectl:v1.37.1@sha256:b7cab618e281b1ee7484e7b706a96e2135fbb6e072c2a573a7dab4e87d7f2385 AS kubectl

FROM docker.io/library/debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl jq \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1001 --create-home --shell /bin/bash runner

COPY --from=kubectl /bin/kubectl /usr/local/bin/kubectl

# The nats CLI asks a crew a question over the bus in the smoke test. Pinned by version
# and checked against the SHA256SUMS entry for the linux-amd64 archive of that release.
ARG NATS_CLI_VERSION=0.5.0
ARG NATS_CLI_SHA256=d9fc93e9e9ab0310deff7e719a8a0da6f9d66f26d473f039aaee70155507aecc
RUN apt-get update \
    && apt-get install -y --no-install-recommends unzip \
    && curl -fsSL -o /tmp/nats.zip "https://github.com/nats-io/natscli/releases/download/v${NATS_CLI_VERSION}/nats-${NATS_CLI_VERSION}-linux-amd64.zip" \
    && echo "${NATS_CLI_SHA256}  /tmp/nats.zip" | sha256sum -c - \
    && unzip -j /tmp/nats.zip "nats-${NATS_CLI_VERSION}-linux-amd64/nats" -d /usr/local/bin \
    && chmod 0755 /usr/local/bin/nats \
    && apt-get purge -y unzip && apt-get autoremove -y \
    && rm -rf /tmp/nats.zip /var/lib/apt/lists/*

# Copy all test scripts
COPY *.sh /tests/

# Make scripts executable
RUN chmod +x /tests/*.sh

USER 1001

WORKDIR /tests

ENTRYPOINT ["/bin/bash"]
