#!/usr/bin/env bash
# Tests for paketo-build-args.sh: comments, blank lines, valid and invalid variables, and
# the buildpacks.env file of each image that ships with one.
# Usage: bash .github/scripts/test-paketo-build-args.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "${here}/../.." && pwd)"
script="${here}/paketo-build-args.sh"

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

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# args FILE_CONTENT: the script's output for a file with that content, lines joined by spaces.
args() {
  printf '%b' "$1" >"${work}/env"
  bash "$script" "${work}/env" 2>&1 | paste -sd' '
}
# status FILE_CONTENT: the script's exit status.
status() {
  printf '%b' "$1" >"${work}/env"
  if bash "$script" "${work}/env" >/dev/null 2>&1; then echo 0; else echo $?; fi
}

check "one variable" "--env BP_A=1" "$(args 'BP_A=1\n')"
check "a glob in a value is kept as text" "--env BP_CPYTHON_VERSION=3.14.*" "$(args 'BP_CPYTHON_VERSION=3.14.*\n')"
check "comments and blank lines are skipped" "--env BP_A=1 --env BP_B=2" "$(args '# why\n\nBP_A=1\n  # more\nBP_B=2\n')"
check "an empty value is allowed" "--env BP_A=" "$(args 'BP_A=\n')"
check "a last line without a newline is read" "--env BP_A=1" "$(args 'BP_A=1')"
check "an empty file prints nothing" "" "$(args '')"
check "a lower case key fails" 2 "$(status 'bp_a=1\n')"
check "a space in the value fails" 2 "$(status 'BP_A=one two\n')"
check "a line without = fails" 2 "$(status 'BP_A\n')"
check "a key that starts with a digit fails" 2 "$(status '1A=1\n')"
check "a missing file fails" 2 "$(bash "$script" "${work}/none" >/dev/null 2>&1 && echo 0 || echo $?)"
check "no argument fails" 2 "$(bash "$script" >/dev/null 2>&1 && echo 0 || echo $?)"

images=(harness-shim/image code-sandbox/image)
for f in "${images[@]}"; do
  check "${f}/buildpacks.env parses" 0 "$(if bash "$script" "${root}/${f}/buildpacks.env" >/dev/null 2>&1; then echo 0; else echo 1; fi)"
done


if [ "$failures" -gt 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all paketo-build-args tests passed"
