#!/usr/bin/env bash
# Tests for check-release-workflow-runs.sh against a fake gh that serves a canned run per workflow.
# Usage: bash .github/scripts/test-check-release-workflow-runs.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/check-release-workflow-runs.sh"

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
mkdir -p "${work}/bin" "${work}/state" "${work}/wf"

# Fake gh: `gh api repos/O/R/actions/workflows/FILE/runs?... -q Q` runs Q over state/FILE.json,
# or fails with the text of state/FILE.error.
cat > "${work}/bin/gh" <<EOF2
#!/usr/bin/env bash
file="\$(sed -E 's|.*/workflows/([^/]+)/runs.*|\1|' <<<"\$2")"
if [ -f "${work}/state/\${file}.error" ]; then cat "${work}/state/\${file}.error" >&2; exit 1; fi
jq -r "\$4" "${work}/state/\${file}.json"
EOF2
chmod +x "${work}/bin/gh"

reset() { rm -rf "${work:?}"/state/* "${work:?}"/wf/*; }
workflow() { : > "${work}/wf/$1"; }
# run_json FILE CONCLUSION: the newest completed run; CONCLUSION "none" is a workflow with no run.
run_json() {
  if [ "$2" = none ]; then echo '{"workflow_runs":[]}' > "${work}/state/$1.json"
  else echo "{\"workflow_runs\":[{\"conclusion\":\"$2\",\"html_url\":\"https://example.test/$1/1\"}]}" > "${work}/state/$1.json"; fi
}
run() { PATH="${work}/bin:${PATH}" GH_REPO=o/r bash "$script" "${work}/wf" >"${work}/out" 2>&1 && echo 0 || echo $?; }

reset
for w in release.yaml release-a.yaml release-b.yaml; do workflow "$w"; run_json "$w" success; done
check "all green passes" 0 "$(run)"
check "every release workflow is listed" 3 "$(grep -c '^- release' "${work}/out")"

reset
workflow release-a.yaml; run_json release-a.yaml failure
workflow release-b.yaml; run_json release-b.yaml success
check "a failed latest run exits 1" 1 "$(run)"
check "the failure names the workflow and the run" 1 "$(grep -c 'release-a.yaml: LATEST RUN ON MAIN FAILED (failure): https://example.test/release-a.yaml/1' "${work}/out")"
check "the green workflow is not named as failed" 0 "$(grep -c 'release-b.yaml: LATEST' "${work}/out")"

reset
workflow release-a.yaml; run_json release-a.yaml timed_out
check "a timed out run counts as failed" 1 "$(run)"

reset
workflow release-a.yaml; run_json release-a.yaml cancelled
workflow release-b.yaml; run_json release-b.yaml none
check "cancelled and never-run workflows are not failures" 0 "$(run)"
check "a never-run workflow is said so" 1 "$(grep -c 'release-b.yaml: no completed run' "${work}/out")"

reset
workflow release-a.yaml; echo 'HTTP 502: bad gateway' > "${work}/state/release-a.yaml.error"
check "an unreadable workflow exits 2" 2 "$(run)"
check "the unreadable line carries the error" 1 "$(grep -c 'UNKNOWN.*HTTP 502' "${work}/out")"

reset
workflow release-a.yaml; run_json release-a.yaml failure
workflow release-b.yaml; echo 'HTTP 502' > "${work}/state/release-b.yaml.error"
check "a failure and an unreadable workflow exit 3" 3 "$(run)"

reset
workflow release-status.yaml
workflow ci-a.yaml
workflow release-a.yaml; run_json release-a.yaml success
check "release-status and non-release workflows are skipped" 0 "$(run)"
check "only the release workflow is read" 1 "$(grep -c '^- ' "${work}/out")"

check "a missing directory exits 64" 64 "$(PATH="${work}/bin:${PATH}" GH_REPO=o/r bash "$script" "${work}/nope" >/dev/null 2>&1 && echo 0 || echo $?)"
check "a missing repository fails" 1 "$(env -u GH_REPO -u GITHUB_REPOSITORY PATH="${work}/bin:${PATH}" bash "$script" "${work}/wf" >/dev/null 2>&1 && echo 0 || echo $?)"

if [ "$failures" -ne 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all checks passed"
