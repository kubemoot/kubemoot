# Custom ARC runner image for the homelab-runners scale set.
#
# The stock ghcr.io/actions/actions-runner image is missing system packages our
# CI workflows assume:
#   - libatomic1: the Node 26 binary (actions/setup-node@v6) links against
#     libatomic.so.1; without it setup-node crashes on its first `node` call.
#   - make, jq: used by various workflows that previously hand-rolled around the gaps.
#   - gnupg, dirmngr: SonarSource/sonarqube-scan-action verifies the scanner's GPG
#     signature, and gpg needs dirmngr to fetch the signing key.
#
# The FROM line is the one source of the runner version: the build workflow tags the
# image <runner version>-homelab from it, and Dependabot bumps it.
#
# Built + pushed by .github/workflows/build-arc-runner-image.yaml (in-cluster Kaniko,
# the only push path to Harbor that bypasses Cloudflare). Referenced from the homelab
# OpenTofu arc-runner-scale-set module via its `runner_image` variable.
FROM ghcr.io/actions/actions-runner:2.337.0

USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends libatomic1 make jq gnupg dirmngr \
    && rm -rf /var/lib/apt/lists/*
USER runner
