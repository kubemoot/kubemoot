#!/usr/bin/env bash
# Render tests for the account broker's TLS (templates/crew-liaison.yaml, templates/deployment.yaml):
# the broker serves TLS from a certificate the operator signs from the install CA, the gateways
# are given an https address, and the certificate and its mounts render only when connected
# accounts are on. Needs helm and yq. Run from anywhere: operator/chart/test-account-broker-tls.sh
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

liaison() { yq -N 'select(.kind == "Deployment" and .metadata.name == "km-kubemoot-operator-liaison")' <<<"$1"; }
liaison_env() { liaison "$1" | yq -N '.spec.template.spec.containers[0].env[] | select(.name == "'"$2"'") | .value // "set"'; }
operator_env() { yq -N 'select(.kind == "Deployment" and .metadata.name == "km-kubemoot-operator") | .spec.template.spec.containers[0].env[] | select(.name == "'"$2"'") | .value' <<<"$1"; }
volume() { liaison "$1" | yq -N '.spec.template.spec.volumes[] | select(.name == "broker-tls") | .secret.secretName'; }
mount() { liaison "$1" | yq -N '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "broker-tls") | .mountPath'; }

# Connected accounts and gateway TLS on (the default): https, the certificate Secret, its mount.
out="$(render --set-json "$on")"
[ "$(operator_env "$out" KUBEMOOT_ACCOUNT_BROKER_URL)" = "https://km-kubemoot-operator-liaison-broker.km.svc:8081" ] || fail "the gateways are not given the https broker address"
[ "$(operator_env "$out" ACCOUNT_BROKER_SERVICE_NAME)" = "km-kubemoot-operator-liaison-broker" ] || fail "the operator does not know the broker Service name"
[ "$(operator_env "$out" ACCOUNT_BROKER_TLS_SECRET)" = "km-kubemoot-operator-liaison-broker-tls" ] || fail "the operator does not know the certificate Secret"
[ "$(volume "$out")" = "km-kubemoot-operator-liaison-broker-tls" ] || fail "the liaison does not mount the certificate Secret"
[ "$(mount "$out")" = "/etc/kubemoot/broker-tls" ] || fail "the certificate mount"
[ "$(liaison_env "$out" LIAISON_BROKER_TLS_CERT_FILE)" = "/etc/kubemoot/broker-tls/tls.crt" ] || fail "the certificate file setting"
[ "$(liaison_env "$out" LIAISON_BROKER_TLS_KEY_FILE)" = "/etc/kubemoot/broker-tls/tls.key" ] || fail "the key file setting"
[ -z "$(liaison_env "$out" LIAISON_ALLOW_INSECURE_BROKER)" ] || fail "the broker accepts plain http with TLS on"
[ "$(yq -N 'select(.kind == "Service" and .metadata.name == "km-kubemoot-operator-liaison-broker") | .spec.ports[0].port' <<<"$out")" = 8081 ] || fail "the broker Service"
[ -n "$(yq -N 'select(.kind == "NetworkPolicy" and .metadata.name == "km-kubemoot-operator-liaison-broker") | .metadata.name' <<<"$out")" ] || fail "the broker NetworkPolicy is gone"
# The Secret holds a key: the chart makes none, the operator does, and no other Deployment mounts it.
[ -z "$(yq -N 'select(.kind == "Secret" and .metadata.name == "km-kubemoot-operator-liaison-broker-tls") | .kind' <<<"$out")" ] || fail "the chart makes the certificate Secret"

# Connected accounts off: nothing about the broker renders.
out="$(render)"
[ -z "$(volume "$out")" ] && [ -z "$(mount "$out")" ] || fail "the default render mounts a broker certificate"
[ -z "$(liaison_env "$out" LIAISON_BROKER_TLS_CERT_FILE)" ] || fail "the default render sets a broker certificate"
[ -z "$(operator_env "$out" ACCOUNT_BROKER_TLS_SECRET)" ] || fail "the operator is told of a broker that is not there"
[ -z "$(operator_env "$out" KUBEMOOT_ACCOUNT_BROKER_URL)" ] || fail "the operator is given a broker address with accounts off"
# The door on, accounts off: still nothing.
out="$(render --set-json "$door")"
[ -z "$(volume "$out")" ] || fail "the door alone mounts a broker certificate"

# Gateway TLS off: the broker is plain and says so, and the gateways are given an http address.
out="$(render --set-json "$on" --set discussionGateway.tls.enabled=false)"
[ "$(operator_env "$out" KUBEMOOT_ACCOUNT_BROKER_URL)" = "http://km-kubemoot-operator-liaison-broker.km.svc:8081" ] || fail "the plain broker address"
[ "$(liaison_env "$out" LIAISON_ALLOW_INSECURE_BROKER)" = "true" ] || fail "the plain broker is not opted into"
[ -z "$(volume "$out")" ] || fail "a certificate is mounted with TLS off"
[ -z "$(operator_env "$out" ACCOUNT_BROKER_TLS_SECRET)" ] || fail "the operator issues a certificate with TLS off"
echo "ok: account broker TLS render tests"
