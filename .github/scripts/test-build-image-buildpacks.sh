#!/usr/bin/env bash
# Tests for build-image-buildpacks.sh: the digest pins, the creator arguments, the build
# pod's Pod Security "restricted" fields, and a whole run against a fake kubectl that
# records its arguments and the context it receives on stdin.
# Usage: bash .github/scripts/test-build-image-buildpacks.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=build-image-buildpacks.sh
source "${here}/build-image-buildpacks.sh"

failures=0
check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   ${name}"
  else
    echo "FAIL ${name}: want [${want}] got [${got}]"
    failures=$((failures + 1))
  fi
}
# check_fails NAME CMD...: the command exits nonzero.
check_fails() {
  local name="$1"; shift
  if ( "$@" ) >/dev/null 2>&1; then check "$name" "fails" "succeeds"; else check "$name" "fails" "fails"; fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The checked-in pins: every entry carries a tag and a digest.
pins="${here}/../buildpacks/images.yaml"
for name in builder-java-tiny run-tiny; do
  ref="$(pinned_image "$pins" "$name")"
  [[ "$ref" =~ ^docker\.io/paketobuildpacks/[a-z-]+:[0-9.]+@sha256:[0-9a-f]{64}$ ]] \
    && got=pinned || got="$ref"
  check "images.yaml ${name} is pinned by tag and digest" pinned "$got"
done

cat > "${work}/images.yaml" <<'EOF'
apiVersion: v1
kind: List
items:
  - name: builder
    image: example.org/builder:1@sha256:aaaa
  - name: unpinned
    image: example.org/run:latest
EOF
check "pinned_image picks the named entry" "example.org/builder:1@sha256:aaaa" \
  "$(pinned_image "${work}/images.yaml" builder)"
check_fails "pinned_image refuses a tag without a digest" pinned_image "${work}/images.yaml" unpinned
check_fails "pinned_image refuses a missing entry" pinned_image "${work}/images.yaml" nothing

check_status() { if valid_env "$2"; then check "$1" "$3" valid; else check "$1" "$3" invalid; fi; }
check_status "env with an upper-case key" "BP_JVM_VERSION=25" valid
check_status "env with an empty value" "BP_EMPTY=" valid
check_status "env with = in the value" "BP_OCI_SOURCE=https://x/?a=b" valid
check_status "env without =" "BP_JVM_VERSION" invalid
check_status "env with a path in the key" "../etc/passwd=x" invalid
check_status "env with a lower-case key" "bp_jvm=25" invalid
check_status "env with a second line" $'A=1\n../x=y' invalid

cargs="$(creator_args reg.example run@sha256:r "" "" reg.example/a:1)"
check "creator args without cache or previous image" \
  '["-app=/workspace","-layers=/layers","-platform=/platform","-run-image=run@sha256:r","-insecure-registry=reg.example","-report=/layers/report.toml","reg.example/a:1"]' \
  "$cargs"
cargs="$(creator_args reg.example run@sha256:r reg.example/a-cache:main reg.example/a:latest reg.example/a:1 reg.example/a:latest)"
check "the image is the last creator argument" "reg.example/a:1" "$(jq -r '.[-1]' <<<"$cargs")"
check "extra images become -tag" '["-tag=reg.example/a:latest"]' "$(jq -c '[.[] | select(startswith("-tag="))]' <<<"$cargs")"
check "cache image passed" 1 "$(jq '[.[] | select(. == "-cache-image=reg.example/a-cache:main")] | length' <<<"$cargs")"
check "previous image passed" 1 "$(jq '[.[] | select(. == "-previous-image=reg.example/a:latest")] | length' <<<"$cargs")"

spec="$(pod_overrides pod1 builder@sha256:b "$cargs" $'BP_JVM_VERSION=25\n' reg.example 10.0.0.1 push-secret)"
check "pod runs as non-root" true "$(jq '.spec.securityContext.runAsNonRoot and .spec.containers[0].securityContext.runAsNonRoot' <<<"$spec")"
check "pod uid is the CNB user" 1001 "$(jq '.spec.securityContext.runAsUser' <<<"$spec")"
check "no privilege escalation" false "$(jq '.spec.containers[0].securityContext.allowPrivilegeEscalation' <<<"$spec")"
check "every capability dropped" '["ALL"]' "$(jq -c '.spec.containers[0].securityContext.capabilities.drop' <<<"$spec")"
check "no capability added" null "$(jq -c '.spec.containers[0].securityContext.capabilities.add' <<<"$spec")"
check "seccomp RuntimeDefault" RuntimeDefault "$(jq -r '.spec.securityContext.seccompProfile.type' <<<"$spec")"
check "not privileged" null "$(jq '.spec.containers[0].securityContext.privileged' <<<"$spec")"
check "no host path volume" 0 "$(jq '[.spec.volumes[] | select(.hostPath)] | length' <<<"$spec")"
check "no service account token" false "$(jq '.spec.automountServiceAccountToken' <<<"$spec")"
check "builder image" builder@sha256:b "$(jq -r '.spec.containers[0].image' <<<"$spec")"
check "host alias to Harbor" "10.0.0.1 reg.example" "$(jq -r '.spec.hostAliases[0] | "\(.ip) \(.hostnames[0])"' <<<"$spec")"
check "push secret mounted" push-secret "$(jq -r '.spec.volumes[] | select(.name == "docker-config") | .secret.secretName' <<<"$spec")"
check "platform API" 0.15 "$(jq -r '.spec.containers[0].env[] | select(.name == "CNB_PLATFORM_API") | .value' <<<"$spec")"
check "creator args are the container args" "$cargs" "$(jq -c '.spec.containers[0].args' <<<"$spec")"

# The in-pod script: the context lands in the workspace, the variables become
# /platform/env files, and the creator gets the arguments. Run it with the pod paths
# mapped into a scratch directory.
root="${work}/pod"
mkdir -p "${root}/workspace" "${root}/platform" "${root}/cnb/lifecycle" "${work}/ctx"
echo hello > "${work}/ctx/app.txt"
cat > "${root}/cnb/lifecycle/creator" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" > "${root}/creator.args"
EOF
chmod +x "${root}/cnb/lifecycle/creator"
pod_script="${POD_SCRIPT//\/workspace/${root}/workspace}"
pod_script="${pod_script//\/platform/${root}/platform}"
pod_script="${pod_script//\/cnb/${root}/cnb}"
tar -C "${work}/ctx" -czf - . | BUILD_ENV=$'BP_JVM_VERSION=25\nBP_OCI_SOURCE=https://x/?a=b\n' \
  bash -c "$pod_script" pod1 -flag img:1
check "context unpacked into the workspace" hello "$(cat "${root}/workspace/app.txt")"
check "a variable written as a platform env file" 25 "$(cat "${root}/platform/env/BP_JVM_VERSION")"
check "a value with = kept whole" "https://x/?a=b" "$(cat "${root}/platform/env/BP_OCI_SOURCE")"
check "creator called with the arguments" "-flag img:1" "$(paste -sd' ' "${root}/creator.args")"

# The tag and cache plan per ref.
sha=0123456789abcdef0123456789abcdef01234567
check "main pushes :<sha> and :latest, reuses :latest, caches as main" \
  "r/a:${sha} r/a:latest cache r/a-buildcache:main previous r/a:latest" \
  "$(image_plan r/a "$sha" refs/heads/main | paste -sd' ')"
check "a branch pushes only :branch-<sha> with its own cache" \
  "r/a:branch-${sha} cache r/a-buildcache:branch-agent-buildpacks-indexer" \
  "$(image_plan r/a "$sha" refs/heads/agent/buildpacks-indexer | paste -sd' ')"
check "a branch never plans :latest or :<sha>" 0 \
  "$(image_plan r/a "$sha" refs/heads/feature | grep -cE "^r/a:(latest|${sha})$" || true)"
long="refs/heads/$(printf 'x%.0s' {1..300})"
check "a long branch name is cut to a valid tag" 127 \
  "$(image_plan r/a "$sha" "$long" | sed -n 's/^cache r\/a-buildcache://p' | tr -d '\n' | wc -c)"
check_fails "image_plan refuses a short SHA" image_plan r/a 0123abcd refs/heads/main
check_fails "image_plan refuses an empty SHA" image_plan r/a "" refs/heads/main

# A whole run against a fake kubectl.
cat > "${work}/kubectl" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" > "${work}/kubectl.args"
cat > "${work}/kubectl.stdin"
EOF
chmod +x "${work}/kubectl"
run_main() { REGISTRY=reg.example REGISTRY_HOST_IP=10.0.0.1 KUBECTL="${work}/kubectl" NAMESPACE=ns1 main "$@"; }
run_main --pod bp-1 --sha "$sha" --ref refs/heads/main --env BP_JVM_VERSION=25 \
  "${work}/ctx" reg.example/a >/dev/null
check "kubectl runs the pod" "run bp-1" "$(sed -n '1,2p' "${work}/kubectl.args" | paste -sd' ')"
check "kubectl in the namespace" 1 "$(grep -cx -- '--namespace=ns1' "${work}/kubectl.args")"
check "kubectl gets the pinned builder" 1 "$(grep -c -- '--image=docker.io/paketobuildpacks/builder-noble-java-tiny:.*@sha256:' "${work}/kubectl.args")"
check "the context arrives on stdin" "./app.txt" "$(tar -tzf "${work}/kubectl.stdin" | grep app.txt)"
overrides="$(sed -n 's/^--overrides=//p' "${work}/kubectl.args")"
cargs="$(jq -c '.spec.containers[0].args' <<<"$overrides")"
check "the run image is pinned" 1 "$(jq '[.[] | select(test("^-run-image=docker.io/paketobuildpacks/ubuntu-noble-run-tiny:.*@sha256:"))] | length' <<<"$cargs")"
check "main builds :<sha>" "reg.example/a:${sha}" "$(jq -r '.[-1]' <<<"$cargs")"
check "main also tags :latest" '["-tag=reg.example/a:latest"]' "$(jq -c '[.[] | select(startswith("-tag="))]' <<<"$cargs")"
check "main caches as main" 1 "$(jq '[.[] | select(. == "-cache-image=reg.example/a-buildcache:main")] | length' <<<"$cargs")"
check "build env reaches the pod" BP_JVM_VERSION=25 "$(jq -r '.spec.containers[0].env[] | select(.name == "BUILD_ENV") | .value' <<<"$overrides")"

run_main --pod bp-2 --sha "$sha" --ref refs/heads/agent/x "${work}/ctx" reg.example/a >/dev/null
cargs="$(sed -n 's/^--overrides=//p' "${work}/kubectl.args" | jq -c '.spec.containers[0].args')"
check "a branch builds only :branch-<sha>" "reg.example/a:branch-${sha}" \
  "$(jq -r '[.[] | select(startswith("reg.example/a:") or startswith("-tag="))] | join(" ")' <<<"$cargs")"
check "a branch reuses no previous image" 0 "$(jq '[.[] | select(startswith("-previous-image="))] | length' <<<"$cargs")"

ok=(--pod p --sha "$sha" --ref refs/heads/main)
check_fails "main without --pod" run_main --sha "$sha" --ref refs/heads/main "${work}/ctx" reg.example/a
check_fails "main without --ref" run_main --pod p --sha "$sha" "${work}/ctx" reg.example/a
check_fails "main without --sha" run_main --pod p --ref refs/heads/main "${work}/ctx" reg.example/a
check_fails "main without a repository" run_main "${ok[@]}" "${work}/ctx"
check_fails "main with an extra argument" run_main "${ok[@]}" "${work}/ctx" reg.example/a reg.example/b
check_fails "main with a missing context" run_main "${ok[@]}" "${work}/nowhere" reg.example/a
check_fails "main with a bad --env" run_main "${ok[@]}" --env bad "${work}/ctx" reg.example/a
check_fails "main with a multi-line --env" run_main "${ok[@]}" --env $'A=1\n../x=y' "${work}/ctx" reg.example/a
check_fails "main with an unknown builder" run_main "${ok[@]}" --builder nothing "${work}/ctx" reg.example/a
check_fails "main with an unknown option" run_main "${ok[@]}" --nope "${work}/ctx" reg.example/a
check_fails "main without REGISTRY" env -u REGISTRY bash "${here}/build-image-buildpacks.sh" "${ok[@]}" "${work}/ctx" reg.example/a

if [ "$failures" -gt 0 ]; then
  echo "${failures} test(s) failed"
  exit 1
fi
echo "all build-image-buildpacks tests passed"
