#!/usr/bin/env bash
# Tests for build-image-buildah.sh and the shared build-image-lib.sh parts it uses: the
# digest pin, the Buildah arguments, the tags per ref, the build pod's user namespace and
# Pod Security "baseline" fields, the in-pod script against a fake buildah, and a whole
# run against a fake kubectl that records its arguments and the context on stdin.
# Usage: bash .github/scripts/test-build-image-buildah.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=build-image-buildah.sh
source "${here}/build-image-buildah.sh"

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
# check_valid NAME FUNCTION VALUE WANT: FUNCTION accepts (valid) or refuses (invalid) VALUE.
check_valid() {
  if "$2" "$3"; then check "$1" "$4" valid; else check "$1" "$4" invalid; fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The checked-in pin carries a tag and a digest.
ref="$(pinned_image "${here}/../buildah/images.yaml" buildah)"
[[ "$ref" =~ ^quay\.io/buildah/stable:v[0-9.]+@sha256:[0-9a-f]{64}$ ]] && got=pinned || got="$ref"
check "buildah/images.yaml buildah is pinned by tag and digest" pinned "$got"
printf 'items:\n  - name: buildah\n    image: quay.io/buildah/stable:latest\n' > "${work}/unpinned.yaml"
check_fails "an unpinned Buildah image is refused" pinned_image "${work}/unpinned.yaml" buildah

check_valid "label with an OCI key" valid_label "org.opencontainers.image.source=https://x/?a=b" valid
check_valid "label with an empty value" valid_label "a=" valid
check_valid "label without =" valid_label "org.opencontainers.image.source" invalid
check_valid "label with a space in the key" valid_label "a b=1" invalid
check_valid "label with a second line" valid_label $'a=1\nb=2' invalid
check_valid "label starting with a dash" valid_label "-x=1" invalid

check_valid "tag with a version" valid_tag "2.338.0-homelab" valid
check_valid "empty tag" valid_tag "" invalid
check_valid "tag starting with a dash" valid_tag "-x" invalid
check_valid "tag with a slash" valid_tag "a/b" invalid
check_valid "tag with a colon" valid_tag "a:b" invalid
check_valid "tag of 129 characters" valid_tag "$(printf 'x%.0s' {1..129})" invalid

check_valid "Dockerfile in the context root" valid_dockerfile Dockerfile valid
check_valid "Dockerfile in a subdirectory" valid_dockerfile build/Containerfile valid
check_valid "absolute Dockerfile path" valid_dockerfile /etc/passwd invalid
check_valid "Dockerfile path leaving the context" valid_dockerfile ../Dockerfile invalid
check_valid "Dockerfile path with .. inside" valid_dockerfile a/../../Dockerfile invalid
check_valid "empty Dockerfile path" valid_dockerfile "" invalid

sha=0123456789abcdef0123456789abcdef01234567
check "main pushes :<sha> and :latest" "r/a:${sha} r/a:latest" \
  "$(build_images r/a "$sha" refs/heads/main | paste -sd' ')"
check "main adds the extra tags" "r/a:${sha} r/a:latest r/a:2.338.0-homelab" \
  "$(build_images r/a "$sha" refs/heads/main 2.338.0-homelab | paste -sd' ')"
check "a branch pushes only :branch-<sha>, without the extra tags" "r/a:branch-${sha}" \
  "$(build_images r/a "$sha" refs/heads/agent/buildah 2.338.0-homelab | paste -sd' ')"
check_fails "a short SHA is refused" build_images r/a 0123abcd refs/heads/main

bargs="$(buildah_args Dockerfile $'org.opencontainers.image.licenses=Apache-2.0\n' $'r/a:1\nr/a:latest\n')"
check "buildah arguments" \
  '["--isolation=chroot","--format=docker","--file=/workspace/Dockerfile","--label=org.opencontainers.image.licenses=Apache-2.0","--tag=r/a:1","--tag=r/a:latest"]' \
  "$bargs"
check "no labels, no label arguments" 0 "$(buildah_args Dockerfile "" r/a:1 | jq '[.[] | select(startswith("--label"))] | length')"

spec="$(pod_overrides pod1 buildah@sha256:b "$bargs" $'r/a:1\n' reg.example 10.0.0.1 push-secret profiles/p.json)"
ctr='.spec.containers[0]'
check "own user namespace" false "$(jq '.spec.hostUsers' <<<"$spec")"
check "not privileged" null "$(jq "${ctr}.securityContext.privileged" <<<"$spec")"
check "pod runs as non-root" true "$(jq ".spec.securityContext.runAsNonRoot and ${ctr}.securityContext.runAsNonRoot" <<<"$spec")"
check "pod uid is the Buildah build user" 1000 "$(jq '.spec.securityContext.runAsUser' <<<"$spec")"
check "every capability dropped" '["ALL"]' "$(jq -c "${ctr}.securityContext.capabilities.drop" <<<"$spec")"
check "only SETUID and SETGID added, both in the baseline set" '["SETGID","SETUID"]' \
  "$(jq -c "${ctr}.securityContext.capabilities.add | sort" <<<"$spec")"
check "Localhost seccomp profile on the pod" "Localhost profiles/p.json" \
  "$(jq -r '.spec.securityContext.seccompProfile | "\(.type) \(.localhostProfile)"' <<<"$spec")"
check "Localhost seccomp profile on the container" "Localhost profiles/p.json" \
  "$(jq -r "${ctr}.securityContext.seccompProfile | \"\(.type) \(.localhostProfile)\"" <<<"$spec")"
check "no Unconfined profile anywhere" 0 "$(jq '[.. | strings | select(. == "Unconfined")] | length' <<<"$spec")"
check "default procMount" null "$(jq "${ctr}.securityContext.procMount" <<<"$spec")"
check "no host path volume" 0 "$(jq '[.spec.volumes[] | select(.hostPath)] | length' <<<"$spec")"
check "no host namespaces" 0 "$(jq '[.spec | (.hostNetwork, .hostPID, .hostIPC) | select(. == true)] | length' <<<"$spec")"
check "no service account token" false "$(jq '.spec.automountServiceAccountToken' <<<"$spec")"
check "Buildah image" buildah@sha256:b "$(jq -r "${ctr}.image" <<<"$spec")"
check "host alias to Harbor" "10.0.0.1 reg.example" "$(jq -r '.spec.hostAliases[0] | "\(.ip) \(.hostnames[0])"' <<<"$spec")"
check "push secret mounted" push-secret "$(jq -r '.spec.volumes[] | select(.name == "docker-config") | .secret.secretName' <<<"$spec")"
check "push credentials from the mounted secret" /docker-config/config.json \
  "$(jq -r "${ctr}.env[] | select(.name == \"REGISTRY_AUTH_FILE\") | .value" <<<"$spec")"
check "layer storage on an emptyDir" "storage /home/build/.local/share/containers" \
  "$(jq -r "${ctr}.volumeMounts[] | select(.name == \"storage\") | \"\(.name) \(.mountPath)\"" <<<"$spec")"
check "buildah arguments are the container args" "$bargs" "$(jq -c "${ctr}.args" <<<"$spec")"
check "the images to push reach the pod" "r/a:1" "$(jq -r "${ctr}.env[] | select(.name == \"BUILD_IMAGES\") | .value" <<<"$spec")"

# The in-pod script: the context lands in the workspace, Buildah's storage is native
# overlay, the build gets the arguments, and every image is pushed. Run it with the pod
# paths mapped into a scratch directory and a fake buildah on PATH.
root="${work}/pod"
mkdir -p "${root}/workspace" "${root}/bin" "${work}/ctx"
echo hello > "${work}/ctx/app.txt"
cat > "${root}/bin/buildah" <<EOF
#!/usr/bin/env bash
echo "\$*" >> "${root}/buildah.calls"
if [ "\$1" = push ]; then
  [ -z "\${FAIL_PUSH:-}" ] || exit 1
  for a in "\$@"; do case "\$a" in --digestfile=*) echo sha256:d > "\${a#--digestfile=}" ;; esac; done
fi
EOF
chmod +x "${root}/bin/buildah"
# /tmp first: the scratch directory itself is under /tmp.
pod_script="${POD_SCRIPT//\/tmp/${root}/tmp}"
pod_script="${pod_script//\/home\/build/${root}/home}"
pod_script="${pod_script//\/workspace/${root}/workspace}"
run_pod() {
  tar -C "${work}/ctx" -czf - . | PATH="${root}/bin:${PATH}" CONTAINERS_STORAGE_CONF="${root}/storage.conf" \
    BUILD_IMAGES=$'r/a:1\nr/a:latest\n' bash -c "$pod_script" pod1 --file=x --tag=r/a:1
}
out="$(run_pod)"
check "context unpacked into the workspace" hello "$(cat "${root}/workspace/app.txt")"
check "storage is native overlay with no FUSE helper" 'driver = "overlay"' "$(grep -x 'driver = "overlay"' "${root}/storage.conf")"
check "storage graph root on the emptyDir" "graphroot = \"${root}/home/.local/share/containers/storage\"" \
  "$(grep '^graphroot' "${root}/storage.conf")"
check "no mount_program in the storage config" 0 "$(grep -c mount_program "${root}/storage.conf" || true)"
check "buildah build gets the arguments and the workspace" "build --file=x --tag=r/a:1 ${root}/workspace" \
  "$(sed -n 1p "${root}/buildah.calls")"
check "each image pushed to its own reference" \
  "push --tls-verify=false --digestfile=${root}/tmp/digest r/a:1 docker://r/a:1|push --tls-verify=false --digestfile=${root}/tmp/digest r/a:latest docker://r/a:latest" \
  "$(sed -n '2,3p' "${root}/buildah.calls" | paste -sd'|')"
check "the digest of each push is printed" "pushed r/a:1@sha256:d pushed r/a:latest@sha256:d" \
  "$(grep '^pushed' <<<"$out" | paste -sd' ')"
rm -f "${root}/buildah.calls"
run_pod_failing_push() { export FAIL_PUSH=1; run_pod; }
check_fails "a failed push fails the pod" run_pod_failing_push

# A whole run against a fake kubectl.
cat > "${work}/kubectl" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" > "${work}/kubectl.args"
cat > "${work}/kubectl.stdin"
EOF
chmod +x "${work}/kubectl"
echo 'FROM scratch' > "${work}/ctx/Dockerfile"
mkdir -p "${work}/ctx/sub" && echo 'FROM scratch' > "${work}/ctx/sub/Containerfile"
run_main() { REGISTRY=reg.example REGISTRY_HOST_IP=10.0.0.1 KUBECTL="${work}/kubectl" NAMESPACE=ns1 main "$@"; }
run_main --pod b-1 --sha "$sha" --ref refs/heads/main --extra-tag 2.338.0-homelab \
  --label org.opencontainers.image.revision="$sha" "${work}/ctx" reg.example/a >/dev/null
check "kubectl runs the pod" "run b-1" "$(sed -n '1,2p' "${work}/kubectl.args" | paste -sd' ')"
check "kubectl in the namespace" 1 "$(grep -cx -- '--namespace=ns1' "${work}/kubectl.args")"
check "kubectl gets the pinned Buildah image" 1 "$(grep -c -- '--image=quay.io/buildah/stable:.*@sha256:' "${work}/kubectl.args")"
check "the context arrives on stdin" "./app.txt" "$(tar -tzf "${work}/kubectl.stdin" | grep app.txt)"
overrides="$(sed -n 's/^--overrides=//p' "${work}/kubectl.args")"
bargs="$(jq -c '.spec.containers[0].args' <<<"$overrides")"
check "main tags :<sha>, :latest and the extra tag" \
  "--tag=reg.example/a:${sha} --tag=reg.example/a:latest --tag=reg.example/a:2.338.0-homelab" \
  "$(jq -r '[.[] | select(startswith("--tag="))] | join(" ")' <<<"$bargs")"
check "main pushes the same three" "reg.example/a:${sha} reg.example/a:latest reg.example/a:2.338.0-homelab" \
  "$(jq -r '.spec.containers[0].env[] | select(.name == "BUILD_IMAGES") | .value' <<<"$overrides" | paste -sd' ')"
check "the label reaches the build" 1 "$(jq "[.[] | select(. == \"--label=org.opencontainers.image.revision=${sha}\")] | length" <<<"$bargs")"
check "the default Dockerfile" 1 "$(jq '[.[] | select(. == "--file=/workspace/Dockerfile")] | length' <<<"$bargs")"
check "the default push secret" harbor-push "$(jq -r '.spec.volumes[] | select(.name == "docker-config") | .secret.secretName' <<<"$overrides")"
check "the default seccomp profile" profiles/image-build.json "$(jq -r '.spec.securityContext.seccompProfile.localhostProfile' <<<"$overrides")"

PUSH_SECRET=s2 SECCOMP_PROFILE=profiles/x.json run_main --pod b-2 --sha "$sha" --ref refs/heads/agent/x \
  --extra-tag 1.0.0 --file sub/Containerfile "${work}/ctx" reg.example/a >/dev/null
overrides="$(sed -n 's/^--overrides=//p' "${work}/kubectl.args")"
check "a branch builds only :branch-<sha>" "--tag=reg.example/a:branch-${sha}" \
  "$(jq -r '[.spec.containers[0].args[] | select(startswith("--tag="))] | join(" ")' <<<"$overrides")"
check "--file picks the Dockerfile" 1 "$(jq '[.spec.containers[0].args[] | select(. == "--file=/workspace/sub/Containerfile")] | length' <<<"$overrides")"
check "PUSH_SECRET picks the secret" s2 "$(jq -r '.spec.volumes[] | select(.name == "docker-config") | .secret.secretName' <<<"$overrides")"
check "SECCOMP_PROFILE picks the profile" profiles/x.json "$(jq -r '.spec.securityContext.seccompProfile.localhostProfile' <<<"$overrides")"

ok=(--pod p --sha "$sha" --ref refs/heads/main)
check_fails "main without --pod" run_main --sha "$sha" --ref refs/heads/main "${work}/ctx" reg.example/a
check_fails "main without --ref" run_main --pod p --sha "$sha" "${work}/ctx" reg.example/a
check_fails "main without --sha" run_main --pod p --ref refs/heads/main "${work}/ctx" reg.example/a
check_fails "main without a repository" run_main "${ok[@]}" "${work}/ctx"
check_fails "main with an extra argument" run_main "${ok[@]}" "${work}/ctx" reg.example/a reg.example/b
check_fails "main with a missing context" run_main "${ok[@]}" "${work}/nowhere" reg.example/a
check_fails "main with a missing Dockerfile" run_main "${ok[@]}" --file nothing "${work}/ctx" reg.example/a
check_fails "main with a Dockerfile outside the context" run_main "${ok[@]}" --file ../Dockerfile "${work}/ctx" reg.example/a
check_fails "main with a bad --label" run_main "${ok[@]}" --label bad "${work}/ctx" reg.example/a
check_fails "main with a bad --extra-tag" run_main "${ok[@]}" --extra-tag 'a:b' "${work}/ctx" reg.example/a
check_fails "main with an unknown option" run_main "${ok[@]}" --nope "${work}/ctx" reg.example/a
check_fails "main without REGISTRY" env -u REGISTRY bash "${here}/build-image-buildah.sh" "${ok[@]}" "${work}/ctx" reg.example/a
check_fails "main without REGISTRY_HOST_IP" env -u REGISTRY_HOST_IP REGISTRY=r bash "${here}/build-image-buildah.sh" "${ok[@]}" "${work}/ctx" reg.example/a

if [ "$failures" -gt 0 ]; then
  echo "${failures} test(s) failed"
  exit 1
fi
echo "all build-image-buildah tests passed"
