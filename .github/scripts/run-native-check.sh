#!/usr/bin/env bash
# Run the native agent-runtime binary against a stub MCP server and its own self-test.
#
# The native check needs no cluster: it starts stub-mcp-server.py on a local port, starts the
# binary with that server as its one MCP server and KUBEMOOT_NATIVE_SELFTEST=true, and fails
# unless
#   - the binary stays up and answers /q/health/live,
#   - its log holds NATIVE_SELFTEST_OK and no NATIVE_SELFTEST_FAILED (the self-test runs the code
#     paths native compilation can break: result type detection, YAML and JSON reading, record
#     binding; see NativeSelfTest.java), and
#   - /tools, which makes the binary connect to the stub, lists the stub's tools.
# The binary needs neither NATS nor a model server for any of this.
#
# Usage: run-native-check.sh RUNNER_BINARY
# Environment: NATIVE_CHECK_PORT (binary's HTTP port, default 18080), NATIVE_CHECK_STUB_PORT
# (default 18081), NATIVE_CHECK_TOOLS (tools the stub serves, default 2),
# NATIVE_CHECK_START_SECONDS (wait for the binary to answer, default 120).
# Tested by test-run-native-check.sh.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
PORT="${NATIVE_CHECK_PORT:-18080}"
STUB_PORT="${NATIVE_CHECK_STUB_PORT:-18081}"
WANT_TOOLS="${NATIVE_CHECK_TOOLS:-2}"
START_SECONDS="${NATIVE_CHECK_START_SECONDS:-120}"

fail() { echo "::error::native check: $*" >&2; exit 1; }

# judge_log LOG: fails unless the self-test logged success and no failure.
judge_log() {
  if grep -q 'NATIVE_SELFTEST_FAILED' "$1"; then
    fail "the native self-test failed; a code path that depends on native-image configuration threw"
  fi
  grep -q 'NATIVE_SELFTEST_OK' "$1" || fail "the native self-test did not report success (no NATIVE_SELFTEST_OK in the log)"
}

# tool_count JSON: the "count" of the /tools answer, 0 when absent.
tool_count() {
  local n
  n="$(printf '%s' "$1" | grep -o '"count":[0-9]*' | head -n 1 | cut -d: -f2 || true)"
  echo "${n:-0}"
}

# wait_live PID LOG: waits until /q/health/live answers; fails when the binary exits or the wait ends.
wait_live() {
  local pid="$1" log="$2" waited=0
  until curl -sf "http://localhost:${PORT}/q/health/live" >/dev/null; do
    if ! kill -0 "$pid" 2>/dev/null; then
      cat "$log"
      fail "the native binary exited during startup"
    fi
    if [ "$waited" -ge "$START_SECONDS" ]; then
      cat "$log"
      fail "the native binary did not answer /q/health/live within ${START_SECONDS}s"
    fi
    sleep 1
    waited=$((waited + 1))
  done
}

# Held outside main so the EXIT trap, which runs after main returns, can still stop them.
work="" stub=0 runner=0
cleanup() {
  kill "$stub" "$runner" 2>/dev/null || true
  [ -z "$work" ] || rm -rf "$work"
}

main() {
  [ $# -eq 1 ] && [ -x "$1" ] || fail "usage: run-native-check.sh RUNNER_BINARY (an executable)"
  local binary tools count
  binary="$(readlink -f "$1")"
  work="$(mktemp -d)"
  python3 -I "${here}/stub-mcp-server.py" "$STUB_PORT" >"${work}/stub.log" 2>&1 &
  stub=$!
  trap cleanup EXIT

  KUBEMOOT_MCP_SERVERS_0__NAME=check \
  KUBEMOOT_MCP_SERVERS_0__ENDPOINT="http://127.0.0.1:${STUB_PORT}" \
  KUBEMOOT_NATIVE_SELFTEST=true \
  QUARKUS_HTTP_PORT="$PORT" \
  KUBEMOOT_AGENT_NAME=native-check \
  KUBEMOOT_AGENT_DESCRIPTION="native image check" \
    "$binary" >"${work}/runner.log" 2>&1 &
  runner=$!

  wait_live "$runner" "${work}/runner.log"
  # The tools endpoint initializes the MCP client, which connects to the stub.
  tools="$(curl -sf "http://localhost:${PORT}/tools")" || { cat "${work}/runner.log"; fail "the native binary did not answer /tools"; }
  kill "$runner" 2>/dev/null || true
  cat "${work}/runner.log"
  judge_log "${work}/runner.log"
  echo "tools: ${tools}"
  count="$(tool_count "$tools")"
  [ "$count" -eq "$WANT_TOOLS" ] || fail "the native binary listed ${count} MCP tools from the stub, want ${WANT_TOOLS}"
  echo "Native check passed: self-test ok, ${count} MCP tools listed from the stub server."
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
