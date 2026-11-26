#!/usr/bin/env bash
# Decides whether a commit pushed to main was already tested on the pull request that
# merged it, so the release does not repeat the operator's lint and tests.
#
# The commit counts as tested in one of two ways.
#
# A. Merge queue: the commit is the head of a merge group, so a merge_group workflow run
#    exists for exactly this SHA (the queue tested the result that landed), and on the commit
#    itself every required check run came from GitHub Actions and its latest run succeeded.
#    If the queue ran and any required check is not green, the answer is "not tested"; if no
#    merge_group run exists (or it cannot be read), rule B decides.
#
# B. Pull request: all of these hold:
#    1. a merged pull request into main has the commit as its merge result
#       (a rebase merge sets the merge commit to the last rebased commit);
#    2. the file tree of that commit equals the file tree of the pull request's head commit,
#       that is the exact files the pull request's checks ran on (main did not move in a way
#       that changes the files after the checks);
#    3. on the head commit, every required check run came from GitHub Actions and its latest
#       run succeeded. A check that never ran (a path filter skipped it) fails this.
# Anything else, including any API failure, means "not tested": the release runs everything.
#
# Usage: find-tested-commit.sh SHA
# Environment: GH_TOKEN (read access to contents, pull requests and checks);
#   GH_REPO or GITHUB_REPOSITORY (OWNER/REPO); BASE_BRANCH (default main);
#   REQUIRED_CHECKS (check names separated by "|"; default "operator-test|all-checks / all-checks").
# Output on stdout, one "key=value" line each (append it to $GITHUB_OUTPUT):
#   tested=true|false   reason=one line of text
#   pr=NUMBER   head=SHA   (rule B only; rule A prints queue=SHA instead)
# Exit status: 0 always for a decision, 64 for a usage error. Tested by
# test-find-tested-commit.sh.
set -euo pipefail

sha="${1:-}"
if ! [[ "$sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "usage: find-tested-commit.sh SHA (40 hex characters)" >&2
  exit 64
fi
repo="${GH_REPO:-${GITHUB_REPOSITORY:-}}"
[ -n "$repo" ] || { echo "find-tested-commit: GH_REPO or GITHUB_REPOSITORY must name the repository" >&2; exit 64; }
base="${BASE_BRANCH:-main}"
required="${REQUIRED_CHECKS:-operator-test|all-checks / all-checks}"

# not_tested REASON: report that the commit must be tested here, and stop.
not_tested() {
  echo "tested=false"
  echo "reason=$1"
  exit 0
}

tree_of() {
  gh api "repos/${repo}/git/commits/$1" | jq -r '.tree.sha // empty'
}

# failing_check REF: prints why a required check did not succeed on REF (nothing when all did).
# Fails when the check runs of REF cannot be read or parsed.
failing_check() {
  local ref="$1" runs conclusion name names
  runs="$(gh api --paginate --slurp "repos/${repo}/commits/${ref}/check-runs?per_page=100")" || return 1
  IFS='|' read -r -a names <<<"$required"
  for name in "${names[@]}"; do
    conclusion="$(jq -r --arg n "$name" '
      [.[].check_runs[] | select(.name == $n and .app.slug == "github-actions")]
      | sort_by(.completed_at // .started_at) | last | .conclusion // "none"' <<<"$runs")" || return 1
    if [ "$conclusion" != success ]; then
      echo "check \"${name}\" on ${ref} is ${conclusion}, not success"
      return 0
    fi
  done
}

# Rule A: the commit was tested by the merge queue.
queue_runs="$(gh api "repos/${repo}/actions/runs?head_sha=${sha}&event=merge_group&per_page=100" 2>/dev/null \
  | jq -r '.workflow_runs | length')" || queue_runs=0
if [[ "$queue_runs" =~ ^[0-9]+$ ]] && [ "$queue_runs" -gt 0 ]; then
  if failure="$(failing_check "$sha")"; then
    [ -z "$failure" ] || not_tested "the merge queue ran on ${sha}, but ${failure}"
    echo "tested=true"
    echo "queue=${sha}"
    echo "reason=the merge queue tested exactly this commit ${sha}, and its required checks (${required//|/, }) succeeded"
    exit 0
  fi
  not_tested "the check runs of the merge queue commit ${sha} could not be read"
fi

pulls="$(gh api "repos/${repo}/commits/${sha}/pulls")" || not_tested "the pull requests of ${sha} could not be read"
pr_line="$(jq -r --arg sha "$sha" --arg base "$base" '
  [.[] | select(.merged_at != null and .base.ref == $base and .merge_commit_sha == $sha)]
  | first // empty | "\(.number) \(.head.sha)"' <<<"$pulls")" || not_tested "the pull requests of ${sha} could not be parsed"
[ -n "$pr_line" ] || not_tested "no merged pull request into ${base} has ${sha} as its merge commit"
pr="${pr_line%% *}"
head="${pr_line##* }"

pushed_tree="$(tree_of "$sha")" || pushed_tree=""
head_tree="$(tree_of "$head")" || head_tree=""
[ -n "$pushed_tree" ] && [ -n "$head_tree" ] || not_tested "the file trees of ${sha} and of pull request #${pr} head ${head} could not be read"
if [ "$pushed_tree" != "$head_tree" ]; then
  not_tested "the files of ${sha} differ from pull request #${pr} head ${head} (main moved after its checks)"
fi

failure="$(failing_check "$head")" || not_tested "the check runs of pull request #${pr} head ${head} could not be read or parsed"
[ -z "$failure" ] || not_tested "${failure/ on / on pull request #${pr} head }"

echo "tested=true"
echo "pr=${pr}"
echo "head=${head}"
echo "reason=same files as pull request #${pr} head ${head}, whose required checks (${required//|/, }) succeeded"
