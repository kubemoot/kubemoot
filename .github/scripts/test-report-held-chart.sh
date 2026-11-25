#!/usr/bin/env bash
# Tests for report-held-chart.sh against a fake gh that serves canned runs per workflow.
# Usage: bash .github/scripts/test-report-held-chart.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/report-held-chart.sh"

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
mkdir -p "${work}/bin" "${work}/state"

# Fake gh: `gh run list --workflow FILE --branch main --json status --jq EXPR` applies EXPR
# with jq to state/FILE.json (the runs as gh returns them, an array of {"status": ...}), or
# fails with the text of state/FILE.error. A call without those arguments fails, and
# state/FILE.blank makes it print nothing and succeed.
cat > "${work}/bin/gh" <<EOF2
#!/usr/bin/env bash
file="" expr="" branch="" json=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    --workflow) file="\$2"; shift ;;
    --jq) expr="\$2"; shift ;;
    --branch) branch="\$2"; shift ;;
    --json) json="\$2"; shift ;;
  esac
  shift
done
if [ -z "\$expr" ] || [ "\$branch" != main ] || [ "\$json" != status ]; then
  echo "fake gh: want --branch main --json status --jq EXPR" >&2; exit 2
fi
if [ -f "${work}/state/\${file}.error" ]; then cat "${work}/state/\${file}.error" >&2; exit 1; fi
[ ! -f "${work}/state/\${file}.blank" ] || exit 0
jq -r "\${expr}" "${work}/state/\${file}.json"
EOF2
chmod +x "${work}/bin/gh"

reset() { rm -rf "${work:?}"/state/* "${work}/held.txt"; }
held_line() {
  echo "$1 0.1.0-rc.2: 3 commit(s) after abc123 touch its sources; build it: gh workflow run release-$1.yaml -f force_release=true" >> "${work}/held.txt"
}
# statuses COMPONENT [STATUS...]: the runs of the component's release workflow; no STATUS is
# a workflow that has never run.
statuses() {
  if [ $# -eq 1 ]; then echo '[]'; else printf '%s\n' "${@:2}" | jq -R '{status: .}' | jq -s .; fi \
    > "${work}/state/release-$1.yaml.json"
}
run() { PATH="${work}/bin:${PATH}" GH_REPO=o/r bash "$script" "${work}/held.txt" >"${work}/out" 2>&1 && echo 0 || echo $?; }

reset
held_line dashboard; statuses dashboard in_progress completed
check "a component whose release is running is a wait" 0 "$(run)"
check "the wait is a notice naming the component" 1 "$(grep -c '^::notice title=Operator chart waits for dashboard::' "${work}/out")"

reset
held_line dashboard; statuses dashboard completed completed
check "a component with no release running fails the job" 1 "$(run)"
check "the error names the component" 1 "$(grep -c '^::error title=Operator chart held: dashboard::' "${work}/out")"
check "the error carries the recovery command" 1 "$(grep -c 'Recover: gh workflow run release-dashboard.yaml -f force_release=true' "${work}/out")"

reset
held_line dashboard; statuses dashboard queued
check "a queued release is a wait" 0 "$(run)"

reset
held_line dashboard; statuses dashboard
check "a workflow with no runs at all fails the job" 1 "$(run)"
check "and is reported as nothing running" 1 "$(grep -c '^::error title=Operator chart held: dashboard::No release-dashboard.yaml run is running' "${work}/out")"

reset
held_line dashboard; held_line indexer
statuses dashboard in_progress; statuses indexer completed
check "one stuck component among waiting ones fails the job" 1 "$(run)"
check "only the stuck component is an error" 1 "$(grep -c '^::error' "${work}/out")"
check "the waiting one is a notice" 1 "$(grep -c '^::notice' "${work}/out")"

reset
held_line dashboard; echo 'HTTP 502: bad gateway' > "${work}/state/release-dashboard.yaml.error"
check "unreadable runs fail the job" 1 "$(run)"
check "the error says the runs could not be read" 1 "$(grep -c 'Could not read the runs of release-dashboard.yaml' "${work}/out")"

reset
held_line dashboard; : > "${work}/state/release-dashboard.yaml.blank"
check "an answer that is not a count fails the job" 1 "$(run)"
check "and is reported as runs that could not be read" 1 "$(grep -c 'Could not read the runs of release-dashboard.yaml' "${work}/out")"

reset
echo "dashboard: cannot list the commits" > "${work}/held.txt"
check "a line that names no workflow fails the job" 1 "$(run)"

reset
: > "${work}/held.txt"
check "an empty file is nothing held" 0 "$(run)"

reset
held_line dashboard; statuses dashboard completed
summary="${work}/summary.md"
PATH="${work}/bin:${PATH}" GH_REPO=o/r GITHUB_STEP_SUMMARY="$summary" bash "$script" "${work}/held.txt" >/dev/null 2>&1 || true
check "the report goes to the step summary as plain markdown" 1 "$(grep -c '^- \*\*Operator chart held: dashboard\*\*: No release-dashboard.yaml run' "$summary")"
check "the summary holds no workflow command" 0 "$(grep -c '^::' "$summary")"

check "a missing file is a usage error" 64 "$(PATH="${work}/bin:${PATH}" GH_REPO=o/r bash "$script" "${work}/absent" >/dev/null 2>&1 && echo 0 || echo $?)"

# Runners without gh read the runs from the REST API with curl.
cat > "${work}/bin/curl" <<EOF2
url="\${@: -1}"
wf=\$(sed -n 's#.*/workflows/\\([^/]*\\)/runs.*#\\1#p' <<<"\$url")
[ -f "${work}/state/\${wf}.json" ] || exit 22
printf '{"total_count":1,"workflow_runs":['
jq -c '[.[] | {status: .status, head_commit: {message: "x"}}]' "${work}/state/\${wf}.json" | sed 's/^\\[//; s/\\]\$//'
printf ']}'
EOF2
chmod +x "${work}/bin/curl"
api_run() { PATH="${work}/bin:${PATH}" GH_REPO=o/r GH_TOKEN=t HELD_CHART_USE_API=1 bash "$script" "${work}/held.txt" >"${work}/out" 2>&1 && echo 0 || echo $?; }
reset; held_line dashboard; statuses dashboard in_progress completed
check "without gh, a running release read from the API is a wait" 0 "$(api_run)"
reset; held_line dashboard; statuses dashboard completed completed
check "without gh, no running release read from the API fails the job" 1 "$(api_run)"
reset; held_line dashboard; statuses dashboard
check "without gh, zero runs is not running" 1 "$(api_run)"
reset; held_line nosuch
check "without gh, an unreadable API answer fails the job" 1 "$(api_run)"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
