#!/usr/bin/env bash
# Render tests for the NATS connection options (nats.tls, nats.auth): nothing changes by
# default; a CA bundle, a client certificate and each kind of credential reach the operator,
# the placement service and the stream bootstrap job; the operator is told which Secrets to
# hand on; contradictory values fail the render. Needs helm and yq.
# Run from anywhere: operator/chart/test-nats-connection.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template kubemoot-operator "$chart" --namespace kubemoot --set placementService.enabled=true "$@"; }
op=kubemoot-operator
env_of() { yq -N "select(.kind == \"$1\" and .metadata.name == \"$2\") | $3 | .[] | select(.name == \"$4\") | .value // .valueFrom.secretKeyRef.key" <<<"$5"; }
op_env() { env_of Deployment "$op" '.spec.template.spec.containers[0].env' "$1" "$2"; }

# Off by default: no NATS_* connection variables beyond the URL, no nats-conn volumes anywhere.
out="$(render)"
if grep -qE 'name: (nats-conn|NATS_CA_FILE|NATS_CREDS_FILE|NATS_TOKEN|NATS_USER|KUBEMOOT_NATS_)' <<<"$out"; then fail "connection options rendered by default"; fi

# CA bundle and client certificate.
out="$(render --set nats.url=tls://nats:4222 --set nats.tls.caSecret=nats-ca --set nats.tls.caKey=bundle.pem --set nats.tls.clientSecret=nats-client)"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "'$op'") | .spec.template.spec.containers[0].env[] | select(.name == "NATS_CA_FILE") | .value' <<<"$out")" = /etc/kubemoot/nats-conn/ca/ca.crt ] || fail "operator CA file"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "'$op'") | .spec.template.spec.containers[0].env[] | select(.name == "NATS_CLIENT_KEY_FILE") | .value' <<<"$out")" = /etc/kubemoot/nats-conn/client/tls.key ] || fail "operator client key file"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "'$op'") | .spec.template.spec.containers[0].env[] | select(.name == "KUBEMOOT_NATS_CA_KEY") | .value' <<<"$out")" = bundle.pem ] || fail "operator is not told the CA key"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "'$op'") | .spec.template.spec.volumes[] | select(.name == "nats-conn-ca") | .secret.items[0].key' <<<"$out")" = bundle.pem ] || fail "CA volume key"
ps=kubemoot-operator-placement-service
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "'$ps'") | .spec.template.spec.volumes[] | select(.name == "nats-conn-client") | .secret.secretName' <<<"$out")" = nats-client ] || fail "placement service client certificate volume"
[ "$(yq -N 'select(.kind == "Job") | .spec.template.spec.containers[0].env[] | select(.name == "NATS_CA_FILE") | .value' <<<"$out")" = /etc/kubemoot/nats-conn/ca/ca.crt ] || fail "bootstrap job CA file"
[ "$(yq -N 'select(.kind == "Job") | .spec.template.spec.volumes[] | select(.name == "nats-conn-ca") | .name' <<<"$out")" = nats-conn-ca ] || fail "bootstrap job CA volume"

# A creds file.
out="$(render --set nats.auth.secret=nats-auth --set nats.auth.credsKey=app.creds)"
[ "$(op_env NATS_CREDS_FILE "$out")" != "" ] || fail "operator creds file"
[ "$(op_env KUBEMOOT_NATS_AUTH_CREDS_KEY "$out")" = app.creds ] || fail "operator is not told the creds key"
[ "$(yq -N 'select(.kind == "Deployment" and .metadata.name == "'$op'") | .spec.template.spec.volumes[] | select(.name == "nats-conn-auth") | .secret.items[0].path' <<<"$out")" = nats.creds ] || fail "creds volume"

# User and password, from the Secret and never the manifest.
out="$(render --set nats.auth.secret=nats-auth --set nats.auth.userKey=u --set nats.auth.passwordKey=p)"
[ "$(op_env NATS_USER "$out")" = u ] || fail "operator NATS_USER"
[ "$(op_env NATS_PASSWORD "$out")" = p ] || fail "operator NATS_PASSWORD"

# A token.
out="$(render --set nats.auth.secret=nats-auth --set nats.auth.tokenKey=t)"
[ "$(op_env NATS_TOKEN "$out")" = t ] || fail "operator NATS_TOKEN"

# The placement service keeps its own credential Secret when it has one.
out="$(render --set nats.auth.secret=nats-auth --set nats.auth.userKey=u --set nats.auth.passwordKey=p --set placementService.nats.existingSecret=own)"
names="$(yq -N 'select(.kind == "Deployment" and .metadata.name == "'$ps'") | .spec.template.spec.containers[0].env[] | select(.name == "NATS_USER") | .valueFrom.secretKeyRef.name' <<<"$out")"
[ "$names" = own ] || fail "placement service lost its own credential Secret: $names"

# The accelerator MCP server asks for the options.
out="$(render --set acceleratorMcp.enabled=true)"
[ "$(yq -N 'select(.kind == "MCPServer" and .metadata.name == "accelerator-mcp") | .metadata.annotations["kubemoot.ai/nats-connection"]' <<<"$out")" = true ] || fail "accelerator-mcp does not ask for the NATS options"

# Contradictions fail the render.
for bad in "--set nats.auth.secret=a" "--set nats.auth.credsKey=c" "--set nats.auth.secret=a --set nats.auth.credsKey=c --set nats.auth.tokenKey=t" "--set nats.auth.secret=a --set nats.auth.userKey=u"; do
  # shellcheck disable=SC2086
  if render $bad >/dev/null 2>&1; then fail "rendered with: $bad"; fi
done
echo "PASS: nats connection options"
