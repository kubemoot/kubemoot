#!/usr/bin/env bash
# Tests fuzz-targets.sh against a fixture module tree.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/fuzz-targets.sh"
failures=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; failures=$((failures + 1)); fi; }

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
mkdir -p "$work/a" "$work/b/c" "$work/d"
printf 'package a\nfunc FuzzParse(f *testing.F) {}\nfunc FuzzEncode(f *testing.F) {}\nfunc TestX(t *testing.T) {}\n' > "$work/a/a_test.go"
printf 'package c\nfunc FuzzDeep(f *testing.F) {}\n' > "$work/b/c/c_test.go"
printf 'package d\n// func FuzzNot(f *testing.F) in a comment\nfunc FuzzReal(f *testing.F) {}\n' > "$work/d/d.go"
printf 'package d\n  func FuzzIndented(f *testing.F) {}\n' > "$work/d/d_test.go"

got="$(cd "$work" && bash "$script" list | tr '\n' ';')"
check "lists each target with its package directory, from test files only" "./a FuzzEncode;./a FuzzParse;./b/c FuzzDeep;" "$got"

mkdir -p "$work/empty"
check "run with no targets fails" "1" "$(cd "$work/empty" && bash "$script" run 1s >/dev/null 2>&1 && echo 0 || echo $?)"
check "run needs a fuzz time" "1" "$(cd "$work" && bash "$script" run >/dev/null 2>&1 && echo 0 || echo $?)"
check "an unknown command is a usage error" "64" "$(cd "$work" && bash "$script" nope >/dev/null 2>&1 && echo 0 || echo $?)"

# run invokes go test once per target with the fuzz time
mkdir -p "$work/bin"
printf '#!/usr/bin/env bash\necho "$*" >> "%s/calls"\n' "$work" > "$work/bin/go"; chmod +x "$work/bin/go"
(cd "$work" && PATH="$work/bin:$PATH" bash "$script" run 7s >/dev/null)
check "run fuzzes every target once" "3" "$(wc -l < "$work/calls" | tr -d ' ')"
check "run passes the fuzz time and the package" "1" "$(grep -c -- '-fuzz=^FuzzDeep\$ -fuzztime=7s ./b/c' "$work/calls")"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
