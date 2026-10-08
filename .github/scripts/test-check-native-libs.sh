#!/usr/bin/env bash
# Tests for check-native-libs.sh: parsing ldd output, matching libraries against an image
# file list, and whole runs against a fake ldd and a fake crane.
# Usage: BUILD_IMAGE_DIR=<release-actions>/build-image bash .github/scripts/test-check-native-libs.sh
#   (exit 0 = all passed; the release-actions setup action exports BUILD_IMAGE_DIR)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=check-native-libs.sh
source "${here}/check-native-libs.sh"

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

# ldd output of a dynamically linked Quarkus native binary.
cat > "${work}/ldd-ok.txt" <<'EOF'
	linux-vdso.so.1 (0x00007ffd5a3e2000)
	libz.so.1 => /lib/x86_64-linux-gnu/libz.so.1 (0x00007f2b1c000000)
	libc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f2b1bc00000)
	/lib64/ld-linux-x86-64.so.2 (0x00007f2b1c100000)
EOF
check "needed_libs lists libraries and the interpreter, not the vdso" \
  "libz.so.1 libc.so.6 /lib64/ld-linux-x86-64.so.2" \
  "$(needed_libs < "${work}/ldd-ok.txt" | tr '\n' ' ' | sed 's/ $//')"

printf '\tnot a dynamic executable\n' > "${work}/ldd-static.txt"
check "needed_libs: a static binary needs nothing" "" "$(needed_libs < "${work}/ldd-static.txt")"
printf '\tstatically linked\n' > "${work}/ldd-static2.txt"
check "needed_libs: statically linked needs nothing" "" "$(needed_libs < "${work}/ldd-static2.txt")"

printf '\tlibfoo.so.2 => not found\n\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x1)\n' > "${work}/ldd-bad.txt"
if needed_libs < "${work}/ldd-bad.txt" >/dev/null 2>&1; then got=succeeds; else got=fails; fi
check "needed_libs fails when the runner cannot resolve a library" fails "$got"
check "needed_libs names the unresolved library" "missing on the runner: libfoo.so.2" \
  "$(needed_libs < "${work}/ldd-bad.txt" 2>&1 >/dev/null || true)"

# The image file list as tar prints it: relative paths, with and without ./, symlinks
# listed by their own name.
cat > "${work}/files.txt" <<'EOF'
./usr/lib/x86_64-linux-gnu/libc.so.6
usr/lib/x86_64-linux-gnu/libz.so.1
usr/lib64/ld-linux-x86-64.so.2
etc/passwd
EOF
needed_libs < "${work}/ldd-ok.txt" > "${work}/needed-ok.txt"
check "missing_libs: every library present" "" "$(missing_libs "${work}/needed-ok.txt" "${work}/files.txt")"
printf 'libz.so.1\nlibssl.so.3\nlibstdc++.so.6\n' > "${work}/needed-more.txt"
check "missing_libs lists only the absent libraries" "libssl.so.3 libstdc++.so.6" \
  "$(missing_libs "${work}/needed-more.txt" "${work}/files.txt" | tr '\n' ' ' | sed 's/ $//')"
printf 'libz.so\n' > "${work}/needed-prefix.txt"
check "missing_libs matches whole names, not prefixes" "libz.so" \
  "$(missing_libs "${work}/needed-prefix.txt" "${work}/files.txt")"
: > "${work}/needed-none.txt"
check "missing_libs: nothing needed, nothing missing" "" "$(missing_libs "${work}/needed-none.txt" "${work}/files.txt")"

# glibc_versions and missing_glibc read version names out of the raw bytes.
printf 'x\0GLIBC_2.2.5\0GLIBC_2.34\0y\0GLIBC_2.41\0GLIBC_2.34\0' > "${work}/bin-glibc"
printf 'GLIBC_2.2.5\0GLIBC_2.34\0GLIBC_2.39\0GLIBC_PRIVATE\0' > "${work}/libc-noble"
check "glibc_versions lists each version once" "GLIBC_2.2.5 GLIBC_2.34 GLIBC_2.41" \
  "$(glibc_versions "${work}/bin-glibc" | tr '\n' ' ' | sed 's/ $//')"
check "glibc_versions: a file without versions gives nothing" "" "$(glibc_versions "${work}/needed-none.txt")"
check "missing_glibc names the version the libc lacks" "GLIBC_2.41" \
  "$(missing_glibc "${work}/bin-glibc" "${work}/libc-noble")"
check "missing_glibc: an older binary needs nothing more" "" \
  "$(missing_glibc "${work}/libc-noble" "${work}/libc-noble")"

# Whole runs. The fake ldd prints LDD_OUT and exits LDD_RC; the fake crane records its
# arguments and writes, to its last argument, a tar of the files named in IMAGE_FILES
# (empty, except libc.so.6, which holds FAKE_LIBC).
mkdir -p "${work}/bin"
cat > "${work}/bin/ldd" <<'EOF'
#!/usr/bin/env bash
cat "$LDD_OUT"
exit "${LDD_RC:-0}"
EOF
cat > "${work}/bin/crane" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" > "$CRANE_ARGS"
root="$(mktemp -d)"
while IFS= read -r f; do mkdir -p "${root}/$(dirname "$f")"; : > "${root}/${f}"; done < "$IMAGE_FILES"
find "$root" -name libc.so.6 -exec cp "$FAKE_LIBC" {} \;
tar -C "$root" -cf "${*: -1}" .
rm -rf "$root"
EOF
chmod +x "${work}/bin/ldd" "${work}/bin/crane"
cat > "${work}/images.yaml" <<'EOF'
apiVersion: v1
kind: List
items:
  - name: run-tiny
    image: example.org/run-tiny:1@sha256:abcd
  - name: unpinned
    image: example.org/run:latest
EOF
printf 'ELF\0GLIBC_2.2.5\0GLIBC_2.34\0' > "${work}/app-runner"
cp "${work}/bin-glibc" "${work}/app-runner-new-glibc"
printf 'usr/lib/x86_64-linux-gnu/libc.so.6\nusr/lib/x86_64-linux-gnu/libz.so.1\nusr/lib64/ld-linux-x86-64.so.2\n' \
  > "${work}/image-files.txt"
printf 'usr/lib/x86_64-linux-gnu/libz.so.1\n' > "${work}/image-files-nolibc.txt"

# run_main LDD_OUTPUT_FILE [ARGS...]: runs the script in a subshell; prints its exit status.
# The caller may set LDD_RC and IMAGE_FILES.
run_main() {
  local out="$1"; shift
  ( LDD="${work}/bin/ldd" CRANE="${work}/bin/crane" LDD_OUT="$out" LDD_RC="${LDD_RC:-0}" \
    IMAGE_FILES="${IMAGE_FILES:-${work}/image-files.txt}" CRANE_ARGS="${work}/crane-args.txt" \
    FAKE_LIBC="${work}/libc-noble" IMAGES_FILE="${work}/images.yaml" \
    bash "${here}/check-native-libs.sh" "$@" ) \
    > "${work}/run.log" 2>&1 && echo 0 || echo $?
}

check "a binary whose libraries are all in the image passes" 0 "$(run_main "${work}/ldd-ok.txt" "${work}/app-runner")"
check "the pinned run image is exported for linux/amd64" "export --platform linux/amd64 example.org/run-tiny:1@sha256:abcd" \
  "$(sed 's/ [^ ]*$//' "${work}/crane-args.txt")"
check "a binary needing a newer GLIBC than the image fails" 1 \
  "$(run_main "${work}/ldd-ok.txt" "${work}/app-runner-new-glibc")"
check "the failure names the GLIBC version" 1 "$(grep -c '^GLIBC_2.41$' "${work}/run.log")"
check "an image without libc.so.6 fails for a binary that needs GLIBC" 1 \
  "$(IMAGE_FILES="${work}/image-files-nolibc.txt" run_main "${work}/ldd-static.txt" "${work}/app-runner")"
printf '\tlibssl.so.3 => /lib/x86_64-linux-gnu/libssl.so.3 (0x1)\n' >> "${work}/ldd-ok.txt"
check "a library missing from the image fails" 1 "$(run_main "${work}/ldd-ok.txt" "${work}/app-runner")"
check "the failure names the missing library" 1 "$(grep -c '^libssl.so.3$' "${work}/run.log")"
: > "${work}/app-static"
check "a static binary passes (ldd exits 1)" 0 "$(LDD_RC=1 run_main "${work}/ldd-static.txt" "${work}/app-static")"
printf 'ldd: command not found\n' > "${work}/ldd-missing.txt"
check "ldd exiting 127 fails" 1 "$(LDD_RC=127 run_main "${work}/ldd-missing.txt" "${work}/app-runner")"
check "the failure reports the ldd exit" 1 "$(grep -c 'ldd failed on .* (exit 127)' "${work}/run.log")"
printf '\tnot an ELF file\n' > "${work}/ldd-weird.txt"
check "an unexpected ldd error fails" 1 "$(LDD_RC=1 run_main "${work}/ldd-weird.txt" "${work}/app-runner")"
check "a library the runner cannot resolve fails" 1 "$(run_main "${work}/ldd-bad.txt" "${work}/app-runner")"
check "a missing binary fails" 1 "$(run_main "${work}/ldd-static.txt" "${work}/no-such-runner")"
check "no arguments fails" 1 "$(run_main "${work}/ldd-static.txt")"
check "an unpinned run image fails" 1 "$(run_main "${work}/ldd-static.txt" "${work}/app-runner" unpinned)"
check "an unknown run image fails" 1 "$(run_main "${work}/ldd-static.txt" "${work}/app-runner" nothing)"
if env -u BUILD_IMAGE_DIR bash "${here}/check-native-libs.sh" "${work}/app-runner" >/dev/null 2>&1; then
  got=succeeds
else
  got=fails
fi
check "without BUILD_IMAGE_DIR the script fails" fails "$got"

if [ "$failures" -gt 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all checks passed"
