#!/usr/bin/env bash
#
# Operator - RAGSource Schedule Test
# Tests cron-based re-indexing schedules
#
# Usage:
#   ./test-ragsource-schedule.sh              # Run with defaults
#   ./test-ragsource-schedule.sh --cleanup    # Clean up only
#   ./test-ragsource-schedule.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="RAGSource-Schedule"
EMBEDDING_MODEL_NAME="schedule-test-embedding"
RAGSOURCE_NAME="schedule-test-source"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up schedule test resources..."
    cleanup_resource ragsource "$RAGSOURCE_NAME"
    cleanup_resource embeddingmodel "$EMBEDDING_MODEL_NAME"
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - RAGSource Schedule Test"

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
# Test 1: Create EmbeddingModel
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
    "kubectl get embeddingmodel $EMBEDDING_MODEL_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 60; then
    log_ok "EmbeddingModel is ready"
else
    log_fail "EmbeddingModel not ready within timeout"
    exit 1
fi

#
# Test 2: Test Valid Cron Schedule
#
log_section "Test 2: Create RAGSource with Valid Schedule"

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
        - content/en/docs/reference/kubectl/generated/kubectl/kubectl.md
  vectorStore:
    type: pgvector
    endpoint: "homelab-pilot-db.homelab-pilot.svc.cluster.local:5432"
    collection: "kubemoot_schedule_test"
    dimensions: 768
    secretRef: "homelab-pilot-db-credentials"
  embeddingModelRef: "$EMBEDDING_MODEL_NAME"
  indexer:
    schedule: "0 2 * * *"  # Daily at 2:00 AM
EOF

log_ok "RAGSource with schedule created"

# Wait for initial indexing
log_section "Test 3: Wait for Initial Indexing"

if wait_for "RAGSource indexing to start" \
    "kubectl get ragsource $RAGSOURCE_NAME -n $NAMESPACE -o jsonpath='{.status.phase}' | grep -q 'Indexing'" 30; then
    log_ok "RAGSource indexing started"
else
    log_warn "RAGSource didn't start indexing quickly (may already be done)"
fi

if wait_for "RAGSource to be ready" \
    "kubectl get ragsource $RAGSOURCE_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 180; then
    log_ok "RAGSource is ready"
else
    log_fail "RAGSource not ready within timeout"
    kubectl describe ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE"
    exit 1
fi

#
# Test 4: Verify NextIndexTime is Set
#
log_section "Test 4: Verify Scheduled Next Run"

NEXT_INDEX_TIME=$(kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.nextIndexTime}')
if [ -n "$NEXT_INDEX_TIME" ]; then
    log_ok "Next index time is set: $NEXT_INDEX_TIME"
else
    log_fail "Next index time not set despite schedule being configured"
    exit 1
fi

# Verify the message includes next run info
MESSAGE=$(kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.message}')
if echo "$MESSAGE" | grep -q "Next run"; then
    log_ok "Status message includes next run info: $MESSAGE"
else
    log_warn "Status message doesn't include next run info: $MESSAGE"
fi

#
# Test 5: Test Invalid Cron Schedule
#
log_section "Test 5: Test Invalid Cron Schedule"

# Create a separate RAGSource with invalid schedule
INVALID_SOURCE_NAME="schedule-test-invalid"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: RAGSource
metadata:
  name: $INVALID_SOURCE_NAME
  namespace: $NAMESPACE
spec:
  source:
    type: git
    git:
      url: https://github.com/kubernetes/website.git
      branch: main
      paths:
        - content/en/docs/reference/kubectl/generated/kubectl/kubectl.md
  vectorStore:
    type: pgvector
    endpoint: "homelab-pilot-db.homelab-pilot.svc.cluster.local:5432"
    collection: "kubemoot_invalid_test"
    dimensions: 768
    secretRef: "homelab-pilot-db-credentials"
  embeddingModelRef: "$EMBEDDING_MODEL_NAME"
  indexer:
    schedule: "invalid cron"
EOF

log_ok "RAGSource with invalid schedule created"

sleep 5

# Check that it has an error status
PHASE=$(kubectl get ragsource "$INVALID_SOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')
if [ "$PHASE" = "Error" ]; then
    log_ok "Invalid schedule correctly reported as Error"
    ERROR_MSG=$(kubectl get ragsource "$INVALID_SOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.message}')
    log_info "Error message: $ERROR_MSG"
else
    log_warn "Invalid schedule didn't produce Error phase (got: $PHASE)"
fi

# Clean up invalid source
kubectl delete ragsource "$INVALID_SOURCE_NAME" -n "$NAMESPACE" --ignore-not-found

#
# Test 6: Test Various Schedule Formats
#
log_section "Test 6: Test Schedule Format Validation"

# Test predefined schedules
for SCHEDULE in "@hourly" "@daily" "@weekly" "@monthly" "*/5 * * * *" "0 */6 * * *"; do
    log_info "Testing schedule format: $SCHEDULE"

    # Just update the existing RAGSource
    kubectl patch ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" --type=merge \
        -p "{\"spec\":{\"indexer\":{\"schedule\":\"$SCHEDULE\"}}}" >/dev/null 2>&1

    sleep 2

    PHASE=$(kubectl get ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')
    if [ "$PHASE" != "Error" ]; then
        log_ok "Schedule '$SCHEDULE' accepted"
    else
        log_fail "Schedule '$SCHEDULE' rejected"
    fi
done

# Restore original schedule
kubectl patch ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE" --type=merge \
    -p '{"spec":{"indexer":{"schedule":"0 2 * * *"}}}' >/dev/null

#
# Cleanup
#
log_section "Cleanup"

kubectl delete ragsource "$RAGSOURCE_NAME" -n "$NAMESPACE"
kubectl delete embeddingmodel "$EMBEDDING_MODEL_NAME" -n "$NAMESPACE"
log_ok "Test resources deleted"

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
