#!/usr/bin/env bash
# Keyless signing of the artifacts a promotion publishes; sourced by promote-release.sh
# after release-lib.sh.
#
# cosign signs each image or chart by digest in the release registry with the GitHub
# OIDC identity of the running workflow, so no key is stored anywhere; the signature is
# stored in the registry next to the artifact. An artifact that already carries a
# signature from this workflow is not signed again. Signed references are recorded in
# OUT_DIR/subjects.tsv (repository, digest), from which the workflow's attest job
# records SLSA build provenance. A dry run signs nothing and records nothing, but checks
# that cosign runs and prints the identity the signatures would carry.
#
# Verify a signature:
#   cosign verify <repository>@<digest> \
#     --certificate-identity https://github.com/<owner>/<repo>/.github/workflows/promote-release.yaml@refs/heads/main \
#     --certificate-oidc-issuer https://token.actions.githubusercontent.com
#
# This file is the same in kubemoot and crews; change both, until it moves to
# kubemoot/release-actions.
#
# Env: DRY_RUN, RELEASE_REGISTRY, GHCR_USERNAME and GHCR_TOKEN (cosign logs in to the
# release registry with them on a real run), and the GitHub Actions variables
# ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN (present when the job
# has id-token: write), GITHUB_SERVER_URL, and GITHUB_WORKFLOW_REF.

SIGN_ISSUER="https://token.actions.githubusercontent.com"
SIGN_IDENTITY=""
SIGN_SUBJECTS=""

sign_error() { echo "ERROR: $*" >&2; return 1; }

# sign_release_preflight OUT_DIR: before anything is published, cosign must run and, for
# a real run or any run in GitHub Actions, the job must be able to mint the OIDC token
# cosign signs with. A real run logs cosign in to the release registry.
sign_release_preflight() {
  local version
  SIGN_SUBJECTS="$1/subjects.tsv"
  : > "$SIGN_SUBJECTS"
  version=$(cosign version 2>/dev/null | sed -nE 's/^GitVersion:[[:space:]]+//p' || true)
  [ -n "$version" ] || { sign_error "cosign is required to sign the release and does not run"; return 1; }
  if { [ -z "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ] || [ -z "${ACTIONS_ID_TOKEN_REQUEST_TOKEN:-}" ]; } \
     && { ! rl_is_dry || [ "${GITHUB_ACTIONS:-}" = "true" ]; }; then
    sign_error "no GitHub OIDC token for keyless signing: the job needs permissions id-token: write"
    return 1
  fi
  [ -z "${GITHUB_WORKFLOW_REF:-}" ] || SIGN_IDENTITY="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_WORKFLOW_REF}"
  echo "signing: cosign ${version}, identity ${SIGN_IDENTITY:-<workflow>@<ref>}, issuer ${SIGN_ISSUER}"
  rl_is_dry && return 0
  if [ -z "${GHCR_TOKEN:-}" ] || [ -z "${GHCR_USERNAME:-}" ]; then
    sign_error "GHCR_USERNAME and GHCR_TOKEN are required to store the signatures"
    return 1
  fi
  echo "${GHCR_TOKEN}" | cosign login "${RELEASE_REGISTRY%%/*}" -u "${GHCR_USERNAME}" --password-stdin \
    || { sign_error "cosign cannot log in to ${RELEASE_REGISTRY%%/*}"; return 1; }
}

# sign_release_signed REF: true when REF already carries a signature from this workflow.
sign_release_signed() {
  [ -n "$SIGN_IDENTITY" ] && cosign verify "$1" --certificate-identity "$SIGN_IDENTITY" \
    --certificate-oidc-issuer "$SIGN_ISSUER" >/dev/null 2>&1
}

# sign_release_artifact REPOSITORY DIGEST [earlier]: sign REPOSITORY@DIGEST in the
# release registry and record it for the provenance attestation. An artifact already
# signed by this workflow is not signed again; it is still recorded unless "earlier"
# says an earlier release published it (and attested it then). A failure returns
# non-zero, which stops the promotion before any tag is pushed.
sign_release_artifact() {
  local repo="$1" digest="$2" earlier="${3:-}"
  if rl_is_dry; then
    echo "sign ${repo}@${digest} (dry run: not signed)"
    return 0
  fi
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] \
    || { sign_error "refusing to sign ${repo} without a sha256 digest (got [${digest}])"; return 1; }
  if sign_release_signed "${repo}@${digest}"; then
    echo "already signed ${repo}@${digest}"
    [ -z "$earlier" ] || return 0
  else
    cosign sign --yes "${repo}@${digest}" || { sign_error "signing ${repo}@${digest} failed"; return 1; }
    echo "signed ${repo}@${digest}"
  fi
  printf '%s\t%s\n' "$repo" "$digest" >> "$SIGN_SUBJECTS"
}

# sign_release_chart_repo TGZ: the release registry repository of a packaged chart.
sign_release_chart_repo() {
  echo "${RELEASE_REGISTRY}/charts/$(helm show chart "$1" | sed -n 's/^name: //p')"
}

# sign_release_subjects_json: the recorded references as a JSON array of {name, digest},
# the matrix of the workflow's attest job.
sign_release_subjects_json() {
  awk -F'\t' 'BEGIN { printf "[" } NR > 1 { printf "," } { printf "{\"name\":\"%s\",\"digest\":\"%s\"}", $1, $2 } END { print "]" }' \
    "$SIGN_SUBJECTS"
}
