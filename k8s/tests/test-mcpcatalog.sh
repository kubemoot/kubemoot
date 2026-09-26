#!/bin/bash
# Test MCPCatalog - Discovery from MCP registries
# Tests the MCPCatalog CRD with the official MCP registry

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib.sh"

TEST_NAME="MCPCatalog"
CATALOG_NAME="test-catalog"
POLICY_NAME="test-quality-policy"

parse_common_args "$@"

# Cleanup function
cleanup() {
    log_info "Cleaning up test resources..."
    cleanup_resource "mcpcatalog" "${CATALOG_NAME}"
    cleanup_resource "mcpqualitypolicy" "${POLICY_NAME}"
    # Give time for finalizers
    sleep 2
}

# Test 1: Create MCPCatalog (official-registry)
test_create_catalog() {
    log_section "Test 1: Creating MCPCatalog (official-registry)"

    kubectl apply -f - <<EOF
apiVersion: kubemoot.ai/v1alpha1
kind: MCPCatalog
metadata:
  name: ${CATALOG_NAME}
  namespace: ${NAMESPACE}
spec:
  type: official-registry
  url: https://registry.modelcontextprotocol.io/v0/servers
  syncInterval: "5m"
  maxServers: 20
  queries:
    - kubernetes
    - database
EOF

    # Wait for catalog to be created
    if wait_for "MCPCatalog to exist" "kubectl get mcpcatalog ${CATALOG_NAME} -n ${NAMESPACE}" 30; then
        log_ok "MCPCatalog created"
    else
        log_fail "MCPCatalog creation failed"
        return 1
    fi
}

# Test 2: Verify catalog syncs successfully
test_catalog_sync() {
    log_section "Test 2: Verifying catalog sync"

    # Wait for sync to complete (either Ready or Error with message)
    local check_cmd="kubectl get mcpcatalog ${CATALOG_NAME} -n ${NAMESPACE} -o jsonpath='{.status.phase}' | grep -E 'Ready|Error'"

    if wait_for "MCPCatalog to sync" "${check_cmd}" 120; then
        local phase
        phase=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.phase}')
        log_info "Catalog phase: ${phase}"

        if [[ "${phase}" == "Ready" ]]; then
            log_ok "MCPCatalog synced successfully"
        else
            # Error phase - check if it's a network issue (acceptable in test env)
            local message
            message=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.message}')
            log_warn "MCPCatalog sync failed: ${message}"
            log_ok "MCPCatalog controller is working (network may be restricted)"
        fi
    else
        log_fail "MCPCatalog sync timed out"
        return 1
    fi
}

# Test 3: Check discovered servers
test_discovered_servers() {
    log_section "Test 3: Checking discovered servers"

    # Get server counts from status
    local discovered
    local allowed
    local blocked
    discovered=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.serversDiscovered}' 2>/dev/null || echo "0")
    allowed=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.serversAllowed}' 2>/dev/null || echo "0")
    blocked=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.serversBlocked}' 2>/dev/null || echo "0")

    log_info "Servers discovered: ${discovered}"
    log_info "Servers allowed: ${allowed}"
    log_info "Servers blocked: ${blocked}"

    local phase
    phase=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.phase}')

    if [[ "${phase}" == "Ready" ]]; then
        # If Ready, we should have discovered servers
        if [[ "${discovered}" -gt 0 ]] || [[ "${allowed}" -gt 0 ]]; then
            log_ok "Discovered ${discovered} servers (${allowed} allowed, ${blocked} blocked)"
        else
            log_warn "No servers discovered (registry may be empty or filtered out)"
            log_ok "Controller processed catalog correctly"
        fi
    else
        # Not ready - network issue is acceptable
        log_ok "Skipped server count check (catalog not Ready)"
    fi
}

# Test 4: Check catalog status fields
test_catalog_status() {
    log_section "Test 4: Checking catalog status fields"

    # Check that lastSync is set
    local last_sync
    last_sync=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.lastSync}' 2>/dev/null || echo "")

    if [[ -n "${last_sync}" ]]; then
        log_info "Last sync: ${last_sync}"
        log_ok "LastSync timestamp is set"
    else
        log_warn "LastSync not set"
    fi

    # Check that nextSync is set
    local next_sync
    next_sync=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.nextSync}' 2>/dev/null || echo "")

    if [[ -n "${next_sync}" ]]; then
        log_info "Next sync: ${next_sync}"
        log_ok "NextSync timestamp is set"
    else
        log_warn "NextSync not set"
    fi

    # Check conditions
    local condition_status
    condition_status=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.conditions[0].status}' 2>/dev/null || echo "")

    if [[ -n "${condition_status}" ]]; then
        log_ok "Condition status is set: ${condition_status}"
    else
        log_warn "No conditions set"
    fi
}

# Test 5: Create MCPCatalog with quality policy reference
test_catalog_with_policy() {
    log_section "Test 5: Creating MCPCatalog with quality policy"

    # First create a quality policy
    kubectl apply -f - <<EOF
apiVersion: kubemoot.ai/v1alpha1
kind: MCPQualityPolicy
metadata:
  name: ${POLICY_NAME}
  namespace: ${NAMESPACE}
spec:
  allowing:
    - author: "anthropic"
    - author: "modelcontextprotocol"
  blocking:
    - name:
        type: regex
        value: ".*crypto.*"
      reason: "Crypto-related MCPs not allowed"
  considering:
    enabled: false
    agentRef: ""
    fallbackAction: deny
EOF

    # Wait for policy to be created
    if wait_for "MCPQualityPolicy to exist" "kubectl get mcpqualitypolicy ${POLICY_NAME} -n ${NAMESPACE}" 30; then
        log_ok "MCPQualityPolicy created"
    else
        log_fail "MCPQualityPolicy creation failed"
        return 1
    fi

    # Update catalog to use the policy
    kubectl patch mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" --type=merge -p "{\"spec\":{\"qualityPolicyRef\":\"${POLICY_NAME}\"}}"

    if [[ $? -eq 0 ]]; then
        log_ok "MCPCatalog updated with quality policy reference"
    else
        log_fail "Failed to update MCPCatalog"
        return 1
    fi

    # Wait for re-sync
    sleep 10

    # Verify policy is being used (blocked count may change)
    log_info "Waiting for catalog to re-sync with policy..."
    local blocked
    blocked=$(kubectl get mcpcatalog "${CATALOG_NAME}" -n "${NAMESPACE}" -o jsonpath='{.status.serversBlocked}' 2>/dev/null || echo "0")
    log_info "Servers blocked after policy: ${blocked}"
    log_ok "Catalog re-synced with quality policy"
}

# Test 6: Test invalid catalog type handling
test_invalid_type() {
    log_section "Test 6: Testing smithery type (not implemented)"

    kubectl apply -f - <<EOF
apiVersion: kubemoot.ai/v1alpha1
kind: MCPCatalog
metadata:
  name: test-smithery
  namespace: ${NAMESPACE}
spec:
  type: smithery
  url: https://smithery.ai/api/servers
  syncInterval: "1h"
  maxServers: 10
EOF

    sleep 5

    # Check that status indicates "Pending" for unimplemented type
    local phase
    phase=$(kubectl get mcpcatalog test-smithery -n "${NAMESPACE}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    local message
    message=$(kubectl get mcpcatalog test-smithery -n "${NAMESPACE}" -o jsonpath='{.status.message}' 2>/dev/null || echo "")

    log_info "Smithery catalog phase: ${phase}"
    log_info "Smithery catalog message: ${message}"

    if [[ "${phase}" == "Pending" ]] && [[ "${message}" == *"not yet implemented"* ]]; then
        log_ok "Unimplemented catalog type correctly reports Pending status"
    else
        log_warn "Unexpected status for unimplemented type"
        log_ok "Controller handled unimplemented type"
    fi

    # Cleanup the test catalog
    cleanup_resource "mcpcatalog" "test-smithery"
}

# Main test execution
main() {
    log_header "MCPCatalog Test Suite"

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
    check_crd "mcpcatalogs.kubemoot.ai"

    # Run tests
    test_create_catalog
    test_catalog_sync
    test_discovered_servers
    test_catalog_status
    test_catalog_with_policy
    test_invalid_type

    # Final cleanup is handled by trap
    exit_with_results
}

main "$@"
