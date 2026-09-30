#!/usr/bin/env bash
#
# Operator - RAG Query Service Test
# Tests semantic search against indexed documents
#
# Prerequisites:
#   - pgvector database with indexed documents (run test-ragsource-real.sh first)
#   - Ollama with nomic-embed-text model
#
# Usage:
#   ./test-rag-query.sh              # Run with defaults
#   ./test-rag-query.sh --cleanup    # Clean up only
#   ./test-rag-query.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="RAG-Query"
QUERY_SERVICE_NAME="rag-query-test"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up RAG Query test resources..."
    cleanup_resource mcpserver "$QUERY_SERVICE_NAME"
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - RAG Query Service Test"

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
check_crd "mcpservers.kubemoot.ai" || exit 1
check_ollama || exit 1

# Check pgvector database is accessible
log_info "Checking pgvector database..."
if kubectl get svc homelab-pilot-db -n homelab-pilot >/dev/null 2>&1; then
    log_ok "pgvector database service found (homelab-pilot-db)"
else
    log_fail "pgvector database not found"
    exit 1
fi

# Check secret exists in kubemoot namespace
if kubectl get secret homelab-pilot-db-credentials -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "Database secret found in kubemoot namespace"
else
    log_fail "homelab-pilot-db-credentials not found in $NAMESPACE namespace"
    exit 1
fi

#
# Test 1: Deploy Query Service via MCPServer
#
log_section "Test 1: Deploy Query Service"

# Get database password from secret
DB_PASSWORD=$(kubectl get secret homelab-pilot-db-credentials -n "$NAMESPACE" -o jsonpath='{.data.password}' | base64 -d)

cat <<MANIFEST | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: $QUERY_SERVICE_NAME
  namespace: $NAMESPACE
spec:
  image: ${QUERY_SERVICE_IMAGE:-ghcr.io/kubemoot/query-service:0.326.0}
  transport: http
  port: 8000
  replicas: 1
  healthPath: /health
  readinessPath: /ready
  env:
    - name: KUBEMOOT_DB_HOST
      value: "homelab-pilot-db.homelab-pilot.svc.cluster.local"
    - name: KUBEMOOT_DB_PORT
      value: "5432"
    - name: KUBEMOOT_DB_NAME
      value: "homelab_pilot"
    - name: KUBEMOOT_DB_USER
      value: "pilot"
    - name: KUBEMOOT_DB_PASSWORD
      valueFrom:
        secretKeyRef:
          name: homelab-pilot-db-credentials
          key: password
    - name: KUBEMOOT_COLLECTION
      value: "kubemoot_test_kubectl"
    - name: KUBEMOOT_EMBEDDING_ENDPOINT
      value: "http://ollama.$OLLAMA_NAMESPACE:11434"
    - name: KUBEMOOT_EMBEDDING_MODEL
      value: "nomic-embed-text"
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "512Mi"
MANIFEST

log_ok "Query Service MCPServer created"

#
# Test 2: Wait for Query Service to be Ready
#
log_section "Test 2: Wait for Query Service Ready"

if wait_for "Query Service to be ready" \
    "kubectl get mcpserver $QUERY_SERVICE_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 120; then
    log_ok "Query Service is ready"
else
    log_fail "Query Service not ready within timeout"
    kubectl describe mcpserver "$QUERY_SERVICE_NAME" -n "$NAMESPACE"
    kubectl logs -n "$NAMESPACE" -l "app.kubernetes.io/name=$QUERY_SERVICE_NAME" --tail=20 || true
    exit 1
fi

ENDPOINT=$(kubectl get mcpserver "$QUERY_SERVICE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.endpoint}')
log_ok "Query Service endpoint: $ENDPOINT"

#
# Test 3: Check Health Endpoint
#
log_section "Test 3: Verify Health Endpoint"

# Port-forward to test the service
kubectl port-forward -n "$NAMESPACE" "svc/$QUERY_SERVICE_NAME" 8080:8000 &
PF_PID=$!
sleep 3

HEALTH=$(curl -s http://localhost:8080/health 2>/dev/null || echo "failed")
if echo "$HEALTH" | grep -q "healthy"; then
    log_ok "Health endpoint working"
else
    log_fail "Health endpoint failed: $HEALTH"
fi

#
# Test 4: List Collections
#
log_section "Test 4: List Collections"

COLLECTIONS=$(curl -s http://localhost:8080/collections 2>/dev/null || echo "failed")
if echo "$COLLECTIONS" | grep -q "kubemoot_test_kubectl"; then
    log_ok "Collection found: kubemoot_test_kubectl"
else
    log_warn "Expected collection not found: $COLLECTIONS"
fi

#
# Test 5: Query for kubectl get
#
log_section "Test 5: Semantic Query - 'kubectl get'"

QUERY_RESULT=$(curl -s -X POST http://localhost:8080/query \
    -H "Content-Type: application/json" \
    -d '{"query": "How do I list pods in Kubernetes?", "top_k": 3}' 2>/dev/null || echo "failed")

if echo "$QUERY_RESULT" | grep -q "results"; then
    log_ok "Query returned results"
    # Check if results contain relevant content
    if echo "$QUERY_RESULT" | grep -qi "get\|pod\|list"; then
        log_ok "Results appear relevant to query"
    else
        log_warn "Results may not be relevant"
    fi
    # Show first result preview
    log_info "First result preview:"
    echo "$QUERY_RESULT" | python3 -c "import sys,json; r=json.load(sys.stdin); print(r['results'][0]['content'][:200] + '...' if r['results'] else 'No results')" 2>/dev/null || echo "$QUERY_RESULT" | head -c 200
else
    log_fail "Query failed: $QUERY_RESULT"
fi

#
# Test 6: Query for kubectl apply
#
log_section "Test 6: Semantic Query - 'kubectl apply'"

QUERY_RESULT2=$(curl -s -X POST http://localhost:8080/query \
    -H "Content-Type: application/json" \
    -d '{"query": "How do I apply a YAML manifest to Kubernetes?", "top_k": 3}' 2>/dev/null || echo "failed")

if echo "$QUERY_RESULT2" | grep -q "results"; then
    log_ok "Second query returned results"
else
    log_fail "Second query failed"
fi

# Kill port-forward
kill $PF_PID 2>/dev/null || true

#
# Cleanup
#
log_section "Cleanup"

kubectl delete mcpserver "$QUERY_SERVICE_NAME" -n "$NAMESPACE"
log_ok "Query Service deleted"

if wait_for "Query Service to be removed" \
    "! kubectl get mcpserver $QUERY_SERVICE_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Query Service removed successfully"
else
    log_warn "Query Service still exists"
fi

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
