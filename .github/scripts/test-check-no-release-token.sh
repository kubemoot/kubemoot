#!/usr/bin/env bash
# Tests check-no-release-token.sh. The fixtures are assembled at run time so this file
# never contains a reference the check would flag.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
check="${here}/check-no-release-token.sh"
scratch="$(mktemp -d)"
trap 'rm -rf "${scratch}"' EXIT
fail=0

s="secrets"
t="RELEASE_TOKEN"
lt="release_token"

expect() { # name want(0 pass,1 fail) content
  local name="$1" want="$2" content="$3" got
  rm -rf "${scratch}/w" && mkdir -p "${scratch}/w"
  printf '%s\n' "${content}" > "${scratch}/w/wf.yaml"
  "${check}" "${scratch}/w" >/dev/null 2>&1
  got=$?
  if [ "${got}" -eq "${want}" ]; then echo "ok   ${name}"; else echo "FAIL ${name} (exit ${got}, want ${want})"; fail=1; fi
}

expect "a workflow with no token reference passes" 0 "token: \${{ steps.app-token.outputs.token }}"
expect "the App secrets pass" 0 "private-key: \${{ ${s}.RELEASE_APP_PRIVATE_KEY }}"
expect "the dot form fails" 1 "token: \${{ ${s}.${t} }}"
expect "the lower-case dot form fails" 1 "token: \${{ ${s}.${lt} }}"
expect "the mixed-case dot form fails" 1 "token: \${{ ${s}.Release_Token }}"
expect "the single-quote bracket form fails" 1 "token: \${{ ${s}['${t}'] }}"
expect "the double-quote bracket form fails" 1 "token: \${{ ${s}[\"${lt}\"] }}"
expect "a spaced bracket form fails" 1 "token: \${{ ${s}[ '${t}' ] }}"
expect "the fallback form fails" 1 "token: \${{ ${s}.${t} || github.token }}"

# Exit codes: 0 clean, 1 a reference found, 2 a directory that does not exist.
"${check}" "${scratch}/missing" >/dev/null 2>&1
got=$?
if [ "${got}" -eq 2 ]; then echo "ok   a missing directory exits 2, not 1"; else echo "FAIL a missing directory exits ${got}, want 2"; fail=1; fi

# With no argument the check reads .github under the current directory.
in_default() { # name want content
  rm -rf "${scratch}/d" && mkdir -p "${scratch}/d/.github/workflows"
  [ -z "$3" ] || printf '%s\n' "$3" > "${scratch}/d/.github/workflows/wf.yaml"
  (cd "${scratch}/d" && "${check}" >/dev/null 2>&1)
  got=$?
  if [ "${got}" -eq "$2" ]; then echo "ok   $1"; else echo "FAIL $1 (exit ${got}, want $2)"; fail=1; fi
}
in_default "no argument: a hit under .github exits 1" 1 "token: \${{ ${s}.${t} }}"
in_default "no argument: a clean .github exits 0" 0 "token: \${{ steps.app-token.outputs.token }}"
rm -rf "${scratch}/e" && mkdir "${scratch}/e"
(cd "${scratch}/e" && "${check}" >/dev/null 2>&1)
got=$?
if [ "${got}" -eq 2 ]; then echo "ok   no argument and no .github exits 2"; else echo "FAIL no argument and no .github exits ${got}, want 2"; fail=1; fi

# A hit outside the default directory is not read.
rm -rf "${scratch}/f" && mkdir -p "${scratch}/f/.github" "${scratch}/f/other"
printf '%s\n' "x: \${{ ${s}.${t} }}" > "${scratch}/f/other/wf.yaml"
(cd "${scratch}/f" && "${check}" >/dev/null 2>&1)
got=$?
if [ "${got}" -eq 0 ]; then echo "ok   only .github is read by default"; else echo "FAIL only .github is read by default (exit ${got})"; fail=1; fi

exit "${fail}"
