#!/usr/bin/env bash
# Render tests for the liaison's per-person sign-in settings (templates/crew-liaison.yaml): all
# off by default, the service token list and the sign-in front each render only when set. Needs
# helm and yq. Run from anywhere: operator/chart/test-liaison-signin.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/kubemoot-operator"
helm dependency build "$chart" >/dev/null

fail() { echo "FAIL: $*" >&2; exit 1; }
render() { helm template kubemoot-operator "$chart" --namespace km "$@"; }
# liaison_env <rendered>: the liaison container's environment as "name=value" lines.
liaison_env() {
  yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator-liaison") | .spec.template.spec.containers[0].env[] | .name + "=" + (.value // "(from secret)")' <<<"$1"
}
liaison_deploy() {
  yq -N 'select(.kind == "Deployment" and .metadata.name == "kubemoot-operator-liaison")' <<<"$1"
}

# Defaults: no service token, no sign-in, no volume, no extra route.
out="$(render)"
env="$(liaison_env "$out")"
for name in ACCESS_ENFORCE ACCESS_SERVICE_TOKENS ACCESS_HASH_KEY LIAISON_SIGNIN_UPSTREAM LIAISON_SIGNIN_SEAL_KEYS_FILE; do
  grep -q "^$name=" <<<"$env" && fail "default render sets $name"
done
[ "$(liaison_deploy "$out" | yq '.spec.template.spec.volumes | length')" = "0" ] || fail "default render adds a volume"
[ "$(liaison_deploy "$out" | yq '.spec.template.spec.containers[0].volumeMounts | length')" = "0" ] || fail "default render adds a mount"

# Service tokens: the list is rendered as JSON the gateway parses, the hash key comes from a Secret.
out="$(render --set-json 'liaison.auth.serviceTokens=[{"name":"oreilly","verifyURL":"https://api.example.com/mcp","subjectClaim":"upstream_claims.sub","passToTools":true}]' \
  --set liaison.auth.hashKeySecret.name=hash)"
env="$(liaison_env "$out")"
grep -q '^ACCESS_ENFORCE=true$' <<<"$env" || fail "service tokens do not turn enforcement on"
[ "$(grep -c '^ACCESS_ENFORCE=' <<<"$env")" = 1 ] || fail "ACCESS_ENFORCE is rendered more than once"
grep -q '^ACCESS_TOKEN_REVIEW=' <<<"$env" && fail "service tokens alone turn Kubernetes token review on"
json="$(grep '^ACCESS_SERVICE_TOKENS=' <<<"$env" | sed 's/^ACCESS_SERVICE_TOKENS=//')"
[ "$(yq -p=json '.[0].name' <<<"$json")" = "oreilly" ] || fail "service token list is '$json'"
[ "$(yq -p=json '.[0].passToTools' <<<"$json")" = "true" ] || fail "passToTools lost in '$json'"
ref="$(liaison_deploy "$out" | yq '.spec.template.spec.containers[0].env[] | select(.name == "ACCESS_HASH_KEY") | .valueFrom.secretKeyRef.name + "/" + .valueFrom.secretKeyRef.key')"
[ "$ref" = "hash/hash-key" ] || fail "hash key reference is '$ref'"

# With Kubernetes tokens too, enforcement is still rendered once.
out="$(render --set liaison.auth.kubernetesTokens=true \
  --set-json 'liaison.auth.serviceTokens=[{"name":"oreilly","verifyURL":"https://api.example.com/mcp","subjectClaim":"sub"}]' \
  --set liaison.auth.hashKeySecret.name=hash)"
env="$(liaison_env "$out")"
[ "$(grep -c '^ACCESS_ENFORCE=' <<<"$env")" = 1 ] || fail "ACCESS_ENFORCE twice with both"
grep -q '^ACCESS_TOKEN_REVIEW=true$' <<<"$env" || fail "token review lost with both"

# Service tokens without a hash key Secret refuse to render.
if render --set-json 'liaison.auth.serviceTokens=[{"name":"oreilly","verifyURL":"https://api.example.com/mcp","subjectClaim":"sub"}]' >/dev/null 2>&1; then
  fail "service tokens rendered with no hash key Secret"
fi

# Sign-in: the environment, the key file mounted from the Secret, the routes.
signin=(--set liaison.signIn.enabled=true --set liaison.signIn.publicURL=https://oguide.example.com
  --set liaison.signIn.upstream.authorizeURL=https://up.example.com/authorize
  --set liaison.signIn.upstream.tokenURL=https://up.example.com/token
  --set liaison.signIn.upstream.clientId=front --set liaison.signIn.sealKeySecret.name=seal
  --set 'liaison.signIn.allowedRedirectHosts={chat.example.com,app.example.com}'
  --set liaison.expose.enabled=true --set gateway.enabled=true --set gateway.name=gw --set gateway.namespace=gw-ns --set gateway.hostname=oguide.example.com)
out="$(render "${signin[@]}")"
env="$(liaison_env "$out")"
for want in LIAISON_SIGNIN_PUBLIC_URL=https://oguide.example.com LIAISON_SIGNIN_UPSTREAM=https://up.example.com/authorize \
  LIAISON_SIGNIN_UPSTREAM_TOKEN_URL=https://up.example.com/token LIAISON_SIGNIN_UPSTREAM_CLIENT_ID=front \
  LIAISON_SIGNIN_UPSTREAM_SCOPES="openid offline_access" LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS=chat.example.com,app.example.com \
  LIAISON_SIGNIN_SEAL_KEYS_FILE=/etc/kubemoot/signin/seal-keys; do
  grep -qxF "$want" <<<"$env" || fail "sign-in env lacks $want"
done
grep -q '^LIAISON_SIGNIN_SEAL_KEYS=' <<<"$env" && fail "the sealing keys are inlined into the environment"
d="$(liaison_deploy "$out")"
[ "$(yq '.spec.template.spec.volumes[0].secret.secretName' <<<"$d")" = "seal" ] || fail "the seal key Secret is not mounted"
[ "$(yq '.spec.template.spec.volumes[0].secret.items[0].key' <<<"$d")" = "seal-keys" ] || fail "the seal key's default key"
[ "$(yq '.spec.template.spec.containers[0].volumeMounts[0].mountPath' <<<"$d")" = "/etc/kubemoot/signin" ] || fail "mount path"
[ "$(yq '.spec.template.spec.containers[0].volumeMounts[0].readOnly' <<<"$d")" = "true" ] || fail "the key mount is writable"
paths="$(yq -N 'select(.kind == "HTTPRoute" and .metadata.name == "kubemoot-operator-liaison") | .spec.rules[].matches[].path.value' <<<"$out" | sort | tr '\n' ' ')"
[ "$paths" = "/.well-known /mcp /oauth " ] || fail "route paths are '$paths'"
# The default route stays /mcp only when sign-in is off.
out="$(render --set liaison.expose.enabled=true --set gateway.enabled=true --set gateway.name=gw --set gateway.namespace=gw-ns --set gateway.hostname=h.example.com)"
paths="$(yq -N 'select(.kind == "HTTPRoute" and .metadata.name == "kubemoot-operator-liaison") | .spec.rules[].matches[].path.value' <<<"$out" | tr '\n' ' ')"
[ "$paths" = "/mcp " ] || fail "route without sign-in is '$paths'"

# Sign-in without its required settings refuses to render.
for missing in publicURL upstream.authorizeURL upstream.tokenURL upstream.clientId sealKeySecret.name; do
  args=(); for a in "${signin[@]}"; do args+=("$a"); done
  if render "${args[@]}" --set "liaison.signIn.$missing=" >/dev/null 2>&1; then
    fail "sign-in rendered with liaison.signIn.$missing empty"
  fi
done
echo "ok: liaison sign-in render tests"
