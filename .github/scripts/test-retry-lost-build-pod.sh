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
echo "[\${BUILD_NO_CACHE:-}]" >> "${work}/cache"
n=\$(wc -l < "${work}/runs")
cat "${work}/out.\${n}" 2>/dev/null || true
exit "\$(cat "${work}/status.\${n}" 2>/dev/null || echo 0)"
EOF2
chmod +x "${work}/build"

reset() { rm -f "${work}"/runs "${work}"/cache "${work}"/out.* "${work}"/status.*; : > "${work}/runs"; }
export RETRY_BACKOFF="0 0"
run() { bash "$script" -- "${work}/build" --pod "p-1" --sha abc >"${work}/log" 2>&1 && echo 0 || echo $?; }
runs() { wc -l < "${work}/runs" | tr -d ' '; }

reset
check "a successful build runs once" 0 "$(run)"
check "and is not retried" 1 "$(runs)"

reset
echo 1 > "${work}/status.1"; echo 'Dockerfile:3: RUN failed: exit status 2' > "${work}/out.1"
check "a build that failed on its own returns its status" 1 "$(run)"
check "and is not retried" 1 "$(runs)"

# The last lines of a build that failed its own check: `kubectl run --rm` reports the failed
# pod and then removes it.
reset
echo 1 > "${work}/status.1"
cat > "${work}/out.1" <<'EOF2'
subprocess exited with status 1
Error: building at STEP "RUN npm run check": exit status 1
pod arc-runners/buildah-dashboard-742da97c-93-1 terminated (Error)
pod "buildah-dashboard-742da97c-93-1" deleted from arc-runners namespace
EOF2
check "a failed build that ends with the pod cleanup line returns its status" 1 "$(run)"
check "and is not retried" 1 "$(runs)"

# The line `kubectl run --rm` prints when it removes the pod.
reset
echo 1 > "${work}/status.1"; echo 'pod "buildah-dashboard-a6491825-139-2" deleted from arc-runners namespace' > "${work}/out.1"
check "the pod cleanup line alone is not a lost pod" 1 "$(run >/dev/null; runs)"

# What kubectl prints for a pod that no longer exists.
reset
echo 1 > "${work}/status.1"; echo 'Error from server (NotFound): pods "buildah-absent-0" not found' > "${work}/out.1"
check "a lost pod is retried and the retry's success is returned" 0 "$(run)"
check "the build ran twice" 2 "$(runs)"
check "the first run has the pod name as given" "[p-1]" "$(sed -n 1p "${work}/runs")"
check "the second run has the retry suffix on the pod name" "[p-1-retry]" "$(sed -n 2p "${work}/runs")"
check "the retry is a visible warning" 1 "$(grep -c '^::warning title=Build pod lost::' "${work}/log")"

# What kubectl prints for an attach whose pod or node is gone.
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
echo 1 > "${work}/status.1"; echo 'E1124 10:00:00.000000    1234 v2.go:104] "Unhandled Error" err="error dialing backend: EOF"' > "${work}/out.1"
check "a lost node in kubectl's log form is retried" 2 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'test output: pods "fixture" not found, pod "fixture" was evicted' > "${work}/out.1"
check "the pod words in the build's own output are not a lost pod" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'npm ERR! request failed: error dialing backend registry, unable to upgrade connection' > "${work}/out.1"
check "the connection words in the build's own output are not a lost pod" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'error: failed to fetch the base image: error dialing backend registry' > "${work}/out.1"
check "a build line that begins error: and mentions dialing is not a lost pod" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'step 7: NodeLost handler compiled, test failed' > "${work}/out.1"
check "NodeLost in the build's own output is not a lost pod" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'Error from server: NodeLost' > "${work}/out.1"
check "NodeLost on a kubectl error line is retried" 2 "$(run >/dev/null; runs)"

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
echo 0 > "${work}/status.1"; echo 'error: unable to upgrade connection: pod does not exist' > "${work}/out.1"
check "a signature in the output of a successful build is not retried" 1 "$(run >/dev/null; runs)"

check "no command is a usage error" 64 "$(bash "$script" -- >/dev/null 2>&1 && echo 0 || echo $?)"
check "no separator is a usage error" 64 "$(bash "$script" "${work}/build" --pod p-1 >/dev/null 2>&1 && echo 0 || echo $?)"

reset
echo 1 > "${work}/status.1"; echo 'custom failure' > "${work}/out.1"
got=$(LOST_POD_PATTERN='custom failure' bash "$script" -- "${work}/build" --pod p-1 >/dev/null 2>&1 && echo 0 || echo $?)
check "the signature can be replaced" "0:2" "${got}:$(runs)"

# Registry failures, with lifecycle output as printed (registry host replaced, colour codes kept).
esc=$'\x1b'
cache_err="${esc}[31;1mERROR: ${esc}[0mfailed to create image cache: accessing cache image \"registry.example/kubemoot/app-buildcache:main\": GET https://registry.example/v2/kubemoot/app-buildcache/blobs/sha256:8a8fdbc9: UNAUTHORIZED: failed to connect to host=db: dial error (no route to host)"
access_err="${esc}[31;1mERROR: ${esc}[0mfailed to initialize analyzer: validating registry write access: failed to ensure registry read/write access to registry.example/kubemoot/app-buildcache:main: HEAD https://registry.example/v2/kubemoot/app-buildcache/manifests/main: unexpected status code 401 Unauthorized (HEAD responses have no body, use GET for details)"
cleanup='pod "buildpacks-app-a0ef4895-23-1" deleted from arc-runners namespace'
cleanup2='pod arc-runners/buildpacks-app-a0ef4895-23-1 terminated (Error)'

reset
echo 1 > "${work}/status.1"; printf '%s\n%s\n%s\n' "$cache_err" "$cleanup" "$cleanup2" > "${work}/out.1"
check "a failed cache image access is retried and the retry's success is returned" 0 "$(run)"
check "the build ran twice" 2 "$(runs)"
check "the retry has its own pod name" "[p-1-retry]" "$(sed -n 2p "${work}/runs")"
check "the retry is a visible warning" 1 "$(grep -c '^::warning title=Build registry error::' "${work}/log")"
check "the second attempt still has the cache" "[]" "$(sed -n 2p "${work}/cache")"

reset
echo 1 > "${work}/status.1"; printf '%s\n%s\n' "$access_err" "$cleanup" > "${work}/out.1"
check "a failed registry write access check is retried" 2 "$(run >/dev/null; runs)"

reset
for n in 1 2 3; do echo 1 > "${work}/status.${n}"; printf '%s\n' "$cache_err" > "${work}/out.${n}"; done
check "a registry error that persists returns the last status" 1 "$(run)"
check "after three runs" 3 "$(runs)"
check "the third pod name is distinct" "[p-1-retry2]" "$(sed -n 3p "${work}/runs")"
check "the first two runs keep the cache" "[]:[]" "$(sed -n 1p "${work}/cache"):$(sed -n 2p "${work}/cache")"
check "the last run drops the cache when the failure named it" "[1]" "$(sed -n 3p "${work}/cache")"

reset
for n in 1 2 3; do echo 1 > "${work}/status.${n}"; printf '%s\n' "ERROR: failed to export: PUT https://registry.example/v2/kubemoot/app/blobs/uploads/x: unexpected status code 503 Service Unavailable" > "${work}/out.${n}"; done
check "an HTTP 5xx from the registry is retried to the limit" 3 "$(run >/dev/null; runs)"
check "the last run keeps the cache when the failure did not name it" "[]" "$(sed -n 3p "${work}/cache")"

reset
echo 1 > "${work}/status.1"; echo 'ERROR: failed to export: PUT https://registry.example/v2/kubemoot/app/blobs/uploads/x: read tcp 10.0.0.1:5000: connection reset by peer' > "${work}/out.1"
check "a connection reset from the registry is retried" 2 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'ERROR: failed to analyze: GET https://registry.example/v2/kubemoot/app/manifests/latest: net/http: TLS handshake timeout' > "${work}/out.1"
check "a registry timeout is retried" 2 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; printf '%s\n' "${esc}[31;1mERROR: ${esc}[0mfailed to build: exit status 1" "$cleanup" > "${work}/out.1"
check "a failed buildpack build is not retried" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo '::error::bad image name: Bad_Name' > "${work}/out.1"
check "the bad image name guard is not retried" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'ERROR: failed to build: GET https://registry.npmjs.org/left-pad: 503 Service Unavailable, timeout' > "${work}/out.1"
check "a build's own download failure is not a registry failure" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'test output: failed to create image cache: accessing cache image x' > "${work}/out.1"
check "the registry words in the build's own output are not retried" 1 "$(run >/dev/null; runs)"

reset
echo 0 > "${work}/status.1"; printf '%s\n' "$cache_err" > "${work}/out.1"
check "a registry error in the output of a successful build is not retried" 1 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; printf '%s\n' "$cache_err" > "${work}/out.1"
check "a registry failure of a command without --pod is not retried" 1 "$(bash "$script" -- "${work}/build" --sha abc >/dev/null 2>&1; runs)"

reset
echo 1 > "${work}/status.1"; printf '%s\n' "$cache_err" > "${work}/out.1"
echo 1 > "${work}/status.2"; echo 'Error from server (NotFound): pods "x" not found' > "${work}/out.2"
check "a lost pod after a registry retry is not retried again" 2 "$(run >/dev/null; runs)"

reset
echo 1 > "${work}/status.1"; echo 'custom registry failure' > "${work}/out.1"
got=$(REGISTRY_PATTERN='custom registry' bash "$script" -- "${work}/build" --pod p-1 >/dev/null 2>&1 && echo 0 || echo $?)
check "the registry signature can be replaced" "0:2" "${got}:$(runs)"

reset
for n in 1 2 3 4; do echo 1 > "${work}/status.${n}"; printf '%s\n' "$access_err" > "${work}/out.${n}"; done
RETRY_BACKOFF="0 0 0" bash "$script" -- "${work}/build" --pod p-1 >/dev/null 2>&1 || true
check "the backoff list sets the number of attempts" "4:[1]" "$(runs):$(sed -n 4p "${work}/cache")"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
