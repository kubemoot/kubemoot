#!/usr/bin/env bash
# Tests for retry-lost-build-pod.sh with fake build commands that count their runs.
# Usage: bash .github/scripts/test-retry-lost-build-pod.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/retry-lost-build-pod.sh"

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

# A fake build: appends the value of its --pod argument to runs, prints out.N for run N
# (nothing when the file is absent) and exits with the status in status.N.
cat > "${work}/build" <<EOF2
#!/usr/bin/env bash
while [ \$# -gt 0 ]; do [ "\$1" = "--pod" ] && pod="\$2"; shift; done
echo "[\${pod:-}]" >> "${work}/runs"
n=\$(wc -l < "${work}/runs")
cat "${work}/out.\${n}" 2>/dev/null || true
exit "\$(cat "${work}/status.\${n}" 2>/dev/null || echo 0)"
EOF2
chmod +x "${work}/build"

reset() { rm -f "${work}"/runs "${work}"/out.* "${work}"/status.*; : > "${work}/runs"; }
run() { bash "$script" -- "${work}/build" --pod "p-1" --sha abc >"${work}/log" 2>&1 && echo 0 || echo $?; }
runs() { wc -l < "${work}/runs" | tr -d ' '; }

reset
check "a successful build runs once" 0 "$(run)"
check "and is not retried" 1 "$(runs)"

reset
echo 1 > "${work}/status.1"; echo 'Dockerfile:3: RUN failed: exit status 2' > "${work}/out.1"
check "a build that failed on its own returns its status" 1 "$(run)"
check "and is not retried" 1 "$(runs)"

reset
echo 1 > "${work}/status.1"; echo 'Error from server (NotFound): pods "buildah-x-1" not found' > "${work}/out.1"
check "a lost pod is retried and the retry's success is returned" 0 "$(run)"
check "the build ran twice" 2 "$(runs)"
check "the first run has the pod name as given" "[p-1]" "$(sed -n 1p "${work}/runs")"
check "the second run has the retry suffix on the pod name" "[p-1-retry]" "$(sed -n 2p "${work}/runs")"
check "the retry is a visible warning" 1 "$(grep -c '^::warning title=Build pod lost::' "${work}/log")"

reset
echo 1 > "${work}/status.1"; echo 'error: unable to upgrade connection: pod does not exist' > "${work}/out.1"
echo 3 > "${work}/status.2"; echo 'pod lost again: pods "x" not found' > "${work}/out.2"
check "a second loss returns the second status" 3 "$(run)"
check "and is retried only once" 2 "$(runs)"

reset
echo 1 > "${work}/status.1"; echo 'Error from server: error dialing backend: dial tcp 10.0.0.9:10250: no route to host' > "${work}/out.1"
check "a node that cannot be reached is retried" 0 "$(run)"
check "the build ran twice" 2 "$(runs)"

reset
echo 1 > "${work}/status.1"; echo 'Error from server: pod "buildah-x-1" was evicted' > "${work}/out.1"
check "an evicted pod is retried" 2 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'error: timed out waiting for the condition' > "${work}/out.1"
check "a plain timeout is not a lost pod" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'step 4: the layer named Evicted failed to build' > "${work}/out.1"
check "a build log that merely says Evicted is not retried" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'Error from server (NotFound): pods "x" not found' > "${work}/out.1"
check "a command without --pod is never retried" 1 "$(bash "$script" -- "${work}/build" --sha abc >/dev/null 2>&1; runs)"

reset
echo 0 > "${work}/status.1"; echo 'a log line that says pod does not exist but the build succeeded' > "${work}/out.1"
check "a signature in the output of a successful build is not retried" 1 "$(run >/dev/null; runs)"

check "no command is a usage error" 64 "$(bash "$script" -- >/dev/null 2>&1 && echo 0 || echo $?)"
check "no separator is a usage error" 64 "$(bash "$script" "${work}/build" --pod p-1 >/dev/null 2>&1 && echo 0 || echo $?)"

reset
echo 1 > "${work}/status.1"; echo 'custom failure' > "${work}/out.1"
got=$(LOST_POD_PATTERN='custom failure' bash "$script" -- "${work}/build" --pod p-1 >/dev/null 2>&1 && echo 0 || echo $?)
check "the signature can be replaced" "0:2" "${got}:$(runs)"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
