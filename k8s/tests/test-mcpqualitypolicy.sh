#!/bin/bash
# Test MCPQualityPolicy - Quality and trust filtering for MCP servers
# Tests the MCPQualityPolicy CRD allowing/blocking/considering logic

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib.sh"

TEST_NAME="MCPQualityPolicy"
POLICY_NAME="test-quality-policy"
POLICY_NAME_FULL="test-full-policy"

parse_common_args "$@"

# Cleanup function
cleanup() {
    log_info "Cleaning up test resources..."
    cleanup_resource "mcpqualitypolicy" "${POLICY_NAME}"
    cleanup_resource "mcpqualitypolicy" "${POLICY_NAME_FULL}"
    # Give time for finalizers
    sleep 2
}

# Test 1: Create basic MCPQualityPolicy
test_create_policy() {
    log_section "Test 1: Creating basic MCPQualityPolicy"

    kubectl apply -f - <<EOF
apiVersion: kubemoot.ai/v1alpha1
kind: MCPQualityPolicy
metadata:
  name: ${POLICY_NAME}
  namespace: ${NAMESPACE}
spec:
  allowing:
    - author: "anthropic"
    - name: "trusted-mcp"
  blocking:
    - name:
        type: exact
        value: "bad-actor-mcp"
      reason: "Known bad actor"
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
}

# Test 2: Verify policy spec is correctly stored
test_policy_spec() {
    log_section "Test 2: Verifying policy spec"

    # Check allowing list
    local allowing_count
    allowing_count=$(kubectl get mcpqualitypolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.allowing}' | jq '. | length' 2>/dev/null || echo "0")
    log_info "Allowing entries: ${allowing_count}"

    if [[ "${allowing_count}" == "2" ]]; then
        log_ok "Allowing list has correct count"
    else
        log_warn "Expected 2 allowing entries, got: ${allowing_count}"
    fi

    # Check blocking list
    local blocking_count
    blocking_count=$(kubectl get mcpqualitypolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.blocking}' | jq '. | length' 2>/dev/null || echo "0")
    log_info "Blocking entries: ${blocking_count}"

    if [[ "${blocking_count}" == "1" ]]; then
        log_ok "Blocking list has correct count"
    else
        log_warn "Expected 1 blocking entry, got: ${blocking_count}"
    fi

    # Check considering config
    local fallback_action
    fallback_action=$(kubectl get mcpqualitypolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.considering.fallbackAction}')
    log_info "Fallback action: ${fallback_action}"

    if [[ "${fallback_action}" == "deny" ]]; then
        log_ok "Fallback action is 'deny' as expected"
    else
        log_fail "Unexpected fallback action: ${fallback_action}"
        return 1
    fi
}

# Test 3: Create full-featured policy with all matching types
test_full_policy() {
    log_section "Test 3: Creating full-featured MCPQualityPolicy"

    kubectl apply -f - <<EOF
apiVersion: kubemoot.ai/v1alpha1
kind: MCPQualityPolicy
metadata:
  name: ${POLICY_NAME_FULL}
  namespace: ${NAMESPACE}
spec:
  # Immediately allowed - trusted sources
  allowing:
    - author: "anthropic"
    - author: "modelcontextprotocol"
    - author: "kubernetes-sigs"
    - name: "github-mcp"

  # Immediately blocked - security risks
  blocking:
    # Regex matching
    - name:
        type: regex
        value: ".*(crypto|mining|hack).*"
      reason: "Security risk - crypto/mining/hack keywords"

    # Glob matching
    - name:
        type: glob
        value: "deprecated-*"
      reason: "Deprecated servers"

    # Exact matching with version constraint
    - name:
        type: exact
        value: "old-kubernetes-mcp"
      version: "<1.0.0"
      reason: "Old versions have security vulnerabilities"

    # Author blocking
    - author:
        type: exact
        value: "untrusted-org"
      reason: "Untrusted organization"

  # AI evaluation for everything else
  considering:
    enabled: false
    agentRef: "quality-evaluator"
    confidenceThreshold: "0.7"
    timeoutSeconds: 30
    fallbackAction: deny
EOF

    # Wait for policy to be created
    if wait_for "Full MCPQualityPolicy to exist" "kubectl get mcpqualitypolicy ${POLICY_NAME_FULL} -n ${NAMESPACE}" 30; then
        log_ok "Full-featured MCPQualityPolicy created"
    else
        log_fail "Full-featured MCPQualityPolicy creation failed"
        return 1
    fi

    # Verify structure
    local yaml_output
    yaml_output=$(kubectl get mcpqualitypolicy "${POLICY_NAME_FULL}" -n "${NAMESPACE}" -o yaml)

    # Check regex blocking entry exists
    if echo "${yaml_output}" | grep -q "type: regex"; then
        log_ok "Regex matcher preserved in spec"
    else
        log_warn "Regex matcher not found"
    fi

    # Check glob blocking entry exists
    if echo "${yaml_output}" | grep -q "type: glob"; then
        log_ok "Glob matcher preserved in spec"
    else
        log_warn "Glob matcher not found"
    fi

    # Check version constraint exists
    if echo "${yaml_output}" | grep -q "version:"; then
        log_ok "Version constraint preserved in spec"
    else
        log_warn "Version constraint not found"
    fi
}

# Test 4: Verify print columns work
test_print_columns() {
    log_section "Test 4: Verifying print columns"

    # Get wide output to see all columns
    local output
    output=$(kubectl get mcpqualitypolicy -n "${NAMESPACE}" -o wide 2>&1)
    log_info "Print columns output:"
    echo "${output}"

    # Check that at least the policy names appear
    if echo "${output}" | grep -q "${POLICY_NAME}"; then
        log_ok "Policy visible in kubectl get output"
    else
        log_fail "Policy not visible in output"
        return 1
    fi
}

# Test 5: Test policy update
test_policy_update() {
    log_section "Test 5: Testing policy update"

    # Update the policy to enable AI evaluation
    kubectl patch mcpqualitypolicy "${POLICY_NAME}" -n "${NAMESPACE}" --type=merge -p '{"spec":{"considering":{"enabled":true,"fallbackAction":"allow"}}}'

    if [[ $? -eq 0 ]]; then
        log_ok "Policy patch succeeded"
    else
        log_fail "Policy patch failed"
        return 1
    fi

    # Verify the update
    local enabled
    enabled=$(kubectl get mcpqualitypolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.considering.enabled}')
    local fallback
    fallback=$(kubectl get mcpqualitypolicy "${POLICY_NAME}" -n "${NAMESPACE}" -o jsonpath='{.spec.considering.fallbackAction}')

    log_info "Considering enabled: ${enabled}"
    log_info "Fallback action: ${fallback}"

    if [[ "${enabled}" == "true" ]] && [[ "${fallback}" == "allow" ]]; then
        log_ok "Policy update verified"
    else
        log_warn "Policy update not fully reflected"
    fi
}

# Test 6: Verify shortname works
test_shortname() {
    log_section "Test 6: Testing short name 'mqp'"

    # mqp is the shortName defined in the CRD
    local output
    output=$(kubectl get mqp -n "${NAMESPACE}" 2>&1)

    if echo "${output}" | grep -q "${POLICY_NAME}"; then
        log_ok "Short name 'mqp' works correctly"
    else
        log_warn "Short name 'mqp' may not be working"
        log_info "Output: ${output}"
    fi
}

# Test 7: Verify deletion works
test_deletion() {
    log_section "Test 7: Testing policy deletion"

    # Delete the full policy
    kubectl delete mcpqualitypolicy "${POLICY_NAME_FULL}" -n "${NAMESPACE}" --wait=true --timeout=30s

    if [[ $? -eq 0 ]]; then
        log_ok "MCPQualityPolicy deleted successfully"
    else
        log_fail "MCPQualityPolicy deletion failed"
        return 1
    fi

    # Verify it's gone
    sleep 2
    if kubectl get mcpqualitypolicy "${POLICY_NAME_FULL}" -n "${NAMESPACE}" > /dev/null 2>&1; then
        log_warn "Policy still exists after deletion"
    else
        log_ok "Policy no longer exists"
    fi
}

# Main test execution
main() {
    log_header "MCPQualityPolicy Test Suite"

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
    check_crd "mcpqualitypolicies.kubemoot.ai"

    # Run tests
    test_create_policy
    test_policy_spec
    test_full_policy
    test_print_columns
    test_policy_update
    test_shortname
    test_deletion

    # Final cleanup is handled by trap
    exit_with_results
}

main "$@"
