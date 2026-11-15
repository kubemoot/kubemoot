#!/usr/bin/env bash
# Keeps one GitHub issue in this repository in step with the release status check: opens or
# updates it while a public repository holds unpublished breaking changes, closes it when none
# does. The daily Release Status workflow runs this after release-status.sh.
#
# Usage: release-status-issue.sh STATUS REPORT_FILE
#   STATUS       the exit status of release-status.sh (bit 1 breaking, bit 2 could not read)
#   REPORT_FILE  its markdown output
# Environment: GH_TOKEN (issues write), GH_REPO or GITHUB_REPOSITORY (the repository that
# holds the issue).
# A check that could not read a repository and found nothing breaking leaves the issue as it
# is: an unreadable repository is not evidence that the release is complete.
# Tested by test-release-status-issue.sh.
set -euo pipefail

TITLE="Public repositories hold unpublished breaking changes"
status="${1:?usage: release-status-issue.sh STATUS REPORT_FILE}"
report_file="${2:?usage: release-status-issue.sh STATUS REPORT_FILE}"
case "$status" in
  ''|*[!0-9]*) echo "release-status-issue: STATUS must be a number, got '${status}'" >&2; exit 64 ;;
esac
export GH_REPO="${GH_REPO:-${GITHUB_REPOSITORY:?GH_REPO or GITHUB_REPOSITORY must name the repository}}"

open_issue() {
  gh issue list --state open --limit 100 --search "in:title \"${TITLE}\"" \
    --json number,title --jq ".[] | select(.title == \"${TITLE}\") | .number" | head -n 1
}

number="$(open_issue)"

if [ $((status & 1)) -ne 0 ]; then
  body="$(mktemp)"
  trap 'rm -f "$body"' EXIT
  {
    echo "A public Kubemoot repository has held a breaking change for more than a day without a published release. Its users cannot reach the new Kubemoot API until it is published (Publish Release in that repository). A release is complete when scripts/release-status.sh of the umbrella repository exits 0."
    echo
    cat "$report_file"
    echo
    echo "Updated by the Release Status workflow; it closes this issue when no repository holds unpublished breaking changes."
  } > "$body"
  if [ -n "$number" ]; then
    gh issue edit "$number" --body-file "$body" >/dev/null
    echo "Updated issue #${number}"
  else
    gh issue create --title "$TITLE" --body-file "$body"
    echo "Opened the issue"
  fi
elif [ $((status & 2)) -ne 0 ]; then
  echo "::warning title=Release status check failed::A repository could not be read; the issue is left as it is."
elif [ -n "$number" ]; then
  gh issue close "$number" --comment "No public repository holds unpublished breaking changes now." >/dev/null
  echo "Closed issue #${number}"
else
  echo "No unpublished breaking changes and no open issue."
fi
