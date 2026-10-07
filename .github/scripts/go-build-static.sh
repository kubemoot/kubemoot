#!/usr/bin/env bash
# Build the statically linked Linux amd64 binary a Go image packages, and check that it
# is static: the image's run image (run-static) has no libc. Uses the Go toolchain the
# workflow set up from the module's go.mod, the one the tests ran with.
#
# Usage: go-build-static.sh [--x IMPORTPATH.NAME=VALUE]... OUTPUT PACKAGE
#   run from the Go module directory
#   --x          a string variable to stamp at link time (-ldflags -X), e.g. a version
#   OUTPUT       the binary to write
#   PACKAGE      the main package, e.g. ./cmd/bridge/
# Env: GO (go)
set -euo pipefail

die() { echo "go-build-static: $*" >&2; exit 2; }

# valid_stamp IMPORTPATH.NAME=VALUE: true when the stamp is one -X flag with no spaces
# or quotes, so it passes through -ldflags unchanged.
valid_stamp() {
  [[ "$1" =~ ^[A-Za-z0-9_./-]+\.[A-Za-z_][A-Za-z0-9_]*=[A-Za-z0-9_.+-]*$ ]]
}

# link_flags STAMP...: the -ldflags value: strip the symbol table and DWARF, then each stamp.
link_flags() {
  local flags="-s -w" stamp
  for stamp in "$@"; do flags+=" -X ${stamp}"; done
  printf '%s\n' "$flags"
}

# is_static BINARY: true when the Go build info records a build with cgo off.
is_static() {
  "${GO:-go}" version -m "$1" | grep -qx $'\tbuild\tCGO_ENABLED=0'
}

main() {
  local stamps=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --x)
        valid_stamp "${2:-}" || die "--x needs IMPORTPATH.NAME=VALUE without spaces, got: ${2:-}"
        stamps+=("$2"); shift 2 ;;
      --) shift; break ;;
      -*) die "unknown option: $1" ;;
      *) break ;;
    esac
  done
  [ $# -eq 2 ] || die "usage: go-build-static.sh [--x IMPORTPATH.NAME=VALUE]... OUTPUT PACKAGE"
  local output="$1" package="$2"

  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "${GO:-go}" build -trimpath \
    -ldflags "$(link_flags ${stamps[@]+"${stamps[@]}"})" -o "$output" "$package"
  is_static "$output" || die "${output} was not built with CGO_ENABLED=0"
  echo "built ${output} (static, linux/amd64)"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
