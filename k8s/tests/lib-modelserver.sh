#!/usr/bin/env bash
# Helpers for the smoke test's ModelServer check. Sourced by test-smoke.sh after lib.sh (the
# log_* functions and $NAMESPACE); tested by lib.test.sh. Needs jq and curl.

# The first Ready ModelServer of any engine that has an address, as "<name> <type> <url>".
# Reads `kubectl get modelservers -o json` on stdin. The address is spec.endpoint for an
# Ollama server and the first served endpoint for a vLLM server (spec.servers, or the ones
# the operator found from spec.serviceRef). A server without a secretRef is preferred, since
# the check sends no credentials. Prints nothing when no Ready server has an address.
first_ready_server() {
    jq -r '
        def url: if .spec.type == "vllm"
            then ((if .spec.serviceRef then .status.vllmServers else .spec.servers end) // [] | .[0].endpoint // "")
            else (.spec.endpoint // "") end;
        [.items[]? | select(.status.phase == "Ready" and .status.ready == true)
            | {n: .metadata.name, t: .spec.type, u: url, s: ((.spec.secretRef // "") != "")}
            | select(.u != "")]
        | sort_by(.s) | .[0] // empty | "\(.n) \(.t) \(.u)"'
}

# The number of ModelServers in `kubectl get modelservers -o json` on stdin.
count_servers() {
    jq -r '.items | length'
}

# The number of models in an OpenAI-style models list (GET /v1/models, which vLLM and Ollama
# both answer) on stdin. Prints nothing and fails when the body is not a models list.
models_list_count() {
    jq -e -r 'if (.data | type) == "array" then .data | length else empty end'
}

# The models list of the server at $1 (a base URL without /v1).
fetch_models_list() {
    curl -fsS --max-time 15 "${1%/}/v1/models"
}

# The ModelServer check every engine can pass: some ModelServer is Ready, it answers its
# models list, and the operator published it as Ready. Fails when ModelServers exist and
# none is Ready, and skips when the namespace has none.
modelserver_smoke_check() {
    local json count picked name type url body models
    json=$(kubectl get modelservers -n "$NAMESPACE" -o json 2>/dev/null || echo '{"items":[]}')
    count=$(count_servers <<<"$json")
    if [ "$count" -eq 0 ]; then
        log_skip "ModelServer check: no ModelServer in $NAMESPACE"
        return 0
    fi
    picked=$(first_ready_server <<<"$json")
    if [ -z "$picked" ]; then
        log_fail "ModelServer check: $count ModelServer(s) in $NAMESPACE and none is Ready with an address"
        return 0
    fi
    read -r name type url <<<"$picked"
    log_ok "ModelServer $name ($type) is Ready and the operator published it as Ready"
    if body=$(fetch_models_list "$url") && models=$(models_list_count <<<"$body"); then
        log_ok "ModelServer $name answers its models list at $url ($models models)"
    else
        log_fail "ModelServer $name does not answer a models list at $url/v1/models"
    fi
}
