#!/usr/bin/env bash
# Tests for release-status-issue.sh against a fake gh that records its calls and serves an
# open issue when state/open exists.
# Usage: bash .github/scripts/test-release-status-issue.sh   (exit 0 = all passed)
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
printf -- '- kmctl: released v0.18.0\n  - UNPUBLISHED BREAKING: feat!: rename\n' > "${work}/report.md"

# Fake gh: `issue list` prints 7 when state/open exists; every other call is recorded and
# `issue edit` records the body it was given.
cat > "${work}/bin/gh" <<EOF2
#!/usr/bin/env bash
echo "\$*" >> "${calls}"
if [ "\$2" = "list" ]; then [ -f "${work}/open" ] && echo 7; exit 0; fi
if [ "\$2" = "edit" ] || [ "\$2" = "create" ]; then
  while [ "\$#" -gt 0 ]; do [ "\$1" = "--body-file" ] && cp "\$2" "${work}/body"; shift; done
fi
exit 0
EOF2
chmod +x "${work}/bin/gh"

# run STATUS: output; the calls land in $calls.
run() { : > "$calls"; GH_REPO=kubemoot/kubemoot PATH="${work}/bin:${PATH}" bash "${here}/release-status-issue.sh" "$1" "${work}/report.md" 2>&1; }
count() { grep -c -- "$1" "$calls" || true; }

rm -f "${work}/open"
run 1 >/dev/null
check "breaking with no open issue opens one" "1" "$(count '^issue create --title Public repositories hold')"
check "the issue body carries the report" "1" "$(grep -c 'UNPUBLISHED BREAKING: feat!: rename' "${work}/body")"
check "it opens only one" "0" "$(count '^issue edit')"

touch "${work}/open"
run 1 >/dev/null
check "breaking with an open issue updates it" "1" "$(count '^issue edit 7')"
check "it does not open a second" "0" "$(count '^issue create')"

run 0 >/dev/null
check "clear with an open issue closes it" "1" "$(count '^issue close 7')"
check "closing says why" "1" "$(count 'No public repository holds unpublished breaking changes now')"

run 3 >/dev/null
check "breaking plus unknown still updates the issue" "1" "$(count '^issue edit 7')"

out="$(run 2)"
check "unknown only leaves the open issue alone" "0" "$(count '^issue \(edit\|close\|create\)')"
check "unknown only warns" "1" "$(grep -c '^::warning title=Release status check failed' <<<"$out")"

rm -f "${work}/open"
run 0 >/dev/null
check "clear with no open issue does nothing" "0" "$(count '^issue \(edit\|close\|create\)')"
run 2 >/dev/null
check "unknown only with no open issue opens nothing" "0" "$(count '^issue create')"

check "a non-numeric status is refused" "64" "$(GH_REPO=x PATH="${work}/bin:${PATH}" bash "${here}/release-status-issue.sh" abc "${work}/report.md" >/dev/null 2>&1; echo $?)"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
