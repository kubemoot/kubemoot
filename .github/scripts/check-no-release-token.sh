#!/usr/bin/env bash
# Fails when a workflow references the retired personal release token in the dot or the
# bracket form of the secrets context, in any letter case.
# Releases mint a kubemoot-release-bot GitHub App token with actions/create-github-app-token.
#
# Usage: check-no-release-token.sh [dir ...]   (default: .github)
# Tested by test-check-no-release-token.sh.
set -euo pipefail

dirs=("$@")
[ "${#dirs[@]}" -gt 0 ] || dirs=(.github)

for d in "${dirs[@]}"; do
  [ -d "${d}" ] || { echo "check-no-release-token: no such directory: ${d}" >&2; exit 2; }
done

if grep -rniE "secrets(\.|\[[[:space:]]*['\"])release_token" "${dirs[@]}"; then
  echo "::error::mint a token with actions/create-github-app-token (vars.RELEASE_APP_ID, secrets.RELEASE_APP_PRIVATE_KEY) instead of the personal release token"
  exit 1
fi
echo "No workflow uses the personal release token."
