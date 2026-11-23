#!/usr/bin/env bash
# Tests for release-status-issue.sh against a fake gh that records its calls and serves open
# issues from the file `open` (lines "<number> <author>").
# Usage: bash .github/scripts/test-release-status-issue.sh   (exit 0 = all passed)
# shellcheck disable=SC2016  # the report fixtures hold literal backticks
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
mkdir -p "${work}/bin"
calls="${work}/calls"
printf -- '- kmctl: released v0.18.0\n  - UNPUBLISHED BREAKING: feat!: rename @someone `code`\n' > "${work}/report.md"

# Fake gh: `issue list` serves the numbers in `open` whose author matches --author, and only
# when --label release-status is given (as the server would filter); `issue edit` and
# `issue create` keep the body they were given; every call is recorded.
cat > "${work}/bin/gh" <<EOF2
#!/usr/bin/env bash
echo "\$*" >> "${calls}"
if [ "\$2" = "list" ]; then
  author=""; label=""
  args=("\$@")
  for i in "\${!args[@]}"; do
    [ "\${args[\$i]}" = "--author" ] && author="\${args[\$((i+1))]}"
    [ "\${args[\$i]}" = "--label" ] && label="\${args[\$((i+1))]}"
  done
  [ "\$label" = "release-status" ] || exit 0
  [ -f "${work}/open" ] && awk -v a="\$author" '\$2 == a { printf "%s\n", \$1 }' "${work}/open" | sort -n
  exit 0
fi
if [ "\$2" = "edit" ] || [ "\$2" = "create" ]; then
  args=("\$@")
  for i in "\${!args[@]}"; do [ "\${args[\$i]}" = "--body-file" ] && cp "\${args[\$((i+1))]}" "${work}/body"; done
fi
exit 0
EOF2
chmod +x "${work}/bin/gh"

run() { : > "$calls"; GH_REPO=kubemoot/kubemoot PATH="${work}/bin:${PATH}" bash "${here}/release-status-issue.sh" "$1" "${work}/report.md" 2>&1; }
code() { : > "$calls"; if GH_REPO=kubemoot/kubemoot PATH="${work}/bin:${PATH}" bash "${here}/release-status-issue.sh" "$1" "${work}/report.md" >/dev/null 2>&1; then echo 0; else echo $?; fi; }
count() { grep -c -- "$1" "$calls" || true; }
writes() { count '^\(issue \(edit\|close\|create\)\|label create\)'; }

rm -f "${work}/open"
run 1 >/dev/null
check "breaking with no open issue opens one" "1" "$(count '^issue create --title Public repositories hold')"
check "it labels the issue" "1" "$(count '^issue create .*--label release-status')"
check "it makes sure the label exists first" "1" "$(count '^label create release-status')"
check "it creates the label with infra's color" "1" "$(count '^label create release-status --color c5def5 ')"
check "it never overwrites an existing label" "0" "$(count '^label create .*--force')"
check "it opens only one" "0" "$(count '^issue edit')"
check "the list asks for the label and the author" "1" "$(count '^issue list .*--label release-status --author app/github-actions')"
check "the issue body carries the report" "1" "$(grep -c 'UNPUBLISHED BREAKING: feat!: rename @someone' "${work}/body")"
check "the report sits inside a code fence" "2" "$(grep -c '^```$' "${work}/body")"

# A subject with backticks gets a longer fence.
printf -- '- x\n  - UNPUBLISHED BREAKING: ```\n@someone\n```\n' > "${work}/report.md"
run 1 >/dev/null
check "a fence inside the report is outlasted" "2" "$(grep -c '^````$' "${work}/body")"
printf -- '- kmctl: released v0.18.0\n  - UNPUBLISHED BREAKING: feat!: rename @someone `code`\n' > "${work}/report.md"

printf '7 app/github-actions\n' > "${work}/open"
run 1 >/dev/null
check "breaking with an open issue updates it" "1" "$(count '^issue edit 7')"
check "it does not open a second" "0" "$(count '^issue create')"

run 0 >/dev/null
check "clear with an open issue closes it" "1" "$(count '^issue close 7')"
check "closing says why" "1" "$(count 'No public repository holds unpublished breaking changes now')"

run 3 >/dev/null
check "breaking plus unknown still updates the issue" "1" "$(count '^issue edit 7')"

out="$(run 2)"
check "unknown only leaves the open issue alone" "0" "$(writes)"
check "unknown only warns" "1" "$(grep -c '^::warning title=Release status check failed' <<<"$out")"
check "unknown only exits 0" "0" "$(code 2)"

# The check itself did not run: 4 and 64 (a bad option), 127 (not found), 1 (a crash).
for st in 4 64 127; do
  check "status ${st} leaves the open issue untouched" "0" "$(code "$st" >/dev/null; writes)"
  check "status ${st} fails the job" "1" "$(code "$st")"
  check "status ${st} says the check did not run" "1" "$(run "$st" | grep -c '^::error title=Release status check did not run')"
done

# Duplicates: edit the oldest, never create a third, and say which was picked.
printf '12 app/github-actions\n9 app/github-actions\n' > "${work}/open"
out="$(run 1)"
check "with two matching issues the oldest is edited" "1" "$(count '^issue edit 9')"
check "with two matching issues none is created" "0" "$(count '^issue create')"
check "with two matching issues the other is not edited" "0" "$(count '^issue edit 12')"
check "the choice is said" "1" "$(grep -c 'using #9, leaving the others' <<<"$out")"
run 0 >/dev/null
check "with two matching issues clearing closes only the oldest" "1|0" "$(count '^issue close 9')|$(count '^issue close 12')"

# An issue with the same title by someone else is ignored (the fake serves by --author).
printf '5 somebody\n' > "${work}/open"
run 1 >/dev/null
check "another author's issue is not edited" "0" "$(count '^issue edit')"
check "another author's issue does not stop a new one" "1" "$(count '^issue create')"
run 0 >/dev/null
check "another author's issue is not closed" "0" "$(writes)"

rm -f "${work}/open"
run 0 >/dev/null
check "clear with no open issue does nothing" "0" "$(writes)"
run 2 >/dev/null
check "unknown only with no open issue opens nothing" "0" "$(writes)"

check "a non-numeric status is refused" "64" "$(GH_REPO=x PATH="${work}/bin:${PATH}" bash "${here}/release-status-issue.sh" abc "${work}/report.md" >/dev/null 2>&1; echo $?)"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
