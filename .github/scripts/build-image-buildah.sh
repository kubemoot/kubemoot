#!/usr/bin/env bash
# Build an image from a Dockerfile with Buildah in the cluster and push it to Harbor.
#
# For the images a buildpack cannot express. Runs Buildah, daemonless, in a short-lived
# pod that `kubectl run` starts from the ARC runner; the build context goes to the pod
# over stdin as a gzipped tar, as in build-image-buildpacks.sh.
#
# The pod runs in its own user namespace (hostUsers: false): uid 0 inside it maps to an
# unprivileged uid on the node. Buildah runs as the image's `build` user (uid 1000) and
# sets up a nested user namespace from its /etc/subuid and /etc/subgid ranges, where the
# Dockerfile's RUN steps run as root with chroot isolation and the layers are stored on
# native overlay in an emptyDir. Nothing is privileged and no host path is mounted.
#
# Pod Security: the pod meets "baseline". It cannot meet two "restricted" fields:
#   - allowPrivilegeEscalation stays true: newuidmap and newgidmap gain CAP_SETUID and
#     CAP_SETGID from file capabilities to write the nested namespace's id maps, and
#     no_new_privs would block that;
#   - capabilities drops ALL but adds SETUID and SETGID back (both in the baseline
#     default set): file capabilities are capped by the bounding set.
# Its seccomp profile is the Localhost profile SECCOMP_PROFILE, which the homelab Talos
# workers install: the container runtime's default profile plus the namespace and mount
# syscalls (unshare, clone, mount, setns ...) that the default allows only with
# CAP_SYS_ADMIN. The kernel still checks capabilities, which the build holds only inside
# its own nested namespace. The nodes must allow user namespaces
# (user.max_user_namespaces above 0).
#
# Harbor is reached in-cluster through the Gateway host alias, pushed to with
# --tls-verify=false (the Gateway certificate is not verified, plain HTTP allowed) and the
# push credentials in PUSH_SECRET. Each build starts from empty storage, so no layer is
# reused from an earlier build. The image is written in the Docker format, which keeps
# a Dockerfile HEALTHCHECK (the OCI format drops it). The Buildah image is pinned by tag and digest in
# .github/buildah/images.yaml.
#
# Usage: build-image-buildah.sh [options] CONTEXT_DIR REPOSITORY
#   CONTEXT_DIR      the build context; the Dockerfile is in it
#   REPOSITORY       the image repository, e.g. <registry>/kubemoot/code-sandbox
# Options:
#   --pod NAME             the build pod name (required)
#   --sha SHA              the commit the image is built from (required)
#   --ref REF              the git ref being built, e.g. refs/heads/main (required)
#   --file PATH            the Dockerfile, relative to CONTEXT_DIR (Dockerfile)
#   --label KEY=VALUE      an image label (repeatable)
#   --extra-tag TAG        one more tag to push, on main only (repeatable); other refs
#                          leave it out, so a branch never moves it
#
# Tags (see image_plan in build-image-lib.sh): main pushes :<sha>, which the release
# retags, and :latest. Any other ref pushes only :branch-<sha>. No layer cache is kept.
# Env:
#   REGISTRY (required)           the Harbor host, e.g. harbor-homelab.dijure.com
#   REGISTRY_HOST_IP (required)   the in-cluster address the host alias points at
#   NAMESPACE (arc-runners)       where the build pod runs
#   PUSH_SECRET (harbor-push)     the docker config secret with the push credentials
#   SECCOMP_PROFILE (profiles/image-build.json)  the node's Localhost seccomp profile
#   IMAGES_FILE (.github/buildah/images.yaml)  KUBECTL (kubectl)
set -euo pipefail

BUILD_IMAGE_TOOL=build-image-buildah
# shellcheck source-path=SCRIPTDIR source=build-image-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/build-image-lib.sh"

# The command the build pod runs: unpack the context from stdin, point Buildah's storage
# at native overlay on the storage emptyDir, build with the arguments, then push each
# image in BUILD_IMAGES (one per line) and print its digest.
# shellcheck disable=SC2016 # expanded inside the pod, not here
POD_SCRIPT='set -euo pipefail
tar -xzf - -C /workspace --no-overwrite-dir
mkdir -p /tmp/containers-run
printf "[storage]\ndriver = \"overlay\"\nrunroot = \"/tmp/containers-run\"\ngraphroot = \"/home/build/.local/share/containers/storage\"\n" > "$CONTAINERS_STORAGE_CONF"
buildah build "$@" /workspace
printf "%s\n" "$BUILD_IMAGES" | while IFS= read -r image; do
  [ -n "$image" ] || continue
  buildah push --tls-verify=false --digestfile=/tmp/digest "$image" "docker://${image}"
  echo "pushed ${image}@$(cat /tmp/digest)"
done'

# valid_label KEY=VALUE: true when KEY is a label key (letters, digits, . _ - /) and the
# whole pair is one line.
valid_label() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*= ]] && [[ "$1" != *$'\n'* ]]
}

# valid_tag TAG: true when TAG is an image tag (at most 128 of [A-Za-z0-9_.-], not
# starting with . or -).
valid_tag() {
  [[ "$1" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]
}

# valid_dockerfile PATH: true when PATH is a relative path that stays in the context.
valid_dockerfile() {
  [[ -n "$1" && "$1" != /* && "/$1/" != */../* && "$1" != *$'\n'* ]]
}

# build_images REPOSITORY SHA REF [EXTRA_TAG...]: the images to push, one per line. The
# image_plan images, then on main each extra tag.
build_images() {
  local repo="$1" sha="$2" ref="$3" plan line tag
  shift 3
  plan="$(image_plan "$repo" "$sha" "$ref")" || exit 2
  while IFS= read -r line; do
    case "$line" in
      "cache "* | "previous "*) ;;
      *) echo "$line" ;;
    esac
  done <<<"$plan"
  [ "$ref" = "refs/heads/main" ] || return 0
  for tag in "$@"; do echo "${repo}:${tag}"; done
}

# buildah_args DOCKERFILE LABELS IMAGES: the `buildah build` arguments as a JSON array.
# LABELS and IMAGES hold one entry per line; each image becomes a --tag.
buildah_args() {
  jq -cn --arg file "$1" --arg labels "$2" --arg images "$3" '
    def lines(s): s | split("\n") | map(select(length > 0));
    ["--isolation=chroot", "--format=docker", "--file=/workspace/\($file)"]
    + (lines($labels) | map("--label=\(.)"))
    + (lines($images) | map("--tag=\(.)"))'
}

# pod_overrides POD IMAGE BUILDAH_ARGS_JSON IMAGES REGISTRY REGISTRY_HOST_IP PUSH_SECRET
# SECCOMP_PROFILE: the `kubectl run --overrides` pod spec for the build.
pod_overrides() {
  local pod="$1" image="$2" args_json="$3" images="$4" registry="$5" host_ip="$6" secret="$7"
  local profile="$8"
  jq -cn --arg pod "$pod" --arg image "$image" --argjson args "$args_json" \
    --arg script "$POD_SCRIPT" --arg images "$images" --arg profile "$profile" \
    --argjson reg "$(pod_parts "$registry" "$host_ip" "$secret")" "${POD_JQ_DEFS}"'
    def seccomp: {type: "Localhost", localhostProfile: $profile};
    $reg.base * {
      spec: {
        hostUsers: false,
        securityContext: {
          runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000,
          seccompProfile: seccomp
        },
        containers: [{
          name: "build",
          image: $image,
          stdin: true,
          stdinOnce: true,
          command: ["/bin/bash", "-c", $script, $pod],
          args: $args,
          env: [
            {name: "BUILDAH_ISOLATION", value: "chroot"},
            {name: "CONTAINERS_STORAGE_CONF", value: "/tmp/storage.conf"},
            {name: "REGISTRY_AUTH_FILE", value: "/docker-config/config.json"},
            {name: "BUILD_IMAGES", value: $images}
          ],
          securityContext: {
            runAsNonRoot: true,
            allowPrivilegeEscalation: true,
            capabilities: {drop: ["ALL"], add: ["SETUID", "SETGID"]},
            seccompProfile: seccomp
          },
          volumeMounts: [
            {name: "workspace", mountPath: "/workspace"},
            {name: "storage", mountPath: "/home/build/.local/share/containers"},
            $reg.mount
          ]
        }],
        volumes: [scratch("workspace"), scratch("storage"), $reg.volume]
      }
    }'
}

main() {
  local pod="" sha="" ref="" file="Dockerfile" labels="" extra_tags=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --pod) pod="${2:?--pod needs a value}"; shift 2 ;;
      --sha) sha="${2:?--sha needs a value}"; shift 2 ;;
      --ref) ref="${2:?--ref needs a value}"; shift 2 ;;
      --file)
        valid_dockerfile "${2:-}" || die "--file needs a path inside the context, got: ${2:-}"
        file="$2"; shift 2 ;;
      --label)
        valid_label "${2:-}" || die "--label needs one-line KEY=VALUE, got: ${2:-}"
        labels+="${2}"$'\n'; shift 2 ;;
      --extra-tag)
        valid_tag "${2:-}" || die "--extra-tag needs an image tag, got: ${2:-}"
        extra_tags+=("$2"); shift 2 ;;
      --) shift; break ;;
      -*) die "unknown option: $1" ;;
      *) break ;;
    esac
  done
  [ -n "$pod" ] || die "--pod is required"
  [ -n "$sha" ] || die "--sha is required"
  [ -n "$ref" ] || die "--ref is required"
  [ $# -eq 2 ] || die "usage: build-image-buildah.sh [options] CONTEXT_DIR REPOSITORY"
  local context="$1" repo="$2"
  [ -d "$context" ] || die "context directory not found: ${context}"
  [ -f "${context}/${file}" ] || die "Dockerfile not found: ${context}/${file}"
  : "${REGISTRY:?REGISTRY required}"
  : "${REGISTRY_HOST_IP:?REGISTRY_HOST_IP required}"

  local images here images_file builder args_json overrides
  images="$(build_images "$repo" "$sha" "$ref" "${extra_tags[@]}")" || exit 2
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  images_file="${IMAGES_FILE:-${here}/../buildah/images.yaml}"
  builder="$(pinned_image "$images_file" buildah)" || exit 2
  args_json="$(buildah_args "$file" "$labels" "$images")"
  overrides="$(pod_overrides "$pod" "$builder" "$args_json" "$images" "$REGISTRY" \
    "$REGISTRY_HOST_IP" "${PUSH_SECRET:-harbor-push}" "${SECCOMP_PROFILE:-profiles/image-build.json}")"

  echo "Building $(paste -sd' ' <<<"$images") from ${file} with ${builder}"
  run_build_pod "$pod" "$builder" "$overrides" "$context"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
