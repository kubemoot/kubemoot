# Custom ARC runner image for the homelab-runners scale set.
#
# The stock ghcr.io/actions/actions-runner image is missing system packages our
# CI workflows assume:
#   - libatomic1: the Node 26 binary (actions/setup-node@v6) links against
#     libatomic.so.1; without it setup-node crashes on its first `node` call.
#   - make, jq: used by various workflows that previously hand-rolled around the gaps.
#
# Built + pushed by .github/workflows/build-arc-runner-image.yaml (in-cluster Kaniko,
# the only push path to Harbor that bypasses Cloudflare). Referenced from the homelab
# OpenTofu arc-runner-scale-set module via its `runner_image` variable.
FROM ghcr.io/actions/actions-runner:2.337.0

USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends libatomic1 make jq \
    && rm -rf /var/lib/apt/lists/*
USER runner
