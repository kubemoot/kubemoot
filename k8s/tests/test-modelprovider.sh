#!/usr/bin/env bash
#
# Operator - ModelProvider Resource Test
# Tests the ModelProvider CRD lifecycle with Ollama backend
#
# Usage:
#   ./test-modelprovider.sh              # Run with defaults
#   ./test-modelprovider.sh --cleanup    # Clean up only
#   ./test-modelprovider.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="ModelProvider"
PROVIDER_NAME="test-provider"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up ModelProvider test resources..."
    cleanup_resource modelprovider "$PROVIDER_NAME"
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - ModelProvider Test"

# Setup cleanup trap
if [ "$DO_CLEANUP" = true ]; then
    trap cleanup EXIT
fi

#
# Pre-flight checks
#
log_section "Pre-flight Checks"

check_cluster || exit 1
check_namespace || exit 1
check_operator || exit 1
check_crd "modelproviders.kubemoot.ai" || exit 1
check_ollama || exit 1

#
# Test 1: Create ModelProvider
#
log_section "Test 1: Create ModelProvider"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: ModelProvider
metadata:
  name: $PROVIDER_NAME
  namespace: $NAMESPACE
spec:
  type: ollama
  endpoint: http://ollama.$OLLAMA_NAMESPACE:11434
EOF

log_ok "ModelProvider resource created"

#
# Test 2: Wait for Ready status
#
log_section "Test 2: Verify Ready Status"

if wait_for "ModelProvider to be ready" \
    "kubectl get modelprovider $PROVIDER_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'"; then
    log_ok "ModelProvider is ready"
else
    log_fail "ModelProvider failed to become ready"
    kubectl get modelprovider "$PROVIDER_NAME" -n "$NAMESPACE" -o yaml
    exit 1
fi

#
# Test 3: Verify provider info
#
log_section "Test 3: Verify Provider Info"

PHASE=$(kubectl get modelprovider "$PROVIDER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')
if [ "$PHASE" = "Ready" ]; then
    log_ok "ModelProvider phase: $PHASE"
else
    log_fail "ModelProvider phase: $PHASE (expected: Ready)"
fi

PROVIDER_VERSION=$(kubectl get modelprovider "$PROVIDER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.providerInfo.version}')
if [ -n "$PROVIDER_VERSION" ]; then
    log_ok "Ollama version detected: $PROVIDER_VERSION"
else
    log_fail "Ollama version not detected"
fi

#
# Test 4: Verify conditions
#
log_section "Test 4: Verify Conditions"

CONDITION_STATUS=$(kubectl get modelprovider "$PROVIDER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
if [ "$CONDITION_STATUS" = "True" ]; then
    log_ok "Ready condition is True"
else
    log_fail "Ready condition is $CONDITION_STATUS (expected: True)"
fi

#
# Test 5: Delete ModelProvider
#
log_section "Test 5: Delete ModelProvider"

kubectl delete modelprovider "$PROVIDER_NAME" -n "$NAMESPACE"
log_ok "ModelProvider deleted"

# Verify deletion
if wait_for "ModelProvider to be removed" \
    "! kubectl get modelprovider $PROVIDER_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "ModelProvider removed successfully"
else
    log_fail "ModelProvider still exists after deletion"
fi

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
