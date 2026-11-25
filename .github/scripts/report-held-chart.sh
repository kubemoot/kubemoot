#!/usr/bin/env bash
# Makes a held operator chart visible. release-operator-chart.sh builds no chart candidate
# while a pinned image is older than the code at HEAD (chart-image-pairing.sh), and records
# the stale pins, one line each, in a file. Normally the component's own release is still
# running, finishes, and triggers the chart release again, so the hold is only a wait.
# When no release of that component is running, nothing will ever clear the hold (its build
# failed or was cancelled, or it released nothing): the operator fix then sits unreleased
# until someone finds the warning in a log. This script turns that into a failed job, which
# shows red and emails, naming the component and the command that recovers it.
#
# Usage: report-held-chart.sh HELD_FILE
#   HELD_FILE  lines "NAME VERSION: N commit(s) after SHA touch its sources; build it:
#              gh workflow run release-NAME.yaml -f force_release=true"
# Environment: GH_TOKEN (actions read), and GH_REPO or GITHUB_REPOSITORY (OWNER/REPO whose
# runs are read). GITHUB_STEP_SUMMARY, when set, receives the same report.
# A release counts as running when it is queued or in progress on main (a run started from
# another branch is not counted). A chart run started by hand right after a push can read the
# runs before the component's own push-triggered run exists; the run that a component release
# triggers when it finishes always finds that component's run, so it is the one this guards.
# Exit status: 0 when every held component has a release running (a wait); 1 when one has
# none, or its runs cannot be read, or a line names no workflow. Tested by
# test-report-held-chart.sh.
set -euo pipefail

file="${1:?usage: report-held-chart.sh HELD_FILE}"
[ -f "$file" ] || { echo "report-held-chart: no such file: ${file}" >&2; exit 64; }
repo="${GH_REPO:-${GITHUB_REPOSITORY:?GH_REPO or GITHUB_REPOSITORY must name the repository}}"

# running WORKFLOW: the number of runs on main that have not finished (queued or running);
# a workflow with no runs at all has none. Fails when the runs cannot be read or the answer
# is not a number.
running() {
  local count
  count=$(gh run list --repo "$repo" --workflow "$1" --branch main --limit 20 --json status \
    --jq '[.[] | select(.status != "completed")] | length') || return 1
  [[ "$count" =~ ^[0-9]+$ ]] || return 1
  echo "$count"
}

report() {
  local line="$1" component workflow count
  component="${line%% *}"
  workflow=$(sed -n 's/.*gh workflow run \([A-Za-z0-9._-]*\.yaml\).*/\1/p' <<<"$line")
  if [ -z "$workflow" ]; then
    echo "::error title=Operator chart held::${line}"
    return 1
  fi
  if ! count=$(running "$workflow"); then
    echo "::error title=Operator chart held: ${component}::Could not read the runs of ${workflow}. ${line}"
    return 1
  fi
  if [ "$count" -gt 0 ]; then
    echo "::notice title=Operator chart waits for ${component}::${workflow} is running; the chart release follows when it succeeds. ${line}"
    return 0
  fi
  echo "::error title=Operator chart held: ${component}::No ${workflow} run is running, so nothing will clear this hold. ${line%%; build it:*}. Recover: ${line##*build it: }"
  return 1
}

held=0
while IFS= read -r line; do
  [ -n "$line" ] || continue
  rc=0
  out=$(report "$line") || rc=$?
  printf '%s\n' "$out"
  [ -z "${GITHUB_STEP_SUMMARY:-}" ] || sed -E 's/^::(error|notice) title=(([^:]|:[^:])*)::/- **\2**: /' <<<"$out" >> "$GITHUB_STEP_SUMMARY"
  [ "$rc" -eq 0 ] || held=1
done < "$file"
exit "$held"
