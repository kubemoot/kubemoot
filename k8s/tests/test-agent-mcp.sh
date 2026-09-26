#!/usr/bin/env bash
#
# Operator - Agent with MCP Integration Test
# Tests the Agent CRD with MCPServer references for tool capabilities
#
# Usage:
#   ./test-agent-mcp.sh              # Run with defaults
#   ./test-agent-mcp.sh --cleanup    # Clean up only
#   ./test-agent-mcp.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="Agent-MCP"
AGENT_NAME="test-agent-mcp"
MCP_SERVER_NAME="test-agent-mcp-server"
MODEL_NAME="test-agent-mcp-model"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up Agent-MCP test resources..."
    cleanup_resource agent "$AGENT_NAME"
    cleanup_resource mcpserver "$MCP_SERVER_NAME"
    cleanup_resource model "$MODEL_NAME"
    # Clean up RBAC if created
    kubectl delete clusterrole test-agent-mcp-readonly --ignore-not-found=true 2>/dev/null || true
    kubectl delete clusterrolebinding test-agent-mcp-readonly --ignore-not-found=true 2>/dev/null || true
    kubectl delete serviceaccount test-mcp-sa -n "$NAMESPACE" --ignore-not-found=true 2>/dev/null || true
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - Agent with MCP Integration Test"

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
check_crd "mcpservers.kubemoot.ai" || exit 1
check_crd "models.kubemoot.ai" || exit 1
check_ollama || exit 1

#
# Test 1: Create RBAC for MCP Server
#
log_section "Test 1: Create RBAC for MCP Server"

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: test-mcp-sa
  namespace: $NAMESPACE
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: test-agent-mcp-readonly
rules:
  - apiGroups: [""]
    resources: ["pods", "services", "namespaces", "events"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "replicasets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: test-agent-mcp-readonly
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: test-agent-mcp-readonly
subjects:
  - kind: ServiceAccount
    name: test-mcp-sa
    namespace: $NAMESPACE
EOF

log_ok "RBAC resources created"

#
# Test 2: Create MCPServer
#
log_section "Test 2: Create MCPServer"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: $MCP_SERVER_NAME
  namespace: $NAMESPACE
spec:
  image: ghcr.io/feiskyer/mcp-kubernetes-server:latest
  port: 8000
  transport: http
  serviceAccountName: test-mcp-sa
  args:
    - "--transport"
    - "streamable-http"
    - "--host"
    - "0.0.0.0"
    - "--port"
    - "8000"
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "512Mi"
EOF

log_ok "MCPServer created"

# Wait for MCPServer to be ready
if wait_for "MCPServer to be ready" \
    "kubectl get mcpserver $MCP_SERVER_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 180; then
    log_ok "MCPServer is ready"
else
    log_warn "MCPServer not ready - continuing with test (may be image pull delay)"
    kubectl get mcpserver "$MCP_SERVER_NAME" -n "$NAMESPACE" -o yaml 2>/dev/null || true
fi

# Verify MCPServer endpoint
MCPSERVER_ENDPOINT=$(kubectl get mcpserver "$MCP_SERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.endpoint}')
if [ -n "$MCPSERVER_ENDPOINT" ]; then
    log_ok "MCPServer endpoint: $MCPSERVER_ENDPOINT"
else
    log_warn "MCPServer endpoint not yet set"
fi

#
# Test 3: Ensure Model exists
#
log_section "Test 3: Create Model for Agent"

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
# Test 4: Create Agent with MCP Server Reference
#
log_section "Test 4: Create Agent with MCPServer Reference"

cat <<EOF | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: $AGENT_NAME
  namespace: $NAMESPACE
spec:
  type: chat
  description: "Test agent with MCP tools for cluster interaction"
  models:
    - name: $MODEL_NAME
      role: primary
  mcpServers:
    - name: $MCP_SERVER_NAME
  prompt:
    system: |
      You are a Kubernetes assistant with MCP tools for cluster interaction.
      Use the available tools to help users query cluster resources.
  inference:
    temperature: "0.7"
    maxTokens: 1024
  memory:
    type: in-memory
    maxMessages: 50
  deployment:
    replicas: 1
    port: 8080
    image: nginx:alpine  # Placeholder for agent runtime
    resources:
      requests:
        cpu: "50m"
        memory: "64Mi"
      limits:
        cpu: "200m"
        memory: "256Mi"
EOF

log_ok "Agent with MCPServer reference created"

#
# Test 5: Verify Agent MCP Status
#
log_section "Test 5: Verify Agent MCP Server Status"

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
    log_warn "Agent model status: $MODEL_STATUS (may be pending)"
fi

# Check MCP server status is tracked
MCP_STATUS=$(kubectl get agent "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.status.mcpServerStatus}')
if [ -n "$MCP_STATUS" ]; then
    log_ok "Agent tracks MCP server status: $MCP_STATUS"
else
    log_warn "Agent MCP server status not yet tracked"
fi

# Check specific MCP server tracking
MCP_SERVER_READY=$(kubectl get agent "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.status.mcpServerStatus[0].ready}')
MCP_SERVER_NAME_STATUS=$(kubectl get agent "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.status.mcpServerStatus[0].name}')
if [ "$MCP_SERVER_NAME_STATUS" = "$MCP_SERVER_NAME" ]; then
    log_ok "Agent correctly references MCPServer: $MCP_SERVER_NAME_STATUS (ready: $MCP_SERVER_READY)"
else
    log_warn "Agent MCP server name: $MCP_SERVER_NAME_STATUS (expected: $MCP_SERVER_NAME)"
fi

#
# Test 6: Verify Agent Deployment
#
log_section "Test 6: Verify Agent Deployment Created"

if kubectl get deployment "$AGENT_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "Deployment created for Agent"

    # Check for MCP-related environment variables
    ENV_VARS=$(kubectl get deployment "$AGENT_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].env[*].name}')

    if echo "$ENV_VARS" | grep -q "KUBEMOOT_AGENT_NAME"; then
        log_ok "Environment has KUBEMOOT_AGENT_NAME"
    else
        log_warn "Missing KUBEMOOT_AGENT_NAME environment"
    fi

    # Check for MCP server endpoint in env (may be set by controller)
    if echo "$ENV_VARS" | grep -qi "MCP"; then
        log_ok "Environment has MCP-related variables"
    else
        log_info "No explicit MCP environment variables (may use config file)"
    fi
else
    log_fail "Deployment not created"
fi

#
# Test 7: Verify Agent Service
#
log_section "Test 7: Verify Agent Service Created"

if kubectl get service "$AGENT_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "Service created for Agent"

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
# Test 8: Verify MCPServer Independence
#
log_section "Test 8: Verify MCPServer Remains Independent"

# MCPServer should still exist and not be affected by Agent
if kubectl get mcpserver "$MCP_SERVER_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "MCPServer exists independently"
else
    log_fail "MCPServer was unexpectedly deleted"
fi

#
# Test 9: Test Agent Deletion (MCPServer should survive)
#
log_section "Test 9: Test Agent Deletion"

kubectl delete agent "$AGENT_NAME" -n "$NAMESPACE"
log_ok "Agent deleted"

if wait_for "Agent to be removed" \
    "! kubectl get agent $AGENT_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Agent removed successfully"
else
    log_warn "Agent still exists"
fi

# Verify MCPServer survives Agent deletion
sleep 3
if kubectl get mcpserver "$MCP_SERVER_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    log_ok "MCPServer survives Agent deletion (no cascade delete)"
else
    log_fail "MCPServer was incorrectly deleted with Agent"
fi

#
# Cleanup
#
log_section "Cleanup"

kubectl delete mcpserver "$MCP_SERVER_NAME" -n "$NAMESPACE" --ignore-not-found
kubectl delete model "$MODEL_NAME" -n "$NAMESPACE" --ignore-not-found
kubectl delete clusterrole test-agent-mcp-readonly --ignore-not-found
kubectl delete clusterrolebinding test-agent-mcp-readonly --ignore-not-found
kubectl delete serviceaccount test-mcp-sa -n "$NAMESPACE" --ignore-not-found
log_ok "Test resources cleaned up"

# Disable trap since we cleaned up
trap - EXIT

exit_with_results
