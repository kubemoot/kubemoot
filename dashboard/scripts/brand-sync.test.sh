#!/usr/bin/env bash
# Tests for brand-sync.sh against a throwaway component and a local brand source, so
# they need no network. Run: scripts/brand-sync.test.sh
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/brand-sync.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

old=1111111111111111111111111111111111111111
new=2222222222222222222222222222222222222222
zero=0000000000000000000000000000000000000000000000000000000000000000
failures=0

pass() { echo "ok - $1"; }
fail() {
  echo "not ok - $1" >&2
  failures=$((failures + 1))
}

# A component with scripts/brand-sync.sh and a brand source holding two commits.
setup() {
  rm -rf "$work/component" "$work/source"
  mkdir -p "$work/component/scripts" "$work/source/$old/brand/icon" "$work/source/$new/brand/icon"
  cp "$script" "$work/component/scripts/brand-sync.sh"
  printf 'old mark\n' >"$work/source/$old/brand/icon/mark.svg"
  printf 'old ico\n' >"$work/source/$old/brand/icon/favicon.ico"
  printf 'new mark\n' >"$work/source/$new/brand/icon/mark.svg"
  printf 'new ico\n' >"$work/source/$new/brand/icon/favicon.ico"
  {
    echo "# a comment that refresh keeps"
    echo "commit $old"
    echo "$zero static/mark.svg icon/mark.svg"
    echo "$zero static/favicon.ico icon/favicon.ico"
  } >"$work/component/brand.lock"
}

sync() {
  BRAND_BASE_URL="file://$work/source" "$work/component/scripts/brand-sync.sh" "$@"
}

# expect_fail NAME PATTERN ARGS...: the command fails and its stderr matches PATTERN.
expect_fail() {
  local name="$1" pattern="$2"
  shift 2
  local err
  if err="$(sync "$@" 2>&1 >/dev/null)"; then
    fail "$name: succeeded"
  elif [[ "$err" != *"$pattern"* ]]; then
    fail "$name: stderr was: $err"
  else
    pass "$name"
  fi
}

lock_line() {
  grep -E " $1 " "$work/component/brand.lock" | cut -d' ' -f1
}

setup
if sync refresh >/dev/null && [[ "$(cat "$work/component/static/mark.svg")" == "old mark" ]] &&
  [[ "$(lock_line static/mark.svg)" == "$(sha256sum "$work/component/static/mark.svg" | cut -d' ' -f1)" ]]; then
  pass "refresh copies from the pinned commit and records each hash"
else
  fail "refresh copies from the pinned commit and records each hash"
fi

if sync check >/dev/null; then pass "check passes on fresh copies"; else fail "check passes on fresh copies"; fi

if head -1 "$work/component/brand.lock" | grep -q "a comment that refresh keeps"; then
  pass "refresh keeps the lock's comments"
else
  fail "refresh keeps the lock's comments"
fi

if sync refresh "$new" >/dev/null && grep -q "^commit $new$" "$work/component/brand.lock" &&
  [[ "$(cat "$work/component/static/favicon.ico")" == "new ico" ]] && sync check >/dev/null; then
  pass "refresh COMMIT moves the pin and the copies together"
else
  fail "refresh COMMIT moves the pin and the copies together"
fi

printf 'hand edit\n' >>"$work/component/static/mark.svg"
expect_fail "check fails on a hand-edited copy" "changed: static/mark.svg" check

rm "$work/component/static/mark.svg"
expect_fail "check fails on a missing copy" "missing: static/mark.svg" check

setup
sed -i "s/^commit .*/commit main/" "$work/component/brand.lock"
expect_fail "check fails without a full commit sha" "commit <40-character sha>" check

setup
echo "nothash static/x.svg icon/mark.svg" >>"$work/component/brand.lock"
expect_fail "check fails on a malformed entry" "malformed entry" check

setup
echo "$zero ../outside.svg icon/mark.svg" >>"$work/component/brand.lock"
expect_fail "refresh refuses a destination outside the component" "destination must be" refresh

setup
echo "$zero static/x.svg ../../etc/passwd" >>"$work/component/brand.lock"
expect_fail "refresh refuses a source outside brand/" "source must be" refresh

setup
grep -v "^$zero" "$work/component/brand.lock" >"$work/lock" && mv "$work/lock" "$work/component/brand.lock"
expect_fail "check fails on a lock with no files" "lists no files" check

setup
sync refresh >/dev/null
before="$(cat "$work/component/brand.lock")"
echo "$zero static/gone.svg icon/gone.svg" >>"$work/component/brand.lock"
expect_fail "refresh fails on a source missing at the commit" "could not fetch brand/icon/gone.svg" refresh
if [[ "$(cat "$work/component/static/mark.svg")" == "old mark" && ! -e "$work/component/static/gone.svg" ]] &&
  [[ "$(head -n 4 "$work/component/brand.lock")" == "$before" ]]; then
  pass "a failed refresh leaves the copies and the lock untouched"
else
  fail "a failed refresh leaves the copies and the lock untouched"
fi

setup
rm "$work/component/brand.lock"
expect_fail "check fails without a lock file" "no lock file" check

setup
sed -i "/^commit /d" "$work/component/brand.lock"
expect_fail "check fails without a commit line" "exactly one 'commit" check

setup
echo "commit $new" >>"$work/component/brand.lock"
expect_fail "check fails on two commit lines" "found 2" check

setup
echo "$zero static/x.svg icon/mark.svg extra" >>"$work/component/brand.lock"
expect_fail "check fails on an entry with extra fields" "malformed entry" check

setup
echo "$zero static/x.svg" >>"$work/component/brand.lock"
expect_fail "check fails on an entry without a source" "malformed entry" check

setup
echo "$zero /tmp/x.svg icon/mark.svg" >>"$work/component/brand.lock"
expect_fail "refresh refuses an absolute destination" "destination must be" refresh

setup
echo "$zero static/x.svg /etc/passwd" >>"$work/component/brand.lock"
expect_fail "refresh refuses an absolute source" "source must be" refresh

setup
echo "$zero static/mark.svg icon/favicon.ico" >>"$work/component/brand.lock"
expect_fail "refresh refuses two entries with one destination" "duplicate destination" refresh

setup
echo "$zero brand.lock icon/mark.svg" >>"$work/component/brand.lock"
expect_fail "refresh refuses to overwrite the lock" "may not be the lock" refresh

setup
echo "$zero scripts/brand-sync.sh icon/mark.svg" >>"$work/component/brand.lock"
expect_fail "refresh refuses to overwrite a sync script" "may not be the lock" refresh

setup
: >"$work/source/$old/brand/icon/favicon.ico"
expect_fail "refresh fails on an empty download" "came back empty" refresh
if [[ ! -e "$work/component/static/mark.svg" ]]; then
  pass "an empty download writes no copy"
else
  fail "an empty download writes no copy"
fi

setup
expect_fail "check takes no arguments" "usage:" check extra
expect_fail "refresh takes at most one commit" "usage:" refresh "$old" extra

expect_fail "refresh rejects a short commit" "full 40-character commit sha" refresh abc123
expect_fail "an unknown command prints usage" "usage:" verify

if ((failures > 0)); then
  echo "$failures brand-sync test(s) failed" >&2
  exit 1
fi
echo "brand-sync: all tests passed"
