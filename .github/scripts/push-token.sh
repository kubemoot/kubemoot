#!/usr/bin/env bash
# Source this first in a script that pushes tags, before anything else runs.
#
# The workflow hands the release bot's token to the step as GH_APP_TOKEN. Helm, crane and
# cosign run in the same script, so the token must not stay in the environment they
# inherit: this file moves it into a shell variable (not exported, so no child process
# has it) and unsets GH_APP_TOKEN. It then wraps `git` so only `git push` carries the
# token, as a one-command GIT_CONFIG_* header; every other git command and every other
# program runs without it.
#
# A real run (DRY_RUN not "true", default "true") with no token stops here with exit 1:
# a push never falls back to anonymous access or another credential.

PUSH_TOKEN_B64=""
if [ -n "${GH_APP_TOKEN:-}" ]; then
  PUSH_TOKEN_B64="$(printf 'x-access-token:%s' "${GH_APP_TOKEN}" | base64 -w0)"
  echo "::add-mask::${PUSH_TOKEN_B64}"
fi
unset GH_APP_TOKEN

if [ "${DRY_RUN:-true}" != "true" ] && [ -z "${PUSH_TOKEN_B64}" ]; then
  echo "ERROR: no release token (GH_APP_TOKEN is empty): the mint step did not run" >&2
  exit 1
fi

git() {
  if [ "${1:-}" = push ]; then
    if [ -z "${PUSH_TOKEN_B64}" ]; then
      echo "ERROR: git push without a release token" >&2
      return 1
    fi
    GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0="http.https://github.com/.extraheader" \
      GIT_CONFIG_VALUE_0="AUTHORIZATION: basic ${PUSH_TOKEN_B64}" command git "$@"
  else
    command git "$@"
  fi
}
