#!/usr/bin/env bash
#
# Operator - RAGSource Resource Test
# Tests the RAGSource CRD lifecycle with Job-based indexing
#
# Note: This test uses a mock indexer (busybox sleep/exit) since actual
# indexing requires a real vector database. It verifies the controller
# creates Jobs correctly and handles status updates.
#
# Usage:
#   ./test-ragsource.sh              # Run with defaults
#   ./test-ragsource.sh --cleanup    # Clean up only
#   ./test-ragsource.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="RAGSource"
PROVIDER_NAME="test-provider"
EMBEDDING_MODEL_NAME="test-embedding"
RAGSOURCE_NAME="test-ragsource"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up RAGSource test resources..."
    cleanup_resource ragsource "$RAGSOURCE_NAME"
    cleanup_resource embeddingmodel "$EMBEDDING_MODEL_NAME"
    cleanup_resource modelprovider "$PROVIDER_NAME"
    # Clean up any leftover jobs
    kubectl delete jobs -n "$NAMESPACE" -l "kubemoot.ai/ragsource=$RAGSOURCE_NAME" --ignore-not-found=true 2>/dev/null || true
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - RAGSource Test"

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
check_crd "ragsources.kubemoot.ai" || exit 1
check_ollama || exit 1

#
# Setup: Create ModelProvider and EmbeddingModel
#
log_section "Setup: Create ModelProvider and EmbeddingModel"

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

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: EmbeddingModel
metadata:
  name: $EMBEDDING_MODEL_NAME
  namespace: $NAMESPACE
spec:
  providerRef: $PROVIDER_NAME
  model: nomic-embed-text
  dimensions: 768
EOF

if wait_for "EmbeddingModel to be ready" \
    "kubectl get embeddingmodel $EMBEDDING_MODEL_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 180; then
    log_ok "EmbeddingModel is ready"
else
    log_fail "EmbeddingModel setup failed"
    exit 1
fi

#
# Test 1: Create RAGSource
#
log_section "Test 1: Create RAGSource"

# Use a mock indexer that just sleeps briefly and exits successfully
# This tests the controller logic without requiring a real vector database
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
      url: https://github.com/kubernetes/website.git
      branch: main
      paths:
        - "content/en/docs/concepts/*.md"

  vectorStore:
    type: pgvector
    endpoint: postgres://mock:5432/vectors
    collection: test_docs
    dimensions: 768
    distanceMetric: cosine

  embeddingModelRef: $EMBEDDING_MODEL_NAME

  chunking:
    chunkSize: 512
    chunkOverlap: 50

  # Use mock indexer for testing (overrides default indexer)
  indexer:
    image: busybox:latest
    resources:
      requests:
        cpu: "50m"
        memory: "64Mi"
    env:
      - name: MOCK_INDEXER
        value: "true"
EOF

log_ok "RAGSource resource created"

#
# Test 2: Verify Job Created
#
log_section "Test 2: Verify Indexing Job Created"

if wait_for "Indexing job to be created" \
    "kubectl get jobs -n $NAMESPACE -l 'kubemoot.ai/ragsource=$RAGSOURCE_NAME' --no-headers | wc -l | grep -qE '^[1-9]'" 30; then
    JOB_NAME=$(kubectl get jobs -n "$NAMESPACE" -l "kubemoot.ai/ragsource=$RAGSOURCE_NAME" -o jsonpath='{.items[0].metadata.name}')
    log_ok "Indexing job created: $JOB_NAME"
else
    log_fail "Indexing job was not created"
    kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o yaml
    exit 1
fi

#
# Test 3: Verify RAGSource status shows Indexing
#
log_section "Test 3: Verify Indexing Status"

PHASE=$(kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')
if [ "$PHASE" = "Indexing" ] || [ "$PHASE" = "Ready" ] || [ "$PHASE" = "Error" ]; then
    log_ok "RAGSource phase: $PHASE"
else
    log_fail "RAGSource phase unexpected: $PHASE"
fi

LAST_JOB=$(kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.lastJobName}')
if [ -n "$LAST_JOB" ]; then
    log_ok "RAGSource tracking job: $LAST_JOB"
else
    log_fail "RAGSource not tracking job name"
fi

#
# Test 4: Verify Job has correct labels
#
log_section "Test 4: Verify Job Labels"

JOB_LABELS=$(kubectl get job "$JOB_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.labels}')
if echo "$JOB_LABELS" | grep -q "kubemoot-operator"; then
    log_ok "Job has managed-by label"
else
    log_fail "Job missing managed-by label"
fi

if echo "$JOB_LABELS" | grep -q "$RAGSOURCE_NAME"; then
    log_ok "Job has ragsource label"
else
    log_fail "Job missing ragsource label"
fi

#
# Test 5: Verify Job has correct environment variables
#
log_section "Test 5: Verify Job Environment"

POD_NAME=$(kubectl get pods -n "$NAMESPACE" -l "job-name=$JOB_NAME" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$POD_NAME" ]; then
    ENV_VARS=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.containers[0].env[*].name}' 2>/dev/null || echo "")

    if echo "$ENV_VARS" | grep -q "KUBEMOOT_SOURCE_TYPE"; then
        log_ok "Job has KUBEMOOT_SOURCE_TYPE env"
    else
        log_fail "Job missing KUBEMOOT_SOURCE_TYPE env"
    fi

    if echo "$ENV_VARS" | grep -q "KUBEMOOT_VECTORSTORE_TYPE"; then
        log_ok "Job has KUBEMOOT_VECTORSTORE_TYPE env"
    else
        log_fail "Job missing KUBEMOOT_VECTORSTORE_TYPE env"
    fi

    if echo "$ENV_VARS" | grep -q "KUBEMOOT_GIT_URL"; then
        log_ok "Job has KUBEMOOT_GIT_URL env"
    else
        log_fail "Job missing KUBEMOOT_GIT_URL env"
    fi
else
    log_warn "Could not find job pod to verify environment (may have completed)"
fi

#
# Test 6: Delete RAGSource
#
log_section "Test 6: Delete RAGSource"

kubectl delete ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE"
log_info "RAGSource resource deleted"

if wait_for "RAGSource to be removed" \
    "! kubectl get ragsource $RAGSOURCE_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "RAGSource removed successfully"
else
    log_fail "RAGSource still exists after deletion"
fi

# Check that job was cleaned up (via owner reference)
if wait_for "Indexing job to be removed" \
    "! kubectl get job $JOB_NAME -n $NAMESPACE 2>/dev/null" 60; then
    log_ok "Indexing job removed via owner reference"
else
    log_warn "Indexing job still exists (may be cleaned up by TTL)"
fi

#
# Cleanup
#
log_section "Cleanup"

kubectl delete embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE"
log_ok "EmbeddingModel deleted"

kubectl delete modelprovider "$PROVIDER_NAME" -n "$NAMESPACE"
log_ok "ModelProvider deleted"

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
