#!/usr/bin/env bash
# Tests for check-native-target.sh: reading the machine target from a native-image
# build-output report, finding the report in a build directory, and failing loudly.
# Usage: bash .github/scripts/test-check-native-target.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/check-native-target.sh"

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

# report FILE MARCH: a trimmed native-image build-output report with the given target.
report() {
  printf '{"general_info":{"name":"app-runner","graal_compiler":{"optimization_level":"2","march":"%s","pgo":[]}},"analysis_results":{}}\n' "$2" > "$1"
}

# run ARGS...: prints the script's exit status; its output goes to run.log.
run() { bash "$script" "$@" > "${work}/run.log" 2>&1 && echo 0 || echo $?; }

report "${work}/compat.json" compatibility
check "a compatibility build passes" 0 "$(run "${work}/compat.json")"
check "the pass names the target" 1 "$(grep -c 'target machine: compatibility' "${work}/run.log")"

report "${work}/v3.json" x86-64-v3
check "an x86-64-v3 build fails" 1 "$(run "${work}/v3.json")"
check "the failure names the observed target" 1 "$(grep -c "target machine is 'x86-64-v3'" "${work}/run.log")"
check "the failure names the fix" 1 "$(grep -c 'Fix: pin -march=compatibility' "${work}/run.log")"

report "${work}/native.json" native
check "a -march=native build fails" 1 "$(run "${work}/native.json")"

report "${work}/empty.json" ""
check "an empty target fails" 1 "$(run "${work}/empty.json")"

echo '{"general_info":{"name":"app-runner"}}' > "${work}/notarget.json"
check "a report without a target fails" 1 "$(run "${work}/notarget.json")"
check "the failure says the target is missing" 1 "$(grep -c 'no general_info.graal_compiler.march' "${work}/run.log")"

echo 'not json' > "${work}/garbage.json"
check "a file that is not JSON fails" 1 "$(run "${work}/garbage.json")"

mkdir -p "${work}/build/app-native-image-source-jar"
report "${work}/build/app-native-image-source-jar/app-build-output-stats.json" compatibility
check "a build directory with one report passes" 0 "$(run "${work}/build")"
report "${work}/build/other-build-output-stats.json" compatibility
check "a build directory with two reports fails" 1 "$(run "${work}/build")"
check "the failure lists the reports" 1 "$(grep -c 'want one build-output report' "${work}/run.log")"

mkdir -p "${work}/nobuild"
check "a directory without a report fails" 1 "$(run "${work}/nobuild")"
check "the failure says the report is missing" 1 "$(grep -c 'did not write its report' "${work}/run.log")"
check "a missing path fails" 1 "$(run "${work}/absent")"
check "no argument fails" 1 "$(run)"

if [ "$failures" -ne 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all checks passed"
