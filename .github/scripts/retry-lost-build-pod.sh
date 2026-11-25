#!/usr/bin/env bash
# Runs an image build again when it failed for a reason outside the build. The image builds
# run in a pod in the cluster (build-image-buildah.sh, build-image-buildpacks.sh of
# kubemoot/release-actions) and push to the in-cluster registry. Two kinds of failure are
# retried, each recognised only by its signature:
#   lost pod       the pod or its node vanished under the build (kubectl's own error lines);
#                  retried once with a new pod name.
#   registry       the registry answered with an error while the lifecycle read or wrote the
#                  build cache image or the image (cache image access, registry read/write
#                  validation, an HTTP 5xx, a timeout or a reset on a registry URL);
#                  retried up to twice more, after a backoff each time.
# When a registry failure names the build cache image, the last attempt runs with
# BUILD_NO_CACHE=1 in its environment, which the build script reads as "build and push
# without the cache image" (build-image-buildpacks.sh of kubemoot/release-actions; an older
# copy ignores it and the last attempt repeats the build as it was), so a bad cache cannot block a release.
# Any other failure (a compile error, a test failure, a timeout, a bad image name guard) is
# returned as it is, so a broken build is not run again.
#
# Usage: retry-lost-build-pod.sh -- COMMAND [ARGS...]
# The command names its pod with "--pod NAME"; the second run uses "NAME-retry", so the new
# pod never collides with the lost one that may still be terminating (a third run uses
# "NAME-retry2"). A command with no "--pod NAME" is never retried, since its second pod could
# collide with the first.
# Exit status: the status of the last run of the command.
# Environment: LOST_POD_PATTERN and REGISTRY_PATTERN replace the signatures (extended regular
# expressions) and RETRY_BACKOFF the seconds to wait before each registry retry (default
# "15 45", which also sets the number of retries), for tests. Tested by
# test-retry-lost-build-pod.sh.
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

# with_retry_pod SUFFIX ARGS...: ARGS with the value of --pod suffixed SUFFIX.
with_retry_pod() {
  local suffix="$1" out=() want_value=false a
  shift
  for a in "$@"; do
    if $want_value; then a="${a}${suffix}"; want_value=false; elif [ "$a" = "--pod" ]; then want_value=true; fi
    out+=("$a")
  done
  printf '%s\0' "${out[@]}"
}

# What kubectl reports when the pod or its node is gone under a running build: the pod no
# longer exists or was evicted, or the node cannot be reached. A message counts only on a
# line kubectl itself wrote ("error: ...", "Error from server...", or its "E<MMDD> ..." log
# form), so the same words in the build's own output do not. `kubectl run --rm` ends every
# build, passed or failed, with `pod "NAME" deleted`, so that line is not part of the
# signature. The dial and upgrade errors are matched only in the form kubectl prints them
# ("Error from server...", "error: unable to upgrade connection", its log form): a build line
# that begins "error:" and mentions dialing is the build's own. A build that failed on its own
# prints none of these, and a plain timeout is not one of them.
kubectl_line='^(error|Error from server|E[0-9]+ )'
default_pattern="${kubectl_line}.*(pods? \"[^\"]*\" (not found|was evicted)|pod does not exist|NodeLost)"
default_pattern+="|^(Error from server|E[0-9]+ ).*error dialing backend|^error: unable to upgrade connection"
pattern="${LOST_POD_PATTERN:-${default_pattern}}"

# What the lifecycle reports when the registry fails under it. The lifecycle prints "ERROR: "
# (in colour) and the failing phase; only these phases and these registry symptoms count, so
# a build that failed on its own ("ERROR: failed to build: exit status 1", a bad image name)
# does not. The first group is the cache image and the registry access check, whatever the
# status; the second is an HTTP 5xx, a timeout or a reset on a registry URL in a lifecycle
# phase that talks to the registry.
lifecycle_line='^ERROR: (failed to (create image cache|initialize analyzer|analyze|restore|export|cache)|accessing cache image)'
registry_pattern="${lifecycle_line}.*(accessing cache image|validating registry (read|write)|ensure registry read/write access)"
registry_pattern+="|${lifecycle_line}.*https?://[^ ]+: (unexpected status code 5[0-9][0-9]|.*(timeout|timed out|connection reset|connection refused|no route to host|EOF|Bad Gateway|Service Unavailable|Gateway Time-?out))"
registry_pattern="${REGISTRY_PATTERN:-${registry_pattern}}"
# A failure that names the build cache image: the cache is what to drop on the last attempt.
cache_pattern='^ERROR: .*(accessing cache image|-buildcache[:/ ]|failed to create image cache)'

read -r -a backoff <<<"${RETRY_BACKOFF:-15 45}"
max_attempts=$((1 + ${#backoff[@]}))

log="$(mktemp)"
clean="$(mktemp)"
trap 'rm -f "$log" "$clean"' EXIT

# classify: prints lost, registry or none for the failed run in $log (colour codes removed).
classify() {
  sed 's/\x1b\[[0-9;]*m//g' "$log" > "$clean"
  if grep -Eq -- "$registry_pattern" "$clean"; then echo registry
  elif grep -Eq -- "$pattern" "$clean"; then echo lost
  else echo none; fi
}

attempt=1
no_cache=false
cmd=("$@")
while :; do
  "${cmd[@]}" 2>&1 | tee "$log"
  rc=${PIPESTATUS[0]}
  [ "$rc" -ne 0 ] || exit 0
  has_pod "$@" || exit "$rc"
  kind="$(classify)"
  case "$kind" in
    registry) [ "$attempt" -lt "$max_attempts" ] || exit "$rc" ;;
    lost) [ "$attempt" -eq 1 ] || exit "$rc" ;;
    *) exit "$rc" ;;
  esac
  ! grep -Eq -- "$cache_pattern" "$clean" || no_cache=true
  signature="$(grep -Em1 -- "$([ "$kind" = registry ] && echo "$registry_pattern" || echo "$pattern")" "$clean" | cut -c1-200)"
  attempt=$((attempt + 1))
  suffix="-retry"; [ "$attempt" -le 2 ] || suffix="-retry$((attempt - 1))"
  if [ "$kind" = registry ]; then
    wait="${backoff[$((attempt - 2))]}"
    echo "::warning title=Build registry error::the build failed on a registry error (${signature}); attempt ${attempt} of ${max_attempts} after ${wait}s"
    sleep "$wait"
  else
    echo "::warning title=Build pod lost::the build failed with the signature of a lost pod or node (${signature}); running it once more with a new pod"
  fi
  if [ "$attempt" -eq "$max_attempts" ] && $no_cache; then
    echo "::warning title=Build cache off::the last attempt runs without the build cache image"
    export BUILD_NO_CACHE=1
  fi
  mapfile -d '' cmd < <(with_retry_pod "$suffix" "$@")
done
