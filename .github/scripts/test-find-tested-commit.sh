#!/usr/bin/env bash
# Tests for find-tested-commit.sh against a fake gh that serves canned API answers.
# Usage: bash .github/scripts/test-find-tested-commit.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/find-tested-commit.sh"

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

SHA=1111111111111111111111111111111111111111
HEAD=2222222222222222222222222222222222222222
TREE=3333333333333333333333333333333333333333
OTHER=4444444444444444444444444444444444444444

# Fake gh: `gh api [--paginate] [--slurp] PATH` prints state/<PATH with every character
# outside [A-Za-z0-9] turned into _ and the query removed>.json, or fails when it is absent.
cat > "${work}/bin/gh" <<EOF2
#!/usr/bin/env bash
[ "\$1" = api ] || exit 2
shift
path=""
for a in "\$@"; do
  case "\$a" in --*) ;; *) path="\$a" ;; esac
done
key="\$(printf '%s' "\${path%%\?*}" | tr -c 'A-Za-z0-9' '_')"
file="${work}/state/\${key}.json"
[ -f "\$file" ] || { echo "gh: no canned answer for \${path}" >&2; exit 1; }
cat "\$file"
EOF2
chmod +x "${work}/bin/gh"

key() { printf '%s' "$1" | tr -c 'A-Za-z0-9' '_'; }
put() { printf '%s\n' "$2" > "${work}/state/$(key "$1").json"; }
reset() { rm -f "${work}/state/"*.json; }

# A merged pull request #7 whose merge commit is SHA and whose head is HEAD.
pulls() {
  put "repos/o/r/commits/${SHA}/pulls" \
    "[{\"number\":7,\"merged_at\":\"2026-11-26T01:00:00Z\",\"merge_commit_sha\":\"$1\",\"base\":{\"ref\":\"main\"},\"head\":{\"sha\":\"${HEAD}\"}}]"
}
trees() {
  put "repos/o/r/git/commits/${SHA}" "{\"tree\":{\"sha\":\"$1\"}}"
  put "repos/o/r/git/commits/${HEAD}" "{\"tree\":{\"sha\":\"$2\"}}"
}
# checks TEST_CONCLUSION ALL_CONCLUSION APP: the check runs of the head commit.
checks() {
  put "repos/o/r/commits/${HEAD}/check-runs" \
    "[{\"check_runs\":[
      {\"name\":\"operator-test\",\"conclusion\":\"$1\",\"completed_at\":\"2026-11-26T00:10:00Z\",\"app\":{\"slug\":\"$3\"}},
      {\"name\":\"all-checks / all-checks\",\"conclusion\":\"$2\",\"completed_at\":\"2026-11-26T00:20:00Z\",\"app\":{\"slug\":\"$3\"}}]}]"
}

run() {
  PATH="${work}/bin:${PATH}" GH_REPO=o/r bash "$script" "$@" 2>&1 || echo "exit=$?"
}
field() { grep -E "^$1=" <<<"$2" | head -1 | cut -d= -f2-; }

# Same files, required checks green: tested, naming the pull request and its head.
reset; pulls "$SHA"; trees "$TREE" "$TREE"; checks success success github-actions
out="$(run "$SHA")"
check "tested" "true" "$(field tested "$out")"
check "tested pr" "7" "$(field pr "$out")"
check "tested head" "$HEAD" "$(field head "$out")"

# main moved: the trees differ, so the release tests again.
reset; pulls "$SHA"; trees "$TREE" "$OTHER"; checks success success github-actions
out="$(run "$SHA")"
check "trees differ" "false" "$(field tested "$out")"
check "trees differ reason" "1" "$(grep -c 'differ from pull request #7' <<<"$out")"

# The operator test never ran on the pull request (a path filter skipped it): not tested.
reset; pulls "$SHA"; trees "$TREE" "$TREE"
put "repos/o/r/commits/${HEAD}/check-runs" \
  '[{"check_runs":[{"name":"all-checks / all-checks","conclusion":"success","completed_at":"2026-11-26T00:20:00Z","app":{"slug":"github-actions"}}]}]'
out="$(run "$SHA")"
check "operator-test missing" "false" "$(field tested "$out")"
check "operator-test missing reason" "1" "$(grep -c 'operator-test" on pull request #7 head .* is none' <<<"$out")"

# A failed or skipped required check is not tested.
reset; pulls "$SHA"; trees "$TREE" "$TREE"; checks failure success github-actions
check "operator-test failed" "false" "$(field tested "$(run "$SHA")")"
reset; pulls "$SHA"; trees "$TREE" "$TREE"; checks success skipped github-actions
check "all-checks skipped" "false" "$(field tested "$(run "$SHA")")"

# A check of the right name from another app does not count.
reset; pulls "$SHA"; trees "$TREE" "$TREE"; checks success success some-other-app
check "foreign app" "false" "$(field tested "$(run "$SHA")")"

# A rerun that failed after an earlier success is the latest: not tested.
reset; pulls "$SHA"; trees "$TREE" "$TREE"
put "repos/o/r/commits/${HEAD}/check-runs" \
  '[{"check_runs":[
    {"name":"operator-test","conclusion":"success","completed_at":"2026-11-26T00:10:00Z","app":{"slug":"github-actions"}},
    {"name":"operator-test","conclusion":"failure","completed_at":"2026-11-26T00:30:00Z","app":{"slug":"github-actions"}},
    {"name":"all-checks / all-checks","conclusion":"success","completed_at":"2026-11-26T00:20:00Z","app":{"slug":"github-actions"}}]}]'
check "latest rerun failed" "false" "$(field tested "$(run "$SHA")")"

# The pushed commit is not the pull request's merge commit (a different commit of the push).
reset; pulls "$OTHER"; trees "$TREE" "$TREE"; checks success success github-actions
out="$(run "$SHA")"
check "not the merge commit" "false" "$(field tested "$out")"

# No pull request (a direct push or a manual run), an unmerged one, or another base.
reset; put "repos/o/r/commits/${SHA}/pulls" '[]'
check "no pull request" "false" "$(field tested "$(run "$SHA")")"
reset; put "repos/o/r/commits/${SHA}/pulls" \
  "[{\"number\":7,\"merged_at\":null,\"merge_commit_sha\":\"${SHA}\",\"base\":{\"ref\":\"main\"},\"head\":{\"sha\":\"${HEAD}\"}}]"
check "unmerged pull request" "false" "$(field tested "$(run "$SHA")")"
reset; put "repos/o/r/commits/${SHA}/pulls" \
  "[{\"number\":7,\"merged_at\":\"2026-11-26T01:00:00Z\",\"merge_commit_sha\":\"${SHA}\",\"base\":{\"ref\":\"release\"},\"head\":{\"sha\":\"${HEAD}\"}}]"
check "other base branch" "false" "$(field tested "$(run "$SHA")")"

# API failures fail closed: the release tests.
reset
check "pulls unreadable" "false" "$(field tested "$(run "$SHA")")"
reset; pulls "$SHA"
check "trees unreadable" "false" "$(field tested "$(run "$SHA")")"
reset; pulls "$SHA"; trees "$TREE" "$TREE"
check "checks unreadable" "false" "$(field tested "$(run "$SHA")")"
reset; pulls "$SHA"; trees "$TREE" "$TREE"; put "repos/o/r/commits/${HEAD}/check-runs" 'not json'
check "checks unparsable" "false" "$(field tested "$(run "$SHA")")"

# Usage errors.
check "no sha" "exit=64" "$(run | tail -1)"
check "short sha" "exit=64" "$(run abc123 | tail -1)"
out="$(PATH="${work}/bin:${PATH}" env -u GH_REPO -u GITHUB_REPOSITORY bash "$script" "$SHA" 2>&1 || echo "exit=$?")"
check "no repository" "exit=64" "$(tail -1 <<<"$out")"

if [ "$failures" -gt 0 ]; then
  echo "${failures} failure(s)"
  exit 1
fi
echo "all passed"
