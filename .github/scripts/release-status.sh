#!/usr/bin/env bash
# Release status of every public Kubemoot repository: the latest published release, the
# newest release candidate after it, and the user-facing commits not yet published, with
# breaking changes flagged. A repository with unpublished breaking changes is not released
# yet, and its users cannot reach the new Kubemoot API. Publish Release runs this after it
# dispatches the clients, and puts the output in its run summary; the umbrella repository's
# scripts/release-status.sh is the same check for a person at a terminal.
#
# This is the one implementation; the umbrella repository's scripts/release-status.sh runs it.
#
# Usage: release-status.sh [--markdown] [repo ...]   (default: the six public repositories)
# Needs gh (authenticated) and git. --markdown prints a list for a GitHub step summary.
# A repository that has never been released (the API answers 404) is reported as such and is
# not a failure. Any other API or git error is reported as "UNKNOWN" for that repository: the
# check could not tell.
# Exit status, as bits: 1 = a repository holds unpublished breaking changes; 2 = the check
# failed for a repository (UNKNOWN); 3 = both; 0 = every repository reported cleanly.
# Tested by test-release-status.sh.
set -euo pipefail

OWNER="${RELEASE_STATUS_OWNER:-kubemoot}"
markdown=false
repos=()
for arg in "$@"; do
  case "$arg" in
    --markdown) markdown=true ;;
    *) repos+=("$arg") ;;
  esac
done
[ "${#repos[@]}" -gt 0 ] || repos=(kubemoot crews kmctl vscode-crewforge kubemoot-docs release-actions)

# The commits that carry a user-facing conventional prefix, from subjects on stdin.
count_user_facing() {
  grep -cE '^(feat|fix|perf)(\([^)]*\))?!?:' || true
}

# The subjects that mark a breaking change (type!:, or a BREAKING CHANGE footer the compare
# query appends to the subject), from subjects on stdin.
breaking_subjects() {
  grep -E '^[a-z]+(\([^)]*\))?!:|\[BREAKING CHANGE in the body\]$' || true
}

# The newest release candidate with the release's tag prefix, from `git ls-remote` output on
# stdin. Arg: the latest release tag.
newest_candidate() {
  local prefix="${1%%[0-9]*}"
  sed 's|.*refs/tags/||' | grep -E -- "^${prefix}[0-9.]+-rc\.[0-9]+$" | sort -V | tail -1 || true
}

# One line per repository, printed in the chosen format. Args: line
emit() {
  if $markdown; then printf -- '- %s\n' "$1"; else printf '%s\n' "$1"; fi
}

emit_breaking() {
  if $markdown; then printf '  - UNPUBLISHED BREAKING: %s\n' "$1"; else printf '    UNPUBLISHED BREAKING: %s\n' "$1"; fi
}

# gh_read ROUTE JQ: the API answer on stdout. Fails with the error text on stderr, and with
# status 44 when the API answered 404.
gh_read() {
  local err rc=0
  err="$(mktemp)"
  gh api "$1" -q "$2" 2>"$err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    if grep -qE 'HTTP 404|Not Found' "$err"; then rc=44; fi
    head -n 1 "$err" >&2
    rm -f "$err"
    return "$rc"
  fi
  rm -f "$err"
}

# unknown REPO WHAT DETAIL: report a repository the check could not read.
unknown() {
  emit "${1}: UNKNOWN, could not read ${2}: ${3:-no detail}"
}

# status_of REPO: prints the repository's line. Returns 1 for unpublished breaking changes,
# 2 when the check could not read the repository, 0 otherwise.
status_of() {
  local repo="$1" latest compare ahead subjects user bang rc line err rc_status=0
  latest="$(gh_read "repos/${OWNER}/${repo}/releases/latest" .tag_name 2>"${work}/err")" || rc_status=$?
  err="$(cat "${work}/err")"
  if [ "$rc_status" -eq 44 ]; then
    emit "${repo}: no published release"
    return 0
  elif [ "$rc_status" -ne 0 ] || [ -z "$latest" ]; then
    unknown "$repo" "the latest release" "${err:-empty answer}"
    return 2
  fi
  compare="$(gh_read "repos/${OWNER}/${repo}/compare/${latest}...main" \
    '[.ahead_by, ([.commits[].commit.message | split("\n")[0] + (if test("(^|\n)BREAKING[ -]CHANGE") then " [BREAKING CHANGE in the body]" else "" end)]|join("\n"))]|@tsv' \
    2>"${work}/err")" || { unknown "$repo" "the commits since ${latest}" "$(cat "${work}/err")"; return 2; }
  ahead="${compare%%$'\t'*}"
  subjects="$(printf '%s' "${compare#*$'\t'}" | sed 's/\\n/\n/g')"
  user="$(count_user_facing <<<"$subjects")"
  bang="$(breaking_subjects <<<"$subjects")"
  rc="$(git ls-remote --tags --refs "https://github.com/${OWNER}/${repo}" 2>"${work}/err")" \
    || { unknown "$repo" "the tags" "$(head -n 1 "${work}/err")"; return 2; }
  rc="$(newest_candidate "$latest" <<<"$rc")"
  emit "${repo}: released ${latest}; main is ${ahead:-?} commit(s) ahead, ${user} user-facing; newest candidate ${rc:-none}"
  if [ -n "$bang" ]; then
    while IFS= read -r line; do emit_breaking "$line"; done <<<"$bang"
    return 1
  fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

result=0
for repo in "${repos[@]}"; do
  rc=0
  status_of "$repo" || rc=$?
  result=$((result | rc))
done

if [ $((result & 1)) -ne 0 ]; then
  echo
  echo "Some repositories hold unpublished breaking changes: publish them with the release."
fi
if [ $((result & 2)) -ne 0 ]; then
  echo
  echo "The check could not read some repositories (UNKNOWN above): their status is not known."
fi
exit "$result"
