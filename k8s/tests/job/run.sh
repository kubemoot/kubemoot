#!/usr/bin/env bash
# Runs a test Job (smoke or all) in the kubemoot namespace by hand. The Job runs as the
# operator chart's kubemoot-verify-runner ServiceAccount, the one source of the test
# runner's permissions: install the chart with verify.enabled=true. The manifests hold
# test-runner:0.0.0; this script resolves the latest released test-runner from the git
# tags (fetch them first), or takes TEST_RUNNER=X.Y.Z, and replaces the previous run.
#
# Usage: k8s/tests/job/run.sh smoke|all [--print]
#   --print  print the resolved Job manifest instead of creating it
# Logs:  kubectl logs -f job/kubemoot-test-<suite> -n kubemoot
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
suite="${1:-}"
case "${suite}" in
  smoke | all) ;;
  *) echo "usage: $0 smoke|all [--print]" >&2; exit 2 ;;
esac

# The latest final, as released to GHCR. The Integration Test resolves the latest
# candidate instead (rl_latest_version test-runner-v HEAD rc), because it pulls from Harbor.
tag="${TEST_RUNNER:-$(git -C "${dir}" tag --list 'test-runner-v*' \
  | sed -nE 's/^test-runner-v([0-9]+\.[0-9]+\.[0-9]+)$/\1/p' | sort -V | tail -n 1)}"
if ! [[ "${tag}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; then
  echo "ERROR: no test-runner version [${tag}]: fetch the git tags or set TEST_RUNNER=X.Y.Z" >&2
  exit 1
fi
manifest="$(sed "s#test-runner:0\.0\.0#test-runner:${tag}#" "${dir}/job-${suite}.yaml")"

if [ "${2:-}" = "--print" ]; then
  printf '%s\n' "${manifest}"
  exit 0
fi
kubectl get serviceaccount kubemoot-verify-runner -n kubemoot >/dev/null || {
  echo "ERROR: no kubemoot-verify-runner ServiceAccount: install the operator chart with verify.enabled=true" >&2
  exit 1
}
kubectl delete job "kubemoot-test-${suite}" -n kubemoot --ignore-not-found
printf '%s\n' "${manifest}" | kubectl create -f -
