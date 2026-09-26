#!/usr/bin/env bash
#
# Operator - Model Resource Test
# Tests the Model CRD lifecycle with Ollama model pull/delete
#
# Usage:
#   ./test-model.sh              # Run with defaults
#   ./test-model.sh --cleanup    # Clean up only
#   ./test-model.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="Model"
PROVIDER_NAME="test-provider"
MODEL_NAME="test-model"
OLLAMA_MODEL="qwen2:0.5b"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up Model test resources..."
    cleanup_resource model "$MODEL_NAME"
    cleanup_resource modelprovider "$PROVIDER_NAME"
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - Model Test"

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
check_crd "models.kubemoot.ai" || exit 1
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
# Test 1: Ensure clean slate
#
log_section "Test 1: Prepare Clean Slate"

if ollama_has_model "$OLLAMA_MODEL"; then
    log_info "Removing existing model from Ollama..."
    ollama_remove_model "$OLLAMA_MODEL"
    sleep 2
fi

if ollama_has_model "$OLLAMA_MODEL"; then
    log_fail "Model still exists in Ollama after removal"
    exit 1
fi
log_ok "Verified model does not exist in Ollama"

#
# Test 2: Create Model (triggers pull)
#
log_section "Test 2: Create Model"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: Model
metadata:
  name: $MODEL_NAME
  namespace: $NAMESPACE
spec:
  providerRef: $PROVIDER_NAME
  model: $OLLAMA_MODEL
EOF

log_ok "Model resource created"

#
# Test 3: Verify pulling state
#
log_section "Test 3: Verify Pull State"

if wait_for "Model to start pulling" \
    "kubectl get model $MODEL_NAME -n $NAMESPACE -o jsonpath='{.status.state}' | grep -qE 'Pulling|Available|Loaded'" 30; then
    STATE=$(kubectl get model "$MODEL_NAME" -n "$NAMESPACE" -o jsonpath='{.status.state}')
    log_ok "Model state: $STATE"
else
    log_fail "Model did not start pulling"
    kubectl get model "$MODEL_NAME" -n "$NAMESPACE" -o yaml
    exit 1
fi

#
# Test 4: Wait for Available state
#
log_section "Test 4: Wait for Available"

if wait_for "Model to be available" \
    "kubectl get model $MODEL_NAME -n $NAMESPACE -o jsonpath='{.status.state}' | grep -qE 'Available|Loaded'" 180; then
    log_ok "Model is available"
else
    log_fail "Model failed to become available"
    kubectl get model "$MODEL_NAME" -n "$NAMESPACE" -o yaml
    exit 1
fi

#
# Test 5: Verify model in Ollama
#
log_section "Test 5: Verify Model in Ollama"

if ollama_has_model "$OLLAMA_MODEL"; then
    log_ok "Model exists in Ollama"
else
    log_fail "Model not found in Ollama"
    kubectl exec -n "$OLLAMA_NAMESPACE" deploy/ollama -- ollama list
    exit 1
fi

#
# Test 6: Verify model metadata
#
log_section "Test 6: Verify Model Metadata"

MODEL_SIZE=$(kubectl get model "$MODEL_NAME" -n "$NAMESPACE" -o jsonpath='{.status.modelInfo.size}')
MODEL_FAMILY=$(kubectl get model "$MODEL_NAME" -n "$NAMESPACE" -o jsonpath='{.status.modelInfo.family}')
MODEL_PARAMS=$(kubectl get model "$MODEL_NAME" -n "$NAMESPACE" -o jsonpath='{.status.modelInfo.parameters}')

if [ -n "$MODEL_SIZE" ]; then
    log_ok "Model size: $MODEL_SIZE"
else
    log_fail "Model size not reported"
fi

if [ -n "$MODEL_FAMILY" ]; then
    log_ok "Model family: $MODEL_FAMILY"
else
    log_fail "Model family not reported"
fi

if [ -n "$MODEL_PARAMS" ]; then
    log_ok "Model parameters: $MODEL_PARAMS"
else
    log_fail "Model parameters not reported"
fi

#
# Test 7: Delete Model (triggers cleanup)
#
log_section "Test 7: Delete Model"

kubectl delete model "$MODEL_NAME" -n "$NAMESPACE"
log_info "Model resource deleted"

if wait_for "Model to be removed from Ollama" \
    "! ollama_has_model '$OLLAMA_MODEL'" 60; then
    log_ok "Model removed from Ollama via finalizer"
else
    log_fail "Model still exists in Ollama after deletion"
    kubectl exec -n "$OLLAMA_NAMESPACE" deploy/ollama -- ollama list
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
