#!/usr/bin/env bash
# Tests for release-lib.sh against a throwaway git repository.
# Usage: bash .github/scripts/test-release-lib.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=release-lib.sh
source "${here}/release-lib.sh"

failures=0
check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   ${name}"
  else
    echo "FAIL ${name}: want [${want}] got [${got}]"
    failures=$((failures + 1))
  fi
}
check_status() {
  local name="$1" want="$2"; shift 2
  local got=0
  "$@" >/dev/null 2>&1 || got=$?
  [ "$got" -ne 0 ] && got=1
  check "$name" "$want" "$got"
}

# The four repositories carry identical copies of release-lib.sh and the
# release-candidate-version action; this pins their content so a copy cannot drift
# alone. After changing either, update every copy and the sum here.
RELEASE_LIB_SHA256="a79a08739ce60a5ed0eb67b11f6ea1463af8cd44e6f68212a4d4bf68d76fb3d5"
RELEASE_ACTION_SHA256="d0b4e0590022b885b6ba7c288c8f989777c3d29e927c78e9f3e20145a006c433"
check "release-lib.sh matches the shared copy" "$RELEASE_LIB_SHA256" "$(sha256sum "${here}/release-lib.sh" | cut -d' ' -f1)"
check "release-candidate-version action matches the shared copy" "$RELEASE_ACTION_SHA256" \
  "$(sha256sum "${here}/../actions/release-candidate-version/action.yml" | cut -d' ' -f1)"

# Pure functions: expected and unexpected inputs.
check_status "rc is rc" 0 rl_is_rc 0.1.2-rc.3
check_status "final is not rc" 1 rl_is_rc 0.1.2
check_status "rc without number is not rc" 1 rl_is_rc 0.1.2-rc
check_status "other pre-release is not rc" 1 rl_is_rc 0.1.2-beta.1
check_status "prefixed tag is not a version" 1 rl_is_rc v0.1.2-rc.1
check "final of rc" "0.1.2" "$(rl_final_of 0.1.2-rc.11)"
check "final of final" "0.1.2" "$(rl_final_of 0.1.2)"
check "next chart rc from a final" "0.92.582-rc.0" "$(rl_next_chart_rc 0.92.581 false)"
check "next chart rc counts up" "0.92.582-rc.4" "$(rl_next_chart_rc 0.92.582-rc.3 false)"
check "next chart rc 9 -> 10" "0.92.582-rc.10" "$(rl_next_chart_rc 0.92.582-rc.9 false)"
check "next chart rc after promotion" "0.92.583-rc.0" "$(rl_next_chart_rc 0.92.582-rc.3 true)"

# rl_release_needed: expected and unexpected inputs (tags checked in the repo below).
needed() { rl_release_needed "$@" 2>/dev/null; }

# Git-backed functions.
repo="$(mktemp -d)"
trap 'rm -rf "$repo"' EXIT
cd "$repo"
git init -q -b main
git config user.email test@example.com
git config user.name test
commit() { git commit -q --allow-empty -m "$1"; git rev-parse HEAD; }

c0=$(commit "chore: init")
git tag -a v0.1.0 -m final "$c0"
c1=$(commit "feat: add widgets")
git tag -a v0.2.0-rc.0 -m rc "$c1"
commit "chore: update chart [skip ci]" >/dev/null
c3=$(commit "fix(api): handle empty input")
git tag -a v0.2.0-rc.9 -m rc "$c3"
c4=$(commit "feat!: rename the field")
git tag -a v0.2.0-rc.10 -m rc "$c4"
git tag -a agent-v0.9.0-rc.1 -m rc "$c4"
git update-ref refs/remotes/origin/main "$c4"

check "latest rc sorts rc.10 above rc.9" "v0.2.0-rc.10" "$(rl_latest_rc v "$c4")"
check "latest rc at an older commit" "v0.2.0-rc.9" "$(rl_latest_rc v "$c3")"
check "latest rc ignores other prefixes" "agent-v0.9.0-rc.1" "$(rl_latest_rc agent-v "$c4")"
check "latest rc with none" "" "$(rl_latest_rc other-v "$c4")"
check "latest final ignores rcs" "v0.1.0" "$(rl_latest_final v "$c4")"
git tag -a v0.2.0 -m final "$c4"
check "latest final excluding the new one" "v0.1.0" "$(rl_latest_final v "$c4" v0.2.0)"
check "latest final" "v0.2.0" "$(rl_latest_final v "$c4")"
check_status "tag exists" 0 rl_tag_exists v0.2.0
check_status "tag missing" 1 rl_tag_exists v9.9.9

check "release needed for a new candidate" "release_created=true" "$(needed v 0.3.0-rc.1 true false)"
check "no release without changes" "release_created=false" "$(needed v 0.3.0-rc.1 false false)"
check "forced release without changes" "release_created=true" "$(needed v 0.3.0-rc.1 false true)"
check "no candidate of a promoted version" "release_created=false" "$(needed v 0.2.0-rc.0 true true)"
check "no release of a non-candidate version" "release_created=false" "$(needed v 0.3.0 true true)"
check "no release of an empty version" "release_created=false" "$(needed v "" true true)"

check "resolve latest" "$c4" "$(rl_resolve_point latest)"
check "resolve an rc tag" "$c3" "$(rl_resolve_point v0.2.0-rc.9)"
check_status "resolve refuses a final tag" 1 rl_resolve_point v0.2.0
check_status "resolve refuses a missing tag" 1 rl_resolve_point v7.0.0-rc.1
git checkout -q -b side "$c0"
c5=$(commit "fix: off main")
git tag -a v0.1.1-rc.0 -m rc "$c5"
check_status "resolve refuses an rc off main" 1 rl_resolve_point v0.1.1-rc.0
git checkout -q main

notes="$(rl_release_notes v0.1.0 "$c4")"
check "notes breaking section" "1" "$(grep -c '^### Breaking changes' <<<"$notes")"
check "notes feature" "1" "$(grep -c '^- feat: add widgets' <<<"$notes")"
check "notes scoped fix" "1" "$(grep -c '^- fix(api): handle empty input' <<<"$notes")"
check "notes leave out bot commits" "0" "$(grep -c 'skip ci' <<<"$notes" || true)"
check "notes range excludes the base" "0" "$(grep -c 'chore: init' <<<"$notes" || true)"
bot=$(commit "chore: update chart [skip ci]")
check_status "notes over bot commits only succeed" 0 rl_release_notes "$c4" "$bot"
check "notes over bot commits only are empty" "" "$(rl_release_notes "$c4" "$bot")"
check "notes with no base include all" "1" "$(rl_release_notes "" "$c4" | grep -c '^### Other changes')"

check "notes limited to a path" "" "$(rl_release_notes v0.1.0 "$c4" some/path)"

# Promotion bookkeeping.
rl_make_tag v0.3.0 v0.2.0-rc.9
check "make_tag plans" "v0.3.0" "${RL_NEW_TAGS[*]}"
check_status "make_tag creates nothing yet" 1 rl_tag_exists v0.3.0
check "dry push pushes nothing" "Final tags: v0.3.0" "$(DRY_RUN=true rl_push_new_tags)"
check_status "dry push creates no tag" 1 rl_tag_exists v0.3.0
check_status "push without an origin fails" 1 env DRY_RUN=false bash -c "source '${here}/release-lib.sh'; RL_NEW_TAGS=(v0.3.0); RL_NEW_TAG_SOURCES=(v0.2.0-rc.9); rl_push_new_tags"
check_status "a failed push leaves no tag" 1 rl_tag_exists v0.3.0
git init -q --bare "${repo}.origin"
git remote add origin "${repo}.origin"
DRY_RUN=false rl_push_new_tags >/dev/null 2>&1
check "push tags the candidate commit" "$c3" "$(git rev-list -n 1 v0.3.0)"
check "push reaches origin" "1" "$(git ls-remote --tags origin refs/tags/v0.3.0 | wc -l | tr -d ' ')"

# rl_promote_single: the latest candidate here is v0.2.0-rc.10, already released as
# v0.2.0, so add a new one.
c6=$(commit "feat: another widget")
git tag -a v0.4.0-rc.0 -m rc "$c6"
git update-ref refs/remotes/origin/main "$c6"
single_out="$(mktemp -d)"
got="$(RC_TAG=latest DRY_RUN=true rl_promote_single v Thing "$single_out" 2>/dev/null)"
check "single dry run output" "final_tag=v0.4.0|previous_tag=v0.3.0|commit=${c6}" "$(tr '\n' '|' <<<"$got" | sed 's/|$//')"
check "single dry run tags nothing" "" "$(git tag -l v0.4.0)"
check "single notes" "1" "$(grep -c '^- feat: another widget' "${single_out}/notes.md")"
check "single release line" "v0.4.0|Thing 0.4.0|notes.md" "$(tr '\t' '|' < "${single_out}/releases.tsv")"
RL_NEW_TAGS=(); RL_NEW_TAG_SOURCES=()
RC_TAG=latest DRY_RUN=false rl_promote_single v Thing "$single_out" >/dev/null 2>&1
check "single real run tags the candidate" "$c6" "$(git rev-list -n 1 v0.4.0)"
check_status "single refuses a released candidate" 1 env RC_TAG=v0.4.0-rc.0 bash -c "source '${here}/release-lib.sh'; rl_promote_single v Thing '${single_out}'"
check_status "single refuses a repository without candidates" 1 env RC_TAG=latest bash -c "source '${here}/release-lib.sh'; rl_promote_single none-v Thing '${single_out}'"
rm -rf "$single_out"
git remote remove origin
rm -rf "${repo}.origin"
rl_checkout_at "$c3"
check "checkout_at makes a worktree" "$c3" "$(git -C "$RL_CHECKOUT" rev-parse HEAD)"
rl_remove_worktrees
check "remove_worktrees cleans up" "1" "$(git worktree list | wc -l | tr -d ' ')"
out="$(mktemp -d)"
rl_add_release "$out" v0.3.0 "Title with spaces" notes.md
check "add_release writes a tsv line" "v0.3.0|Title with spaces|notes.md" "$(tr '\t' '|' < "$out/releases.tsv")"
rm -rf "$out"

if [ "$failures" -ne 0 ]; then
  echo "${failures} test(s) failed"
  exit 1
fi
echo "all release-lib tests passed"
