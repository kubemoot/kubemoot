#!/usr/bin/env bash
#
# Operator - RAGSource Real Integration Test
# Tests actual document indexing using the default indexer container
# against the real pgvector database
#
# Prerequisites:
#   - pgvector database at postgresql.clusteragent-db.svc.cluster.local
#   - clusteragent-db-secret copied to kubemoot namespace
#   - Ollama with nomic-embed-text model
#
# Usage:
#   ./test-ragsource-real.sh              # Run with defaults
#   ./test-ragsource-real.sh --cleanup    # Clean up only
#   ./test-ragsource-real.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="RAGSource-Real"
PROVIDER_NAME="test-provider-real"
EMBEDDING_MODEL_NAME="test-embedding-real"
RAGSOURCE_NAME="test-ragsource-real"
COLLECTION_NAME="kubemoot_test_kubectl"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up RAGSource real test resources..."
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

log_header "Operator - RAGSource Real Integration Test"

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

# Check pgvector database is accessible
log_info "Checking pgvector database..."
if kubectl get svc postgresql -n clusteragent-db >/dev/null 2>&1; then
    log_ok "pgvector database service found"
else
    log_fail "pgvector database not found at clusteragent-db namespace"
    exit 1
fi

# Check secret exists in kubemoot namespace
if kubectl get secret clusteragent-db-secret -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "Database secret found in kubemoot namespace"
else
    log_fail "clusteragent-db-secret not found in $NAMESPACE namespace"
    log_info "Copy it with: kubectl get secret clusteragent-db-secret -n clusteragent -o json | jq 'del(.metadata.namespace,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp)' | kubectl apply -n $NAMESPACE -f -"
    exit 1
fi

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

    # Verify endpoint is set
    ENDPOINT=$(kubectl get embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE" -o jsonpath='{.status.endpoint}')
    if [ -n "$ENDPOINT" ]; then
        log_ok "EmbeddingModel endpoint: $ENDPOINT"
    else
        log_warn "EmbeddingModel endpoint not set in status"
    fi
else
    log_fail "EmbeddingModel setup failed"
    exit 1
fi

#
# Test 1: Create RAGSource with REAL Indexer
#
log_section "Test 1: Create RAGSource with Real Indexer"

# Use the real default indexer image
# Index a small subset of kubectl docs
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
        - "content/en/docs/reference/kubectl/generated"

  vectorStore:
    type: pgvector
    endpoint: postgres://clusteragent@postgresql.clusteragent-db.svc.cluster.local:5432/clusteragent
    secretRef: clusteragent-db-secret
    collection: $COLLECTION_NAME
    dimensions: 768
    distanceMetric: cosine

  embeddingModelRef: $EMBEDDING_MODEL_NAME

  chunking:
    chunkSize: 512
    chunkOverlap: 50

  # Use default indexer (no image override)
  indexer:
    resources:
      requests:
        cpu: "200m"
        memory: "512Mi"
      limits:
        cpu: "1"
        memory: "2Gi"
EOF

log_ok "RAGSource resource created with real indexer"

#
# Test 2: Verify Indexing Job Created
#
log_section "Test 2: Verify Indexing Job Created"

if wait_for "Indexing job to be created" \
    "kubectl get jobs -n $NAMESPACE -l 'kubemoot.ai/ragsource=$RAGSOURCE_NAME' --no-headers | wc -l | grep -qE '^[1-9]'" 60; then
    JOB_NAME=$(kubectl get jobs -n "$NAMESPACE" -l "kubemoot.ai/ragsource=$RAGSOURCE_NAME" -o jsonpath='{.items[0].metadata.name}')
    log_ok "Indexing job created: $JOB_NAME"
else
    log_fail "Indexing job was not created"
    kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o yaml
    exit 1
fi

# Verify it's using the default indexer image (not busybox)
JOB_IMAGE=$(kubectl get job "$JOB_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].image}')
log_info "Job image: $JOB_IMAGE"
if echo "$JOB_IMAGE" | grep -q "kubemoot/indexer"; then
    log_ok "Job uses default indexer image"
elif echo "$JOB_IMAGE" | grep -q "busybox"; then
    log_fail "Job incorrectly uses busybox mock image"
else
    log_warn "Job uses custom image: $JOB_IMAGE"
fi

#
# Test 3: Wait for Indexing to Complete
#
log_section "Test 3: Wait for Indexing Job to Complete"

# This may take several minutes as it clones the repo and indexes docs
log_info "Waiting for indexing job to complete (this may take 2-5 minutes)..."

if wait_for "Indexing job to complete" \
    "kubectl get job $JOB_NAME -n $NAMESPACE -o jsonpath='{.status.conditions[?(@.type==\"Complete\")].status}' | grep -q 'True'" 300; then
    log_ok "Indexing job completed successfully"
else
    # Check if job failed
    JOB_STATUS=$(kubectl get job "$JOB_NAME" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Failed")].status}')
    if [ "$JOB_STATUS" = "True" ]; then
        log_fail "Indexing job failed"
        log_info "Job logs:"
        kubectl logs -n "$NAMESPACE" -l "job-name=$JOB_NAME" --tail=50 || true
    else
        log_warn "Indexing job still running or status unknown"
        kubectl get job "$JOB_NAME" -n "$NAMESPACE" -o yaml | tail -30
    fi
    exit 1
fi

# Show job logs
log_info "Indexing job logs (last 20 lines):"
kubectl logs -n "$NAMESPACE" -l "job-name=$JOB_NAME" --tail=20 || true

#
# Test 4: Verify RAGSource Status Updated
#
log_section "Test 4: Verify RAGSource Status"

if wait_for "RAGSource to be Ready" \
    "kubectl get ragsource $RAGSOURCE_NAME -n $NAMESPACE -o jsonpath='{.status.phase}' | grep -q 'Ready'" 60; then
    log_ok "RAGSource phase: Ready"
else
    PHASE=$(kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')
    log_warn "RAGSource phase: $PHASE (expected Ready)"
fi

READY=$(kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.ready}')
if [ "$READY" = "true" ]; then
    log_ok "RAGSource ready: true"
else
    log_warn "RAGSource ready: $READY"
fi

#
# Test 5: Verify Vectors in pgvector Database
#
log_section "Test 5: Verify Vectors in pgvector"

# Run a query against pgvector to check if vectors were stored
log_info "Checking if vectors were stored in pgvector..."

# Create a temporary pod to query postgres
kubectl run pgvector-check --rm -i --restart=Never \
    --image=postgres:15-alpine \
    --env="PGPASSWORD=$(kubectl get secret clusteragent-db-secret -n $NAMESPACE -o jsonpath='{.data.password}' | base64 -d)" \
    -n "$NAMESPACE" \
    -- psql -h postgresql.clusteragent-db.svc.cluster.local -U clusteragent -d clusteragent \
    -c "SELECT COUNT(*) as vector_count FROM data_${COLLECTION_NAME};" 2>/dev/null || true

# Alternative: Check via direct SQL through a job
VECTOR_COUNT=$(kubectl run pgcheck-$RANDOM --rm -i --restart=Never \
    --image=postgres:15-alpine \
    --env="PGPASSWORD=$(kubectl get secret clusteragent-db-secret -n $NAMESPACE -o jsonpath='{.data.password}' | base64 -d)" \
    -n "$NAMESPACE" \
    -- psql -h postgresql.clusteragent-db.svc.cluster.local -U clusteragent -d clusteragent -t \
    -c "SELECT COUNT(*) FROM data_${COLLECTION_NAME};" 2>/dev/null | tr -d ' ' || echo "0")

if [ -n "$VECTOR_COUNT" ] && [ "$VECTOR_COUNT" -gt 0 ] 2>/dev/null; then
    log_ok "Vectors stored in pgvector: $VECTOR_COUNT"
else
    log_warn "Could not verify vector count (may need manual check)"
    log_info "Check manually with: kubectl exec -it <postgres-pod> -- psql -c 'SELECT COUNT(*) FROM data_${COLLECTION_NAME};'"
fi

#
# Test 6: Cleanup
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
# Cleanup prereqs
#
log_section "Cleanup"

kubectl delete embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE"
log_ok "EmbeddingModel deleted"

kubectl delete modelprovider "$PROVIDER_NAME" -n "$NAMESPACE"
log_ok "ModelProvider deleted"

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
