#!/usr/bin/env bash
# fuzz-targets.sh list|run [FUZZ_TIME]: from a Go module's root, lists every native fuzz
# target as "PACKAGE_DIR TARGET" (found in *_test.go, no compiling), or fuzzes each one for
# FUZZ_TIME. Tested by test-fuzz-targets.sh.
set -euo pipefail

list() {
  grep -rEo --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\(' . 2>/dev/null \
    | sed -E 's#^(.*)/[^/]+_test\.go:func (Fuzz[A-Za-z0-9_]*)\($#\1 \2#' \
    | sort -u
}

run() {
  local time="${1:?usage: fuzz-targets.sh run FUZZ_TIME}" dir target found=0
  while read -r dir target; do
    found=1
    echo "::group::${target} (${dir})"
    go test -run='^$' -fuzz="^${target}\$" -fuzztime="${time}" "${dir}"
    echo "::endgroup::"
  done < <(list)
  [ "$found" = 1 ] || { echo "fuzz-targets: no fuzz targets found" >&2; exit 1; }
}

case "${1:-}" in
  list) list ;;
  run) shift; run "$@" ;;
  *) echo "usage: fuzz-targets.sh list|run FUZZ_TIME" >&2; exit 64 ;;
esac
