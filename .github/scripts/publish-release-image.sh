#!/usr/bin/env bash
# Publish a released image to the release registry.
#
# The in-cluster Harbor registry is the build and deploy loop; it is reachable only
# from inside the cluster. Releases are copied from Harbor to the release registry
# (GHCR for the open-source project) at release time, versioned tags only: no
# :latest, no SHA tags.
#
# Usage: publish-release-image.sh <source-ref> <version>
#   source-ref  the versioned Harbor reference just retagged,
#               e.g. harbor-homelab.dijure.com/kubemoot/agent-runtime:0.322.4
#   version     the release version, e.g. 0.322.4
#
# Required env:
#   RELEASE_REGISTRY  registry plus namespace, e.g. ghcr.io/kubemoot
#   GHCR_USERNAME    account with write:packages on that namespace
#   GHCR_TOKEN       its token
#   HARBOR_USERNAME  read access to the source registry
#   HARBOR_PASSWORD  its password
#
# The image name is the last path segment of the source reference, so
# kubemoot/agent-runtime on Harbor becomes ${RELEASE_REGISTRY}/agent-runtime.
set -euo pipefail

source_ref="${1:?source image reference required}"
version="${2:?version required}"
: "${RELEASE_REGISTRY:?RELEASE_REGISTRY required}"
: "${GHCR_USERNAME:?GHCR_USERNAME required}"
: "${GHCR_TOKEN:?GHCR_TOKEN required}"
: "${HARBOR_USERNAME:?HARBOR_USERNAME required}"
: "${HARBOR_PASSWORD:?HARBOR_PASSWORD required}"

image_name="${source_ref##*/}"      # last path segment, e.g. agent-runtime:0.322.4
image_name="${image_name%%:*}"      # strip the tag (a registry port never reaches here)
registry_host="${RELEASE_REGISTRY%%/*}"
source_host="${source_ref%%/*}"
target="${RELEASE_REGISTRY}/${image_name}:${version}"

# Not every release workflow installs crane before this step (test-runner builds
# straight from Kaniko), so provision it here when missing.
if ! command -v crane >/dev/null 2>&1; then
  curl -sL https://github.com/google/go-containerregistry/releases/download/v0.20.2/go-containerregistry_Linux_x86_64.tar.gz \
    | tar -xzf - crane
  sudo mv crane /usr/local/bin/
fi

# Log in to both ends here so no caller has to remember a prior crane login.
echo "${HARBOR_PASSWORD}" | crane auth login --insecure "${source_host}" -u "${HARBOR_USERNAME}" --password-stdin
echo "${GHCR_TOKEN}" | crane auth login "${registry_host}" -u "${GHCR_USERNAME}" --password-stdin

# --insecure lets crane read the plain-HTTP Harbor source; the release target is HTTPS.
crane copy --insecure "${source_ref}" "${target}"
echo "Published ${target}"
