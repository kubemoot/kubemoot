#!/usr/bin/env bash
# Shared helpers for release candidates and promotion. Sourced, never run.
#
# The same file lives in kubemoot, crews, kmctl, and kubemoot-docs under
# .github/scripts/release-lib.sh, beside test-release-lib.sh and the composite action
# .github/actions/release-candidate-version. The test pins this file's sha256, so an
# edit to one copy fails that repository's test until all four copies (and the pinned
# sum) are updated together; kubemoot holds the reference copy.
#
# Versions: every push to main builds X.Y.Z-rc.N; a promotion tags the candidate's
# commit with the final <prefix>X.Y.Z. A tag is "<prefix><version>", for example
# agent-runtime-v0.342.32-rc.3 or v0.5.0.

# rl_is_rc VERSION: true when VERSION is a release candidate X.Y.Z-rc.N.
rl_is_rc() {
  [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]]
}

# rl_final_of VERSION: X.Y.Z-rc.N -> X.Y.Z (a final version is returned unchanged).
rl_final_of() {
  printf '%s\n' "${1%-rc.*}"
}

# rl_release_needed PREFIX VERSION CHANGED FORCE: prints release_created=true|false for
# $GITHUB_OUTPUT (the reason goes to stderr). X.Y.Z-rc.N is built only while X.Y.Z is
# not yet promoted: a run on a promoted commit computes X.Y.Z-rc.0 of the final and
# must not release it.
rl_release_needed() {
  local prefix="$1" version="$2" changed="$3" force="$4"
  if ! rl_is_rc "$version"; then
    echo "Skipping: ${version} is not a release-candidate version" >&2
    echo "release_created=false"
  elif rl_tag_exists "${prefix}$(rl_final_of "$version")"; then
    echo "Skipping: ${prefix}$(rl_final_of "$version") is already a promoted release" >&2
    echo "release_created=false"
  elif [ "$changed" = "true" ] || [ "$force" = "true" ]; then
    echo "Will create release candidate ${prefix}${version}" >&2
    echo "release_created=true"
  else
    echo "Skipping: no commits since the last release" >&2
    echo "release_created=false"
  fi
}

# rl_tag_exists TAG: true when the tag exists locally (the caller fetched tags).
rl_tag_exists() {
  git rev-parse -q --verify "refs/tags/$1" >/dev/null
}

# rl_latest_rc PREFIX COMMIT: the highest <prefix>X.Y.Z-rc.N tag reachable from
# COMMIT, or nothing. sort -V orders rc.10 above rc.9 within the candidate tags.
rl_latest_rc() {
  local prefix="$1" commit="$2"
  git tag --merged "$commit" --list "${prefix}[0-9]*-rc.*" \
    | grep -E "^${prefix}[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$" \
    | sort -V | tail -n 1 || true
}

# rl_latest_final PREFIX COMMIT [EXCLUDE]: the highest final <prefix>X.Y.Z tag
# reachable from COMMIT, other than EXCLUDE, or nothing.
rl_latest_final() {
  local prefix="$1" commit="$2" exclude="${3:-}"
  git tag --merged "$commit" --list "${prefix}[0-9]*" \
    | grep -E "^${prefix}[0-9]+\.[0-9]+\.[0-9]+$" \
    | grep -vxF -- "${exclude:-/}" \
    | sort -V | tail -n 1 || true
}

# rl_next_chart_rc CURRENT FINAL_PROMOTED: the next release-candidate version of a
# chart whose version is a counter (the operator chart). CURRENT is the version in
# Chart.yaml; FINAL_PROMOTED is "true" when the final of CURRENT's X.Y.Z is tagged.
#   0.92.581            -> 0.92.582-rc.0  (a final: start the next patch)
#   0.92.582-rc.3 false -> 0.92.582-rc.4
#   0.92.582-rc.3 true  -> 0.92.583-rc.0  (0.92.582 was promoted)
rl_next_chart_rc() {
  local current="$1" promoted="$2" base major minor patch
  base=$(rl_final_of "$current")
  IFS=. read -r major minor patch <<<"$base"
  if rl_is_rc "$current" && [ "$promoted" != "true" ]; then
    printf '%s-rc.%s\n' "$base" "$(( ${current##*-rc.} + 1 ))"
  else
    printf '%s.%s.%s-rc.0\n' "$major" "$minor" "$(( patch + 1 ))"
  fi
}

# rl_resolve_point REF: the commit a promotion starts from. "latest" (or empty) is
# the tip of origin/main; otherwise REF must be a release-candidate tag on main.
rl_resolve_point() {
  local ref="${1:-latest}" commit
  if [ "$ref" = "latest" ]; then
    git rev-parse origin/main
    return
  fi
  if ! [[ "$ref" =~ -rc\.[0-9]+$ ]] || ! rl_tag_exists "$ref"; then
    echo "ERROR: ${ref} is not a release-candidate tag in this repository" >&2
    return 1
  fi
  commit=$(git rev-list -n 1 "$ref")
  if ! git merge-base --is-ancestor "$commit" origin/main; then
    echo "ERROR: ${ref} is not on main" >&2
    return 1
  fi
  printf '%s\n' "$commit"
}

# rl_release_notes FROM TO [PATH]: Markdown notes from the conventional commits in
# FROM..TO (FROM empty: all history up to TO), limited to commits touching PATH when
# given. The release bot's [skip ci] commits are left out.
rl_release_notes() {
  local from="$1" to="$2" range paths=()
  range="$to"
  [ -n "$from" ] && range="${from}..${to}"
  [ -n "${3:-}" ] && paths=(-- "$3")
  git log --no-merges --format='%h%x09%s' "$range" "${paths[@]}" \
    | { grep -vF '[skip ci]' || true; } \
    | awk -F '\t' '
        function add(section, line) { body[section] = body[section] "- " line "\n" }
        {
          line = $2 " (" $1 ")"
          if ($2 ~ /^[a-z]+(\([^)]*\))?!:/) add("breaking", line)
          else if ($2 ~ /^feat(\([^)]*\))?:/) add("feat", line)
          else if ($2 ~ /^fix(\([^)]*\))?:/) add("fix", line)
          else add("other", line)
        }
        END {
          split("breaking feat fix other", order, " ")
          title["breaking"] = "Breaking changes"; title["feat"] = "Features"
          title["fix"] = "Fixes"; title["other"] = "Other changes"
          for (i = 1; i <= 4; i++) {
            s = order[i]
            if (body[s] != "") printf "### %s\n\n%s\n", title[s], body[s]
          }
        }'
}

# Promotion bookkeeping. DRY_RUN="true" (the default) records what would happen and
# changes nothing: no tag, no push.
RL_NEW_TAGS=()
RL_NEW_TAG_SOURCES=()
RL_WORKTREES=()

rl_is_dry() {
  [ "${DRY_RUN:-true}" = "true" ]
}

# rl_make_tag FINAL_TAG FROM_TAG: plan an annotated final tag on FROM_TAG's commit.
# Nothing is created until rl_push_new_tags, so a run that stops early leaves no tag.
rl_make_tag() {
  RL_NEW_TAGS+=("$1")
  RL_NEW_TAG_SOURCES+=("$2")
}

# rl_push_new_tags: create every planned tag and push them all or none. A rejected
# push deletes the local tags again, so a re-run starts from the same state.
rl_push_new_tags() {
  local i
  echo "Final tags: ${RL_NEW_TAGS[*]:-none}"
  rl_is_dry && return 0
  [ "${#RL_NEW_TAGS[@]}" -gt 0 ] || return 0
  for i in "${!RL_NEW_TAGS[@]}"; do
    git tag -a "${RL_NEW_TAGS[$i]}" -m "Release ${RL_NEW_TAGS[$i]} (promoted from ${RL_NEW_TAG_SOURCES[$i]})" \
      "$(git rev-list -n 1 "${RL_NEW_TAG_SOURCES[$i]}")"
  done
  if ! git push --atomic origin "${RL_NEW_TAGS[@]/#/refs/tags/}"; then
    git tag -d "${RL_NEW_TAGS[@]}" >/dev/null
    echo "ERROR: pushing the final tags failed; none was created" >&2
    return 1
  fi
}

# rl_checkout_at COMMIT: a scratch worktree of COMMIT, its path in RL_CHECKOUT (call it
# directly, not in $(...), so rl_remove_worktrees can find it).
rl_checkout_at() {
  RL_CHECKOUT="$(mktemp -d)/src"
  git worktree add --quiet --detach "$RL_CHECKOUT" "$1"
  RL_WORKTREES+=("$RL_CHECKOUT")
}

rl_remove_worktrees() {
  local wt
  for wt in "${RL_WORKTREES[@]}"; do git worktree remove --force "$wt" 2>/dev/null || true; done
  return 0
}

# rl_add_release OUT_DIR TAG TITLE NOTES_FILE: one line of OUT_DIR/releases.tsv, which
# the workflow's release job turns into GitHub Releases (NOTES_FILE is relative to OUT_DIR).
rl_add_release() {
  printf '%s\t%s\t%s\n' "$2" "$3" "$4" >> "$1/releases.tsv"
}

# rl_promote_single PREFIX TITLE OUT_DIR: promote the latest release candidate of a
# repository with one version stream (kmctl, kubemoot-docs), up to RC_TAG ("latest"
# or a candidate tag on main). Plans the final <prefix>X.Y.Z tag on the candidate's
# commit, writes OUT_DIR/notes.md and OUT_DIR/releases.tsv, pushes the tag unless
# DRY_RUN, and prints final_tag=, previous_tag=, and commit= lines for $GITHUB_OUTPUT
# (everything else goes to stderr).
rl_promote_single() {
  local prefix="$1" title="$2" out="$3" point rc_tag final src prev
  point=$(rl_resolve_point "${RC_TAG:-latest}") || return 1
  rc_tag=$(rl_latest_rc "$prefix" "$point")
  if [ -z "$rc_tag" ]; then
    echo "ERROR: no ${prefix}X.Y.Z-rc.N tag at or before ${RC_TAG:-latest}" >&2
    return 1
  fi
  final="${prefix}$(rl_final_of "${rc_tag#"$prefix"}")"
  if rl_tag_exists "$final"; then
    echo "ERROR: ${final} is already released" >&2
    return 1
  fi
  src=$(git rev-list -n 1 "$rc_tag")
  prev=$(rl_latest_final "$prefix" "$src")
  echo "Promoting ${rc_tag} (commit ${src}) to ${final}; dry run: ${DRY_RUN:-true}" >&2
  mkdir -p "$out"
  { echo "## Changes${prev:+ since ${prev}}"; echo; rl_release_notes "$prev" "$src"; } > "${out}/notes.md"
  : > "${out}/releases.tsv"
  rl_add_release "$out" "$final" "${title} ${final#"$prefix"}" notes.md
  rl_make_tag "$final" "$rc_tag"
  rl_push_new_tags >&2
  printf 'final_tag=%s\nprevious_tag=%s\ncommit=%s\n' "$final" "$prev" "$src"
}
