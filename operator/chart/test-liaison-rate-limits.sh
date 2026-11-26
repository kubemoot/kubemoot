#!/usr/bin/env bash
# Render tests for the liaison's rate limits and trusted proxies on the connect pages
# (templates/crew-liaison.yaml): defaults render with connected accounts on, the values reach the
# liaison, the proxy trust is off unless cidrs are listed, and a header without cidrs refuses to
# render. Needs helm and yq. Run from anywhere: operator/chart/test-liaison-rate-limits.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template km "$chart" --namespace km "$@"; }

door='{"liaison":{"expose":{"enabled":true,"hostname":"moot.example.org"},"signIn":{"enabled":true,"publicURL":"https://moot.example.org","allowedRedirectHosts":["app.example.org"],"sealKeySecret":{"name":"seal"},"identity":{"provider":"oidc","oidc":{"issuer":"https://idp.example.org","clientId":"c","clientSecret":{"name":"oidc"}},"signingKeySecret":{"name":"sign"}}}},"gateway":{"hostname":"moot.example.org","enabled":true}}'
acct='{"liaison":{"auth":{"hashKeySecret":{"name":"hash"}},"connectedAccounts":{"enabled":true,"sealKeySecret":{"name":"vault"},"accounts":{"library":{"clientId":"cid","allowedCrews":["ns/guide"]}}}}}'
merge() { yq -o=json eval-all '. as $item ireduce ({}; . * $item)' <(echo "$1") <(echo "$2"); }
on="$(merge "$door" "$acct")"
env_of() { yq -N 'select(.kind == "Deployment" and .metadata.name == "km-kubemoot-operator-liaison") | .spec.template.spec.containers[0].env[] | select(.name == "'"$2"'") | .value' <<<"$1"; }

out="$(render --set-json "$on")"
[ "$(env_of "$out" LIAISON_RATE_PERSON_PER_MINUTE)" = 20 ] || fail "person rate default"
[ "$(env_of "$out" LIAISON_RATE_PERSON_BURST)" = 20 ] || fail "person burst default"
[ "$(env_of "$out" LIAISON_RATE_ADDRESS_PER_MINUTE)" = 120 ] || fail "address rate default"
[ "$(env_of "$out" LIAISON_RATE_ADDRESS_BURST)" = 60 ] || fail "address burst default"
[ -z "$(env_of "$out" LIAISON_TRUSTED_PROXY_CIDRS)" ] || fail "a proxy is trusted by default"
[ -z "$(env_of "$out" LIAISON_TRUSTED_PROXY_HEADER)" ] || fail "a proxy header is set by default"

set_json="$(merge "$on" '{"liaison":{"connectedAccounts":{"rateLimits":{"personPerMinute":6,"addressBurst":200},"trustedProxy":{"cidrs":["10.0.0.0/8","fd00::/8"],"header":"CF-Connecting-IP"}}}}')"
out="$(render --set-json "$set_json")"
[ "$(env_of "$out" LIAISON_RATE_PERSON_PER_MINUTE)" = 6 ] || fail "person rate override"
[ "$(env_of "$out" LIAISON_RATE_ADDRESS_BURST)" = 200 ] || fail "address burst override"
[ "$(env_of "$out" LIAISON_TRUSTED_PROXY_CIDRS)" = "10.0.0.0/8,fd00::/8" ] || fail "trusted proxy cidrs"
[ "$(env_of "$out" LIAISON_TRUSTED_PROXY_HEADER)" = "CF-Connecting-IP" ] || fail "trusted proxy header"

only_cidrs="$(merge "$on" '{"liaison":{"connectedAccounts":{"trustedProxy":{"cidrs":["10.0.0.0/8"]}}}}')"
out="$(render --set-json "$only_cidrs")"
[ -z "$(env_of "$out" LIAISON_TRUSTED_PROXY_HEADER)" ] || fail "a header is set that nobody chose"

header_only="$(merge "$on" '{"liaison":{"connectedAccounts":{"trustedProxy":{"header":"CF-Connecting-IP"}}}}')"
if render --set-json "$header_only" >/dev/null 2>&1; then fail "a header without cidrs rendered"; fi

# Connected accounts off: none of it renders.
out="$(render)"
[ -z "$(env_of "$out" LIAISON_RATE_PERSON_BURST)" ] || fail "the default render sets a rate limit"
echo "ok: liaison rate limit render tests"
