#!/usr/bin/env bash
#
# Operator - EmbeddingModel Resource Test
# Tests the EmbeddingModel CRD lifecycle with Ollama embedding models
#
# Usage:
#   ./test-embeddingmodel.sh              # Run with defaults
#   ./test-embeddingmodel.sh --cleanup    # Clean up only
#   ./test-embeddingmodel.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="EmbeddingModel"
PROVIDER_NAME="test-provider"
EMBEDDING_MODEL_NAME="test-embedding"
OLLAMA_EMBEDDING_MODEL="nomic-embed-text"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up EmbeddingModel test resources..."
    cleanup_resource embeddingmodel "$EMBEDDING_MODEL_NAME"
    cleanup_resource modelprovider "$PROVIDER_NAME"
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - EmbeddingModel Test"

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
check_crd "embeddingmodels.kubemoot.ai" || exit 1
check_ollama || exit 1

#
# Setup: Create ModelProvider
#
log_section "Setup: Create ModelProvider"

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

if wait_for "ModelProvider to be ready" \
    "kubectl get modelprovider $PROVIDER_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'"; then
    log_ok "ModelProvider is ready"
else
    log_fail "ModelProvider setup failed"
    exit 1
fi

#
# Test 1: Create EmbeddingModel
#
log_section "Test 1: Create EmbeddingModel"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: EmbeddingModel
metadata:
  name: $EMBEDDING_MODEL_NAME
  namespace: $NAMESPACE
spec:
  providerRef: $PROVIDER_NAME
  model: $OLLAMA_EMBEDDING_MODEL
  dimensions: 768
EOF

log_ok "EmbeddingModel resource created"

#
# Test 2: Wait for Available state
#
log_section "Test 2: Wait for Available State"

if wait_for "EmbeddingModel to be available" \
    "kubectl get embeddingmodel $EMBEDDING_MODEL_NAME -n $NAMESPACE -o jsonpath='{.status.state}' | grep -qE 'Available'" 180; then
    log_ok "EmbeddingModel is available"
else
    log_fail "EmbeddingModel failed to become available"
    kubectl get embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE" -o yaml
    exit 1
fi

#
# Test 3: Verify status fields
#
log_section "Test 3: Verify Status Fields"

READY=$(kubectl get embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE" -o jsonpath='{.status.ready}')
if [ "$READY" = "true" ]; then
    log_ok "EmbeddingModel ready: $READY"
else
    log_fail "EmbeddingModel not ready"
fi

STATE=$(kubectl get embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE" -o jsonpath='{.status.state}')
if [ "$STATE" = "Available" ]; then
    log_ok "EmbeddingModel state: $STATE"
else
    log_fail "EmbeddingModel state: $STATE (expected: Available)"
fi

# Check model info
DIMENSIONS=$(kubectl get embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE" -o jsonpath='{.status.modelInfo.dimensions}')
if [ -n "$DIMENSIONS" ] && [ "$DIMENSIONS" -gt 0 ] 2>/dev/null; then
    log_ok "EmbeddingModel dimensions: $DIMENSIONS"
else
    log_warn "EmbeddingModel dimensions not reported (may be OK for some models)"
fi

#
# Test 4: Verify model exists in Ollama
#
log_section "Test 4: Verify Model in Ollama"

if ollama_has_model "$OLLAMA_EMBEDDING_MODEL"; then
    log_ok "Embedding model exists in Ollama"
else
    log_fail "Embedding model not found in Ollama"
fi

#
# Test 5: Delete EmbeddingModel
#
log_section "Test 5: Delete EmbeddingModel"

kubectl delete embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE"
log_ok "EmbeddingModel deleted"

if wait_for "EmbeddingModel to be removed" \
    "! kubectl get embeddingmodel $EMBEDDING_MODEL_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "EmbeddingModel removed successfully"
else
    log_fail "EmbeddingModel still exists after deletion"
fi

#
# Cleanup: Delete ModelProvider
#
log_section "Cleanup: Delete ModelProvider"

kubectl delete modelprovider "$PROVIDER_NAME" -n "$NAMESPACE"
log_ok "ModelProvider deleted"

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
