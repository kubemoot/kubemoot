# Kubemoot Test Runner
# Container image with all test scripts baked in
FROM bitnami/kubectl:latest@sha256:f7f9e4f64d9e114650c115a4ac6fd383394b3d494061a8f17b1a0f6c8d55bc25

USER root

# Copy all test scripts
COPY *.sh /tests/

# Make scripts executable
RUN chmod +x /tests/*.sh

USER 1001

WORKDIR /tests

ENTRYPOINT ["/bin/bash"]
