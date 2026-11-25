#!/usr/bin/env bash
# Fails when a name that belongs to one install appears in what every install ships: the
# crew liaison, the operator chart's templates and values, and the liaison's and sign-in
# documentation. The door's host is the install's setting (liaison.expose.hostname), and which
# outside services a crew uses is declared on the crew, so none of it is named here.
#
#   dijure       a host of one particular deployment, anywhere in scope
#   homelab      the word for one particular deployment, in any case (the homelab-pilot reference crew and
#                the product name Homelab Pilot are not it)
#   oreilly      one outside service, in the liaison code, chart and integration docs (tests
#                name it by fixture and may)
#
# Usage: scripts/check-neutral-names.sh [root]   (root defaults to the repository root)
set -euo pipefail

root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$root"

# Everything the rules look at; a path that does not exist is skipped.
shipped=(crew-liaison operator/chart/kubemoot-operator/templates operator/chart/kubemoot-operator/values.yaml
  docs/integrations docs/concepts/who-is-asking.md docs/reference/connected-accounts.md)
# Where the one outside service must not be assumed.
service_scope=(crew-liaison operator/chart/kubemoot-operator/templates operator/chart/kubemoot-operator/values.yaml
  docs/integrations)

existing() {
  local p
  for p in "$@"; do [ -e "$p" ] && printf '%s\n' "$p"; done
  return 0
}

# search <label> <regex> <exclude-regex-or-empty> <paths...>: prints each hit, returns 1 on any.
search() {
  local label="$1" pattern="$2" skip="$3"
  shift 3
  local -a paths
  mapfile -t paths < <(existing "$@")
  [ "${#paths[@]}" -gt 0 ] || return 0
  local hits
  hits="$(grep -rInEi --exclude='*_test.go' --exclude='go.sum' -e "$pattern" "${paths[@]}" || true)"
  if [ -n "$skip" ] && [ -n "$hits" ]; then
    hits="$(grep -viE -e "$skip" <<<"$hits" || true)"
  fi
  [ -z "$hits" ] && return 0
  echo "$label:" >&2
  printf '  %s\n' "${hits//$'\n'/$'\n'  }" >&2
  return 1
}

status=0
search "a deployment host (dijure)" 'dijure' '' "${shipped[@]}" || status=1
search "the word homelab (name the install setting instead)" '(^|[^A-Za-z_-])homelab([^A-Za-z_-]|-[^p]|$)' 'homelab pilot' "${shipped[@]}" || status=1
search "an outside service assumed by the liaison (declare it on the crew)" "[Oo]reilly|O'Reilly" '' "${service_scope[@]}" || status=1

if [ "$status" -ne 0 ]; then
  echo "check-neutral-names: names that belong to one install are in what every install ships" >&2
fi
exit "$status"
