#!/usr/bin/env bash
# Tests for go-build-static.sh: the link flags, the stamp check, the static check, and a
# whole run against a fake go that records its arguments and environment.
# Usage: bash .github/scripts/test-go-build-static.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=go-build-static.sh
source "${here}/go-build-static.sh"

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
# check_fails NAME CMD...: the command exits nonzero.
check_fails() {
  local name="$1"; shift
  if ( "$@" ) >/dev/null 2>&1; then check "$name" "fails" "succeeds"; else check "$name" "fails" "fails"; fi
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

check "link flags without stamps" "-s -w" "$(link_flags)"
check "link flags with a stamp" "-s -w -X example.com/m/internal/v.Version=0123abcd" \
  "$(link_flags example.com/m/internal/v.Version=0123abcd)"
check "link flags with two stamps" "-s -w -X a/b.X=1 -X a/b.Y=2" "$(link_flags a/b.X=1 a/b.Y=2)"

check_stamp() { if valid_stamp "$2"; then check "$1" "$3" valid; else check "$1" "$3" invalid; fi; }
check_stamp "stamp with an import path" "github.com/kubemoot/kubemoot/crew-liaison/internal/liaison.Version=0123abcd" valid
check_stamp "stamp with an empty value" "a/b.Version=" valid
check_stamp "stamp with a space in the value" "a/b.Version=1 2" invalid
check_stamp "stamp with a quote" 'a/b.Version="1"' invalid
check_stamp "stamp without a variable name" "a/b=1" invalid
check_stamp "stamp without =" "a/b.Version" invalid
check_stamp "stamp that is another flag" "-extldflags=-static" invalid

# A fake go: `build` records its arguments and cgo setting and writes the output file;
# `version -m` reports the cgo setting the build used, or FAKE_CGO when set.
cat > "${work}/go" <<EOF
#!/usr/bin/env bash
case "\$1" in
  build)
    printf '%s\n' "\$@" > "${work}/go.args"
    echo "CGO_ENABLED=\${CGO_ENABLED:-} GOOS=\${GOOS:-} GOARCH=\${GOARCH:-}" > "${work}/go.env"
    out=""; prev=""
    for a in "\$@"; do [ "\$prev" = "-o" ] && out="\$a"; prev="\$a"; done
    echo binary > "\$out" ;;
  version)
    printf '%s\tpath\texample.com/m\n\tbuild\tCGO_ENABLED=%s\n' "\$3" "\${FAKE_CGO:-\$(sed -n 's/^CGO_ENABLED=\([0-9]*\).*/\1/p' "${work}/go.env")}" ;;
esac
EOF
chmod +x "${work}/go"
run_main() { (cd "$work" && GO="${work}/go" main "$@"); }

run_main --x a/b.Version=0123abcd out1 ./cmd/x/ >/dev/null
check "builds the package into the output" "build -trimpath -ldflags -s -w -X a/b.Version=0123abcd -o out1 ./cmd/x/" \
  "$(paste -sd' ' "${work}/go.args")"
check "builds with cgo off for linux/amd64" "CGO_ENABLED=0 GOOS=linux GOARCH=amd64" "$(cat "${work}/go.env")"
check "the binary is written" binary "$(cat "${work}/out1")"
check "is_static on a cgo-off build" static "$(GO="${work}/go" is_static "${work}/out1" && echo static || echo dynamic)"
check "is_static on a cgo build" dynamic "$(FAKE_CGO=1 GO="${work}/go" is_static "${work}/out1" && echo static || echo dynamic)"

run_main out2 . >/dev/null
check "no stamps: only -s -w" "build -trimpath -ldflags -s -w -o out2 ." "$(paste -sd' ' "${work}/go.args")"
run_main --x a/b.X=1 --x a/b.Y=2 out2b . >/dev/null
check "two stamps through main" "build -trimpath -ldflags -s -w -X a/b.X=1 -X a/b.Y=2 -o out2b ." "$(paste -sd' ' "${work}/go.args")"

check_fails "refuses a binary built with cgo" env FAKE_CGO=1 bash -c "cd '$work' && GO='${work}/go' bash '${here}/go-build-static.sh' out3 ."
check_fails "refuses a bad stamp" run_main --x 'a/b.V=1 2' out4 .
check_fails "refuses a missing package" run_main out5
check_fails "refuses an extra argument" run_main out6 . ./more
check_fails "refuses an unknown option" run_main --nope out7 .

if [ "$failures" -gt 0 ]; then
  echo "${failures} test(s) failed"
  exit 1
fi
echo "all go-build-static tests passed"
