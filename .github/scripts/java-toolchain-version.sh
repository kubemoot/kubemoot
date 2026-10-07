#!/usr/bin/env bash
# Print the Java version a Gradle project's toolchain declares, so CI sets up the same
# JDK for the tests and asks the buildpacks for the same JRE (BP_JVM_VERSION) without a
# second copy of the number.
#
# Usage: java-toolchain-version.sh BUILD_FILE
#   BUILD_FILE   a build.gradle.kts with `languageVersion = JavaLanguageVersion.of(N)`
# Prints N. Fails when the file declares no toolchain version or more than one.
set -euo pipefail

die() { echo "java-toolchain-version: $*" >&2; exit 2; }

# toolchain_version FILE: the one JavaLanguageVersion.of(N) in FILE.
toolchain_version() {
  local file="$1" versions
  [ -f "$file" ] || die "build file not found: ${file}"
  versions="$(sed -n 's/.*JavaLanguageVersion\.of([[:space:]]*\([0-9][0-9]*\)[[:space:]]*).*/\1/p' "$file" | sort -u)"
  [ -n "$versions" ] || die "no JavaLanguageVersion.of(N) in ${file}"
  [ "$(wc -l <<<"$versions")" -eq 1 ] || die "more than one Java toolchain version in ${file}: $(paste -sd' ' <<<"$versions")"
  echo "$versions"
}

main() {
  [ $# -eq 1 ] || die "usage: java-toolchain-version.sh BUILD_FILE"
  toolchain_version "$1"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
