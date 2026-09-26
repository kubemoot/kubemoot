#!/usr/bin/env bash
#
# Operator - RAGSource Query Service Auto-Deploy Test
# Tests that RAGSource automatically deploys a query service
#
# Usage:
#   ./test-ragsource-queryservice.sh              # Run with defaults
#   ./test-ragsource-queryservice.sh --cleanup    # Clean up only
#   ./test-ragsource-queryservice.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="RAGSource-QueryService"
RAGSOURCE_NAME="test-rag-queryservice"
EMBEDDING_MODEL_NAME="test-embedding-qs"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up RAGSource Query Service test resources..."
    cleanup_resource ragsource "$RAGSOURCE_NAME"
    cleanup_resource embeddingmodel "$EMBEDDING_MODEL_NAME"
    # Delete any deployments/services that might remain
    kubectl delete deployment "${RAGSOURCE_NAME}-query" -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete service "${RAGSOURCE_NAME}-query" -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - RAGSource Query Service Auto-Deploy Test"

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
check_crd "ragsources.kubemoot.ai" || exit 1
check_crd "embeddingmodels.kubemoot.ai" || exit 1
check_ollama || exit 1
check_pgvector || exit 1

#
# Test 1: Create EmbeddingModel prerequisite
#
log_section "Test 1: Create EmbeddingModel"

# First ensure ModelProvider exists
if ! kubectl get modelprovider ollama-local -n "$NAMESPACE" >/dev/null 2>&1; then
    log_info "Creating ModelProvider for test..."
    cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: ModelProvider
metadata:
  name: ollama-local
  namespace: $NAMESPACE
spec:
  type: ollama
  endpoint: "http://ollama.$OLLAMA_NAMESPACE:11434"
EOF
    sleep 3
fi

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: EmbeddingModel
metadata:
  name: $EMBEDDING_MODEL_NAME
  namespace: $NAMESPACE
spec:
  providerRef: ollama-local
  model: nomic-embed-text
  dimensions: 768
EOF

log_ok "EmbeddingModel created"

if wait_for "EmbeddingModel to be ready" \
    "kubectl get embeddingmodel $EMBEDDING_MODEL_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 120; then
    log_ok "EmbeddingModel is ready"
else
    log_fail "EmbeddingModel not ready within timeout"
    exit 1
fi

#
# Test 2: Create RAGSource with queryService enabled
#
log_section "Test 2: Create RAGSource with Query Service"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: RAGSource
metadata:
  name: $RAGSOURCE_NAME
  namespace: $NAMESPACE
spec:
  source:
    type: git
    git:
      url: https://github.com/kubernetes/kubectl.git
      branch: master
      paths:
        - pkg/cmd/get
  vectorStore:
    type: pgvector
    endpoint: "homelab-pilot-db.homelab-pilot.svc.cluster.local:5432"
    collection: ${RAGSOURCE_NAME//-/_}
    dimensions: 768
    secretRef: "homelab-pilot-db-credentials"
  embeddingModelRef: $EMBEDDING_MODEL_NAME
  chunking:
    chunkSize: 512
    chunkOverlap: 50
  queryService:
    enabled: true
    replicas: 1
    port: 8000
    topK: 5
EOF

log_ok "RAGSource created with queryService enabled"

#
# Test 3: Verify Query Service Deployment Created
#
log_section "Test 3: Verify Query Service Deployment"

if wait_for "Query Service deployment to be created" \
    "kubectl get deployment ${RAGSOURCE_NAME}-query -n $NAMESPACE 2>/dev/null" 60; then
    log_ok "Query Service deployment created"
else
    log_fail "Query Service deployment not created"
    kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o yaml
    kubectl logs -n operator-system deploy/operator-controller-manager -c manager --tail=50
    exit 1
fi

# Verify deployment labels
LABELS=$(kubectl get deployment "${RAGSOURCE_NAME}-query" -n "$NAMESPACE" -o jsonpath='{.metadata.labels}')
if echo "$LABELS" | grep -q "kubemoot.ai/ragsource"; then
    log_ok "Deployment has kubemoot.ai/ragsource label"
else
    log_warn "Missing kubemoot.ai/ragsource label"
fi

#
# Test 4: Verify Query Service Service Created
#
log_section "Test 4: Verify Query Service Service"

if kubectl get service "${RAGSOURCE_NAME}-query" -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "Query Service service created"

    PORT=$(kubectl get service "${RAGSOURCE_NAME}-query" -n "$NAMESPACE" -o jsonpath='{.spec.ports[0].port}')
    if [ "$PORT" = "8000" ]; then
        log_ok "Service port is correct: $PORT"
    else
        log_warn "Unexpected port: $PORT (expected 8000)"
    fi
else
    log_fail "Query Service service not created"
fi

#
# Test 5: Verify RAGSource Status QueryEndpoint
#
log_section "Test 5: Verify RAGSource Status"

if wait_for "RAGSource queryEndpoint to be set" \
    "kubectl get ragsource $RAGSOURCE_NAME -n $NAMESPACE -o jsonpath='{.status.queryEndpoint}' | grep -q 'http'" 30; then
    ENDPOINT=$(kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.queryEndpoint}')
    log_ok "QueryEndpoint set: $ENDPOINT"
else
    log_warn "QueryEndpoint not set in status"
    kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status}'
fi

#
# Test 6: Verify Environment Variables
#
log_section "Test 6: Verify Environment Variables"

ENV_VARS=$(kubectl get deployment "${RAGSOURCE_NAME}-query" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].env[*].name}' 2>/dev/null || echo "")

check_env() {
    local var=$1
    if echo "$ENV_VARS" | grep -q "$var"; then
        log_ok "Environment has $var"
    else
        log_warn "Missing $var environment"
    fi
}

check_env "KUBEMOOT_RAGSOURCE_NAME"
check_env "KUBEMOOT_VECTORSTORE_TYPE"
check_env "KUBEMOOT_VECTORSTORE_ENDPOINT"
check_env "KUBEMOOT_VECTORSTORE_COLLECTION"
check_env "KUBEMOOT_EMBEDDING_ENDPOINT"
check_env "KUBEMOOT_EMBEDDING_MODEL"

#
# Test 7: Test Query Service is Functional (if deployment is ready)
#
log_section "Test 7: Test Query Service Functionality"

# Wait for deployment to be ready (may take time as indexing needs to complete)
if wait_for "Query Service pods to be ready" \
    "kubectl get deployment ${RAGSOURCE_NAME}-query -n $NAMESPACE -o jsonpath='{.status.readyReplicas}' | grep -q '1'" 60; then
    log_ok "Query Service pods ready"

    # Port-forward and test
    kubectl port-forward -n "$NAMESPACE" "svc/${RAGSOURCE_NAME}-query" 8081:8000 &
    PF_PID=$!
    sleep 3

    HEALTH=$(curl -s http://localhost:8081/health 2>/dev/null || echo "failed")
    if echo "$HEALTH" | grep -q "healthy"; then
        log_ok "Health endpoint responding"
    else
        log_warn "Health endpoint not responding: $HEALTH"
    fi

    kill $PF_PID 2>/dev/null || true
else
    log_warn "Query Service pods not ready (indexing may be in progress)"
fi

#
# Test 8: Test RAGSource Deletion Cleans Up Query Service
#
log_section "Test 8: Test Cleanup via Owner References"

kubectl delete ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE"
log_ok "RAGSource deleted"

if wait_for "Query Service deployment to be garbage collected" \
    "! kubectl get deployment ${RAGSOURCE_NAME}-query -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Query Service deployment garbage collected"
else
    log_warn "Query Service deployment still exists"
fi

if wait_for "Query Service service to be garbage collected" \
    "! kubectl get service ${RAGSOURCE_NAME}-query -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Query Service service garbage collected"
else
    log_warn "Query Service service still exists"
fi

#
# Cleanup
#
log_section "Cleanup"

kubectl delete embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE" --ignore-not-found
log_ok "Test resources cleaned up"

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
