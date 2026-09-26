#!/usr/bin/env bash
# Publish a packaged Helm chart to the release registry.
#
# Harbor is the deploy path and is pushed by the caller first. This script adds the
# second destination, oci://${RELEASE_REGISTRY}/charts, so the same artifact is
# available outside the cluster. It is a no-op when no release registry is set.
#
# Usage: publish-release-chart.sh <chart.tgz>
#
# Env:
#   RELEASE_REGISTRY  registry plus namespace, e.g. ghcr.io/kubemoot (optional)
#   GHCR_USERNAME     account with write:packages on that namespace
#   GHCR_TOKEN        its token
set -euo pipefail

tgz="${1:?packaged chart path required}"
[ -n "${RELEASE_REGISTRY:-}" ] || { echo "RELEASE_REGISTRY not set; skipping release-registry chart push"; exit 0; }
: "${GHCR_USERNAME:?GHCR_USERNAME required}"
: "${GHCR_TOKEN:?GHCR_TOKEN required}"

echo "${GHCR_TOKEN}" | helm registry login "${RELEASE_REGISTRY%%/*}" -u "${GHCR_USERNAME}" --password-stdin

for attempt in 1 2 3; do
  if helm push "${tgz}" "oci://${RELEASE_REGISTRY}/charts"; then
    echo "Published ${tgz} to oci://${RELEASE_REGISTRY}/charts"
    exit 0
  fi
  echo "release-registry helm push failed (attempt ${attempt}/3); retrying in 5s"
  sleep 5
done
echo "ERROR: chart is in Harbor but the push to ${RELEASE_REGISTRY} failed after 3 attempts" >&2
exit 1
