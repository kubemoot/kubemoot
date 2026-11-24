#!/usr/bin/env bash
# What the operator chart release knows about its components, in one place: the release
# workflow file of a component, the prefix of its candidate tags, and the images a values
# file stamps from tags. Sourced by release-operator-chart.sh, chart-image-pairing.sh,
# check-chart-triggers.sh and their tests.

# component_workflow NAME: the release workflow file name of the component. The operator
# is named kubemoot-operator in the chart's pins and releases from release.yaml.
component_workflow() {
  if [ "$1" = "kubemoot-operator" ]; then echo "release.yaml"; else echo "release-$1.yaml"; fi
}

# component_tag_prefix NAME: the prefix of the component's candidate tags.
component_tag_prefix() {
  if [ "$1" = "kubemoot-operator" ]; then echo "v"; else echo "$1-v"; fi
}

# stamped_images VALUES_FILE: the names of the images stamped from tags, one per line:
# "<key>: <name>:0.0.0" with no registry or path in front of the name.
stamped_images() {
  { grep -oE ':[[:space:]]+[a-z0-9-]+:0\.0\.0([[:space:]]|$)' "$1" || true; } \
    | sed -E 's/^:[[:space:]]+//; s/:0\.0\.0.*$//' | sort -u
}
