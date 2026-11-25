#!/usr/bin/env bash
# Tests for check-neutral-names.sh against throwaway trees. Run: scripts/check-neutral-names.test.sh
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-neutral-names.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0

pass() { echo "ok - $1"; }
fail() { echo "not ok - $1" >&2; failures=$((failures + 1)); }

# tree <file> <content>: a fresh tree holding one file.
tree() {
  rm -rf "$work/t"
  mkdir -p "$work/t/$(dirname "$1")"
  printf '%s\n' "$2" >"$work/t/$1"
}
passes() { # <name> <file> <content>
  tree "$2" "$3"
  if "$script" "$work/t" >/dev/null 2>&1; then pass "$1"; else fail "$1 (was refused)"; fi
}
refuses() { # <name> <file> <content>
  tree "$2" "$3"
  if "$script" "$work/t" >/dev/null 2>&1; then fail "$1 (was accepted)"; else pass "$1"; fi
}

mkdir -p "$work/empty"
if "$script" "$work/empty" >/dev/null 2>&1; then pass "an empty tree passes"; else fail "an empty tree is refused"; fi

passes "neutral docs pass" docs/integrations/crew-liaison.md 'Open https://<liaison host>/mcp in your chat app.'
passes "the reference crew name is not the homelab" docs/integrations/claude-code.md 'Ask the homelab-pilot crew which nodes exist. Homelab Pilot too.'
passes "the plural in prose is not the word" operator/chart/kubemoot-operator/values.yaml '# tolerates slow homelabs and busy clusters'
passes "a test may name a service by fixture" crew-liaison/internal/liaison/x_test.go 'const svc = "oreilly"'
passes "docs outside the scope may name a service" docs/concepts/who-is-asking.md '"name": "oreilly"'

refuses "a deployment host in docs" docs/integrations/claude-desktop.md 'Connect to https://kubemoot.dijure.com/mcp'
refuses "a deployment host in a chart template" operator/chart/kubemoot-operator/templates/crew-liaison.yaml 'host: a.dijure.com'
refuses "a deployment host in who-is-asking" docs/concepts/who-is-asking.md 'dijure'
refuses "the word homelab in the liaison" crew-liaison/main.go '// the homelab host'
refuses "the word homelab at line start" docs/integrations/crew-liaison.md 'homelab sets the host'
refuses "the word Homelab in any case" docs/integrations/crew-liaison.md 'Homelab sets the host'
refuses "HOMELAB in capitals" crew-liaison/main.go '// HOMELAB host'
refuses "homelab path form" operator/chart/kubemoot-operator/values.yaml '# set in homelab/flux'
refuses "a service in the liaison code" crew-liaison/internal/liaison/tools.go 'return "O'"'"'Reilly"'
refuses "a service in the chart values" operator/chart/kubemoot-operator/values.yaml 'upstream: oreilly'
refuses "a service in the integration docs" docs/integrations/signing-in.md 'sign in to Oreilly'

tree docs/integrations/a.md 'dijure'
out="$("$script" "$work/t" 2>&1 || true)"
if grep -q 'docs/integrations/a.md:1:dijure' <<<"$out"; then pass "a hit names its file and line"; else fail "a hit does not name its file and line"; fi

[ "$failures" -eq 0 ] || { echo "$failures failed" >&2; exit 1; }
echo "all passed"
