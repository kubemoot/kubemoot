#!/usr/bin/env bash
# Check that a run image can run a native binary built on the runner.
#
# Reads the binary's dynamic dependencies with ldd on the runner and the files of the
# pinned run image from .github/buildpacks/images.yaml. Fails when a library (or the
# program interpreter) has no file of the same name in the image, or when the binary
# needs a GLIBC symbol version the image's libc.so.6 does not define (a runner on a newer
# Ubuntu than the run image). A packaged binary with either gap builds and pushes fine and
# then fails only when the pod starts, so the image build runs this first.
#
# Usage: check-native-libs.sh BINARY [RUN_IMAGE_NAME]
#   BINARY           the native executable to check
#   RUN_IMAGE_NAME   the images.yaml entry of the run image (run-tiny)
# Env:
#   IMAGES_FILE (.github/buildpacks/images.yaml)  CRANE (crane)  LDD (ldd)
#   PLATFORM (linux/amd64)  the run image platform to read
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=build-image-buildpacks.sh
source "${here}/build-image-buildpacks.sh"

fail() { echo "check-native-libs: $*" >&2; exit 1; }

# is_static_report FILE: true when the ldd output says the binary links nothing dynamically.
is_static_report() {
  grep -Eq 'not a dynamic executable|statically linked' "$1"
}

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

# glibc_versions FILE: the GLIBC_x.y version names an ELF file references (a binary's
# needs) or defines (libc.so.6), one per line, sorted and unique.
glibc_versions() {
  { grep -ao 'GLIBC_[0-9][0-9.]*[0-9]' "$1" || true; } | sort -u
}

# missing_glibc BINARY LIBC: prints each GLIBC version the binary needs that LIBC lacks.
missing_glibc() {
  comm -23 <(glibc_versions "$1") <(glibc_versions "$2")
}

# run_ldd BINARY OUT: writes the ldd report to OUT. A nonzero ldd exit passes only for a
# static binary; anything else (ldd missing, not an ELF file) fails with the raw output.
run_ldd() {
  local rc=0
  "${LDD:-ldd}" "$1" > "$2" 2>&1 || rc=$?
  sed 's/^/  /' "$2"
  [ "$rc" -eq 0 ] || is_static_report "$2" || fail "ldd failed on $1 (exit ${rc})"
}

# libc_member FILES_FILE: the tar member path of libc.so.6 in the image, if any.
libc_member() {
  grep -E '(^|/)libc\.so\.6$' "$1" | head -n 1
}

# check_glibc BINARY TAR FILES_FILE RUN_IMAGE WORK: fails when the image's libc lacks a
# GLIBC version the binary needs. Skipped for a binary that needs none.
check_glibc() {
  local binary="$1" tarball="$2" files="$3" run_image="$4" work="$5" member missing
  [ -n "$(glibc_versions "$binary")" ] || return 0
  member="$(libc_member "$files")"
  [ -n "$member" ] || fail "${run_image} has no libc.so.6"
  tar -xOf "$tarball" "$member" > "${work}/libc.so.6"
  [ -s "${work}/libc.so.6" ] || fail "${member} in ${run_image} is empty or a link to elsewhere"
  missing="$(missing_glibc "$binary" "${work}/libc.so.6")"
  [ -z "$missing" ] || fail "$(printf '%s needs GLIBC versions %s lacks:\n%s' "$binary" "$run_image" "$missing")"
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

  run_ldd "$binary" "${work}/ldd.txt"
  needed_libs < "${work}/ldd.txt" > "${work}/needed.txt" \
    || fail "ldd could not resolve every library of ${binary}"
  "${CRANE:-crane}" export --platform "${PLATFORM:-linux/amd64}" "$run_image" "${work}/image.tar"
  tar -tf "${work}/image.tar" > "${work}/files.txt"
  [ -s "${work}/files.txt" ] || fail "listed no files in ${run_image}"

  local missing
  missing="$(missing_libs "${work}/needed.txt" "${work}/files.txt")"
  [ -z "$missing" ] || fail "$(printf '%s is missing from %s:\n%s' "$binary" "$run_image" "$missing")"
  check_glibc "$binary" "${work}/image.tar" "${work}/files.txt" "$run_image" "$work"
  echo "${run_image} holds all $(wc -l < "${work}/needed.txt") libraries and the GLIBC versions ${binary} needs"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
