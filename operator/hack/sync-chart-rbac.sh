#!/usr/bin/env bash
# Copies the rules controller-gen wrote to config/rbac/role.yaml into the manager
# ClusterRole of the operator chart (the first "rules:" block of templates/rbac.yaml,
# which ends at the next "---"). Run by `make manifests`; CI fails when the result
# differs from what is committed.
# Usage: hack/sync-chart-rbac.sh [ROLE_YAML] [CHART_RBAC_YAML]
set -euo pipefail

role="${1:-config/rbac/role.yaml}"
chart="${2:-chart/kubemoot-operator/templates/rbac.yaml}"
out="$(mktemp)"
trap 'rm -f "${out}"' EXIT

awk '
  NR == FNR { if (seen) rules = rules $0 "\n"; if ($0 == "rules:") seen = 1; next }
  state == 0 { print; if ($0 == "rules:") { printf "%s", rules; state = 1 }; next }
  state == 1 { if ($0 == "---") { print; state = 2 }; next }
  { print }
  END { if (!seen || state != 2) exit 1 }
' "${role}" "${chart}" > "${out}" || {
  echo "ERROR: expected a rules: block in ${role} and a rules: block ending at --- in ${chart}" >&2
  exit 1
}
cat "${out}" > "${chart}"
