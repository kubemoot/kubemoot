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
# The issue is the open one labelled release-status and authored by github-actions, so a
# person's issue with the same words is never touched. When several match, the oldest is
# kept up to date and the others are named and left alone.
# A status above 3 means release-status.sh itself did not run (a bad option, a crash), and a
# check that could not read a repository and found nothing breaking proves nothing: both
# leave the issue as it is. The first also exits 1 so the job fails visibly.
# Tested by test-release-status-issue.sh.
set -euo pipefail

TITLE="Public repositories hold unpublished breaking changes"
LABEL="release-status"
AUTHOR="app/github-actions"
status="${1:?usage: release-status-issue.sh STATUS REPORT_FILE}"
report_file="${2:?usage: release-status-issue.sh STATUS REPORT_FILE}"
case "$status" in
  ''|*[!0-9]*) echo "release-status-issue: STATUS must be a number, got '${status}'" >&2; exit 64 ;;
esac
if [ "$status" -gt 3 ]; then
  echo "::error title=Release status check did not run::release-status.sh exited ${status}; the release status issue is left as it is."
  exit 1
fi
export GH_REPO="${GH_REPO:-${GITHUB_REPOSITORY:?GH_REPO or GITHUB_REPOSITORY must name the repository}}"

# The numbers of the open release status issues, oldest first.
open_issues() {
  gh issue list --state open --label "$LABEL" --author "$AUTHOR" --limit 100 \
    --json number --jq 'sort_by(.number) | .[].number'
}

# The report in a code fence longer than any run of backticks inside it: a commit subject
# can then neither render as markdown nor mention anyone.
fenced() {
  local fence='```'
  while grep -qF -- "$fence" "$1"; do fence="${fence}\`"; done
  printf '%s\n' "$fence"
  cat "$1"
  printf '%s\n' "$fence"
}

mapfile -t numbers < <(open_issues)
number="${numbers[0]:-}"
if [ "${#numbers[@]}" -gt 1 ]; then
  echo "Several open release status issues (${numbers[*]}): using #${number}, leaving the others"
fi

if [ $((status & 1)) -ne 0 ]; then
  body="$(mktemp)"
  trap 'rm -f "$body"' EXIT
  {
    echo "A public Kubemoot repository has held a breaking change for more than a day without a published release. Its users cannot reach the new Kubemoot API until it is published (Publish Release in that repository). A release is complete when scripts/release-status.sh of the umbrella repository exits 0."
    echo
    fenced "$report_file"
    echo
    echo "Updated by the Release Status workflow; it closes this issue when no repository holds unpublished breaking changes."
  } > "$body"
  if [ -n "$number" ]; then
    gh issue edit "$number" --body-file "$body" >/dev/null
    echo "Updated issue #${number}"
  else
    gh label create "$LABEL" --description "Raised by the daily Release Status workflow" --force >/dev/null
    gh issue create --title "$TITLE" --label "$LABEL" --body-file "$body"
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
