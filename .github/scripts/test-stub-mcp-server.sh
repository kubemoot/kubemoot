#!/usr/bin/env bash
# Tests for stub-mcp-server.py: the SSE handshake, initialize, tools/list and tools/call over the
# transport the agent-runtime's MCP client uses, and its refusals.
# Usage: bash .github/scripts/test-stub-mcp-server.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${here}/stub-mcp-server.py"
port=$((20000 + RANDOM % 20000))
base="http://127.0.0.1:${port}"

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
python3 -I "$script" "$port" 2>"${work}/server.log" &
server=$!
sse=""
cleanup() { kill "$server" $sse 2>/dev/null || true; rm -rf "$work"; }
trap cleanup EXIT

for _ in $(seq 1 50); do
  curl -s -o /dev/null "${base}/nothing" && break
  sleep 0.1
done

curl -sN --max-time 20 "${base}/sse" >"${work}/stream" 2>/dev/null &
sse=$!
for _ in $(seq 1 50); do grep -q 'sessionId=' "${work}/stream" 2>/dev/null && break; sleep 0.1; done
endpoint="$(sed -n 's/^data: //p' "${work}/stream" | head -n 1)"
check "the stream announces a message endpoint" "/message?" "${endpoint%sessionId=*}"

post() { curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d "$1" "${base}${endpoint}"; }
wait_for() { for _ in $(seq 1 50); do grep -q "$1" "${work}/stream" && return 0; sleep 0.1; done; return 1; }
reply() { grep '^data: ' "${work}/stream" | grep "\"id\": $1[,}]" | head -n 1 | sed 's/^data: //'; }

check "initialize is accepted" 202 "$(post '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}')"
wait_for '"id": 1[,}]' || true
check "initialize echoes the protocol version" 2025-03-26 "$(reply 1 | jq -r .result.protocolVersion)"
check "a notification is accepted" 202 "$(post '{"jsonrpc":"2.0","method":"notifications/initialized"}')"
check "tools/list is accepted" 202 "$(post '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')"
wait_for '"id": 2[,}]' || true
check "tools/list names both tools" "echo list_pods" "$(reply 2 | jq -r '[.result.tools[].name] | join(" ")')"
check "every tool has an object input schema" "object object" "$(reply 2 | jq -r '[.result.tools[].inputSchema.type] | join(" ")')"
check "tools/call echo is accepted" 202 "$(post '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}')"
wait_for '"id": 3[,}]' || true
check "echo returns its text" hi "$(reply 3 | jq -r '.result.content[0].text')"
check "tools/call list_pods is accepted" 202 "$(post '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_pods"}}')"
wait_for '"id": 4[,}]' || true
check "list_pods returns YAML" "- name: web-1" "$(reply 4 | jq -r '.result.content[0].text' | head -n 1)"
check "an unknown tool is an error result" true "$(post '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope"}}' >/dev/null; wait_for '"id": 5[,}]' || true; reply 5 | jq -r .result.isError)"
check "an unknown method is a JSON-RPC error" -32601 "$(post '{"jsonrpc":"2.0","id":6,"method":"resources/list"}' >/dev/null; wait_for '"id": 6[,}]' || true; reply 6 | jq -r .error.code)"
check "a body that is not JSON is refused" 400 "$(post 'not json')"
check "an unknown session is refused" 404 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -d '{}' "${base}/message?sessionId=nope")"
check "ready answers once the server accepts connections" ok "$(curl -s "${base}/ready")"
check "an unknown path is refused" 404 "$(curl -s -o /dev/null -w '%{http_code}' "${base}/other")"
check "a missing port is a usage error" 64 "$(python3 -I "$script" >/dev/null 2>&1 && echo 0 || echo $?)"

if [ "$failures" -ne 0 ]; then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all checks passed"
