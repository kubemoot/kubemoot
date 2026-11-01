#!/usr/bin/env bash
# Check that a native image was built for baseline x86-64, so it runs on every CPU.
#
# Without -march, GraalVM native-image targets x86-64-v3 (AVX2) when the BUILD machine has
# it and falls back to compatibility only when it does not. The runners sit on nodes with
# and without AVX2, so a binary was portable or not by luck, and one built for x86-64-v3
# crash-loops on a node without AVX2. This reads the machine target native-image records
# in its build-output JSON report (general_info.graal_compiler.march, the value behind the
# "target machine:" line of the build log) and fails unless it is "compatibility".
#
# Quarkus always asks native-image for the report (-H:BuildOutputJSONFile), written as
# <name>-build-output-stats.json beside the build's other native output.
#
# Usage: check-native-target.sh REPORT_OR_DIR
#   REPORT_OR_DIR   the *-build-output-stats.json file, or a directory searched for exactly one
set -euo pipefail

WANT_TARGET=compatibility

fail() { echo "check-native-target: $*" >&2; exit 1; }

# find_report PATH: prints the report file PATH names, or the single report under PATH.
find_report() {
  local path="$1" found
  if [ -f "$path" ]; then echo "$path"; return 0; fi
  [ -d "$path" ] || fail "not a file or directory: ${path}"
  found="$(find "$path" -name '*-build-output-stats.json' -type f | sort)"
  [ -n "$found" ] || fail "no *-build-output-stats.json under ${path}; the native build did not write its report"
  [ "$(printf '%s\n' "$found" | wc -l)" -eq 1 ] \
    || fail "want one build-output report under ${path}, found: $(printf '%s' "$found" | tr '\n' ' ')"
  echo "$found"
}

# target_machine REPORT: prints the machine target the build used; fails when the report
# is not JSON or has no target.
target_machine() {
  local target
  target="$(jq -er '.general_info.graal_compiler.march' "$1" 2>/dev/null)" \
    || fail "$1 has no general_info.graal_compiler.march (not a native-image build-output report?)"
  printf '%s\n' "$target"
}

main() {
  [ $# -eq 1 ] || fail "usage: check-native-target.sh REPORT_OR_DIR"
  command -v jq >/dev/null || fail "jq is required"
  local report target
  report="$(find_report "$1")"
  target="$(target_machine "$report")"
  if [ "$target" != "$WANT_TARGET" ]; then
    echo "::error::The native image targets '${target}', not '${WANT_TARGET}'. It crashes on a CPU without that target's features (x86-64-v3 needs AVX2). Pin -march=${WANT_TARGET} in quarkus.native.additional-build-args." >&2
    fail "${report}: target machine is '${target}', want '${WANT_TARGET}'. Fix: pin -march=${WANT_TARGET} in quarkus.native.additional-build-args"
  fi
  echo "native image target machine: ${target} (${report})"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
