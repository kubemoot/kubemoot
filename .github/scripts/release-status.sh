#!/usr/bin/env bash
# Release status of every public Kubemoot repository: the latest published release, the
# newest release candidate after it, and the user-facing commits not yet published, with
# breaking changes flagged. A repository with unpublished breaking changes is not released
# yet, and its users cannot reach the new Kubemoot API. Publish Release runs this after it
# dispatches the clients, and puts the output in its run summary; the umbrella repository's
# scripts/release-status.sh is the same check for a person at a terminal.
#
# Usage: release-status.sh [--markdown] [repo ...]   (default: the six public repositories)
# Needs gh (authenticated) and git. Exit 1 when any repository has unpublished breaking
# changes. --markdown prints a list for a GitHub step summary.
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

status_of() {
  local repo="$1" latest compare ahead subjects user bang rc line
  latest="$(gh api "repos/${OWNER}/${repo}/releases/latest" -q .tag_name 2>/dev/null || true)"
  if [ -z "$latest" ]; then
    emit "${repo}: no published release"
    return 0
  fi
  compare="$(gh api "repos/${OWNER}/${repo}/compare/${latest}...main" \
    -q '[.ahead_by, ([.commits[].commit.message | split("\n")[0] + (if test("(^|\n)BREAKING[ -]CHANGE") then " [BREAKING CHANGE in the body]" else "" end)]|join("\n"))]|@tsv' 2>/dev/null || true)"
  ahead="${compare%%$'\t'*}"
  subjects="$(printf '%s' "${compare#*$'\t'}" | sed 's/\\n/\n/g')"
  user="$(count_user_facing <<<"$subjects")"
  bang="$(breaking_subjects <<<"$subjects")"
  rc="$(git ls-remote --tags --refs "https://github.com/${OWNER}/${repo}" 2>/dev/null | newest_candidate "$latest" || true)"
  emit "${repo}: released ${latest}; main is ${ahead:-?} commit(s) ahead, ${user} user-facing; newest candidate ${rc:-none}"
  if [ -n "$bang" ]; then
    while IFS= read -r line; do emit_breaking "$line"; done <<<"$bang"
    return 1
  fi
}

breaking=0
for repo in "${repos[@]}"; do
  status_of "$repo" || breaking=1
done

if [ "$breaking" -ne 0 ]; then
  echo
  echo "Some repositories hold unpublished breaking changes: publish them with the release."
  exit 1
fi
