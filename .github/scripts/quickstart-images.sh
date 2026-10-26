#!/usr/bin/env bash
# The Kubemoot component images a quickstart run needs, read from rendered chart manifests.
# One list and one core check for both users: the published-chart quickstart job and
# export-quickstart-candidate.sh.
#
# Usage: helm template ... | quickstart-images.sh   prints one image reference per line, as the
#        chart names it (registry prefix included; name:tag, name@sha256:digest, or both).
#        Fails when no component image is found or a core component is missing.
# Sourced (by export-quickstart-candidate.sh and the tests), it defines the functions only.
# Tested by test-quickstart-images.sh.
set -euo pipefail

# The component images the quickstart can run; the first three are always pinned.
QUICKSTART_COMPONENTS="kubemoot-operator|agent-runtime|discussion-gateway|crew-liaison|dashboard"
QUICKSTART_CORE_COMPONENTS="kubemoot-operator agent-runtime discussion-gateway"

# quickstart_images: component image references in the manifests on stdin, distinct and sorted.
quickstart_images() {
  grep -oE "[A-Za-z0-9._:/-]+/(${QUICKSTART_COMPONENTS})(:[A-Za-z0-9._-]+(@sha256:[a-f0-9]{64})?|@sha256:[a-f0-9]{64})" | sort -u || true
}

# quickstart_require_core: fail unless the reference list on stdin is not empty and holds the
# core components. Prints the list again on success.
quickstart_require_core() {
  local list core
  list="$(cat)"
  [ -n "$list" ] || { echo "ERROR: the chart names no Kubemoot component image" >&2; return 1; }
  for core in ${QUICKSTART_CORE_COMPONENTS}; do
    grep -qE "/${core}[:@]" <<<"$list" || { echo "ERROR: the chart pins no ${core} image" >&2; return 1; }
  done
  printf '%s\n' "$list"
}

# image_name REF / image_tag REF / image_digest REF: the parts of a reference.
image_name() { local r="${1##*/}"; r="${r%%@*}"; echo "${r%%:*}"; }
image_tag() { local r="${1##*/}"; r="${r%%@*}"; case "$r" in *:*) echo "${r#*:}" ;; esac; }
image_digest() { case "$1" in *@sha256:*) echo "sha256:${1##*@sha256:}" ;; esac; }

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  quickstart_images | quickstart_require_core
fi
