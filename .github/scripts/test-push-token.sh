#!/usr/bin/env bash
# Tests push-token.sh with a stub git that records its arguments and environment.
# The check helpers are called by name through check; the single quotes keep ${...} for the inner bash.
# shellcheck disable=SC2329,SC2016
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
scratch="$(mktemp -d)"
trap 'rm -rf "${scratch}"' EXIT
fail=0
mkdir "${scratch}/bin"
cat > "${scratch}/bin/git" <<'STUB'
#!/usr/bin/env bash
echo "args: $*" >> "${STUB_LOG}"
echo "header: ${GIT_CONFIG_VALUE_0:-none}" >> "${STUB_LOG}"
echo "token-in-env: ${GH_APP_TOKEN:-none}" >> "${STUB_LOG}"
STUB
chmod +x "${scratch}/bin/git"
export STUB_LOG="${scratch}/log"

# check NAME COMMAND...: passes when COMMAND succeeds.
check() {
  local name="$1"
  shift
  if "$@"; then echo "ok   ${name}"; else echo "FAIL ${name}"; fail=1; fi
}
logged() { grep -q -- "$1" "${STUB_LOG}"; }
printed() { grep -q -- "$1" "${scratch}/out"; }
not() { ! "$@"; }

# run TOKEN DRY_RUN BODY: BODY runs in a bash that sourced push-token.sh; sets rc.
run() {
  : > "${STUB_LOG}"
  PATH="${scratch}/bin:${PATH}" GH_APP_TOKEN="$1" DRY_RUN="$2" bash -c "source '${here}/push-token.sh'; $3" >"${scratch}/out" 2>&1
  rc=$?
}
rc_is() { [ "${rc}" -eq "$1" ]; }
rc_not() { [ "${rc}" -ne "$1" ]; }
no_header_once() { [ "$(grep -c '^header: none' "${STUB_LOG}")" -eq 1 ]; }
log_empty() { [ ! -s "${STUB_LOG}" ]; }
expected="$(printf 'x-access-token:%s' tok123 | base64 -w0)"

run tok123 false 'git push origin refs/tags/v1; git tag -l'
check "a push with a token succeeds" rc_is 0
check "the push carries the header" logged "header: AUTHORIZATION: basic ${expected}"
check "a non-push git command carries no header" no_header_once
check "git does not see GH_APP_TOKEN" not logged "token-in-env: tok123"

run tok123 false 'bash -c "echo child:\${GH_APP_TOKEN:-unset}:\${PUSH_TOKEN_B64:-unset}"'
check "a child process sees neither variable" printed "child:unset:unset"

run "" false 'true'
check "a real run without a token exits 1" rc_is 1
check "the message names the missing token" printed "no release token"

run "" true 'git tag -l; echo reached'
check "a dry run needs no token" printed reached

run "" true 'git push origin x'
check "a push without a token fails even in a dry run" rc_not 0
check "and never reaches git" log_empty

run tok123 "" 'echo reached'
check "an empty DRY_RUN with a token passes" rc_is 0

exit "${fail}"
