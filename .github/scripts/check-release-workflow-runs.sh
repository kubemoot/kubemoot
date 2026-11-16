#!/usr/bin/env bash
# Report every component release workflow whose latest finished run on main failed.
#
# A release workflow runs only when its component's paths change, so one that breaks stays
# red and unnoticed until the next change to that component (Release Code Sandbox failed for
# 16 days). This reads the latest completed run on main of each release workflow in the
# workflows directory (release.yaml and release-*.yaml, except release-status.yaml, which is
# the check itself) and names the ones that failed.
#
# Usage: check-release-workflow-runs.sh [WORKFLOWS_DIR]   (default: .github/workflows)
# Environment: GH_TOKEN, and GH_REPO or GITHUB_REPOSITORY (OWNER/REPO whose runs are read).
# Output: one markdown list line per workflow. Exit status, as bits: 1 = a latest run
# failed; 2 = the runs of a workflow could not be read; 3 = both; 0 = none failed.
# A cancelled or skipped run is not a failure, and a workflow that has never run is reported
# as such. Tested by test-check-release-workflow-runs.sh.
set -euo pipefail

dir="${1:-.github/workflows}"
repo="${GH_REPO:-${GITHUB_REPOSITORY:?GH_REPO or GITHUB_REPOSITORY must name the repository}}"
[ -d "$dir" ] || { echo "check-release-workflow-runs: not a directory: ${dir}" >&2; exit 64; }

# latest_run FILE: "<conclusion><TAB><url>" of the newest completed run on main, empty when none.
latest_run() {
  gh api "repos/${repo}/actions/workflows/$1/runs?branch=main&status=completed&per_page=1" \
    -q '.workflow_runs[0] | select(. != null) | [.conclusion, .html_url] | @tsv'
}

# report FILE: prints the workflow's line; returns 1 for a failed run, 2 when unreadable.
report() {
  local file="$1" run conclusion url
  run="$(latest_run "$file" 2>&1)" || { echo "- ${file}: UNKNOWN, could not read its runs: $(head -n 1 <<<"$run")"; return 2; }
  if [ -z "$run" ]; then echo "- ${file}: no completed run on main"; return 0; fi
  conclusion="$(cut -f1 <<<"$run")"
  url="$(cut -f2 <<<"$run")"
  case "$conclusion" in
    failure|timed_out|startup_failure) echo "- ${file}: LATEST RUN ON MAIN FAILED (${conclusion}): ${url}"; return 1 ;;
    *) echo "- ${file}: ${conclusion}" ;;
  esac
}

result=0
for path in "$dir"/release.yaml "$dir"/release-*.yaml; do
  [ -f "$path" ] || continue
  file="$(basename "$path")"
  [ "$file" != "release-status.yaml" ] || continue
  rc=0
  report "$file" || rc=$?
  result=$((result | rc))
done
[ $((result & 1)) -eq 0 ] || echo "A release workflow failed on main: its component is not publishing. Fix the failure; the next merge to that component does not clear it by itself."
exit "$result"
