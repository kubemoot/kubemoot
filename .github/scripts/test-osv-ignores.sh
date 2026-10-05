#!/usr/bin/env bash
# The advisory ignores (osv-scanner.toml) must sit next to every go.mod that requires
# golang.org/x/crypto, because osv-scanner and OpenSSF Scorecard read the file beside
# each manifest, and every copy must be identical so the reasons cannot drift.
#
#   test-osv-ignores.sh            check this repository
#   test-osv-ignores.sh --self     run the checker's own tests
set -euo pipefail

# check_osv_ignores ROOT: prints each problem and returns 1 when there is one.
check_osv_ignores() {
  local root="$1" fail=0 mod dir first=""
  while IFS= read -r mod; do
    dir="$(dirname "${mod}")"
    grep -q 'golang.org/x/crypto ' "${mod}" || continue
    if [[ ! -f "${dir}/osv-scanner.toml" ]]; then
      echo "missing: ${dir#"${root}"/}/osv-scanner.toml"
      fail=1
    fi
  done < <(find "${root}" -name go.mod -not -path '*/node_modules/*' | sort)
  while IFS= read -r toml; do
    if [[ -z "${first}" ]]; then
      first="${toml}"
    elif ! cmp -s "${first}" "${toml}"; then
      echo "differs: ${toml#"${root}"/} from ${first#"${root}"/}"
      fail=1
    fi
  done < <(find "${root}" -name osv-scanner.toml -not -path '*/node_modules/*' | sort)
  return "${fail}"
}

self_test() {
  local tmp failures=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' RETURN
  expect() { # expect NAME WANT_RC
    local rc=0
    check_osv_ignores "${tmp}" >/dev/null || rc=$?
    if [[ "${rc}" == "$2" ]]; then echo "ok   $1"; else echo "FAIL $1 (rc ${rc})"; failures=$((failures + 1)); fi
  }
  mkdir -p "${tmp}/a" "${tmp}/b" "${tmp}/c"
  printf 'module a\nrequire golang.org/x/crypto v0.1.0 // indirect\n' > "${tmp}/a/go.mod"
  printf 'module b\nrequire golang.org/x/crypto v0.1.0\n' > "${tmp}/b/go.mod"
  printf 'module c\n' > "${tmp}/c/go.mod"
  expect "a module needing x/crypto without the file fails" 1
  echo 'x' > "${tmp}/a/osv-scanner.toml"
  echo 'x' > "${tmp}/b/osv-scanner.toml"
  expect "identical copies beside each module pass" 0
  echo 'y' > "${tmp}/b/osv-scanner.toml"
  expect "a copy that differs fails" 1
  echo 'x' > "${tmp}/b/osv-scanner.toml"
  echo 'x' > "${tmp}/c/osv-scanner.toml"
  expect "a module without x/crypto may carry the file" 0
  return "${failures}"
}

if [[ "${1:-}" == "--self" ]]; then
  self_test
else
  check_osv_ignores "$(git rev-parse --show-toplevel)" && echo "ok   osv-scanner.toml beside every x/crypto module, all identical"
fi
