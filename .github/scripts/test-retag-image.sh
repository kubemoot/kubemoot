#!/usr/bin/env bash
# Tests for retag-image.sh against a fake crane that knows a set of image references and
# records every call.
# Usage: bash .github/scripts/test-retag-image.sh   (exit 0 = all passed)
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

# The fake crane: `manifest REF` succeeds when REF is listed in known; every call is logged.
cat > "${work}/crane" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "${work}/calls"
case "\$1" in
  manifest) grep -qxF -- "\$3" "${work}/known" ;;
  copy) exit 0 ;;
  *) exit 3 ;;
esac
EOF
chmod +x "${work}/crane"

sha=0123456789abcdef0123456789abcdef01234567
img=reg.example/kubemoot/mcp-bridge
run() { : > "${work}/calls"; CRANE="${work}/crane" bash "${here}/retag-image.sh" "$@" >/dev/null 2>&1; }
status() { if run "$@"; then echo 0; else echo $?; fi; }

printf '%s\n' "${img}:${sha}" "${img}:latest" > "${work}/known"
check "retags the :<sha> image" 0 "$(status "$img" "$sha" 0.4.1-rc.2)"
check "copies :<sha> to the version" "copy --insecure ${img}:${sha} ${img}:0.4.1-rc.2" "$(grep '^copy' "${work}/calls")"

printf '%s\n' "${img}:latest" > "${work}/known"
check "fails when :<sha> is missing" 1 "$(status "$img" "$sha" 0.4.1-rc.2)"
check "never falls back to :latest" 0 "$(grep -c '^copy' "${work}/calls" || true)"

check "a final version is accepted" 0 "$(printf '%s\n' "${img}:${sha}" > "${work}/known"; status "$img" "$sha" 1.2.3)"
check "refuses a short SHA" 2 "$(status "$img" 0123abcd 1.2.3)"
check "refuses a version that is not one" 2 "$(status "$img" "$sha" latest)"
check "refuses a version with a space" 2 "$(status "$img" "$sha" '1.2.3 x')"
check "refuses an empty image" 2 "$(status "" "$sha" 1.2.3)"
check "refuses a missing argument" 2 "$(status "$img" "$sha")"
check "no crane call on bad input" 0 "$(run "$img" short 1.2.3 || true; wc -l < "${work}/calls" | tr -d ' ')"

if [ "$failures" -gt 0 ]; then
  echo "${failures} test(s) failed"
  exit 1
fi
echo "all retag-image tests passed"
