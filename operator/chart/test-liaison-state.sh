#!/usr/bin/env bash
# Render tests for where the liaison keeps its state and for connected accounts
# (templates/crew-liaison.yaml): nothing is kept by default, the door's single-use memory moves
# to NATS when the door is on, and connected accounts render only when enabled with what they
# need. Needs helm and yq. Run from anywhere: operator/chart/test-liaison-state.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template km "$chart" --namespace km "$@"; }

door='{"liaison":{"expose":{"enabled":true,"hostname":"moot.example.org"},"signIn":{"enabled":true,"publicURL":"https://moot.example.org","allowedRedirectHosts":["app.example.org"],"sealKeySecret":{"name":"seal"},"identity":{"provider":"oidc","oidc":{"issuer":"https://idp.example.org","clientId":"c","clientSecret":{"name":"oidc"}},"signingKeySecret":{"name":"sign"}}}},"gateway":{"hostname":"moot.example.org","enabled":true}}'
acct='{"liaison":{"auth":{"hashKeySecret":{"name":"hash"}},"connectedAccounts":{"enabled":true,"sealKeySecret":{"name":"vault"},"accounts":{"library":{"clientId":"cid","clientSecret":{"name":"lib-secret"},"allowedCrews":["ns/guide"],"hosts":["auth.example.org"],"maxLifetimeHours":48}}}}}'
merge() { yq -o=json eval-all '. as $item ireduce ({}; . * $item)' <(echo "$1") <(echo "$2"); }
withdoor() { merge "$door" "${1:-{\}}"; }

liaison() { yq -N 'select(.kind == "Deployment" and .metadata.name == "km-kubemoot-operator-liaison")' <<<"$1"; }
env_of() { liaison "$1" | yq -N '.spec.template.spec.containers[0].env[] | select(.name == "'"$2"'") | .value // "set"'; }
has_service() { yq -N 'select(.kind == "Service" and .metadata.name == "km-kubemoot-operator-liaison-broker") | .metadata.name' <<<"$1"; }
operator_env() { yq -N 'select(.kind == "Deployment" and .metadata.name == "km-kubemoot-operator") | .spec.template.spec.containers[0].env[] | select(.name == "KUBEMOOT_ACCOUNT_BROKER_URL") | .value' <<<"$1"; }
reviews() { yq -N 'select(.kind == "ClusterRole" and .metadata.name == "km-kubemoot-operator-liaison") | .rules[] | select(.resources[0] == "tokenreviews") | .verbs[0]' <<<"$1"; }

# Off by default: memory, no accounts, no broker, nothing handed to the operator.
out="$(render)"
[ "$(env_of "$out" LIAISON_STATE_BACKEND)" = "memory" ] || fail "the default backend is not memory"
[ -z "$(env_of "$out" LIAISON_ACCOUNTS_ENABLED)" ] || fail "the default render turns accounts on"
[ -z "$(has_service "$out")" ] || fail "the default render has a broker Service"
[ -z "$(operator_env "$out")" ] || fail "the default render gives the operator a broker address"
[ -z "$(reviews "$out")" ] || fail "the default render may review tokens"
[ -z "$(yq -N 'select(.kind == "NetworkPolicy" and .metadata.name == "km-kubemoot-operator-liaison-broker") | .metadata.name' <<<"$out")" ] || fail "the default render has a broker NetworkPolicy"

# The door on: its single-use memory goes to NATS, and no account exists.
out="$(render --set-json "$(withdoor)")"
[ "$(env_of "$out" LIAISON_STATE_BACKEND)" = "nats" ] || fail "the door's backend is '$(env_of "$out" LIAISON_STATE_BACKEND)', want nats"
[ "$(env_of "$out" NATS_URL)" = "nats://nats.nats.svc.cluster.local:4222" ] || fail "the NATS address did not reach the liaison"
[ -z "$(env_of "$out" LIAISON_ACCOUNTS_ENABLED)" ] || fail "the door alone turns accounts on"
[ "$(render --set-json "$(withdoor '{"liaison":{"state":{"backend":"memory"}}}')" | yq -N 'select(.kind == "Deployment" and .metadata.name == "km-kubemoot-operator-liaison") | .spec.template.spec.containers[0].env[] | select(.name == "LIAISON_STATE_BACKEND") | .value')" = "memory" ] || fail "an explicit memory backend was ignored"

# Accounts on: the env, the broker Service, the mounts, the review right and the operator's address.
out="$(render --set-json "$(withdoor "$acct")")"
[ "$(env_of "$out" LIAISON_ACCOUNTS_ENABLED)" = "true" ] || fail "accounts did not turn on"
[ "$(env_of "$out" LIAISON_ACCOUNTS_SEAL_KEYS_FILE)" = "/etc/kubemoot/accounts/vault-keys" ] || fail "the vault keys are not mounted"
json="$(env_of "$out" LIAISON_ACCOUNTS)"
[ "$(yq -p=json '.library.clientId' <<<"$json")" = "cid" ] || fail "the client id is missing from the registrations"
[ "$(yq -p=json '.library.clientSecretFile' <<<"$json")" = "/etc/kubemoot/accounts/library-client-secret" ] || fail "the client secret is not a file path"
[ "$(yq -p=json '.library.maxLifetimeHours' <<<"$json")" = "48" ] || fail "the lifetime is missing"
[ "$(yq -p=json '.library.hosts[0]' <<<"$json")" = "auth.example.org" ] || fail "the pinned host is missing"
[ "$(env_of "$out" ACCESS_HASH_KEY)" = "set" ] || fail "the hash key is not given to the liaison"
[ -n "$(has_service "$out")" ] || fail "no broker Service"
[ "$(reviews "$out")" = "create" ] || fail "the liaison cannot review a gateway's token"
[ "$(operator_env "$out")" = "https://km-kubemoot-operator-liaison-broker.km.svc:8081" ] || fail "the operator's broker address is '$(operator_env "$out")'"
[ "$(liaison "$out" | yq -N '.spec.template.spec.volumes[] | select(.name == "accounts") | .projected.sources | length')" = "2" ] || fail "the accounts volume lacks the keys or the client secret"
[ "$(liaison "$out" | yq -N '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "accounts") | .readOnly')" = "true" ] || fail "the accounts mount is writable"
if grep -q 'lib-secret-value' <<<"$out"; then fail "a secret value is in the render"; fi

# Only the crews' gateways reach the broker port, and the liaison may read ServiceAccounts.
policy() { yq -N 'select(.kind == "NetworkPolicy" and .metadata.name == "km-kubemoot-operator-liaison-broker")' <<<"$1"; }
[ -n "$(policy "$out")" ] || fail "no NetworkPolicy for the broker"
[ "$(policy "$out" | yq -N '.spec.ingress[1].from[0].podSelector.matchLabels."app.kubernetes.io/component"')" = "discussion-gateway" ] || fail "the broker port is not limited to discussion gateways"
[ "$(policy "$out" | yq -N '.spec.ingress[1].ports[0].port')" = "broker" ] || fail "the gateway rule is not for the broker port"
[ "$(policy "$out" | yq -N '.spec.ingress[0].ports[0].port')" = "http" ] || fail "the route's port was closed"
[ "$(yq -N 'select(.kind == "ClusterRole" and .metadata.name == "km-kubemoot-operator-liaison") | .rules[] | select(.resources[0] == "serviceaccounts") | .verbs[0]' <<<"$out")" = "get" ] || fail "the liaison cannot read ServiceAccounts"

# The broker is never behind the public route.
if yq -N 'select(.kind == "HTTPRoute") | .spec.rules[].backendRefs[].name' <<<"$out" | grep -q broker; then fail "the broker is routed publicly"; fi

# OpenBao: the address, mount, token file and the Secret volume.
out="$(render --set-json "$(withdoor '{"liaison":{"state":{"backend":"openbao","openbao":{"address":"https://bao.example.org","tokenSecret":{"name":"bao-token"}}}}}')")"
[ "$(env_of "$out" LIAISON_OPENBAO_ADDR)" = "https://bao.example.org" ] || fail "the OpenBao address is missing"
[ "$(env_of "$out" LIAISON_OPENBAO_TOKEN_FILE)" = "/etc/kubemoot/openbao/token" ] || fail "the OpenBao token file is missing"
[ -n "$(liaison "$out" | yq -N '.spec.template.spec.volumes[] | select(.name == "openbao") | .name')" ] || fail "the OpenBao volume is missing"

# Refusals.
refuses() {
  if render --set-json "$2" >/dev/null 2>&1; then fail "$1 rendered"; fi
}
refuses "accounts without the door" "$acct"
refuses "accounts without seal keys" "$(withdoor "$(merge "$acct" '{"liaison":{"connectedAccounts":{"sealKeySecret":{"name":""}}}}')")"
refuses "accounts without the hash key" "$(withdoor "$(merge "$acct" '{"liaison":{"auth":{"hashKeySecret":{"name":""}}}}')")"
refuses "accounts on two replicas in memory" "$(withdoor "$(merge "$acct" '{"liaison":{"replicas":2,"state":{"backend":"memory"}}}')")"
refuses "an account without a client id" "$(withdoor "$(merge "$acct" '{"liaison":{"connectedAccounts":{"accounts":{"library":{"clientId":""}}}}}')")"
refuses "an account with neither allowedCrews nor hosts" "$(withdoor "$(merge "$acct" '{"liaison":{"connectedAccounts":{"accounts":{"library":{"allowedCrews":null,"hosts":null,"clientSecret":null}}}}}')")"
refuses "a client secret without hosts" "$(withdoor "$(merge "$acct" '{"liaison":{"connectedAccounts":{"accounts":{"library":{"hosts":null}}}}}')")"
refuses "a bare crew name" "$(withdoor "$(merge "$acct" '{"liaison":{"connectedAccounts":{"accounts":{"library":{"allowedCrews":["guide"]}}}}}')")"
render --set-json "$(withdoor "$(merge "$acct" '{"liaison":{"connectedAccounts":{"accounts":{"library":{"allowedCrews":null,"clientSecret":null}}}}}')")" >/dev/null || fail "hosts alone, with no secret, was refused"
refuses "an unknown backend" "$(withdoor '{"liaison":{"state":{"backend":"etcd"}}}')"
refuses "OpenBao without an address" "$(withdoor '{"liaison":{"state":{"backend":"openbao"}}}')"
refuses "NATS without NATS" "$(withdoor '{"nats":{"enabled":false},"liaison":{"state":{"backend":"nats"}}}')"
# Two replicas are fine once the state is shared.
render --set-json "$(withdoor "$(merge "$acct" '{"liaison":{"replicas":2}}')")" >/dev/null || fail "two replicas on NATS were refused"
echo "ok: liaison state and connected accounts render tests"
