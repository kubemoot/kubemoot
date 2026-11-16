#!/usr/bin/env bash
# Tests for run-native-check.sh with a fake binary that logs a self-test result, serves
# /q/health/live and /tools, and can exit early, as chosen by FAKE_* variables.
# Usage: bash .github/scripts/test-run-native-check.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/run-native-check.sh"

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
port=$((20000 + RANDOM % 10000))
export NATIVE_CHECK_PORT="$port" NATIVE_CHECK_STUB_PORT=$((port + 1)) NATIVE_CHECK_START_SECONDS=5

# The fake binary. FAKE_SELFTEST: ok | failed | none. FAKE_COUNT: the tools count it reports.
# FAKE_EXIT: when set, exits at once with that status. FAKE_HANG: never answers.
cat > "${work}/fake-runner" <<'EOF2'
#!/usr/bin/env python3
import os, sys, time
from http.server import BaseHTTPRequestHandler, HTTPServer
if os.environ.get("FAKE_EXIT"):
    print("fatal: cannot start", flush=True)
    sys.exit(int(os.environ["FAKE_EXIT"]))
mode = os.environ.get("FAKE_SELFTEST", "ok")
if mode == "ok":
    print("NATIVE_SELFTEST_OK checks=12", flush=True)
elif mode == "failed":
    print("NATIVE_SELFTEST_FAILED yaml mapping: java.lang.NoClassDefFoundError", flush=True)
if os.environ.get("FAKE_HANG"):
    time.sleep(60)
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        body = b"{}" if self.path.startswith("/q/") else ('{"count":%s,"tools":[]}' % os.environ.get("FAKE_COUNT", "2")).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
HTTPServer(("127.0.0.1", int(os.environ["QUARKUS_HTTP_PORT"])), H).serve_forever()
EOF2
chmod +x "${work}/fake-runner"

# run: prints the script's exit status; its output goes to run.log.
run() { bash "$script" "${work}/fake-runner" >"${work}/run.log" 2>&1 && echo 0 || echo $?; }

check "a binary with a passing self-test and two tools passes" 0 "$(run)"
check "the pass says what it checked" 1 "$(grep -c 'Native check passed: self-test ok, 2 MCP tools' "${work}/run.log")"

export FAKE_SELFTEST=failed
check "a failed self-test fails the check" 1 "$(run)"
check "the failure shows the failing check" 1 "$(grep -c 'NATIVE_SELFTEST_FAILED yaml mapping' "${work}/run.log")"
check "the failure says what it means" 1 "$(grep -c 'the native self-test failed' "${work}/run.log")"

export FAKE_SELFTEST=none
check "a missing self-test result fails the check" 1 "$(run)"
check "the failure says the marker is missing" 1 "$(grep -c 'no NATIVE_SELFTEST_OK' "${work}/run.log")"

export FAKE_SELFTEST=ok FAKE_COUNT=0
check "no tools from the stub fails the check" 1 "$(run)"
check "the failure names the counts" 1 "$(grep -c 'listed 0 MCP tools from the stub, want 2' "${work}/run.log")"

export FAKE_COUNT=1
check "fewer tools than the stub serves fails the check" 1 "$(run)"
unset FAKE_COUNT

export FAKE_EXIT=3
check "a binary that exits during startup fails the check" 1 "$(run)"
check "the failure says it exited" 1 "$(grep -c 'exited during startup' "${work}/run.log")"
unset FAKE_EXIT

export FAKE_HANG=1
check "a binary that never answers fails after the wait" 1 "$(run)"
check "the failure names the wait" 1 "$(grep -c 'did not answer /q/health/live within 5s' "${work}/run.log")"
unset FAKE_HANG

check "no argument is a usage error" 1 "$(bash "$script" >"${work}/run.log" 2>&1 && echo 0 || echo $?)"
check "a missing binary is a usage error" 1 "$(bash "$script" "${work}/nope" >"${work}/run.log" 2>&1 && echo 0 || echo $?)"

if [ "$failures" -ne 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all checks passed"
