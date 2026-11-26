#!/usr/bin/env bash
# Turn an image's buildpacks.env file into the --env arguments of build-image-buildpacks.sh
# (kubemoot/release-actions). The same file feeds a local `pack build --env-file`.
#
# The file holds one KEY=VALUE per line: build-time variables for the buildpacks, such as
# BP_NODE_RUN_SCRIPTS or BP_CPYTHON_VERSION. Blank lines and lines starting with # are
# skipped. A key is upper case letters, digits and underscores; a value has no space.
#
# Usage: paketo-build-args.sh FILE
# Prints two lines per variable, "--env" then "KEY=VALUE", so a caller reads them with
#   mapfile -t args < <(paketo-build-args.sh FILE)
# Exit status: 0 on success, 2 on a missing file or a line that is not a variable.
set -euo pipefail

die() { echo "paketo-build-args: $*" >&2; exit 2; }

# valid_line LINE: true when LINE is KEY=VALUE with a variable-name key and no space.
valid_line() {
  [[ "$1" =~ ^[A-Z_][A-Z0-9_]*=[^[:space:]]*$ ]]
}

# emit_args FILE: the --env pairs of FILE, or an error naming the first bad line.
emit_args() {
  local line n=0
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
    valid_line "$line" || die "$1 line ${n} is not KEY=VALUE without spaces: ${line}"
    printf '%s\n%s\n' --env "$line"
  done <"$1"
}

main() {
  [ $# -eq 1 ] || die "usage: paketo-build-args.sh FILE"
  [ -f "$1" ] || die "no such file: $1"
  emit_args "$1"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
