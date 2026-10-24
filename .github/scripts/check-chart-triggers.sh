#!/usr/bin/env bash
# Checks that every Kubemoot image the operator chart stamps (a "name:0.0.0" value with no
# registry) has a release workflow, .github/workflows/release-<name>.yaml, and that the
# workflow's name is one of the workflow_run triggers of the operator chart release. A
# component missing from the triggers is released without a new chart, so the cluster keeps
# running the previous image.
# Usage: check-chart-triggers.sh [REPO_ROOT]   (exit 0 = every image is covered)
set -euo pipefail

root="${1:-.}"
values="${root}/operator/chart/kubemoot-operator/values.yaml"
chart_workflow="${root}/.github/workflows/release-chart.yaml"

# The workflow names listed under the chart release's workflow_run.workflows.
triggers() {
  awk '
    /^[[:space:]]*workflows:[[:space:]]*$/ { inside = 1; next }
    inside && /^[[:space:]]*-[[:space:]]/ { sub(/^[[:space:]]*-[[:space:]]*/, ""); print; next }
    inside { inside = 0 }
  ' "$chart_workflow"
}

# The image names stamped from tags: "<key>: <name>:0.0.0" with no "/" in the name.
stamped_images() {
  grep -oE ':[[:space:]]+[a-z0-9-]+:0\.0\.0([[:space:]]|$)' "$values" \
    | sed -E 's/^:[[:space:]]+//; s/:0\.0\.0.*$//' | sort -u
}

missing=0
listed="$(triggers)"
while IFS= read -r image; do
  workflow="${root}/.github/workflows/release-${image}.yaml"
  if [ ! -f "$workflow" ]; then
    echo "MISSING ${image}: no release workflow at .github/workflows/release-${image}.yaml"
    missing=$((missing + 1))
    continue
  fi
  name="$(grep -m1 -E '^name:' "$workflow" | sed -E 's/^name:[[:space:]]*//')"
  if ! grep -qxF -- "$name" <<<"$listed"; then
    echo "MISSING ${image}: \"${name}\" does not trigger the operator chart release"
    missing=$((missing + 1))
  fi
done < <(stamped_images)

if [ "$missing" -gt 0 ]; then
  echo "${missing} image(s) would not reach the chart"
  exit 1
fi
echo "every stamped image triggers the operator chart release"
