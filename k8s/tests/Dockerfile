# Kubemoot Test Runner
# Container image with all test scripts baked in.
#
# kubectl comes from the Kubernetes project's own image (registry.k8s.io/kubectl). That
# image is distroless, with no shell, so the scripts run on Debian slim, which adds bash,
# curl, jq and the GNU tools they call. Bump the kubectl tag with the cluster's minor.
FROM registry.k8s.io/kubectl:v1.37.1@sha256:b7cab618e281b1ee7484e7b706a96e2135fbb6e072c2a573a7dab4e87d7f2385 AS kubectl

FROM docker.io/library/debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl jq \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1001 --create-home --shell /bin/bash runner

COPY --from=kubectl /bin/kubectl /usr/local/bin/kubectl

# Copy all test scripts
COPY *.sh /tests/

# Make scripts executable
RUN chmod +x /tests/*.sh

USER 1001

WORKDIR /tests

ENTRYPOINT ["/bin/bash"]
