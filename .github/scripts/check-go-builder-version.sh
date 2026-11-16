#!/usr/bin/env bash
# Check that no Go module requires a newer Go than the Dockerfile builder that compiles it.
#
# A Dockerfile builds with GOTOOLCHAIN=local (the image's Go, never a download), so a go.mod
# `go` (or `toolchain`) line newer than the builder's `golang:` tag fails the image build.
# Dependabot raises the gomod and docker ecosystems in separate pull requests, so the go
# directive can move ahead of the builder image; this fails the first pull request that
# makes them disagree instead of the release workflow after merge.
#
# Each Dockerfile under ROOT with a `FROM ...golang:X.Y[.Z]` stage is checked against the
# go.mod in its own directory. A builder tag with a patch version must be at least the
# module's version; a tag without one (golang:1.27) covers every patch of that minor.
#
# Usage: check-go-builder-version.sh [ROOT]   (default: the current directory)
set -euo pipefail

fail() { echo "check-go-builder-version: $*" >&2; exit 1; }

# module_version GOMOD: the newest Go version the module asks for (go or toolchain line).
module_version() {
  local want
  want="$(sed -nE 's/^(go|toolchain)[[:space:]]+(go)?([0-9]+(\.[0-9]+){1,2}).*/\3/p' "$1" | sort -V | tail -1)"
  [ -n "$want" ] || fail "$1 has no go directive"
  printf '%s\n' "$want"
}

# builder_versions DOCKERFILE: the version of every golang: stage, one per line. A golang:
# stage whose tag is not a literal version fails: the check cannot read it.
builder_versions() {
  local line tag
  while IFS= read -r line; do
    tag="$(sed -nE 's/^FROM[[:space:]]+(--platform=[^[:space:]]+[[:space:]]+)?([^[:space:]]*\/)?golang:([^[:space:]@]*).*/\3/p' <<<"$line")"
    [ -n "$tag" ] || continue
    [[ "$tag" =~ ^([0-9]+(\.[0-9]+){1,2})([^0-9.].*)?$ ]] || fail "$1: cannot read a Go version from golang:${tag}"
    printf '%s\n' "${BASH_REMATCH[1]}"
  done < <(grep -iE '^FROM[[:space:]].*golang:' "$1" || true)
}

# covers WANT BUILDER: succeeds when the builder can compile a module that needs WANT. WANT is
# cut to the builder's number of version parts, so golang:1.27 covers 1.27.2.
covers() {
  local want="$1" builder="$2" parts newest
  parts="$(awk -F. '{print NF}' <<<"$builder")"
  want="$(cut -d. -f1-"$parts" <<<"$want")"
  newest="$(printf '%s\n%s\n' "$want" "$builder" | sort -V | tail -1)"
  [ "$newest" = "$builder" ]
}

check_dockerfile() {
  local dockerfile="$1" dir gomod want builder builders status=0
  dir="$(dirname "$dockerfile")"
  gomod="${dir}/go.mod"
  [ -f "$gomod" ] || { echo "check-go-builder-version: ${dockerfile} builds with Go but ${gomod} does not exist; the check cannot tell which module it compiles" >&2; return 1; }
  # The reads run in command substitutions, where fail only ends the substitution.
  want="$(module_version "$gomod")" || return 1
  builders="$(builder_versions "$dockerfile")" || return 1
  while IFS= read -r builder; do
    [ -n "$builder" ] || continue
    if covers "$want" "$builder"; then
      echo "ok: ${gomod} needs Go ${want}; ${dockerfile} builds on ${builder}"
    else
      echo "::error file=${dockerfile}::${gomod} needs Go ${want} but the builder image is golang:${builder}. With GOTOOLCHAIN=local the build fails. Raise the golang tag in ${dockerfile} to ${want} or newer, or lower the go directive." >&2
      status=1
    fi
  done <<<"$builders"
  return "$status"
}

main() {
  [ $# -le 1 ] || fail "usage: check-go-builder-version.sh [ROOT]"
  local root="${1:-.}" dockerfile checked=0 status=0
  [ -d "$root" ] || fail "not a directory: ${root}"
  while IFS= read -r dockerfile; do
    grep -qiE '^FROM[[:space:]].*golang:' "$dockerfile" || continue
    checked=$((checked + 1))
    check_dockerfile "$dockerfile" || status=1
  done < <(find "$root" -name 'Dockerfile*' -type f -not -path '*/node_modules/*' -not -path '*/.git/*' | sort)
  [ "$checked" -gt 0 ] || fail "no Dockerfile with a golang: builder under ${root}"
  exit "$status"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
