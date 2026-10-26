#!/usr/bin/env bash
# Tests for release-status.sh against a fake gh and a fake git that serve a canned release
# state per repository.
# Usage: bash .github/scripts/test-release-status.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"

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

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "${work}/bin" "${work}/state"

# Fake gh: `gh api repos/OWNER/REPO/releases/latest -q ...` prints state/REPO.latest (fails when
# the file is missing); `.../compare/...` prints state/REPO.compare ("<ahead>\t<subjects joined
# by a literal backslash-n>").
cat > "${work}/bin/gh" <<EOF2
#!/usr/bin/env bash
repo="\$(cut -d/ -f3 <<<"\$2")"
case "\$2" in
  */releases/latest) cat "${work}/state/\${repo}.latest" ;;
  */compare/*) cat "${work}/state/\${repo}.compare" ;;
  *) exit 2 ;;
esac
EOF2
# Fake git: `git ls-remote --tags --refs URL` prints state/REPO.tags.
cat > "${work}/bin/git" <<EOF2
#!/usr/bin/env bash
repo="\$(basename "\$4")"
cat "${work}/state/\${repo}.tags" 2>/dev/null
EOF2
chmod +x "${work}/bin/gh" "${work}/bin/git"

# A repository: name latest-tag ahead subjects(with \n between) tags(newline separated)
repo() {
  printf '%s\n' "$2" > "${work}/state/$1.latest"
  printf '%s\t%s\n' "$3" "$4" > "${work}/state/$1.compare"
  printf '%s' "$5" | sed 's|^|abc\trefs/tags/|' > "${work}/state/$1.tags"
}
run() { PATH="${work}/bin:${PATH}" bash "${here}/release-status.sh" "$@" 2>&1; }
status() { if PATH="${work}/bin:${PATH}" bash "${here}/release-status.sh" "$@" >/dev/null 2>&1; then echo 0; else echo $?; fi; }

repo kubemoot operator-chart-v0.94.0 2 'feat: a\nchore: b' $'operator-chart-v0.94.0\noperator-chart-v0.94.1-rc.2\noperator-chart-v0.94.1-rc.10\noperator-chart-v0.93.0-rc.3'
repo kmctl v0.18.0 3 'feat!: rename\nfix: x\ndocs: y' $'v0.18.0\nv0.18.1-rc.1'
repo kubemoot-docs v0.5.0 0 '' ''
printf '' > "${work}/state/crews.tags"

check "a current repository: counts and the newest candidate by version order" \
  "kubemoot: released operator-chart-v0.94.0; main is 2 commit(s) ahead, 1 user-facing; newest candidate operator-chart-v0.94.1-rc.10" \
  "$(run kubemoot)"
check "a current repository exits 0" "0" "$(status kubemoot kubemoot-docs)"
check "a repository with no candidate says none" \
  "kubemoot-docs: released v0.5.0; main is 0 commit(s) ahead, 0 user-facing; newest candidate none" \
  "$(run kubemoot-docs)"

out="$(run kmctl || true)"
check "an unpublished breaking change is named" "1" "$(grep -c 'UNPUBLISHED BREAKING: feat!: rename' <<<"$out")"
check "the breaking line counts the user-facing commits" "1" \
  "$(grep -c 'kmctl: released v0.18.0; main is 3 commit(s) ahead, 2 user-facing; newest candidate v0.18.1-rc.1' <<<"$out")"
check "an unpublished breaking change exits 1" "1" "$(status kmctl)"
check "one breaking repository among clean ones exits 1" "1" "$(status kubemoot kmctl kubemoot-docs)"
check "the report still lists the repositories after a breaking one" "1" \
  "$(run kmctl kubemoot-docs | grep -c '^kubemoot-docs: released')"

check "a scoped breaking prefix counts" "1" \
  "$( repo scoped v1.0.0 1 'fix(api)!: drop a field' 'v1.0.0'; run scoped | grep -c UNPUBLISHED )"
check "a BREAKING CHANGE footer is flagged" "1" \
  "$( repo body v1.0.0 1 'feat: new thing [BREAKING CHANGE in the body]' 'v1.0.0'; run body | grep -c UNPUBLISHED )"

repo none-yet '' 0 '' ''
rm -f "${work}/state/none-yet.latest"
check "a repository with no release is reported and does not fail" "none-yet: no published release" "$(run none-yet)"
check "a repository with no release exits 0" "0" "$(status none-yet)"

md="$(run --markdown kmctl kubemoot-docs || true)"
check "markdown lists a repository as a bullet" "1" "$(grep -c '^- kubemoot-docs: released v0.5.0' <<<"$md")"
check "markdown nests the breaking change" "1" "$(grep -c '^  - UNPUBLISHED BREAKING: feat!: rename' <<<"$md")"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
