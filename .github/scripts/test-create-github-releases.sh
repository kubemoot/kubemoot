#!/usr/bin/env bash
# Tests for create-github-releases.sh: gh is stubbed (a directory stands in for the
# repository's releases), jq is the real one.
# Usage: bash .github/scripts/test-create-github-releases.sh (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
command -v jq >/dev/null || { echo "jq is required"; exit 1; }

failures=0
check() {
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi
}

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT
export LOG="${root}/calls.log" GH_STATE="${root}/releases" GITHUB_REPOSITORY=kubemoot/test
mkdir -p "${root}/bin" "$GH_STATE"

# gh stub: a release is GH_STATE/<tag>/state (draft or published) with its title, notes,
# and uploaded assets beside it. Like GitHub, the release list holds drafts and the tag
# lookup of `release view` does not. GH_EDIT_FAIL makes publishing a draft fail;
# GH_LIST_FAIL makes listing the releases fail.
cat > "${root}/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >> "$LOG"
if [ "$1" = api ]; then
  [ -z "${GH_LIST_FAIL:-}" ] || exit 1
  [ "$2" = --paginate ] && [ "$3" = "repos/${GITHUB_REPOSITORY}/releases?per_page=100" ] || exit 1
  for d in "$GH_STATE"/*/; do
    [ -f "${d}state" ] || continue
    tag="$(basename "$d")"
    if [ "$(cat "${d}state")" = draft ]; then draft=true; else draft=false; fi
    printf '{"tag_name":"%s","draft":%s}\n' "$tag" "$draft"
  done | jq -s -c . | jq -r "${5}"
  exit 0
fi
[ "$1" = release ] || exit 1
cmd="$2" tag="$3"; shift 3
dir="${GH_STATE}/${tag}"
case "$cmd" in
  view)
    [ "$(cat "${dir}/state" 2>/dev/null)" = published ] || exit 1
    echo false ;;
  create)
    [ ! -f "${dir}/state" ] || exit 1
    mkdir -p "${dir}/assets"; echo draft > "${dir}/state"
    while [ $# -gt 0 ]; do
      case "$1" in
        --title) echo "$2" > "${dir}/title"; shift ;;
        --notes-file) cp "$2" "${dir}/notes" ;;
      esac
      shift
    done ;;
  upload)
    [ "$(cat "${dir}/state" 2>/dev/null)" = draft ] || exit 1
    for f in "$@"; do [ -f "$f" ] && cp "$f" "${dir}/assets/"; done ;;
  edit)
    [ -z "${GH_EDIT_FAIL:-}" ] || exit 1
    [ -f "${dir}/state" ] || exit 1
    echo published > "${dir}/state" ;;
  *) exit 1 ;;
esac
EOF
chmod +x "${root}/bin/gh"
export PATH="${root}/bin:${PATH}"

# bundle FILE NAME: a Sigstore bundle as actions/attest writes it, on several lines here
# so the test also sees the asset written one bundle per line.
bundle() {
  printf '{\n  "mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",\n  "subject": "%s"\n}\n' "$2" > "$1"
}

# setup NAME: a fresh release-files and provenance directory, two crews, three subjects.
setup() {
  local d="${root}/$1"
  mkdir -p "${d}/files" "${d}/prov"
  printf 'alpha-v0.5.0\talpha 0.5.0\talpha.md\nbeta-v0.30.1\tbeta 0.30.1\tbeta.md\n' > "${d}/files/releases.tsv"
  echo "alpha notes" > "${d}/files/alpha.md"
  echo "beta notes" > "${d}/files/beta.md"
  printf '%s\t%s\t%s\t%s\n' \
    alpha-v0.5.0 alpha_0.5.0.intoto.jsonl ghcr.test/kubemoot/charts/alpha sha256:aaa \
    alpha-v0.5.0 alpha_0.5.0.intoto.jsonl ghcr.test/kubemoot/alpha sha256:ccc \
    beta-v0.30.1 beta_0.30.1.intoto.jsonl ghcr.test/kubemoot/charts/beta sha256:bbb \
    > "${d}/files/provenance.tsv"
  bundle "${d}/prov/sha256-aaa.jsonl" charts/alpha
  bundle "${d}/prov/sha256-ccc.jsonl" alpha
  bundle "${d}/prov/sha256-bbb.jsonl" charts/beta
}

run() {
  : > "$LOG"
  bash "${here}/create-github-releases.sh" "${root}/$1/files" "${root}/$1/prov" > "${root}/$1.log" 2>&1
}

state() { cat "${GH_STATE}/$1/state" 2>/dev/null || echo none; }

# 1. Fresh releases: each is a draft, gets its own provenance, then is published.
setup fresh
run fresh && status=0 || status=$?
check "creates the releases" 0 "$status"
[ "$status" -eq 0 ] || sed 's/^/    /' "${root}/fresh.log"
check "publishes both" "published|published" "$(state alpha-v0.5.0)|$(state beta-v0.30.1)"
check "keeps the title and notes" "alpha 0.5.0|alpha notes" \
  "$(cat "${GH_STATE}/alpha-v0.5.0/title")|$(cat "${GH_STATE}/alpha-v0.5.0/notes")"
check "creates each as a draft on its existing tag" 2 "$(grep -c '^gh release create .* --verify-tag --draft ' "$LOG")"
check "uploads before it publishes" "create upload edit" \
  "$(grep '^gh release [a-z]* alpha-v0.5.0' "$LOG" | awk '{print $3}' | tr '\n' ' ' | sed 's/ $//')"
check "one provenance asset per release" "alpha_0.5.0.intoto.jsonl|beta_0.30.1.intoto.jsonl" \
  "$(ls "${GH_STATE}/alpha-v0.5.0/assets")|$(ls "${GH_STATE}/beta-v0.30.1/assets")"
asset="${GH_STATE}/alpha-v0.5.0/assets/alpha_0.5.0.intoto.jsonl"
check "holds every subject of its release, one bundle per line" "2|charts/alpha alpha" \
  "$(wc -l < "$asset" | tr -d ' ')|$(jq -r .subject "$asset" | tr '\n' ' ' | sed 's/ $//')"
check "each line is a Sigstore bundle" 2 \
  "$(jq -c 'select(.mediaType | startswith("application/vnd.dev.sigstore.bundle"))' "$asset" | wc -l | tr -d ' ')"
check "holds no other release's provenance" "charts/beta" \
  "$(jq -r .subject "${GH_STATE}/beta-v0.30.1/assets/beta_0.30.1.intoto.jsonl")"

# 2. A rerun leaves published releases alone.
run fresh && status=0 || status=$?
check "a rerun succeeds" 0 "$status"
check "and writes nothing" 0 "$(grep -cE '^gh release (create|upload|edit)' "$LOG" || true)"
check "reads the state from the release list" 2 "$(grep -c '^gh api --paginate repos/kubemoot/test/releases?per_page=100 ' "$LOG")"
check "and says so" 2 "$(grep -c 'is already published' "${root}/fresh.log")"

# 3. A failed publish leaves a draft; the rerun completes it without a second create.
rm -rf "${GH_STATE:?}"/*
GH_EDIT_FAIL=1 run fresh && status=0 || status=$?
check "a failed publish fails the run" 1 "$((status != 0))"
check "and leaves the draft" draft "$(state alpha-v0.5.0)"
run fresh && status=0 || status=$?
check "the rerun publishes the draft" "0|published|published" "${status}|$(state alpha-v0.5.0)|$(state beta-v0.30.1)"
check "without creating it again" 0 "$(grep -c '^gh release create alpha-v0.5.0 ' "$LOG" || true)"
check "with one copy of its provenance" 2 "$(wc -l < "${GH_STATE}/alpha-v0.5.0/assets/alpha_0.5.0.intoto.jsonl" | tr -d ' ')"

# 4. Unexpected inputs: nothing is created for a release whose provenance is not right.
rm -rf "${GH_STATE:?}"/*
setup missing
rm "${root}/missing/prov/sha256-ccc.jsonl"
run missing && status=0 || status=$?
check "a missing bundle fails" "1|1" "$((status != 0))|$(grep -c 'no provenance bundle for ghcr.test/kubemoot/alpha@sha256:ccc' "${root}/missing.log")"
check "and creates no release" "none|0" "$(state alpha-v0.5.0)|$(grep -c '^gh release create' "$LOG" || true)"

setup broken
echo '{"mediaType": "text/plain"}' > "${root}/broken/prov/sha256-aaa.jsonl"
run broken && status=0 || status=$?
check "a file that is not a Sigstore bundle fails" "1|1" "$((status != 0))|$(grep -c 'is not a Sigstore bundle' "${root}/broken.log")"
check "and creates no release" none "$(state alpha-v0.5.0)"

GH_LIST_FAIL=1 run broken && status=0 || status=$?
check "a release list that cannot be read fails" "1|1|none" \
  "$((status != 0))|$(grep -c 'cannot list the releases of kubemoot/test' "${root}/broken.log")|$(state alpha-v0.5.0)"

setup notjson
echo 'not json' > "${root}/notjson/prov/sha256-aaa.jsonl"
run notjson && status=0 || status=$?
check "a file that is not JSON fails" "1|none" "$((status != 0))|$(state alpha-v0.5.0)"

setup empty
: > "${root}/empty/prov/sha256-aaa.jsonl"
run empty && status=0 || status=$?
check "an empty bundle fails" "1|none" "$((status != 0))|$(state alpha-v0.5.0)"

setup unmapped
sed -i '/^beta-v0.30.1/d' "${root}/unmapped/files/provenance.tsv"
run unmapped && status=0 || status=$?
check "a release with no provenance fails" "1|1" "$((status != 0))|$(grep -c 'no provenance recorded for beta-v0.30.1' "${root}/unmapped.log")"
check "and is not created" none "$(state beta-v0.30.1)"
rm -rf "${GH_STATE:?}"/*

setup nomap
rm "${root}/nomap/files/provenance.tsv"
run nomap && status=0 || status=$?
check "a run without provenance.tsv fails" "1|0" "$((status != 0))|$(grep -c '^gh release create' "$LOG" || true)"

setup norepo
( unset GITHUB_REPOSITORY; bash "${here}/create-github-releases.sh" "${root}/norepo/files" "${root}/norepo/prov" ) >/dev/null 2>&1 && status=0 || status=$?
check "a run without GITHUB_REPOSITORY fails" 1 "$((status != 0))"
bash "${here}/create-github-releases.sh" >/dev/null 2>&1 && status=0 || status=$?
check "a run without arguments fails" 1 "$((status != 0))"

if [ "$failures" -ne 0 ]; then echo "${failures} test(s) failed"; exit 1; fi
echo "all create-github-releases tests passed"
