#!/usr/bin/env bash
# Tests for run.sh (resolving the test-runner tag) and the one-source rules for the test
# Jobs: no typed test-runner version and no RBAC copy beside the chart's.
# Usage: bash k8s/tests/job/run.test.sh   (exit 0 = all passed)
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
unset TEST_RUNNER
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
image() { sed -nE 's/^ +image: (.*)$/\1/p'; }

for suite in smoke all; do
  check "job-${suite} holds the test-runner placeholder" "ghcr.io/kubemoot/test-runner:0.0.0" \
    "$(image < "${dir}/job-${suite}.yaml")"
  check "job-${suite} runs as the chart's verify runner" "1" \
    "$(grep -c '^      serviceAccountName: kubemoot-verify-runner$' "${dir}/job-${suite}.yaml")"
  check "run.sh ${suite} stamps TEST_RUNNER" "ghcr.io/kubemoot/test-runner:1.2.3" \
    "$(TEST_RUNNER=1.2.3 "${dir}/run.sh" "${suite}" --print | image)"
done
check "run.sh accepts a candidate" "ghcr.io/kubemoot/test-runner:1.2.3-rc.4" \
  "$(TEST_RUNNER=1.2.3-rc.4 "${dir}/run.sh" smoke --print | image)"
check "run.sh refuses a bad version" "1" "$(TEST_RUNNER=latest "${dir}/run.sh" smoke --print >/dev/null 2>&1 && echo 0 || echo 1)"
check "run.sh refuses an unknown suite" "2" "$("${dir}/run.sh" nightly --print >/dev/null 2>&1 && echo 0 || echo $?)"

# The latest final test-runner tag, from a throwaway repository.
repo="$(mktemp -d)"
trap 'rm -rf "${repo}"' EXIT
mkdir -p "${repo}/k8s/tests/job"
cp "${dir}/run.sh" "${dir}"/job-*.yaml "${repo}/k8s/tests/job/"
git -C "${repo}" init -q -b main
git -C "${repo}" -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m init
check "run.sh without a test-runner tag fails" "1" \
  "$("${repo}/k8s/tests/job/run.sh" smoke --print >/dev/null 2>&1 && echo 0 || echo 1)"
for t in test-runner-v0.9.0 test-runner-v0.10.0 test-runner-v0.11.0-rc.3 other-v9.9.9; do
  git -C "${repo}" tag "$t"
done
check "run.sh takes the latest final test-runner tag" "ghcr.io/kubemoot/test-runner:0.10.0" \
  "$("${repo}/k8s/tests/job/run.sh" smoke --print | image)"

check "no RBAC beside the chart's verify runner" "" \
  "$(grep -lE '^kind: (Cluster)?Role(Binding)?$|^kind: ServiceAccount$' "${dir}"/*.yaml || true)"

if [ "$failures" -ne 0 ]; then echo "${failures} test(s) failed"; exit 1; fi
echo "all test job tests passed"
