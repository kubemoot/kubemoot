#!/usr/bin/env bash
# Shared steps of the chart release scripts (release-operator-chart.sh and
# release-vllm-chart.sh): the next candidate version from the tags, the manifest a
# candidate tag records, and the claim of a version by tag followed by the push to
# Harbor. Source it after release-lib.sh of kubemoot/release-actions; it needs
# CHART_PREFIX (the candidate tag prefix, such as operator-chart-v) set by the caller,
# and REGISTRY for a push.

die() { echo "ERROR: $*" >&2; exit 1; }

# output NAME VALUE: a step output for the workflow, when there is one.
output() {
  [ -n "${GITHUB_OUTPUT:-}" ] && echo "$1=$2" >> "$GITHUB_OUTPUT"
  return 0
}

# next_chart_version COMMIT [FIRST]: the version after the latest chart candidate
# reachable from COMMIT. With no candidate yet it is FIRST, or an error when the caller
# gives none.
next_chart_version() {
  local latest current published=false
  latest=$(rl_latest_rc "$CHART_PREFIX" "$1")
  if [ -z "$latest" ]; then
    [ -n "${2:-}" ] || die "no ${CHART_PREFIX}X.Y.Z-rc.N tag reachable from $1 to count from"
    echo "$2"
    return 0
  fi
  current="${latest#"$CHART_PREFIX"}"
  rl_tag_exists "${CHART_PREFIX}$(rl_final_of "$current")" && published=true
  rl_next_chart_rc "$current" "$published"
}

# current_manifest COMMIT: the manifest recorded by the latest chart candidate, if any.
current_manifest() {
  local latest
  latest=$(rl_latest_rc "$CHART_PREFIX" "$1")
  [ -n "$latest" ] || return 0
  git tag -l --format='%(contents:body)' "$latest" | sed '/^$/d'
}

# package_chart_at COMMIT DIR VERSION OUT_DIR: DIR as it is at COMMIT, in a scratch worktree
# (never committed; git holds 0.0.0), stamped with VERSION and packaged into OUT_DIR. The
# worktree's path is left in RL_CHECKOUT, so call this directly, not in $(...).
package_chart_at() {
  rl_checkout_at "$1"
  rl_stamp_chart "${RL_CHECKOUT}/$2" "$3"
  helm package "${RL_CHECKOUT}/$2" --destination "$4" >&2
}

# push_chart TGZ: the chart to Harbor, three attempts.
push_chart() {
  local attempt
  for attempt in 1 2 3; do
    helm push --insecure-skip-tls-verify "$1" "oci://${REGISTRY}/kubemoot/charts" && return 0
    echo "helm push failed (attempt ${attempt}/3); retrying in 5s"
    sleep 5
  done
  return 1
}

# claim_and_push_chart VERSION COMMIT MANIFEST TGZ LABEL: claim the version with the
# candidate tag, then push the chart. A tag without its chart would make every later run
# see the chart as current, so a failed push removes the tag or stops with the command
# to remove it.
claim_and_push_chart() {
  local version="$1" commit="$2" body="$3" tgz="$4" label="$5" tag="${CHART_PREFIX}$1"
  git tag -a "$tag" -m "Release candidate ${label} ${version}" -m "$body" "$commit"
  if ! git push origin "refs/tags/${tag}"; then
    git tag -d "$tag" >/dev/null
    die "could not push ${tag}; it exists on origin already (another chart release claimed it), so run again"
  fi
  if push_chart "$tgz"; then
    echo "Released ${label} ${version}"
    return 0
  fi
  git tag -d "$tag" >/dev/null
  git push --delete origin "refs/tags/${tag}" \
    || die "the chart push failed and ${tag} could not be removed; remove it by hand: git push --delete origin refs/tags/${tag}"
  die "the chart push failed after 3 attempts; ${tag} was removed again"
}
