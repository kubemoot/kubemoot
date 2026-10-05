#!/usr/bin/env bash
# Write the GitHub Releases a Publish Release run named, each carrying the SLSA build
# provenance of what it publishes as a <name>_<version>.intoto.jsonl asset: one Sigstore
# bundle per line, as actions/attest wrote it. Anyone can check a published artifact
# against it with `gh attestation verify oci://<artifact> --bundle <asset> --repo <repo>`,
# and the OpenSSF Scorecard Signed-Releases check reads it.
#
# Releases are immutable once published, so each one is created as a draft, given its
# provenance, and then published. A release already published is left alone, and a
# draft a failed run left behind is completed, so "Re-run failed jobs" is safe. The
# provenance of a release is checked before its draft is created: a missing or broken
# bundle stops the run with nothing written for that release.
#
# Usage: create-github-releases.sh RELEASE_FILES PROVENANCE_DIR
#   RELEASE_FILES/releases.tsv     tag, title, notes file (relative to RELEASE_FILES)
#   RELEASE_FILES/provenance.tsv   tag, asset name, subject, subject digest
#   PROVENANCE_DIR/<digest>.jsonl  the attest job's bundle for that digest, with the
#                                  ':' of the digest written as '-'
# Env: GITHUB_REPOSITORY, GH_TOKEN (contents: write)
set -euo pipefail
shopt -s inherit_errexit

usage="usage: create-github-releases.sh RELEASE_FILES PROVENANCE_DIR"
files="${1:?${usage}}"
bundles="${2:?${usage}}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY required}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

die() { echo "ERROR: $*" >&2; exit 1; }

# release_state TAG: published, draft, or none. Read from the release list, which holds
# drafts; the tag lookup behind `gh release view` does not.
release_state() {
  local draft
  draft="$(gh api --paginate "repos/${repo}/releases?per_page=100" \
    --jq ".[] | select(.tag_name == \"$1\") | .draft")" || die "cannot list the releases of ${repo}"
  case "$draft" in
    "") echo none ;;
    true) echo draft ;;
    *) echo published ;;
  esac
}

# check_bundle FILE: FILE holds one or more Sigstore bundles and nothing else.
check_bundle() {
  jq -e -s 'length > 0 and all(.[]; (.mediaType // "") | startswith("application/vnd.dev.sigstore.bundle"))' \
    "$1" >/dev/null 2>&1 || die "$1 is not a Sigstore bundle"
}

# provenance_assets TAG DIR: writes the provenance assets of TAG into DIR, one bundle per
# line, from every subject provenance.tsv maps to TAG.
provenance_assets() {
  local tag="$1" dir="$2" t asset subject digest bundle
  while IFS=$'\t' read -r t asset subject digest; do
    [ "$t" = "$tag" ] || continue
    bundle="${bundles}/${digest//:/-}.jsonl"
    [ -f "$bundle" ] || die "no provenance bundle for ${subject}@${digest}"
    check_bundle "$bundle"
    jq -c . "$bundle" >> "${dir}/${asset}"
  done < "${files}/provenance.tsv"
  [ -n "$(find "$dir" -type f -print -quit)" ] || die "no provenance recorded for ${tag}"
}

# publish_release TAG TITLE NOTES: the release as a draft, its provenance, then published.
publish_release() {
  local tag="$1" title="$2" notes="$3" state dir asset
  state="$(release_state "$tag")"
  if [ "$state" = published ]; then
    echo "${tag} is already published"
    return 0
  fi
  dir="${work}/${tag}"
  mkdir -p "$dir"
  provenance_assets "$tag" "$dir"
  if [ "$state" = none ]; then
    gh release create "$tag" --repo "$repo" --verify-tag --draft \
      --title "$title" --notes-file "${files}/${notes}"
  fi
  gh release upload "$tag" --repo "$repo" --clobber "$dir"/*
  gh release edit "$tag" --repo "$repo" --draft=false
  for asset in "$dir"/*; do echo "${tag} published with ${asset##*/}"; done
}

main() {
  local tag title notes
  [ -f "${files}/releases.tsv" ] || die "${files}/releases.tsv not found"
  [ -f "${files}/provenance.tsv" ] || die "${files}/provenance.tsv not found"
  # gh reads stdin, so the release list comes in on its own descriptor.
  while IFS=$'\t' read -r -u 3 tag title notes; do
    publish_release "$tag" "$title" "$notes"
  done 3< "${files}/releases.tsv"
}

main
