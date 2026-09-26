#!/bin/bash
# Test Internal Agents - Validates internal agents deployed via Helm
# Tests the internal agents created by the kubemoot-operator chart

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib.sh"

TEST_NAME="InternalAgents"

parse_common_args "$@"

# Cleanup function
cleanup() {
    log_info "Cleanup not needed for internal agents (managed by Helm)"
}

# Test 1: Check ModelProvider exists
test_modelprovider() {
    log_section "Test 1: Checking shared ModelProvider (ollama-gpu)"

    if kubectl get modelprovider ollama-gpu -n "${NAMESPACE}" > /dev/null 2>&1; then
        log_ok "ModelProvider 'ollama-gpu' exists"

        # Verify endpoint
        local endpoint
        endpoint=$(kubectl get modelprovider ollama-gpu -n "${NAMESPACE}" -o jsonpath='{.spec.endpoint}')
        log_info "Endpoint: ${endpoint}"

        if [[ "${endpoint}" == *"ollama"* ]]; then
            log_ok "Endpoint points to Ollama service"
        else
            log_warn "Unexpected endpoint configuration"
        fi
    else
        log_fail "ModelProvider 'ollama-gpu' not found"
        log_info "Internal agents may be disabled in values.yaml"
        return 1
    fi
}

# Test 2: Check Model exists
test_model() {
    log_section "Test 2: Checking shared Model (quality-eval-model)"

    if kubectl get model quality-eval-model -n "${NAMESPACE}" > /dev/null 2>&1; then
        log_ok "Model 'quality-eval-model' exists"

        # Verify provider reference
        local provider
        provider=$(kubectl get model quality-eval-model -n "${NAMESPACE}" -o jsonpath='{.spec.providerRef}')
        log_info "Provider ref: ${provider}"

        if [[ "${provider}" == "ollama-gpu" ]]; then
            log_ok "Model references correct provider"
        else
            log_warn "Model references unexpected provider: ${provider}"
        fi

        # Verify model name
        local model_name
        model_name=$(kubectl get model quality-eval-model -n "${NAMESPACE}" -o jsonpath='{.spec.model}')
        log_info "Model name: ${model_name}"
    else
        log_fail "Model 'quality-eval-model' not found"
        return 1
    fi
}

# Test 3: Check Quality Evaluator Agent
test_quality_evaluator_agent() {
    log_section "Test 3: Checking MCP Quality Evaluator Agent"

    if kubectl get agent mcp-quality-evaluator -n "${NAMESPACE}" > /dev/null 2>&1; then
        log_ok "Agent 'mcp-quality-evaluator' exists"

        # Verify labels
        local internal_label
        internal_label=$(kubectl get agent mcp-quality-evaluator -n "${NAMESPACE}" -o jsonpath='{.metadata.labels.kubemoot\.ai/internal}')

        if [[ "${internal_label}" == "true" ]]; then
            log_ok "Agent has internal label"
        else
            log_warn "Agent missing kubemoot.ai/internal label"
        fi

        # Verify purpose label
        local purpose
        purpose=$(kubectl get agent mcp-quality-evaluator -n "${NAMESPACE}" -o jsonpath='{.metadata.labels.kubemoot\.ai/purpose}')
        log_info "Purpose: ${purpose}"
    else
        log_fail "Agent 'mcp-quality-evaluator' not found"
        return 1
    fi
}

# Test 4: Check Quality Evaluator Prompts ConfigMap
test_quality_evaluator_prompts() {
    log_section "Test 4: Checking Quality Evaluator Prompts ConfigMap"

    if kubectl get configmap quality-evaluator-prompts -n "${NAMESPACE}" > /dev/null 2>&1; then
        log_ok "ConfigMap 'quality-evaluator-prompts' exists"

        # Verify system prompt key exists
        local keys
        keys=$(kubectl get configmap quality-evaluator-prompts -n "${NAMESPACE}" -o jsonpath='{.data}' | jq -r 'keys[]' 2>/dev/null)

        if echo "${keys}" | grep -q "system-prompt.txt"; then
            log_ok "ConfigMap contains system-prompt.txt"
        else
            log_warn "ConfigMap missing system-prompt.txt key"
        fi
    else
        log_fail "ConfigMap 'quality-evaluator-prompts' not found"
        return 1
    fi
}

# Test 5: Check Catalog Discovery Agent
test_catalog_discovery_agent() {
    log_section "Test 5: Checking MCP Catalog Discovery Agent"

    if kubectl get agent mcp-catalog-discovery -n "${NAMESPACE}" > /dev/null 2>&1; then
        log_ok "Agent 'mcp-catalog-discovery' exists"

        # Verify labels
        local internal_label
        internal_label=$(kubectl get agent mcp-catalog-discovery -n "${NAMESPACE}" -o jsonpath='{.metadata.labels.kubemoot\.ai/internal}')

        if [[ "${internal_label}" == "true" ]]; then
            log_ok "Agent has internal label"
        else
            log_warn "Agent missing kubemoot.ai/internal label"
        fi

        # Verify purpose label
        local purpose
        purpose=$(kubectl get agent mcp-catalog-discovery -n "${NAMESPACE}" -o jsonpath='{.metadata.labels.kubemoot\.ai/purpose}')
        log_info "Purpose: ${purpose}"
    else
        log_fail "Agent 'mcp-catalog-discovery' not found"
        return 1
    fi
}

# Test 6: Check Catalog Discovery Prompts ConfigMap
test_catalog_discovery_prompts() {
    log_section "Test 6: Checking Catalog Discovery Prompts ConfigMap"

    if kubectl get configmap catalog-discovery-prompts -n "${NAMESPACE}" > /dev/null 2>&1; then
        log_ok "ConfigMap 'catalog-discovery-prompts' exists"

        # Verify system prompt key exists
        local keys
        keys=$(kubectl get configmap catalog-discovery-prompts -n "${NAMESPACE}" -o jsonpath='{.data}' | jq -r 'keys[]' 2>/dev/null)

        if echo "${keys}" | grep -q "system-prompt.txt"; then
            log_ok "ConfigMap contains system-prompt.txt"
        else
            log_warn "ConfigMap missing system-prompt.txt key"
        fi
    else
        log_fail "ConfigMap 'catalog-discovery-prompts' not found"
        return 1
    fi
}

# Test 7: Verify all resources have Helm labels
test_helm_labels() {
    log_section "Test 7: Verifying Helm labels on internal agents"

    local resources_checked=0
    local labels_ok=0

    for kind in "modelprovider/ollama-gpu" "model/quality-eval-model" "agent/mcp-quality-evaluator" "agent/mcp-catalog-discovery"; do
        local managed_by
        managed_by=$(kubectl get "${kind}" -n "${NAMESPACE}" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}' 2>/dev/null || echo "")
        resources_checked=$((resources_checked + 1))

        if [[ "${managed_by}" == "Helm" ]]; then
            labels_ok=$((labels_ok + 1))
        fi
    done

    log_info "Checked ${resources_checked} resources, ${labels_ok} have Helm managed-by label"

    if [[ "${labels_ok}" -eq "${resources_checked}" ]]; then
        log_ok "All internal agent resources have Helm labels"
    else
        log_warn "Some resources missing Helm labels (${labels_ok}/${resources_checked})"
    fi
}

# Main test execution
main() {
    log_header "Internal Agents Test Suite"

    # Handle cleanup-only mode
    if [[ "${CLEANUP_ONLY:-false}" == "true" ]]; then
        cleanup
        echo "Cleanup complete"
        exit 0
    fi

    # Ensure cleanup on exit
    if [[ "${DO_CLEANUP:-true}" == "true" ]]; then
        trap cleanup EXIT
    fi

    # Pre-flight checks
    log_section "Pre-flight checks"
    check_cluster
    check_namespace
    check_operator
    check_crd "agents.kubemoot.ai"
    check_crd "models.kubemoot.ai"
    check_crd "modelproviders.kubemoot.ai"

    # Run tests
    test_modelprovider
    test_model
    test_quality_evaluator_agent
    test_quality_evaluator_prompts
    test_catalog_discovery_agent
    test_catalog_discovery_prompts
    test_helm_labels

    # Final cleanup is handled by trap
    exit_with_results
}

main "$@"
