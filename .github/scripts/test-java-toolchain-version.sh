#!/usr/bin/env bash
# Tests for java-toolchain-version.sh: the checked-in Java projects, and build files with
# one, none, several, or repeated toolchain versions.
# Usage: bash .github/scripts/test-java-toolchain-version.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=java-toolchain-version.sh
source "${here}/java-toolchain-version.sh"

failures=0
check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   ${name}"
  else
    echo "FAIL ${name}: want [${want}] got [${got}]"
    failures=$((failures + 1))
  fi
}
# check_fails NAME CMD...: the command exits nonzero.
check_fails() {
  local name="$1"; shift
  if ( "$@" ) >/dev/null 2>&1; then check "$name" "fails" "succeeds"; else check "$name" "fails" "fails"; fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The projects whose images the buildpacks build declare one toolchain version.
for project in indexer mcp-gateway; do
  got="$(toolchain_version "${here}/../../${project}/build.gradle.kts")"
  [[ "$got" =~ ^[0-9]+$ ]] && got=number
  check "${project} declares one toolchain version" number "$got"
done

printf '%s\n' 'java {' '    toolchain {' '        languageVersion = JavaLanguageVersion.of(21)' '    }' '}' > "${work}/one.kts"
check "reads the version" 21 "$(toolchain_version "${work}/one.kts")"
check "the script prints the version" 21 "$(bash "${here}/java-toolchain-version.sh" "${work}/one.kts")"

echo 'kotlin { jvmToolchain { languageVersion.set(JavaLanguageVersion.of( 17 )) } }' > "${work}/spaced.kts"
check "spaces inside the parentheses" 17 "$(toolchain_version "${work}/spaced.kts")"

printf '%s\n' 'a = JavaLanguageVersion.of(25)' 'b = JavaLanguageVersion.of(25)' > "${work}/same.kts"
check "the same version twice counts once" 25 "$(toolchain_version "${work}/same.kts")"

printf '%s\n' 'a = JavaLanguageVersion.of(21)' 'b = JavaLanguageVersion.of(25)' > "${work}/two.kts"
check_fails "refuses two different versions" toolchain_version "${work}/two.kts"

echo 'plugins { java }' > "${work}/none.kts"
check_fails "refuses a file without a toolchain" toolchain_version "${work}/none.kts"

echo 'languageVersion = JavaLanguageVersion.of(VERSION)' > "${work}/symbolic.kts"
check_fails "refuses a version that is not a number" toolchain_version "${work}/symbolic.kts"

check_fails "refuses a missing file" toolchain_version "${work}/nowhere.kts"
check_fails "the script refuses no argument" bash "${here}/java-toolchain-version.sh"
check_fails "the script refuses two arguments" bash "${here}/java-toolchain-version.sh" "${work}/one.kts" "${work}/one.kts"

if [ "$failures" -gt 0 ]; then
  echo "${failures} test(s) failed"
  exit 1
fi
echo "all java-toolchain-version tests passed"
