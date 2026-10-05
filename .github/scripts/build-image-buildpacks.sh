#!/usr/bin/env bash
# Build an image with Cloud Native Buildpacks in the cluster and push it to Harbor.
#
# Runs the lifecycle `creator` from a pinned Paketo builder in a short-lived pod that
# `kubectl run` starts from the ARC runner. The build context goes to the pod over
# stdin as a gzipped tar. The pod needs no daemon, no root and no added capabilities:
# it runs as the builder's non-root CNB user (1001) with every capability dropped, no
# privilege escalation and the RuntimeDefault seccomp profile, which Pod Security
# "restricted" admits. It reaches Harbor in-cluster through the Gateway host alias, over
# plain HTTP (-insecure-registry), with the push credentials in PUSH_SECRET.
#
# The builder and run images are pinned by tag and digest in .github/buildpacks/images.yaml.
# The lifecycle writes the SBOM (CycloneDX, SPDX, Syft) into the image as a layer.
#
# Usage: build-image-buildpacks.sh [options] CONTEXT_DIR IMAGE [EXTRA_IMAGE...]
#   CONTEXT_DIR      the application directory the buildpacks detect and build
#   IMAGE            the image reference to push, e.g. <registry>/kubemoot/indexer:<sha>
#   EXTRA_IMAGE      more references for the same image, e.g. <registry>/kubemoot/indexer:latest
# Options:
#   --pod NAME             the build pod name (required)
#   --builder NAME         the images.yaml entry of the builder (builder-java-tiny)
#   --run-image NAME       the images.yaml entry of the run image (run-tiny)
#   --cache-image REF      a registry image that holds the build cache between builds
#   --previous-image REF   the image whose launch layers this build may reuse
#   --env KEY=VALUE        a build-time variable for the buildpacks (repeatable)
# Env:
#   REGISTRY (required)           the Harbor host, e.g. harbor-homelab.dijure.com
#   REGISTRY_HOST_IP (required)   the in-cluster address the host alias points at
#   NAMESPACE (arc-runners)       where the build pod runs
#   PUSH_SECRET (kaniko-docker-config)  the docker config secret with the push credentials
#   IMAGES_FILE (.github/buildpacks/images.yaml)  KUBECTL (kubectl)
set -euo pipefail

# The command the build pod runs: unpack the context from stdin, write the build-time
# variables as /platform/env files, then hand over to the creator with the arguments.
# shellcheck disable=SC2016 # expanded inside the pod, not here
POD_SCRIPT='set -euo pipefail
tar -xzf - -C /workspace
mkdir -p /platform/env
printf "%s\n" "${BUILD_ENV:-}" | while IFS= read -r kv; do
  [ -n "$kv" ] || continue
  printf "%s" "${kv#*=}" > "/platform/env/${kv%%=*}"
done
exec /cnb/lifecycle/creator "$@"'

die() { echo "build-image-buildpacks: $*" >&2; exit 2; }

# pinned_image FILE NAME: the image of the images.yaml entry NAME; fails when it has no digest.
pinned_image() {
  local file="$1" name="$2" ref
  ref="$(awk -v want="$name" '
    /^[[:space:]]*-[[:space:]]*name:/ { current = $NF }
    /^[[:space:]]*image:/ && current == want { print $NF; exit }
  ' "$file")"
  [ -n "$ref" ] || die "no image named ${name} in ${file}"
  case "$ref" in
    *@sha256:*) echo "$ref" ;;
    *) die "image ${name} in ${file} is not pinned by digest: ${ref}" ;;
  esac
}

# valid_env KEY=VALUE: true when KEY can be a /platform/env file name and a variable name.
valid_env() {
  [[ "$1" =~ ^[A-Z_][A-Z0-9_]*= ]]
}

# creator_args REGISTRY RUN_IMAGE CACHE_IMAGE PREVIOUS_IMAGE IMAGE [EXTRA_IMAGE...]:
# the creator's arguments as a JSON array. Empty cache and previous images are left out.
creator_args() {
  local registry="$1" run_image="$2" cache_image="$3" previous_image="$4" image="$5"
  shift 5
  local args=(-app=/workspace -layers=/layers -platform=/platform
    "-run-image=${run_image}" "-insecure-registry=${registry}" -report=/layers/report.toml)
  [ -z "$cache_image" ] || args+=("-cache-image=${cache_image}")
  [ -z "$previous_image" ] || args+=("-previous-image=${previous_image}")
  local extra
  for extra in "$@"; do args+=("-tag=${extra}"); done
  args+=("$image")
  jq -cn '$ARGS.positional' --args -- "${args[@]}"
}

# pod_overrides POD BUILDER CREATOR_ARGS_JSON BUILD_ENV REGISTRY REGISTRY_HOST_IP PUSH_SECRET:
# the `kubectl run --overrides` pod spec for the build.
pod_overrides() {
  local pod="$1" builder="$2" creator_json="$3" build_env="$4" registry="$5" host_ip="$6" secret="$7"
  jq -cn --arg pod "$pod" --arg builder "$builder" --argjson creator "$creator_json" \
    --arg script "$POD_SCRIPT" --arg env "$build_env" --arg registry "$registry" \
    --arg ip "$host_ip" --arg secret "$secret" '
    def scratch(name): {name: name, emptyDir: {}};
    {
      apiVersion: "v1",
      spec: {
        restartPolicy: "Never",
        automountServiceAccountToken: false,
        hostAliases: [{ip: $ip, hostnames: [$registry]}],
        securityContext: {
          runAsNonRoot: true, runAsUser: 1001, runAsGroup: 1001, fsGroup: 1001,
          seccompProfile: {type: "RuntimeDefault"}
        },
        containers: [{
          name: "build",
          image: $builder,
          stdin: true,
          stdinOnce: true,
          command: ["/bin/bash", "-c", $script, $pod],
          args: $creator,
          env: [
            {name: "CNB_PLATFORM_API", value: "0.15"},
            {name: "DOCKER_CONFIG", value: "/docker-config"},
            {name: "BUILD_ENV", value: $env}
          ],
          securityContext: {
            runAsNonRoot: true,
            allowPrivilegeEscalation: false,
            capabilities: {drop: ["ALL"]},
            seccompProfile: {type: "RuntimeDefault"}
          },
          volumeMounts: [
            {name: "workspace", mountPath: "/workspace"},
            {name: "layers", mountPath: "/layers"},
            {name: "platform", mountPath: "/platform"},
            {name: "docker-config", mountPath: "/docker-config", readOnly: true}
          ]
        }],
        volumes: [
          scratch("workspace"), scratch("layers"), scratch("platform"),
          {name: "docker-config", secret: {secretName: $secret,
            items: [{key: "config.json", path: "config.json"}]}}
        ]
      }
    }'
}

main() {
  local pod="" builder_name="builder-java-tiny" run_name="run-tiny" cache_image="" previous_image=""
  local build_env=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --pod) pod="${2:?--pod needs a value}"; shift 2 ;;
      --builder) builder_name="${2:?--builder needs a value}"; shift 2 ;;
      --run-image) run_name="${2:?--run-image needs a value}"; shift 2 ;;
      --cache-image) cache_image="${2:?--cache-image needs a value}"; shift 2 ;;
      --previous-image) previous_image="${2:?--previous-image needs a value}"; shift 2 ;;
      --env)
        valid_env "${2:-}" || die "--env needs KEY=VALUE with an upper-case KEY, got: ${2:-}"
        build_env+="${2}"$'\n'; shift 2 ;;
      --) shift; break ;;
      -*) die "unknown option: $1" ;;
      *) break ;;
    esac
  done
  [ -n "$pod" ] || die "--pod is required"
  [ $# -ge 2 ] || die "usage: build-image-buildpacks.sh [options] CONTEXT_DIR IMAGE [EXTRA_IMAGE...]"
  local context="$1"; shift
  [ -d "$context" ] || die "context directory not found: ${context}"
  : "${REGISTRY:?REGISTRY required}"
  : "${REGISTRY_HOST_IP:?REGISTRY_HOST_IP required}"

  local here images_file builder run_image creator_json overrides
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  images_file="${IMAGES_FILE:-${here}/../buildpacks/images.yaml}"
  builder="$(pinned_image "$images_file" "$builder_name")"
  run_image="$(pinned_image "$images_file" "$run_name")"
  creator_json="$(creator_args "$REGISTRY" "$run_image" "$cache_image" "$previous_image" "$@")"
  overrides="$(pod_overrides "$pod" "$builder" "$creator_json" "$build_env" \
    "$REGISTRY" "$REGISTRY_HOST_IP" "${PUSH_SECRET:-kaniko-docker-config}")"

  echo "Building $* with ${builder} on ${run_image}"
  # The first pull of the builder can take minutes; the pod-running timeout is only a safety net.
  tar -C "$context" -czf - . | "${KUBECTL:-kubectl}" run "$pod" \
    --rm -i --restart=Never --namespace="${NAMESPACE:-arc-runners}" \
    --pod-running-timeout=10m --image="$builder" --overrides="$overrides"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
