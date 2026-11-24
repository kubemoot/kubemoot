#!/usr/bin/env bash
# Add the JSON Schemas of the Kubemoot CRDs to the GitHub Release of Kubemoot's own
# release, as assets: one schema per kind (crew_v1alpha1.json, ...) and kubemoot_v1alpha1.json,
# which covers every kind. Editors and CI validate a crew's manifests against them offline:
#   https://github.com/kubemoot/kubemoot/releases/download/<tag>/kubemoot_v1alpha1.json
#   https://github.com/kubemoot/kubemoot/releases/latest/download/kubemoot_v1alpha1.json
# The first is pinned to a release; the second follows the newest one (Latest is always
# Kubemoot's own release, see create-github-releases.sh).
#
# The schemas are generated from the CRDs of the release's source (operator/config/crd/bases)
# by operator/cmd/crd-schema, so the version they carry is the git tag and nothing else. The
# files go into RELEASE_FILES/schemas and are listed in assets.tsv for create-github-releases.sh.
#
# Usage: write-schema-assets.sh RELEASE_FILES OPERATOR_DIR TAG
#   OPERATOR_DIR   the operator module of the release's source checkout (the operator/ directory)
#   TAG            the release the assets belong to (operator-chart-vX.Y.Z)
set -euo pipefail

usage="usage: write-schema-assets.sh RELEASE_FILES OPERATOR_DIR TAG"
files="${1:?${usage}}"
operator="${2:?${usage}}"
tag="${3:?${usage}}"

die() { echo "ERROR: $*" >&2; exit 1; }

[ -d "${operator}/config/crd/bases" ] || die "${operator}/config/crd/bases not found"
out="${files}/schemas"
rm -rf "$out"
mkdir -p "$out"
# A second run does not list the schemas twice.
if [ -f "${files}/assets.tsv" ]; then
  grep -v $'\tschemas/' "${files}/assets.tsv" > "${files}/assets.tsv.tmp" || true
  mv "${files}/assets.tsv.tmp" "${files}/assets.tsv"
fi
(cd "$operator" && go run ./cmd/crd-schema -crds config/crd/bases -out "$(cd "$out" && pwd)") >/dev/null \
  || die "the CRD schemas could not be generated"

[ -f "${out}/kubemoot_v1alpha1.json" ] || die "the combined schema was not written"
count=0
for f in "$out"/*.json; do
  # A broken schema stops the run before anything is published.
  jq -e . "$f" >/dev/null 2>&1 || die "${f##*/} is not valid JSON"
  printf '%s\t%s\n' "$tag" "schemas/${f##*/}" >> "${files}/assets.tsv"
  count=$((count + 1))
done
echo "${count} CRD schemas for ${tag}"
