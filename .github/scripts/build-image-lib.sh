#!/usr/bin/env bash
# Shared parts of the in-cluster image builds (build-image-buildpacks.sh and
# build-image-buildah.sh): the digest pins, the tag plan per git ref, the registry
# parts of the build pod, and starting the pod with the build context on stdin.
#
# Sourced, never run. The sourcing script sets BUILD_IMAGE_TOOL to its own name for the
# error messages.

# die MESSAGE: print MESSAGE prefixed with the build tool's name and exit 2.
die() { echo "${BUILD_IMAGE_TOOL:-build-image}: $*" >&2; exit 2; }

# pinned_image FILE NAME: the image of the pin file entry NAME; fails when it has no digest.
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

# image_plan REPOSITORY SHA REF: the image references to push, one per line, then a line
# "cache <ref>" and, on main, a line "previous <ref>". Main pushes :<sha>, which the
# release retags, and :latest. Any other ref pushes only :branch-<sha>, so it never moves
# a tag main uses. Each ref keeps its own build cache image, <repository>-buildcache:<ref>.
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

# The jq helper both pod specs use: scratch(name), an emptyDir volume.
# shellcheck disable=SC2034 # used by the sourcing scripts
POD_JQ_DEFS='def scratch(name): {name: name, emptyDir: {}};'

# pod_parts REGISTRY REGISTRY_HOST_IP PUSH_SECRET: a JSON object with the parts every
# build pod shares. base: the pod skeleton (never restarted, no service account token,
# the host alias to Harbor at the in-cluster Gateway address), which a script merges
# its own spec into with `*`; volume and mount: the push credentials, whose config.json
# the build reads at /docker-config.
pod_parts() {
  jq -cn --arg registry "$1" --arg ip "$2" --arg secret "$3" '{
    base: {apiVersion: "v1", spec: {
      restartPolicy: "Never",
      automountServiceAccountToken: false,
      hostAliases: [{ip: $ip, hostnames: [$registry]}]
    }},
    volume: {name: "docker-config", secret: {secretName: $secret,
      items: [{key: "config.json", path: "config.json"}]}},
    mount: {name: "docker-config", mountPath: "/docker-config", readOnly: true}
  }'
}

# run_build_pod POD IMAGE OVERRIDES CONTEXT_DIR: start the build pod from IMAGE with the
# pod spec OVERRIDES, stream CONTEXT_DIR to it as a gzipped tar on stdin, follow its
# output and remove it when it ends. Env: NAMESPACE (arc-runners), KUBECTL (kubectl).
run_build_pod() {
  local pod="$1" image="$2" overrides="$3" context="$4"
  # The first pull of the build image can take minutes; the pod-running timeout is only
  # a safety net.
  tar -C "$context" -czf - . | "${KUBECTL:-kubectl}" run "$pod" \
    --rm -i --restart=Never --namespace="${NAMESPACE:-arc-runners}" \
    --pod-running-timeout=10m --image="$image" --overrides="$overrides"
}
