#!/usr/bin/env bash
# Tests for release-status.sh against a fake gh and a fake git that serve a canned release
# state per repository.
# Usage: bash .github/scripts/test-release-status.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"

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

# Fake gh: `gh api repos/OWNER/REPO/releases/latest -q ...` prints state/REPO.latest (fails when
# the file is missing); `.../compare/...` prints state/REPO.compare (the query's output: the
# commits ahead, then "<flag><TAB><subject>" lines), or, when state/REPO.compare.json exists,
# runs the query it was given (`-q`, the real jq program of release-status.sh) over that JSON.
cat > "${work}/bin/gh" <<EOF2
#!/usr/bin/env bash
repo="\$(cut -d/ -f3 <<<"\$2")"
kind="\$(sed -E 's|.*/(releases/latest\|compare)(/.*)?|\1|' <<<"\$2" | tr '/' '-')"
if [ -f "${work}/state/\${repo}.fail-\${kind}" ]; then cat "${work}/state/\${repo}.fail-\${kind}" >&2; exit 1; fi
case "\$2" in
  */releases/latest) cat "${work}/state/\${repo}.latest" ;;
  */compare/*)
    if [ -f "${work}/state/\${repo}.compare.json" ]; then jq -r "\$4" "${work}/state/\${repo}.compare.json"
    else cat "${work}/state/\${repo}.compare"; fi ;;
  *) exit 2 ;;
esac
EOF2
# A state/REPO.fail-releases-latest, .fail-compare or .fail-tags file makes that call fail with
# the file's text as its error.
# Fake git: `git ls-remote --tags --refs URL` prints state/REPO.tags.
cat > "${work}/bin/git" <<EOF2
#!/usr/bin/env bash
repo="\$(basename "\$4")"
if [ -f "${work}/state/\${repo}.fail-tags" ]; then cat "${work}/state/\${repo}.fail-tags" >&2; exit 128; fi
cat "${work}/state/\${repo}.tags" 2>/dev/null
EOF2
chmod +x "${work}/bin/gh" "${work}/bin/git"

# A repository: name latest-tag ahead subjects(with \n between) tags(newline separated)
repo() {
  printf '%s\n' "$2" > "${work}/state/$1.latest"
  { printf '%s\n' "$3"; if [ -n "$4" ]; then printf '%s\n' "$4" | sed 's/\\n/\n/g; s/^/0\t/'; fi; } > "${work}/state/$1.compare"
  printf '%s' "$5" | sed 's|^|abc\trefs/tags/|' > "${work}/state/$1.tags"
}
run() { PATH="${work}/bin:${PATH}" bash "${here}/release-status.sh" "$@" 2>&1; }
status() { if PATH="${work}/bin:${PATH}" bash "${here}/release-status.sh" "$@" >/dev/null 2>&1; then echo 0; else echo $?; fi; }

repo kubemoot operator-chart-v0.94.0 2 'feat: a\nchore: b' $'operator-chart-v0.94.0\noperator-chart-v0.94.1-rc.2\noperator-chart-v0.94.1-rc.10\noperator-chart-v0.93.0-rc.3'
repo kmctl v0.18.0 3 'feat!: rename\nfix: x\ndocs: y' $'v0.18.0\nv0.18.1-rc.1'
repo kubemoot-docs v0.5.0 0 '' ''
printf '' > "${work}/state/crews.tags"

check "a current repository: counts and the newest candidate by version order" \
  "kubemoot: released operator-chart-v0.94.0; main is 2 commit(s) ahead, 1 user-facing; newest candidate operator-chart-v0.94.1-rc.10" \
  "$(run kubemoot)"
check "a current repository exits 0" "0" "$(status kubemoot kubemoot-docs)"
check "a repository with no candidate says none" \
  "kubemoot-docs: released v0.5.0; main is 0 commit(s) ahead, 0 user-facing; newest candidate none" \
  "$(run kubemoot-docs)"

out="$(run kmctl || true)"
check "an unpublished breaking change is named" "1" "$(grep -c 'UNPUBLISHED BREAKING: feat!: rename' <<<"$out")"
check "the breaking line counts the user-facing commits" "1" \
  "$(grep -c 'kmctl: released v0.18.0; main is 3 commit(s) ahead, 2 user-facing; newest candidate v0.18.1-rc.1' <<<"$out")"
check "an unpublished breaking change exits 1" "1" "$(status kmctl)"
check "one breaking repository among clean ones exits 1" "1" "$(status kubemoot kmctl kubemoot-docs)"
check "the report still lists the repositories after a breaking one" "1" \
  "$(run kmctl kubemoot-docs | grep -c '^kubemoot-docs: released')"

check "a scoped breaking prefix counts" "1" \
  "$( repo scoped v1.0.0 1 'fix(api)!: drop a field' 'v1.0.0'; run scoped | grep -c UNPUBLISHED )"
check "a BREAKING CHANGE footer is flagged" "1" \
  "$( repo body v1.0.0 1 'feat: new thing [BREAKING CHANGE in the body]' 'v1.0.0'; run body | grep -c UNPUBLISHED )"

repo none-yet '' 0 '' ''
echo 'gh: Not Found (HTTP 404)' > "${work}/state/none-yet.fail-releases-latest"
check "a repository with no release is reported and does not fail" "none-yet: no published release" "$(run none-yet)"
check "a repository with no release exits 0" "0" "$(status none-yet)"

# Errors other than a 404 are not "no release".
repo flaky v1.0.0 0 '' 'v1.0.0'
echo 'gh: HTTP 502: Bad Gateway' > "${work}/state/flaky.fail-releases-latest"
out="$(run flaky || true)"
check "an API error on the latest release is UNKNOWN, not no release" "1|0" \
  "$(grep -c '^flaky: UNKNOWN, could not read the latest release: gh: HTTP 502' <<<"$out")|$(grep -c 'no published release' <<<"$out")"
check "an API error exits 2" "2" "$(status flaky)"
check "an auth failure is UNKNOWN too" "1" \
  "$( echo 'gh: HTTP 401: Bad credentials' > "${work}/state/flaky.fail-releases-latest"; run flaky | grep -c 'UNKNOWN.*401' )"
rm -f "${work}/state/flaky.fail-releases-latest"
echo 'gh: HTTP 403: rate limit exceeded' > "${work}/state/flaky.fail-compare"
check "a failing compare is UNKNOWN" "1" "$(run flaky | grep -c 'flaky: UNKNOWN, could not read the commits since v1.0.0: gh: HTTP 403')"
check "a failing compare exits 2" "2" "$(status flaky)"
rm -f "${work}/state/flaky.fail-compare"
echo 'fatal: unable to access' > "${work}/state/flaky.fail-tags"
check "a failing tag listing is UNKNOWN" "1" "$(run flaky | grep -c 'flaky: UNKNOWN, could not read the tags: fatal')"
rm -f "${work}/state/flaky.fail-tags"
echo 'gh: HTTP 500' > "${work}/state/flaky.fail-compare"
check "breaking plus unknown exits 3" "3" "$(status kmctl flaky)"
check "an unknown repository does not hide the others" "1" "$(run flaky kubemoot-docs | grep -c '^kubemoot-docs: released')"
check "the closing lines name both problems" "1|1" \
  "$(run kmctl flaky | grep -c 'unpublished breaking changes')|$(run kmctl flaky | grep -c 'could not read some repositories')"
rm -f "${work}/state/flaky.fail-compare"
check "a healthy repository after the failures exits 0" "0" "$(status flaky)"
md="$(run --markdown kmctl kubemoot-docs || true)"
check "markdown lists a repository as a bullet" "1" "$(grep -c '^- kubemoot-docs: released v0.5.0' <<<"$md")"
check "markdown nests the breaking change" "1" "$(grep -c '^  - UNPUBLISHED BREAKING: feat!: rename' <<<"$md")"

# RELEASE_STATUS_MIN_AGE_HOURS, through the real jq program over a JSON fixture of the compare
# answer: a young breaking commit is not reported, an old one is, and a subject that contains
# the old marker text cannot hide itself.
ago() { date -u -d "-$1 hours" +%Y-%m-%dT%H:%M:%SZ; }
# jrepo NAME: the compare answer of NAME is the commits given as "HOURS_AGO|MESSAGE" lines on stdin.
jrepo() {
  printf '%s\n' v1.0.0 > "${work}/state/$1.latest"
  : > "${work}/state/$1.tags"
  local n=0 lines=() h m
  while IFS='|' read -r h m; do
    n=$((n + 1)); m="${m//\\n/$'\n'}"
    lines+=("$(jq -n --arg m "$m" --arg d "$(ago "$h")" '{commit: {message: $m, committer: {date: $d}}}')")
  done
  printf '%s\n' "${lines[@]}" | jq -s --argjson n "$n" '{ahead_by: $n, commits: .}' > "${work}/state/$1.compare.json"
}
jrepo young <<'EOF2'
1|feat!: just merged
EOF2
jrepo aged <<'EOF2'
48|feat!: held a while
1|fix: young and ordinary
EOF2
jrepo forged <<'EOF2'
48|feat!: old [WITHIN GRACE]
EOF2
jrepo footer <<'EOF2'
48|fix: old with a footer\n\nBREAKING CHANGE: gone
EOF2
export RELEASE_STATUS_MIN_AGE_HOURS=24
check "a breaking change inside the window is not reported" "0" "$(status young)"
check "a breaking change older than the window is reported" "1" "$(status aged)"
check "the window does not hide the old one among young ones" "1" "$(run aged | grep -c 'UNPUBLISHED BREAKING: feat!: held a while')"
check "a subject carrying the marker text is still reported" "1" "$(status forged)"
check "an old BREAKING CHANGE footer is reported" "1" "$(status footer)"
check "the commit count comes from the query" "1" "$(run aged | grep -c 'aged: released v1.0.0; main is 2 commit(s) ahead, 2 user-facing')"
unset RELEASE_STATUS_MIN_AGE_HOURS
check "without a window a young breaking change is reported" "1" "$(status young)"
export RELEASE_STATUS_MIN_AGE_HOURS=0
check "a window of 0 reports it too" "1" "$(status young)"
unset RELEASE_STATUS_MIN_AGE_HOURS
check "a non-numeric window is refused" "64" \
  "$(RELEASE_STATUS_MIN_AGE_HOURS=soon PATH="${work}/bin:${PATH}" bash "${here}/release-status.sh" young >/dev/null 2>&1; echo $?)"
# A rate-limit answer is UNKNOWN (bit 2), never "no published release" and never clean.
repo limited v1.0.0 0 '' 'v1.0.0'
echo 'gh: API rate limit exceeded (HTTP 403)' > "${work}/state/limited.fail-releases-latest"
check "a 403 rate limit on the latest release is UNKNOWN" "1|0" \
  "$(run limited | grep -c '^limited: UNKNOWN, could not read the latest release: gh: API rate limit exceeded')|$(run limited | grep -c 'no published release')"
check "a 403 rate limit exits 2" "2" "$(status limited)"
rm -f "${work}/state/limited.fail-releases-latest"
echo 'fatal: unable to access the repository' > "${work}/state/limited.fail-tags"
check "a failing git ls-remote exits 2" "2" "$(status limited)"
check "a failing git ls-remote with a breaking repository exits 3" "3" "$(status kmctl limited)"
rm -f "${work}/state/limited.fail-tags"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
