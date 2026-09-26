# Kubemoot Test Runner
# Container image with all test scripts baked in
FROM bitnami/kubectl:latest

USER root

# Copy all test scripts
COPY *.sh /tests/

# Make scripts executable
RUN chmod +x /tests/*.sh

USER 1001

WORKDIR /tests

ENTRYPOINT ["/bin/bash"]
