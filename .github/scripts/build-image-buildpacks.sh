#!/usr/bin/env bash
# Build an image with Cloud Native Buildpacks in the cluster and push it to Harbor.
#
# Runs the lifecycle `creator` from a pinned Paketo builder in a short-lived pod that
# `kubectl run` starts from the ARC runner. The build context goes to the pod over
# stdin as a gzipped tar. The pod needs no daemon, no root and no added capabilities:
# it runs as the builder's non-root CNB user (1001) with every capability dropped, no
# privilege escalation and the RuntimeDefault seccomp profile, which Pod Security
# "restricted" admits. It reaches Harbor in-cluster through the Gateway host alias, with
# -insecure-registry (plain HTTP allowed, the Gateway certificate not verified) and the
# push credentials in PUSH_SECRET.
#
# The builder and run images are pinned by tag and digest in .github/buildpacks/images.yaml.
# The lifecycle writes the SBOM (CycloneDX, SPDX, Syft) into the image as a layer.
#
# Usage: build-image-buildpacks.sh [options] CONTEXT_DIR REPOSITORY
#   CONTEXT_DIR      the application directory the buildpacks detect and build
#   REPOSITORY       the image repository, e.g. <registry>/kubemoot/indexer
# Options:
#   --pod NAME             the build pod name (required)
#   --sha SHA              the commit the image is built from (required)
#   --ref REF              the git ref being built, e.g. refs/heads/main (required)
#   --builder NAME         the images.yaml entry of the builder (builder-java-tiny)
#   --run-image NAME       the images.yaml entry of the run image (run-tiny)
#   --env KEY=VALUE        a build-time variable for the buildpacks (repeatable)
#   --buildpack ID         a buildpack of the builder to run, in order (repeatable); when
#                          given, these form the only detect group instead of the builder's
#                          own order, e.g. paketo-buildpacks/procfile for a packaged binary.
#                          The pod takes each one's version from the builder.
#
# Tags (see image_plan): main pushes :<sha>, which the release retags, and :latest. Any
# other ref pushes only :branch-<sha>, so it never moves a tag main uses. Each ref keeps
# its own build cache image, <repository>-buildcache:<ref>.
# Env:
#   REGISTRY (required)           the Harbor host, e.g. harbor-homelab.dijure.com
#   REGISTRY_HOST_IP (required)   the in-cluster address the host alias points at
#   NAMESPACE (arc-runners)       where the build pod runs
#   PUSH_SECRET (kaniko-docker-config)  the docker config secret with the push credentials
#   IMAGES_FILE (.github/buildpacks/images.yaml)  KUBECTL (kubectl)
set -euo pipefail

# The command the build pod runs: unpack the context from stdin, write the build-time
# variables as /platform/env files, write /platform/order.toml when buildpacks are named
# (each at the one version the builder holds), then hand over to the creator.
# shellcheck disable=SC2016 # expanded inside the pod, not here
POD_SCRIPT='set -euo pipefail
tar -xzf - -C /workspace --no-overwrite-dir
mkdir -p /platform/env
printf "%s\n" "${BUILD_ENV:-}" | while IFS= read -r kv; do
  [ -n "$kv" ] || continue
  printf "%s" "${kv#*=}" > "/platform/env/${kv%%=*}"
done
if [ -n "${BUILD_BUILDPACKS:-}" ]; then
  echo "[[order]]" > /platform/order.toml
  printf "%s\n" "$BUILD_BUILDPACKS" | while IFS= read -r id; do
    [ -n "$id" ] || continue
    versions=(/cnb/buildpacks/"${id//\//_}"/*)
    if [ "${#versions[@]}" -ne 1 ] || [ ! -d "${versions[0]}" ]; then
      echo "the builder has no single version of buildpack ${id}" >&2; exit 1
    fi
    printf "  [[order.group]]\n    id = \"%s\"\n    version = \"%s\"\n" "$id" "${versions[0]##*/}" >> /platform/order.toml
  done
fi
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

# valid_env KEY=VALUE: true when KEY can be a /platform/env file name and a variable
# name, and the whole pair is one line (BUILD_ENV carries one pair per line).
valid_env() {
  [[ "$1" =~ ^[A-Z_][A-Z0-9_]*= ]] && [[ "$1" != *$'\n'* ]]
}

# valid_buildpack_id ID: true when ID is a buildpack id (letters, digits, . _ - and /).
valid_buildpack_id() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]]
}

# image_plan REPOSITORY SHA REF: the image references to push, one per line, then a line
# "cache <ref>" and, on main, a line "previous <ref>".
image_plan() {
  local repo="$1" sha="$2" ref="$3"
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || die "--sha needs a full commit SHA, got: ${sha}"
  if [ "$ref" = "refs/heads/main" ]; then
    printf '%s\n' "${repo}:${sha}" "${repo}:latest" "cache ${repo}-buildcache:main" "previous ${repo}:latest"
    return
  fi
  # A tag holds at most 128 characters from [A-Za-z0-9_.-].
  local name="${ref#refs/heads/}"
  name="${name//[^A-Za-z0-9_.-]/-}"
  printf '%s\n' "${repo}:branch-${sha}" "cache ${repo}-buildcache:branch-${name:0:120}"
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

# pod_overrides POD BUILDER CREATOR_ARGS_JSON BUILD_ENV REGISTRY REGISTRY_HOST_IP PUSH_SECRET
# [BUILDPACKS]: the `kubectl run --overrides` pod spec for the build; BUILDPACKS holds one
# buildpack id per line.
pod_overrides() {
  local pod="$1" builder="$2" creator_json="$3" build_env="$4" registry="$5" host_ip="$6" secret="$7"
  local buildpacks="${8:-}"
  jq -cn --arg pod "$pod" --arg builder "$builder" --argjson creator "$creator_json" \
    --arg script "$POD_SCRIPT" --arg env "$build_env" --arg registry "$registry" \
    --arg ip "$host_ip" --arg secret "$secret" --arg buildpacks "$buildpacks" '
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
            {name: "BUILD_ENV", value: $env},
            {name: "BUILD_BUILDPACKS", value: $buildpacks}
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
  local pod="" sha="" ref="" builder_name="builder-java-tiny" run_name="run-tiny" build_env=""
  local buildpacks=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --pod) pod="${2:?--pod needs a value}"; shift 2 ;;
      --sha) sha="${2:?--sha needs a value}"; shift 2 ;;
      --ref) ref="${2:?--ref needs a value}"; shift 2 ;;
      --builder) builder_name="${2:?--builder needs a value}"; shift 2 ;;
      --run-image) run_name="${2:?--run-image needs a value}"; shift 2 ;;
      --env)
        valid_env "${2:-}" || die "--env needs one-line KEY=VALUE with an upper-case KEY, got: ${2:-}"
        build_env+="${2}"$'\n'; shift 2 ;;
      --buildpack)
        valid_buildpack_id "${2:-}" || die "--buildpack needs a buildpack id, got: ${2:-}"
        buildpacks+=("$2"); shift 2 ;;
      --) shift; break ;;
      -*) die "unknown option: $1" ;;
      *) break ;;
    esac
  done
  [ -n "$pod" ] || die "--pod is required"
  [ -n "$ref" ] || die "--ref is required"
  [ $# -eq 2 ] || die "usage: build-image-buildpacks.sh [options] CONTEXT_DIR REPOSITORY"
  local context="$1" repo="$2"
  [ -d "$context" ] || die "context directory not found: ${context}"
  : "${REGISTRY:?REGISTRY required}"
  : "${REGISTRY_HOST_IP:?REGISTRY_HOST_IP required}"

  local plan cache="" previous="" line
  local images=()
  plan="$(image_plan "$repo" "$sha" "$ref")" || exit 2
  while IFS= read -r line; do
    case "$line" in
      "cache "*) cache="${line#cache }" ;;
      "previous "*) previous="${line#previous }" ;;
      *) images+=("$line") ;;
    esac
  done <<<"$plan"

  local here images_file builder run_image creator_json overrides buildpack_lines=""
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  images_file="${IMAGES_FILE:-${here}/../buildpacks/images.yaml}"
  builder="$(pinned_image "$images_file" "$builder_name")" || exit 2
  run_image="$(pinned_image "$images_file" "$run_name")" || exit 2
  creator_json="$(creator_args "$REGISTRY" "$run_image" "$cache" "$previous" "${images[@]}")"
  if [ "${#buildpacks[@]}" -gt 0 ]; then
    buildpack_lines="$(printf '%s\n' "${buildpacks[@]}")"
    creator_json="$(jq -c '["-order=/platform/order.toml"] + .' <<<"$creator_json")"
  fi
  overrides="$(pod_overrides "$pod" "$builder" "$creator_json" "$build_env" \
    "$REGISTRY" "$REGISTRY_HOST_IP" "${PUSH_SECRET:-kaniko-docker-config}" "$buildpack_lines")"

  echo "Building ${images[*]} with ${builder} on ${run_image}"
  # The first pull of the builder can take minutes; the pod-running timeout is only a safety net.
  tar -C "$context" -czf - . | "${KUBECTL:-kubectl}" run "$pod" \
    --rm -i --restart=Never --namespace="${NAMESPACE:-arc-runners}" \
    --pod-running-timeout=10m --image="$builder" --overrides="$overrides"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
