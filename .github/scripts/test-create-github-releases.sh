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
        --latest*) echo "$1" > "${dir}/create-latest" ;;
      esac
      shift
    done ;;
  upload)
    [ "$(cat "${dir}/state" 2>/dev/null)" = draft ] || exit 1
    for f in "$@"; do [ -f "$f" ] && cp "$f" "${dir}/assets/"; done ;;
  edit)
    [ -z "${GH_EDIT_FAIL:-}" ] || exit 1
    [ -f "${dir}/state" ] || exit 1
    echo published > "${dir}/state"
    echo "$*" > "${dir}/edit-args" ;;
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

# 3b. Other assets: assets.tsv adds files to a release beside its provenance.
rm -rf "${GH_STATE:?}"/*
setup assets
mkdir -p "${root}/assets/files/dashboards"
echo '{"title":"x"}' > "${root}/assets/files/dashboards/kubemoot-scheduler.json"
printf 'alpha-v0.5.0\tdashboards/kubemoot-scheduler.json\n' > "${root}/assets/files/assets.tsv"
run assets && status=0 || status=$?
check "a release with other assets is published" "0|published" "${status}|$(state alpha-v0.5.0)"
check "with the asset beside its provenance" "alpha_0.5.0.intoto.jsonl kubemoot-scheduler.json" \
  "$(find "${GH_STATE}/alpha-v0.5.0/assets" -type f -printf '%f\n' | sort | paste -sd' ')"
check "and only on its own release" "beta_0.30.1.intoto.jsonl" "$(ls "${GH_STATE}/beta-v0.30.1/assets")"

rm -rf "${GH_STATE:?}"/*
setup asset-missing
printf 'alpha-v0.5.0\tdashboards/gone.json\n' > "${root}/asset-missing/files/assets.tsv"
run asset-missing && status=0 || status=$?
check "a missing asset fails the run" 1 "$((status != 0))"
check "and names it" 1 "$(grep -c 'asset dashboards/gone.json of alpha-v0.5.0 not found' "${root}/asset-missing.log")"
check "before the release is created" none "$(state alpha-v0.5.0)"

for clash in "alpha_0.5.0.intoto.jsonl" "dashboards/a/x.json dashboards/b/x.json"; do
  rm -rf "${GH_STATE:?}"/*
  setup asset-clash
  mkdir -p "${root}/asset-clash/files/dashboards/a" "${root}/asset-clash/files/dashboards/b"
  : > "${root}/asset-clash/files/assets.tsv"
  for f in $clash; do
    echo '{}' > "${root}/asset-clash/files/${f}"
    printf 'alpha-v0.5.0\t%s\n' "$f" >> "${root}/asset-clash/files/assets.tsv"
  done
  run asset-clash && status=0 || status=$?
  check "assets with one name fail the run (${clash})" "1|1|none" \
    "$((status != 0))|$(grep -c 'two assets of alpha-v0.5.0 are named' "${root}/asset-clash.log")|$(state alpha-v0.5.0)"
done

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

setup listfail
GH_LIST_FAIL=1 run listfail && status=0 || status=$?
check "a release list that cannot be read fails" "1|1|none" \
  "$((status != 0))|$(grep -c 'cannot list the releases of kubemoot/test' "${root}/listfail.log")|$(state alpha-v0.5.0)"

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

# 5. Kubemoot's own release goes last and is the only one marked Latest.
rm -rf "${GH_STATE:?}"/* "${root}/order"
setup order
printf 'operator-chart-v0.94.0\tKubemoot 0.94.0\top.md\nalpha-v0.5.0\talpha 0.5.0\talpha.md\nbeta-v0.30.1\tbeta 0.30.1\tbeta.md\n' \
  > "${root}/order/files/releases.tsv"
echo "op notes" > "${root}/order/files/op.md"
printf 'operator-chart-v0.94.0\top_0.94.0.intoto.jsonl\tghcr.test/kubemoot/charts/op\tsha256:ddd\n' >> "${root}/order/files/provenance.tsv"
bundle "${root}/order/prov/sha256-ddd.jsonl" charts/op
run order && status=0 || status=$?
check "a run with the operator release first succeeds" 0 "$status"
check "publishes the operator release last" "alpha-v0.5.0 beta-v0.30.1 operator-chart-v0.94.0" \
  "$(grep '^gh release edit ' "$LOG" | awk '{print $4}' | paste -sd' ')"
check "creates the operator release last" "alpha-v0.5.0 beta-v0.30.1 operator-chart-v0.94.0" \
  "$(grep '^gh release create ' "$LOG" | awk '{print $4}' | paste -sd' ')"
check "marks only the operator release Latest when publishing" "--latest=false|--latest=false|--latest" \
  "$(grep -o '[-]-latest[^ ]*$' "${GH_STATE}/alpha-v0.5.0/edit-args")|$(grep -o '[-]-latest[^ ]*$' "${GH_STATE}/beta-v0.30.1/edit-args")|$(grep -o '[-]-latest[^ ]*$' "${GH_STATE}/operator-chart-v0.94.0/edit-args")"
check "and when creating" "--latest=false|--latest=false|--latest" \
  "$(cat "${GH_STATE}/alpha-v0.5.0/create-latest")|$(cat "${GH_STATE}/beta-v0.30.1/create-latest")|$(cat "${GH_STATE}/operator-chart-v0.94.0/create-latest")"

# 5b. A rerun with the operator release still a draft completes it and still publishes it last.
rm -rf "${GH_STATE:?}"/*
mkdir -p "${GH_STATE}/operator-chart-v0.94.0/assets"; echo draft > "${GH_STATE}/operator-chart-v0.94.0/state"
mkdir -p "${GH_STATE}/alpha-v0.5.0"; echo published > "${GH_STATE}/alpha-v0.5.0/state"
run order && status=0 || status=$?
check "a resumed run succeeds" "0|published|published" "${status}|$(state operator-chart-v0.94.0)|$(state beta-v0.30.1)"
check "leaves the published release alone" "beta-v0.30.1 operator-chart-v0.94.0" \
  "$(grep '^gh release edit ' "$LOG" | awk '{print $4}' | paste -sd' ')"

# 5d. Operator already published, alpha new: alpha is not Latest, and the operator is
# handed Latest again at the end.
rm -rf "${GH_STATE:?}"/*
mkdir -p "${GH_STATE}/operator-chart-v0.94.0"; echo published > "${GH_STATE}/operator-chart-v0.94.0/state"
mkdir -p "${GH_STATE}/beta-v0.30.1"; echo published > "${GH_STATE}/beta-v0.30.1/state"
run order && status=0 || status=$?
check "a run with the operator already published succeeds" "0|published" "${status}|$(state alpha-v0.5.0)"
check "alpha is created and published as not Latest" "--latest=false|--latest=false" \
  "$(cat "${GH_STATE}/alpha-v0.5.0/create-latest")|$(grep -o '[-]-latest[^ ]*$' "${GH_STATE}/alpha-v0.5.0/edit-args")"
check "the operator release is given Latest again, last" "alpha-v0.5.0 operator-chart-v0.94.0|operator-chart-v0.94.0 --repo kubemoot/test --latest" \
  "$(grep '^gh release edit ' "$LOG" | awk '{print $4}' | paste -sd' ')|$(grep '^gh release edit ' "$LOG" | tail -1 | cut -d' ' -f4-)"

# 5e. A broken bundle of any release stops the run before anything is published.
rm -rf "${GH_STATE:?}"/* "${root}/order"
setup order
printf 'operator-chart-v0.94.0\tKubemoot 0.94.0\top.md\nalpha-v0.5.0\talpha 0.5.0\talpha.md\nbeta-v0.30.1\tbeta 0.30.1\tbeta.md\n' \
  > "${root}/order/files/releases.tsv"
echo "op notes" > "${root}/order/files/op.md"
printf 'operator-chart-v0.94.0\top_0.94.0.intoto.jsonl\tghcr.test/kubemoot/charts/op\tsha256:ddd\n' >> "${root}/order/files/provenance.tsv"
run order && status=0 || status=$?
check "a missing operator bundle fails the run" "1|1" "$((status != 0))|$(grep -c 'no provenance bundle for ghcr.test/kubemoot/charts/op' "${root}/order.log")"
check "before any component is created" "0|none" "$(grep -c '^gh release create' "$LOG" || true)|$(state alpha-v0.5.0)"

# 5c. Components only: nothing is Latest.
rm -rf "${GH_STATE:?}"/*
setup components
run components && status=0 || status=$?
check "a components-only run succeeds" 0 "$status"
check "marks none Latest" "2|0" \
  "$(grep -c '^gh release edit .* --draft=false --latest=false$' "$LOG")|$(grep -cE '^gh release (edit|create) .* --latest( |$)' "$LOG" || true)"

if [ "$failures" -ne 0 ]; then echo "${failures} test(s) failed"; exit 1; fi
echo "all create-github-releases tests passed"
