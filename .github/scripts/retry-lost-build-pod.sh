#!/usr/bin/env bash
# Runs an image build once more when its build pod was lost. The image builds run in a pod in
# the cluster (build-image-buildah.sh, build-image-buildpacks.sh of kubemoot/release-actions).
# A failure whose output carries the signature of a lost pod or node is retried once with a new
# pod name; any other failure (the build itself failed, a timeout) is returned as it is, so a
# broken Dockerfile is not run twice.
#
# Usage: retry-lost-build-pod.sh -- COMMAND [ARGS...]
# The command names its pod with "--pod NAME"; the second run uses "NAME-retry", so the new
# pod never collides with the lost one that may still be terminating. A command with no
# "--pod NAME" is never retried, since its second pod could collide with the first.
# Exit status: the status of the last run of the command.
# Environment: LOST_POD_PATTERN replaces the signature (an extended regular expression), for
# tests. Tested by test-retry-lost-build-pod.sh.
set -uo pipefail

if [ "${1:-}" != "--" ] || [ $# -lt 2 ]; then
  echo "usage: retry-lost-build-pod.sh -- COMMAND [ARGS...]" >&2
  exit 64
fi
shift

# has_pod ARGS...: true when ARGS name the pod with "--pod NAME".
has_pod() {
  local a
  for a in "$@"; do [ "$a" != "--pod" ] || return 0; done
  return 1
}

# with_retry_pod ARGS...: ARGS with the value of --pod suffixed "-retry".
with_retry_pod() {
  local out=() want_value=false a
  for a in "$@"; do
    if $want_value; then a="${a}-retry"; want_value=false; elif [ "$a" = "--pod" ]; then want_value=true; fi
    out+=("$a")
  done
  printf '%s\0' "${out[@]}"
}

# What kubectl reports when the pod or its node is gone under a running build: the pod no
# longer exists or was evicted, or the node cannot be reached. A build that failed on its own
# prints none of these, and a plain timeout is not one of them.
pattern="${LOST_POD_PATTERN:-pods? \"[^\"]*\" (not found|was evicted|deleted)|pod does not exist|error dialing backend|unable to upgrade connection|NodeLost}"

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

"$@" 2>&1 | tee "$log"
rc=${PIPESTATUS[0]}
[ "$rc" -ne 0 ] || exit 0

if ! grep -Eq -- "$pattern" "$log" || ! has_pod "$@"; then
  exit "$rc"
fi
echo "::warning title=Build pod lost::the build failed with the signature of a lost pod or node ($(grep -Em1 -- "$pattern" "$log" | cut -c1-200)); running it once more with a new pod"
mapfile -d '' retry_cmd < <(with_retry_pod "$@")
"${retry_cmd[@]}"
