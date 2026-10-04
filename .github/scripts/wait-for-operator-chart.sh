#!/usr/bin/env bash
# Wait until Flux reports that the operator HelmRelease applied a chart version.
#
# Judges the HelmRelease's state, not elapsed time: the chart version in its latest
# release history entry, that entry's status, the Ready condition, and the version Flux
# last attempted. A Ready HelmRelease is not enough: until Flux sees the new chart in
# Harbor, the previous release is Ready too. Flux upgrades with wait on, so a Ready
# release of the version means its workloads rolled out.
#
#   applied     the history's latest entry is VERSION, deployed, and Ready is True
#   superseded  a newer chart version is applied (a later candidate or its final)
#   failed      Flux attempted VERSION and failed, or the HelmRelease is stalled
#   pending     anything else: Flux has not picked VERSION up or is still upgrading
#
# Writes state=applied|superseded to $GITHUB_OUTPUT (stdout outside Actions) and exits 0,
# or exits 1 when the release failed or the safety-net TIMEOUT passed while pending. A
# failed upgrade counts as final: the homelab HelmRelease sets no remediation retries.
#
# Usage: wait-for-operator-chart.sh VERSION
# Env:   HR_NAMESPACE (kubemoot), HR_NAME (kubemoot-operator), TIMEOUT seconds (1800, the
#        HelmRelease's own timeout), POLL seconds (10), KUBECTL (kubectl)
set -euo pipefail

# version_gt A B: true when semver A (X.Y.Z or X.Y.Z-rc.N) is above B.
version_gt() {
  local a="$1" b="$2"
  [ "$a" != "$b" ] || return 1
  if [ "${a%%-*}" != "${b%%-*}" ]; then
    [ "$(printf '%s\n%s\n' "${a%%-*}" "${b%%-*}" | sort -V | tail -n 1)" = "${a%%-*}" ]
    return
  fi
  # Same X.Y.Z: the final is above its candidates, and rc.N compares numerically.
  [ "$a" = "${a%%-*}" ] && return 0
  [ "$b" = "${b%%-*}" ] && return 1
  [ "$((10#${a##*-rc.}))" -gt "$((10#${b##*-rc.}))" ]
}

# chart_state VERSION READY|REASON|HISTORY_VERSION|HISTORY_STATUS|ATTEMPTED|STALLED
chart_state() {
  local want="$1" ready reason hist status attempted stalled
  IFS='|' read -r ready reason hist status attempted stalled <<<"$2"
  # helm-controller may append "+<digest>" to a chart version from an OCI source.
  hist="${hist%%+*}"
  attempted="${attempted%%+*}"
  if [ "$hist" = "$want" ] && [ "$status" = "deployed" ] && [ "$ready" = "True" ]; then
    echo applied
  elif [ "$hist" = "$want" ] && [ "$status" = "failed" ]; then
    echo failed
  elif [ "$attempted" = "$want" ] && [ "$ready" = "False" ] && [[ "$reason" == *Failed ]]; then
    echo failed
  elif [ "$stalled" = "True" ]; then
    echo failed
  elif [ -n "$hist" ] && [ "$status" = "deployed" ] && [ "$ready" = "True" ] && version_gt "$hist" "$want"; then
    echo superseded
  else
    echo pending
  fi
}

read_release() {
  local out
  if ! out="$("${KUBECTL:-kubectl}" get helmrelease "${HR_NAME:-kubemoot-operator}" -n "${HR_NAMESPACE:-kubemoot}" -o jsonpath="$(
    printf '%s' '{.status.conditions[?(@.type=="Ready")].status}|{.status.conditions[?(@.type=="Ready")].reason}|' \
      '{.status.history[0].chartVersion}|{.status.history[0].status}|{.status.lastAttemptedRevision}|' \
      '{.status.conditions[?(@.type=="Stalled")].status}')" 2>&1)"; then
    echo "kubectl: ${out}" >&2
    out=""
  fi
  printf '%s' "$out"
}

main() {
  local want="${1:?usage: wait-for-operator-chart.sh VERSION}" timeout="${TIMEOUT:-1800}" poll="${POLL:-10}"
  local start=$SECONDS line state last=""
  if ! [[ "$want" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; then
    echo "ERROR: [${want}] is not a chart version" >&2
    return 1
  fi
  while :; do
    line="$(read_release)"
    state="$(chart_state "$want" "$line")"
    [ "$line" = "$last" ] || echo "HelmRelease: ${line} -> ${state}" >&2
    last="$line"
    case "$state" in
      applied | superseded)
        [ "$state" = applied ] || echo "A newer chart than ${want} is applied: ${line}" >&2
        echo "state=${state}" >> "${GITHUB_OUTPUT:-/dev/stdout}"
        return 0
        ;;
      failed)
        echo "ERROR: Flux did not apply chart ${want}: ${line}" >&2
        return 1
        ;;
    esac
    if [ $((SECONDS - start)) -ge "$timeout" ]; then
      echo "ERROR: chart ${want} still not applied after ${timeout}s (safety net): ${line}" >&2
      return 1
    fi
    sleep "$poll"
  done
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
