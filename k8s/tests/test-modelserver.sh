#!/usr/bin/env bash
#
# Operator - ModelServer Resource Test
# Tests the ModelServer CRD lifecycle with Ollama backend
#
# Usage:
#   ./test-modelserver.sh              # Run with defaults
#   ./test-modelserver.sh --cleanup    # Clean up only
#   ./test-modelserver.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="ModelServer"
SERVER_NAME="test-server"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up ModelServer test resources..."
    cleanup_resource modelserver "$SERVER_NAME"
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - ModelServer Test"

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
check_crd "modelservers.kubemoot.ai" || exit 1
check_ollama || exit 1

#
# Test 1: Create ModelServer
#
log_section "Test 1: Create ModelServer"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata:
  name: $SERVER_NAME
  namespace: $NAMESPACE
spec:
  type: ollama
  endpoint: http://ollama.$OLLAMA_NAMESPACE:11434
EOF

log_ok "ModelServer resource created"

#
# Test 2: Wait for Ready status
#
log_section "Test 2: Verify Ready Status"

if wait_for "ModelServer to be ready" \
    "kubectl get modelserver $SERVER_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'"; then
    log_ok "ModelServer is ready"
else
    log_fail "ModelServer failed to become ready"
    kubectl get modelserver "$SERVER_NAME" -n "$NAMESPACE" -o yaml
    exit 1
fi

#
# Test 3: Verify server info
#
log_section "Test 3: Verify Server Info"

PHASE=$(kubectl get modelserver "$SERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')
if [ "$PHASE" = "Ready" ]; then
    log_ok "ModelServer phase: $PHASE"
else
    log_fail "ModelServer phase: $PHASE (expected: Ready)"
fi

SERVER_VERSION=$(kubectl get modelserver "$SERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.serverInfo.version}')
if [ -n "$SERVER_VERSION" ]; then
    log_ok "Ollama version detected: $SERVER_VERSION"
else
    log_fail "Ollama version not detected"
fi

#
# Test 4: Verify conditions
#
log_section "Test 4: Verify Conditions"

CONDITION_STATUS=$(kubectl get modelserver "$SERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
if [ "$CONDITION_STATUS" = "True" ]; then
    log_ok "Ready condition is True"
else
    log_fail "Ready condition is $CONDITION_STATUS (expected: True)"
fi

#
# Test 5: Delete ModelServer
#
log_section "Test 5: Delete ModelServer"

kubectl delete modelserver "$SERVER_NAME" -n "$NAMESPACE"
log_ok "ModelServer deleted"

# Verify deletion
if wait_for "ModelServer to be removed" \
    "! kubectl get modelserver $SERVER_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "ModelServer removed successfully"
else
    log_fail "ModelServer still exists after deletion"
fi

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
