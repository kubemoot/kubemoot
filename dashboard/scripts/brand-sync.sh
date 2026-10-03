#!/usr/bin/env bash
# Keeps this component's copies of the Kubemoot brand files identical to the brand/
# folder of github.com/kubemoot/.github, the single source of the mark.
#
# brand.lock (next to this scripts/ directory) pins the source commit and lists every
# copy as "<sha256> <destination> <source>": the destination relative to the
# component, the source relative to brand/.
#
#   brand-sync.sh check             every copy exists and matches its sha256 in brand.lock
#   brand-sync.sh refresh [COMMIT]  copy every file again from COMMIT (default: the
#                                   pinned commit) and rewrite brand.lock
#
# Never edit a copy by hand: change the brand repository, then refresh.
# BRAND_LOCK overrides the lock file and BRAND_BASE_URL the place files are fetched
# from (default https://raw.githubusercontent.com/kubemoot/.github, then
# /<commit>/brand/<source>), for tests and mirrors.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
lock="${BRAND_LOCK:-$root/brand.lock}"
base_url="${BRAND_BASE_URL:-https://raw.githubusercontent.com/kubemoot/.github}"

die() {
  echo "brand-sync: $*" >&2
  exit 1
}

usage() {
  echo "usage: brand-sync.sh check | refresh [COMMIT]" >&2
  exit 2
}

sha256_of() {
  sha256sum "$1" | cut -d' ' -f1
}

is_commit() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]]
}

# A relative path that stays inside its directory.
is_safe_path() {
  [[ -n "$1" && "$1" != /* && "/$1/" != */../* ]]
}

# The lock's entry lines: everything but comments, blank lines, and the commit line.
entry_lines() {
  grep -Ev '^[[:space:]]*(#|$)|^[[:space:]]*commit([[:space:]]|$)' "$lock" || true
}

check_entry() {
  local sha="$1" dest="$2" src="$3" extra="$4"
  [[ "$sha" =~ ^[0-9a-f]{64}$ && -n "$src" && -z "$extra" ]] ||
    die "malformed entry in $lock, want '<sha256> <destination> <source>': $sha $dest $src $extra"
  is_safe_path "$dest" || die "destination must be a relative path inside the component: $dest"
  is_safe_path "$src" || die "source must be a relative path inside brand/: $src"
  [[ "$dest" != "brand.lock" && "$dest" != scripts/* ]] ||
    die "destination may not be the lock or a sync script: $dest"
}

# Validates the whole lock before anything reads a copy or writes a file, and prints its
# pinned commit: exactly one commit line, at least one entry, well-formed and distinct
# destinations.
validate_lock() {
  [[ -f "$lock" ]] || die "no lock file at $lock"
  local commits sha dest src extra
  commits="$(grep -Ec '^[[:space:]]*commit([[:space:]]|$)' "$lock" || true)"
  [[ "$commits" == 1 ]] || die "$lock needs exactly one 'commit <40-character sha>' line, found $commits"
  local commit
  commit="$(awk '$1 == "commit" { print $2 }' "$lock")"
  is_commit "$commit" || die "$lock needs one 'commit <40-character sha>' line"
  declare -A seen=()
  while read -r sha dest src extra; do
    check_entry "$sha" "$dest" "$src" "$extra"
    [[ -z "${seen[$dest]:-}" ]] || die "duplicate destination in $lock: $dest"
    seen[$dest]=1
  done < <(entry_lines)
  ((${#seen[@]} > 0)) || die "$lock lists no files"
  printf '%s\n' "$commit"
}

check() {
  local commit sha dest src extra count=0 drifted=0
  commit="$(validate_lock)"
  while read -r sha dest src extra; do
    count=$((count + 1))
    if [[ ! -f "$root/$dest" ]]; then
      echo "missing: $dest (brand/$src)" >&2
      drifted=1
    elif [[ "$(sha256_of "$root/$dest")" != "$sha" ]]; then
      echo "changed: $dest no longer matches brand/$src at ${commit:0:12}" >&2
      drifted=1
    fi
  done < <(entry_lines)
  ((drifted == 0)) || die "brand copies differ from brand.lock; run scripts/brand-sync.sh refresh instead of editing a copy"
  echo "brand-sync: $count files match brand.lock (kubemoot/.github@${commit:0:12})"
}

# Writes FILE to TARGET through a temporary file beside it, so TARGET is never half written.
install_file() {
  local file="$1" target="$2"
  mkdir -p "$(dirname "$target")"
  cp "$file" "$target.brand-sync.tmp"
  mv -f "$target.brand-sync.tmp" "$target"
}

refresh() {
  local pinned commit
  pinned="$(validate_lock)"
  commit="${1:-$pinned}"
  is_commit "$commit" || die "refresh needs a full 40-character commit sha, got '$commit'"

  local staging line sha dest src extra count=0
  staging="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand now: the trap must remove this run's directory
  trap "rm -rf '$staging'" EXIT

  # Fetch everything first, so a failed download leaves the copies and the lock untouched.
  while read -r sha dest src extra; do
    mkdir -p "$staging/files/$(dirname "$dest")"
    curl -fsSL "$base_url/$commit/brand/$src" -o "$staging/files/$dest" ||
      die "could not fetch brand/$src at $commit"
    [[ -s "$staging/files/$dest" ]] || die "brand/$src at $commit came back empty"
    count=$((count + 1))
  done < <(entry_lines)

  # Rewrite the lock in place of the old one: comments and order stay, hashes and commit change.
  while IFS= read -r line; do
    read -r sha dest src extra <<<"$line" || true
    if [[ "$line" =~ ^[[:space:]]*(#|$) ]]; then
      printf '%s\n' "$line"
    elif [[ "$sha" == "commit" ]]; then
      printf 'commit %s\n' "$commit"
    else
      printf '%s %s %s\n' "$(sha256_of "$staging/files/$dest")" "$dest" "$src"
    fi
  done <"$lock" >"$staging/brand.lock"

  # Each copy is replaced whole, and the lock last: an interrupted run leaves a lock that
  # check reports against, never a half-written file.
  while read -r sha dest src extra; do
    install_file "$staging/files/$dest" "$root/$dest"
  done < <(entry_lines)
  install_file "$staging/brand.lock" "$lock"
  echo "brand-sync: copied $count files from kubemoot/.github@${commit:0:12}"
}

case "${1:-}" in
  check)
    [[ $# -eq 1 ]] || usage
    check
    ;;
  refresh)
    [[ $# -le 2 ]] || usage
    refresh "${2:-}"
    ;;
  *) usage ;;
esac
