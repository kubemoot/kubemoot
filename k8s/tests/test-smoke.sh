#!/bin/bash
# Smoke Test - Quick validation (~2 min)
# Tests core functionality without full CRD coverage

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib.sh"

NAMESPACE="${NAMESPACE:-kubemoot}"
TEST_NAME="Smoke Test"

echo ""
echo "=============================================="
echo "  Operator - Smoke Test"
echo "=============================================="
echo ""
echo "  Namespace: $NAMESPACE"
echo ""

# Pre-flight checks
log_section "Pre-flight Checks"

if ! kubectl cluster-info &>/dev/null; then
    log_fail "Cannot connect to Kubernetes cluster"
    exit 1
fi
log_ok "Kubernetes cluster accessible"

if ! kubectl get namespace "$NAMESPACE" &>/dev/null; then
    log_fail "Namespace '$NAMESPACE' does not exist"
    exit 1
fi
log_ok "Namespace '$NAMESPACE' exists"

if [ -z "$(kubectl get deployment -n "$NAMESPACE" -l "$OPERATOR_SELECTOR" -o name 2>/dev/null)" ]; then
    log_fail "Operator not deployed"
    exit 1
fi
log_ok "Operator is deployed"

# Check operator is running
OPERATOR_READY=$(kubectl get deployment -n "$NAMESPACE" -l "$OPERATOR_SELECTOR" -o jsonpath='{.items[0].status.readyReplicas}' 2>/dev/null || echo "0")
if [[ "$OPERATOR_READY" -lt 1 ]]; then
    log_fail "Operator not ready"
    exit 1
fi
log_ok "Operator is ready"

# A Ready pod is not yet a running operator: wait until a Ready pod holds the leader lease
wait_for_operator_leader || exit 1

# Test 1: ModelServer
log_section "Test 1: ModelServer Quick Check"

# A second server on the endpoint of one the cluster already has Ready, so the check
# follows the cluster's model servers.
OLLAMA_ENDPOINT=$(ready_ollama_endpoint)
if [ -z "$OLLAMA_ENDPOINT" ]; then
    log_skip "ModelServer check: no Ready Ollama ModelServer in $NAMESPACE to share an endpoint with"
else
    cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata:
  name: smoke-test-server
  namespace: $NAMESPACE
spec:
  type: ollama
  endpoint: $OLLAMA_ENDPOINT
EOF
    log_ok "ModelServer created on $OLLAMA_ENDPOINT"

    if wait_for "ModelServer to be ready" "kubectl get modelserver -n $NAMESPACE smoke-test-server -o jsonpath='{.status.phase}' 2>/dev/null | grep -q 'Ready'" 60; then
        log_ok "ModelServer is Ready"
    else
        log_fail "ModelServer did not become Ready"
    fi

    kubectl delete modelserver -n "$NAMESPACE" smoke-test-server --ignore-not-found &>/dev/null
    log_ok "ModelServer cleaned up"
fi

# Test 2: KubemootConfig exists
log_section "Test 2: KubemootConfig Check"

if kubectl get kubemootconfig default &>/dev/null; then
    log_ok "KubemootConfig 'default' exists"

    # Check images are set
    INDEXER=$(kubectl get kubemootconfig default -o jsonpath='{.spec.images.indexer}' 2>/dev/null)
    if [[ -n "$INDEXER" && "$INDEXER" != *":latest"* ]]; then
        log_ok "KubemootConfig has versioned indexer image: $INDEXER"
    else
        log_fail "KubemootConfig indexer image missing or uses :latest"
    fi
else
    log_fail "KubemootConfig 'default' not found"
fi

# Test 3: CRDs installed
log_section "Test 3: CRD Installation Check"

EXPECTED_CRDS="modelservers models embeddingmodels mcpservers mcpgateways ragsources agents mcpqualitypolicies mcpcatalogs mcpserverreports kubemootconfigs"
for crd in $EXPECTED_CRDS; do
    if kubectl get crd "${crd}.kubemoot.ai" &>/dev/null; then
        log_ok "CRD '${crd}.kubemoot.ai' installed"
    else
        log_fail "CRD '${crd}.kubemoot.ai' missing"
    fi
done

# Results
exit_with_results
