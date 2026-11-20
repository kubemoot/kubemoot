#!/usr/bin/env bash
# Choose and build the kmctl the scaffolded-crew check runs.
#
# The quickstart tests the Kubemoot release candidate, so the check runs the kmctl release
# candidate beside it: the newest vX.Y.Z-rc.N tag of kubemoot/kmctl, built from source at
# that tag. kmctl's candidate build publishes no binary (goreleaser runs with publish
# skipped), and a hosted runner cannot reach Harbor anyway, so the tag is the one public
# handle on a candidate. A candidate must be newer than the last final release: otherwise
# the newest rc tag is a leftover of a release already published, and testing it would
# say nothing about what kmctl publishes next.
#
# Usage: kmctl-ref.sh candidate|final OUT_DIR   builds OUT_DIR/kmctl and prints its version.
# Sourced (by the tests), it defines the functions only.
# Environment: KMCTL_REPO (default https://github.com/kubemoot/kmctl), KMCTL_REF (a tag to
# use instead of choosing one). Needs git and go.
set -euo pipefail

# newest_tag PATTERN: the highest version among the tag names on stdin that match PATTERN.
newest_tag() { grep -E "$1" | sort -V | tail -n 1; }

# tag_names: the tag names in `git ls-remote --tags` output on stdin (peeled entries dropped).
tag_names() { sed -n 's#^[0-9a-f]\{40\}[[:space:]]refs/tags/\([^^]*\)$#\1#p'; }

# choose_ref MODE: read tag names on stdin, print the tag to build for MODE (candidate|final).
choose_ref() {
  local mode="$1" tags final rc
  tags="$(cat)"
  final="$(newest_tag '^v[0-9]+\.[0-9]+\.[0-9]+$' <<<"$tags" || true)"
  case "$mode" in
    final)
      [ -n "$final" ] || { echo "kmctl-ref: no final kmctl release tag" >&2; return 1; }
      echo "$final" ;;
    candidate)
      rc="$(newest_tag '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' <<<"$tags" || true)"
      [ -n "$rc" ] || { echo "kmctl-ref: no kmctl release candidate tag" >&2; return 1; }
      # The newest version sorts last, final or candidate; a candidate counts only when its
      # base version is above the last final.
      if [ -n "$final" ] && [ "$(printf '%s\n%s\n' "$final" "${rc%%-*}" | sort -V | tail -n 1)" != "${rc%%-*}" ]; then
        echo "kmctl-ref: newest candidate $rc is not newer than the last release $final" >&2; return 1
      fi
      if [ "${rc%%-*}" = "$final" ]; then
        echo "kmctl-ref: newest candidate $rc is already released as $final" >&2; return 1
      fi
      echo "$rc" ;;
    *) echo "kmctl-ref: mode must be candidate or final, got '$mode'" >&2; return 2 ;;
  esac
}

main() {
  local mode="${1:?usage: kmctl-ref.sh candidate|final OUT_DIR}" out="${2:?usage: kmctl-ref.sh candidate|final OUT_DIR}"
  local repo="${KMCTL_REPO:-https://github.com/kubemoot/kmctl}" ref="${KMCTL_REF:-}" src
  if [ -z "$ref" ]; then
    ref="$(git ls-remote --tags "$repo" 'v*' | tag_names | choose_ref "$mode")"
  fi
  echo "kmctl $ref ($mode)"
  src="$(mktemp -d)"
  git clone -q --depth 1 --branch "$ref" "$repo" "$src"
  mkdir -p "$out"
  out="$(cd "$out" && pwd)"
  # Its own go.mod names the toolchain; the go command fetches it when the runner's is older.
  (cd "$src" && CGO_ENABLED=0 GOTOOLCHAIN=auto go build \
    -ldflags "-s -w -X github.com/kubemoot/kmctl/internal/version.Version=${ref#v}" \
    -o "$out/kmctl" .)
  rm -rf "$src"
  "$out/kmctl" version
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
