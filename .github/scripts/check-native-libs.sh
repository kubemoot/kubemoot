#!/usr/bin/env bash
# Check that a run image holds every shared library a native binary links against.
#
# Reads the binary's dynamic dependencies with ldd on the runner, lists the files of the
# pinned run image from .github/buildpacks/images.yaml, and fails when a library (or the
# program interpreter) has no file of the same name in the image. A packaged binary whose
# library is missing builds and pushes fine and then fails only when the pod starts, so
# the image build runs this first.
#
# Usage: check-native-libs.sh BINARY [RUN_IMAGE_NAME]
#   BINARY           the native executable to check
#   RUN_IMAGE_NAME   the images.yaml entry of the run image (run-tiny)
# Env:
#   IMAGES_FILE (.github/buildpacks/images.yaml)  CRANE (crane)  LDD (ldd)
#   PLATFORM (linux/amd64)  the run image platform to list
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=build-image-buildpacks.sh
source "${here}/build-image-buildpacks.sh"

fail() { echo "check-native-libs: $*" >&2; exit 1; }

# needed_libs: reads ldd output on stdin and prints each library name or interpreter
# path the binary needs, one per line. Fails when ldd could not resolve a library on
# the runner. A static binary needs nothing and prints nothing.
needed_libs() {
  awk '
    /not a dynamic executable|statically linked/ { next }
    /=> not found/ { print "missing on the runner: " $1 > "/dev/stderr"; bad = 1; next }
    $1 ~ /^linux-vdso|^linux-gate/ { next }
    NF > 0 { print $1 }
    END { exit bad }
  '
}

# missing_libs NEEDED_FILE FILES_FILE: prints each needed library whose file name does not
# appear in the image file list. The list holds tar member paths; a library counts as
# present when some member has the same base name (a file or a symlink to one).
missing_libs() {
  awk '
    NR == FNR { n = split($0, part, "/"); have[part[n]] = 1; next }
    { n = split($0, part, "/"); if (!(part[n] in have)) print $0 }
  ' "$2" "$1"
}

main() {
  [ $# -ge 1 ] && [ $# -le 2 ] || fail "usage: check-native-libs.sh BINARY [RUN_IMAGE_NAME]"
  local binary="$1" run_name="${2:-run-tiny}"
  [ -f "$binary" ] || fail "binary not found: ${binary}"
  local images_file run_image work
  images_file="${IMAGES_FILE:-${here}/../buildpacks/images.yaml}"
  run_image="$(pinned_image "$images_file" "$run_name")" || exit 1
  work="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand work now, the trap runs after main returns
  trap "rm -rf '${work}'" EXIT

  "${LDD:-ldd}" "$binary" > "${work}/ldd.txt" 2>&1 || true
  sed 's/^/  /' "${work}/ldd.txt"
  needed_libs < "${work}/ldd.txt" > "${work}/needed.txt" \
    || fail "ldd could not resolve every library of ${binary}"
  "${CRANE:-crane}" export --platform "${PLATFORM:-linux/amd64}" "$run_image" - \
    | tar -tf - > "${work}/files.txt"
  [ -s "${work}/files.txt" ] || fail "listed no files in ${run_image}"

  local missing
  missing="$(missing_libs "${work}/needed.txt" "${work}/files.txt")"
  if [ -n "$missing" ]; then
    fail "$(printf '%s is missing from %s:\n%s' "$binary" "$run_image" "$missing")"
  fi
  echo "${run_image} holds all $(wc -l < "${work}/needed.txt") libraries ${binary} needs"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
