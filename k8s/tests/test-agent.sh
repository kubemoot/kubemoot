#!/usr/bin/env bash
#
# Operator - Agent CRD Test
# Tests the Agent CRD which orchestrates models, RAG sources, and MCP servers
#
# Usage:
#   ./test-agent.sh              # Run with defaults
#   ./test-agent.sh --cleanup    # Clean up only
#   ./test-agent.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="Agent"
AGENT_NAME="test-agent"
MODEL_NAME="test-agent-model"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up Agent test resources..."
    cleanup_resource agent "$AGENT_NAME"
    cleanup_resource model "$MODEL_NAME"
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - Agent CRD Test"

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
check_crd "agents.kubemoot.ai" || exit 1
check_crd "models.kubemoot.ai" || exit 1
check_ollama || exit 1

#
# Test 1: Ensure Model exists
#
log_section "Test 1: Create Model for Agent"

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
kind: Model
metadata:
  name: $MODEL_NAME
  namespace: $NAMESPACE
spec:
  providerRef: ollama-local
  model: qwen2:0.5b
EOF

log_ok "Model created"

if wait_for "Model to be ready" \
    "kubectl get model $MODEL_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 120; then
    log_ok "Model is ready"
else
    log_fail "Model not ready within timeout"
    kubectl describe model "$MODEL_NAME" -n "$NAMESPACE"
    exit 1
fi

#
# Test 2: Create basic Agent
#
log_section "Test 2: Create Basic Agent"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: $AGENT_NAME
  namespace: $NAMESPACE
spec:
  type: chat
  description: "Test chat agent for validation"
  models:
    - name: $MODEL_NAME
      role: primary
  prompt:
    system: "You are a helpful assistant for testing the operator."
  inference:
    temperature: "0.7"
    maxTokens: 1024
  memory:
    type: in-memory
    maxMessages: 50
  deployment:
    replicas: 1
    port: 8080
    image: nginx:alpine  # Using nginx as placeholder
    resources:
      requests:
        cpu: "50m"
        memory: "64Mi"
      limits:
        cpu: "200m"
        memory: "256Mi"
EOF

log_ok "Agent created"

#
# Test 3: Verify Agent Status
#
log_section "Test 3: Verify Agent Status"

sleep 5

# Check phase
PHASE=$(kubectl get agent "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')
if [ -n "$PHASE" ]; then
    log_ok "Agent phase: $PHASE"
else
    log_warn "Agent phase not set"
fi

# Check model status is tracked
MODEL_STATUS=$(kubectl get agent "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.status.modelStatus[0].ready}')
if [ "$MODEL_STATUS" = "true" ]; then
    log_ok "Agent tracks model as ready"
else
    log_warn "Agent model status: $MODEL_STATUS"
fi

# Wait for deployment
if wait_for "Agent deployment to be ready" \
    "kubectl get agent $AGENT_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 60; then
    log_ok "Agent is ready"
else
    log_warn "Agent not ready (expected with placeholder image)"
    # Show deployment status
    kubectl get deployment "$AGENT_NAME" -n "$NAMESPACE" -o wide 2>/dev/null || true
fi

#
# Test 4: Verify Deployment Created
#
log_section "Test 4: Verify Deployment Created"

if kubectl get deployment "$AGENT_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "Deployment created for Agent"

    # Check labels
    LABELS=$(kubectl get deployment "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.labels}')
    if echo "$LABELS" | grep -q "kubemoot.ai/agent"; then
        log_ok "Deployment has kubemoot.ai/agent label"
    else
        log_warn "Missing kubemoot.ai/agent label"
    fi
else
    log_fail "Deployment not created"
fi

#
# Test 5: Verify Service Created
#
log_section "Test 5: Verify Service Created"

if kubectl get service "$AGENT_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "Service created for Agent"

    # Check port
    PORT=$(kubectl get service "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.ports[0].port}')
    if [ "$PORT" = "8080" ]; then
        log_ok "Service port is correct: $PORT"
    else
        log_warn "Unexpected port: $PORT"
    fi
else
    log_fail "Service not created"
fi

#
# Test 6: Verify Endpoint Set
#
log_section "Test 6: Verify Endpoint"

ENDPOINT=$(kubectl get agent "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.status.endpoint}')
if [ -n "$ENDPOINT" ]; then
    log_ok "Agent endpoint: $ENDPOINT"
else
    log_warn "Agent endpoint not set"
fi

#
# Test 7: Verify Environment Variables
#
log_section "Test 7: Verify Environment Variables"

ENV_VARS=$(kubectl get deployment "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].env[*].name}')
if echo "$ENV_VARS" | grep -q "KUBEMOOT_AGENT_NAME"; then
    log_ok "Environment has KUBEMOOT_AGENT_NAME"
else
    log_warn "Missing KUBEMOOT_AGENT_NAME environment"
fi

if echo "$ENV_VARS" | grep -q "KUBEMOOT_SYSTEM_PROMPT"; then
    log_ok "Environment has KUBEMOOT_SYSTEM_PROMPT"
else
    log_warn "Missing KUBEMOOT_SYSTEM_PROMPT environment"
fi

#
# Test 8: Test Agent Deletion
#
log_section "Test 8: Test Agent Deletion"

kubectl delete agent "$AGENT_NAME" -n "$NAMESPACE"
log_ok "Agent deleted"

if wait_for "Agent to be removed" \
    "! kubectl get agent $AGENT_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Agent removed successfully"
else
    log_warn "Agent still exists"
fi

# Verify deployment is cleaned up (via owner references)
if wait_for "Deployment to be garbage collected" \
    "! kubectl get deployment $AGENT_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Deployment garbage collected"
else
    log_warn "Deployment still exists"
fi

# Verify service is cleaned up
if wait_for "Service to be garbage collected" \
    "! kubectl get service $AGENT_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Service garbage collected"
else
    log_warn "Service still exists"
fi

#
# Cleanup
#
log_section "Cleanup"

kubectl delete model "$MODEL_NAME" -n "$NAMESPACE" --ignore-not-found
log_ok "Test resources cleaned up"

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
