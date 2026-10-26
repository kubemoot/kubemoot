#!/usr/bin/env bash
# Tests for the pure helpers in lib.sh and the ModelServer smoke check.
# Usage: bash k8s/tests/lib.test.sh   (exit 0 = all passed)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=k8s/tests/lib.sh
source "${here}/lib.sh"
# shellcheck source=k8s/tests/lib-modelserver.sh
source "${here}/lib-modelserver.sh"
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

# A ModelServer item: name type ready(true|false) spec-json-fragment status-json-fragment
ms() {
  printf '{"metadata":{"name":"%s"},"spec":{"type":"%s"%s},"status":{"phase":"%s","ready":%s%s}}' \
    "$1" "$2" "${4:+,$4}" "$([ "$3" = true ] && echo Ready || echo Pending)" "$3" "${5:+,$5}"
}
items() { local IFS=,; printf '{"items":[%s]}' "$*"; }

ollama_only=$(items "$(ms ollama-rig0 ollama false '"endpoint":"http://ollama.rig0:11434"')" \
  "$(ms ollama-cpu ollama true '"endpoint":"http://ollama.cpu:11434"')")
vllm_only=$(items "$(ms vllm-sleepy vllm false '"servers":[{"model":"a","endpoint":"http://a:8000"}]')" \
  "$(ms vllm-listed vllm true '"servers":[{"model":"q","endpoint":"http://vllm-q.models:8000"},{"model":"r","endpoint":"http://r:8000"}]')")
vllm_discovered=$(items "$(ms vllm-svc vllm true '"serviceRef":{"name":"kubemoot-vllm"}' '"vllmServers":[{"model":"q","endpoint":"http://found.models:8000"}]')")

check "Ollama-only cluster: the Ready Ollama server" "ollama-cpu ollama http://ollama.cpu:11434 open" \
  "$(first_ready_server <<<"$ollama_only")"
check "vLLM-only cluster: the Ready vLLM server's first endpoint" "vllm-listed vllm http://vllm-q.models:8000 open" \
  "$(first_ready_server <<<"$vllm_only")"
check "vLLM server found through serviceRef uses the discovered endpoint" "vllm-svc vllm http://found.models:8000 open" \
  "$(first_ready_server <<<"$vllm_discovered")"
check "a server without a secretRef is preferred" "plain ollama http://plain:11434 open" \
  "$(first_ready_server <<<"$(items "$(ms locked ollama true '"endpoint":"http://locked:11434","secretRef":"k"')" "$(ms plain ollama true '"endpoint":"http://plain:11434"')")")"
check "a Ready server with a secretRef is still picked when it is the only one" "locked ollama http://locked:11434 secret" \
  "$(first_ready_server <<<"$(items "$(ms locked ollama true '"endpoint":"http://locked:11434","secretRef":"k"')")")"
check "nothing when no server is Ready" "" \
  "$(first_ready_server <<<"$(items "$(ms a ollama false '"endpoint":"http://a:11434"')")")"
check "nothing for a Ready Ollama server with no endpoint" "" \
  "$(first_ready_server <<<"$(items "$(ms a ollama true '')")")"
check "nothing for a Ready vLLM server serving nothing yet" "" \
  "$(first_ready_server <<<"$(items "$(ms a vllm true '"serviceRef":{"name":"x"}' '"vllmServers":[]')")")"
check "nothing from no servers" "" "$(first_ready_server <<<'{"items":[]}')"
check "nothing from an empty body" "" "$(first_ready_server <<<'{}')"
check "server count" "2" "$(count_servers <<<"$ollama_only")"
check "server count of none" "0" "$(count_servers <<<'{"items":[]}')"

check "models list with two models" "2" "$(models_list_count <<<'{"object":"list","data":[{"id":"a"},{"id":"b"}]}')"
check "models list with no models" "0" "$(models_list_count <<<'{"data":[]}')"
check "an HTML error page is not a models list" "" "$(models_list_count <<<'<html>502</html>' 2>/dev/null || true)"
check "a body without data is not a models list" "" "$(models_list_count <<<'{"error":"nope"}' 2>/dev/null || true)"

# The check itself, with kubectl and the models request replaced by stubs.
STUB_SERVERS='{"items":[]}'
STUB_MODELS='{"data":[{"id":"m"}]}'
STUB_KUBECTL_FAIL=""
kubectl() { [ -z "$STUB_KUBECTL_FAIL" ] || { echo "$STUB_KUBECTL_FAIL" >&2; return 1; }; printf '%s' "$STUB_SERVERS"; }
fetch_models_list() { [ -n "$STUB_MODELS" ] && printf '%s' "$STUB_MODELS"; }
run_check() { # servers-json models-body -> "passed failed skipped"
  TESTS_PASSED=0 TESTS_FAILED=0 TESTS_SKIPPED=0
  STUB_SERVERS="$1" STUB_MODELS="$2"
  modelserver_smoke_check >/dev/null
  echo "$TESTS_PASSED $TESTS_FAILED $TESTS_SKIPPED"
}
check "check passes on an Ollama-only cluster" "2 0 0" "$(run_check "$ollama_only" '{"data":[{"id":"m"}]}')"
check "check passes on a vLLM-only cluster" "2 0 0" "$(run_check "$vllm_only" '{"data":[{"id":"m"}]}')"
check "check fails when the server answers no models list" "1 1 0" "$(run_check "$vllm_only" '')"
check "check fails when the server answers a body that is not a list" "1 1 0" "$(run_check "$ollama_only" '{"error":"x"}')"
check "check fails when servers exist and none is Ready" "0 1 0" \
  "$(run_check "$(items "$(ms a ollama false '"endpoint":"http://a:11434"')")" '{"data":[]}')"
locked_only=$(items "$(ms locked ollama true '"endpoint":"http://locked:11434","secretRef":"k"')")
fetched=0
fetch_models_list() { fetched=$((fetched + 1)); printf '%s' "$STUB_MODELS"; }
check "a server with a secretRef passes Ready and skips the models list" "1 0 1" "$(run_check "$locked_only" '{"data":[]}')"
fetch_models_list() { [ -n "$STUB_MODELS" ] && printf '%s' "$STUB_MODELS"; }
check "the models list is not requested for a secretRef server" "0" "$fetched"
check "check fails when the ModelServers cannot be listed" "0 1 0" \
  "$(STUB_KUBECTL_FAIL='Error from server (Forbidden)' run_check "$ollama_only" '{"data":[]}')"
STUB_KUBECTL_FAIL='Error from server (Forbidden)'
check "the listing error is in the failure" "1" "$(modelserver_smoke_check | grep -c 'Forbidden')"
STUB_KUBECTL_FAIL=""
check "check skips when there are no servers" "0 0 1" "$(run_check '{"items":[]}' '')"

[ "$failures" -eq 0 ] || { echo "${failures} failed"; exit 1; }
echo "all passed"
