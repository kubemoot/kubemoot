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
# The one volume and mount a default render has is the install CA (test-install-ca.sh); sign-in adds none.
[ "$(liaison_deploy "$out" | yq '.spec.template.spec.volumes | map(.name) | join(",")')" = "install-ca" ] || fail "default render adds a volume"
[ "$(liaison_deploy "$out" | yq '.spec.template.spec.containers[0].volumeMounts | map(.name) | join(",")')" = "install-ca" ] || fail "default render adds a mount"

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
[ "$(yq '.spec.template.spec.volumes[] | select(.name == "signin-seal-keys").secret.secretName' <<<"$d")" = "seal" ] || fail "the seal key Secret is not mounted"
[ "$(yq '.spec.template.spec.volumes[] | select(.name == "signin-seal-keys").secret.items[0].key' <<<"$d")" = "seal-keys" ] || fail "the seal key's default key"
[ "$(yq '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "signin-seal-keys").mountPath' <<<"$d")" = "/etc/kubemoot/signin" ] || fail "mount path"
[ "$(yq '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "signin-seal-keys").readOnly' <<<"$d")" = "true" ] || fail "the key mount is writable"
paths="$(yq -N 'select(.kind == "HTTPRoute" and .metadata.name == "kubemoot-operator-liaison") | .spec.rules[].matches[].path.value' <<<"$out" | sort | tr '\n' ' ')"
[ "$paths" = "/.well-known /mcp /oauth " ] || fail "route paths are '$paths'"
# The default route stays /mcp only when sign-in is off.
out="$(render --set liaison.expose.enabled=true --set gateway.enabled=true --set gateway.name=gw --set gateway.namespace=gw-ns --set gateway.hostname=h.example.com)"
paths="$(yq -N 'select(.kind == "HTTPRoute" and .metadata.name == "kubemoot-operator-liaison") | .spec.rules[].matches[].path.value' <<<"$out" | tr '\n' ' ')"
[ "$paths" = "/mcp " ] || fail "route without sign-in is '$paths'"

# The door's name: neutral by default, the install's own when set.
out="$(render)"
grep -qxF 'LIAISON_DISPLAY_NAME=Kubemoot' <<<"$(liaison_env "$out")" || fail "default display name is not Kubemoot"
out="$(render --set 'liaison.displayName=Example Community')"
grep -qxF 'LIAISON_DISPLAY_NAME=Example Community' <<<"$(liaison_env "$out")" || fail "display name not rendered"

# The public URL must be https:// plus the door hostname: the exposed hostname wins over the
# gateway's, a trailing slash is fine, any other host or scheme refuses to render.
host=(--set liaison.expose.enabled=true --set gateway.enabled=true --set gateway.name=gw --set gateway.namespace=gw-ns --set gateway.hostname=gw.example.com)
base=(--set liaison.signIn.enabled=true --set liaison.signIn.upstream.authorizeURL=https://up.example.com/authorize
  --set liaison.signIn.upstream.tokenURL=https://up.example.com/token --set liaison.signIn.upstream.clientId=front
  --set liaison.signIn.sealKeySecret.name=seal)
render "${base[@]}" "${host[@]}" --set liaison.signIn.publicURL=https://gw.example.com/ >/dev/null 2>&1 || fail "trailing slash refused"
out="$(render "${base[@]}" "${host[@]}" --set liaison.expose.hostname=door.example.com --set liaison.signIn.publicURL=https://door.example.com)" \
  || fail "expose.hostname matching publicURL refused"
[ "$(yq -N 'select(.kind == "HTTPRoute" and .metadata.name == "kubemoot-operator-liaison") | .spec.hostnames[0]' <<<"$out")" = "door.example.com" ] || fail "route host is not expose.hostname"
for bad in https://other.example.com http://gw.example.com https://gw.example.com:8443 https://gw.example.com/mcp; do
  if render "${base[@]}" "${host[@]}" --set "liaison.signIn.publicURL=$bad" >/dev/null 2>&1; then
    fail "publicURL $bad rendered against host gw.example.com"
  fi
done
if render "${base[@]}" "${host[@]}" --set liaison.expose.hostname=door.example.com --set liaison.signIn.publicURL=https://gw.example.com >/dev/null 2>&1; then
  fail "publicURL matching the gateway host rendered though expose.hostname differs"
fi

# The route serves the liaison and nothing else: every backend is the liaison Service, and the
# paths are exactly /mcp, /oauth and /.well-known with sign-in on (checked above), /mcp alone off.
out="$(render "${base[@]}" "${host[@]}" --set liaison.signIn.publicURL=https://gw.example.com)"
backends="$(yq -N 'select(.kind == "HTTPRoute" and .metadata.name == "kubemoot-operator-liaison") | .spec.rules[].backendRefs[].name' <<<"$out" | sort -u | tr '\n' ' ')"
[ "$backends" = "kubemoot-operator-liaison " ] || fail "route backends are '$backends'"

# Sign-in without its required settings refuses to render.
for missing in publicURL upstream.authorizeURL upstream.tokenURL upstream.clientId sealKeySecret.name; do
  args=(); for a in "${signin[@]}"; do args+=("$a"); done
  if render "${args[@]}" --set "liaison.signIn.$missing=" >/dev/null 2>&1; then
    fail "sign-in rendered with liaison.signIn.$missing empty"
  fi
done
echo "ok: liaison sign-in render tests"

# The door's own identity: off unless chosen, never beside the upstream front, its secrets mounted
# read-only from Secrets (never inlined), and a redirect allow-list required once it is exposed.
door=(--set liaison.signIn.enabled=true --set liaison.signIn.publicURL=https://door.example.com
  --set liaison.signIn.sealKeySecret.name=seal --set liaison.signIn.identity.provider=oidc
  --set liaison.signIn.identity.oidc.issuer=https://idp.example.com --set liaison.signIn.identity.oidc.clientId=door
  --set liaison.signIn.identity.oidc.clientSecret.name=idp --set liaison.signIn.identity.signingKeySecret.name=keys
  --set liaison.expose.hostname=door.example.com --set 'liaison.signIn.allowedRedirectHosts={chat.example.com}')
out="$(render)"
grep -q '^LIAISON_SIGNIN_IDENTITY=' <<<"$(liaison_env "$out")" && fail "default render turns the door's identity on"
out="$(render "${door[@]}")"
env="$(liaison_env "$out")"
for want in LIAISON_SIGNIN_IDENTITY=oidc LIAISON_SIGNIN_OIDC_ISSUER=https://idp.example.com LIAISON_SIGNIN_OIDC_CLIENT_ID=door \
  LIAISON_SIGNIN_OIDC_CLIENT_SECRET_FILE=/etc/kubemoot/signin-identity/oidc-client-secret \
  LIAISON_SIGNIN_SIGNING_KEYS_FILE=/etc/kubemoot/signin-identity/signing-keys LIAISON_SIGNIN_OIDC_USER_PREFIX=oidc: \
  LIAISON_SIGNIN_REFRESH_MAX_HOURS=168 LIAISON_SIGNIN_ALLOWED_REDIRECT_HOSTS=chat.example.com ACCESS_ENFORCE=true \
  ACCESS_ALLOW_ANONYMOUS=false; do
  grep -qxF "$want" <<<"$env" || fail "door env lacks $want"
done
for absent in LIAISON_SIGNIN_UPSTREAM LIAISON_SIGNIN_UPSTREAM_CLIENT_ID LIAISON_SIGNIN_HOP_KEYS_FILE; do
  grep -q "^$absent=" <<<"$env" && fail "door render sets $absent"
done
grep -q '^LIAISON_SIGNIN_OIDC_CLIENT_SECRET=' <<<"$env" && fail "the client secret is in the environment"
d="$(liaison_deploy "$out")"
[ "$(yq '.spec.template.spec.volumes[] | select(.name == "signin-identity") | .projected.sources | length' <<<"$d")" = 2 ] || fail "door secrets are not two sources without a hop key"
[ "$(yq '.spec.template.spec.volumes[] | select(.name == "signin-identity") | .projected.sources[0].secret.name' <<<"$d")" = "idp" ] || fail "client secret source"
[ "$(yq '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "signin-identity").readOnly' <<<"$d")" = "true" ] || fail "door secrets are writable"
out="$(render "${door[@]}" --set liaison.signIn.identity.hopKeySecret.name=hop)"
grep -qxF 'LIAISON_SIGNIN_HOP_KEYS_FILE=/etc/kubemoot/signin-identity/hop-keys' <<<"$(liaison_env "$out")" || fail "hop key file not set with a hop Secret"
[ "$(liaison_deploy "$out" | yq '.spec.template.spec.volumes[] | select(.name == "signin-identity") | .projected.sources | length')" = 3 ] || fail "hop key source missing"
# The door's identity beside the upstream front, an unknown provider, or missing pieces: no render.
for bad in "liaison.signIn.upstream.authorizeURL=https://up.example.com/authorize" "liaison.signIn.identity.provider=ldap" \
  liaison.signIn.identity.oidc.issuer= liaison.signIn.identity.oidc.clientId= liaison.signIn.identity.oidc.clientSecret.name= \
  liaison.signIn.identity.signingKeySecret.name= liaison.signIn.publicURL= liaison.signIn.sealKeySecret.name=; do
  if render "${door[@]}" --set "$bad" >/dev/null 2>&1; then fail "door rendered with $bad"; fi
done
# A provider with sign-in switched off would silently render no door.
if render "${door[@]}" --set liaison.signIn.enabled=false >/dev/null 2>&1; then fail "identity.provider rendered with signIn.enabled off"; fi
# The door's identity accepts no service's own token.
if render "${door[@]}" --set-json 'liaison.auth.serviceTokens=[{"name":"svc","verifyURL":"https://api.example.com/mcp","subjectClaim":"sub"}]' --set liaison.auth.hashKeySecret.name=hash >/dev/null 2>&1; then
  fail "the door's identity rendered beside service tokens"
fi
# Exposed with no redirect allow-list, anyone could ask a person to allow their app.
if render "${door[@]}" "${host[@]}" --set liaison.expose.enabled=true --set 'liaison.signIn.allowedRedirectHosts=null' --set liaison.signIn.publicURL=https://door.example.com >/dev/null 2>&1; then
  fail "an exposed door rendered with no redirect hosts"
fi
render "${door[@]}" "${host[@]}" --set liaison.expose.enabled=true --set liaison.signIn.publicURL=https://door.example.com >/dev/null 2>&1 || fail "an exposed door with redirect hosts did not render"
echo "ok: liaison door identity render tests"
