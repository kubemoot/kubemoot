#!/usr/bin/env bash
#
# Operator - MCPServer Resource Test
# Tests the MCPServer CRD lifecycle with Deployment and Service creation
#
# Usage:
#   ./test-mcpserver.sh              # Run with defaults
#   ./test-mcpserver.sh --cleanup    # Clean up only
#   ./test-mcpserver.sh --no-cleanup # Leave resources
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

TEST_NAME="MCPServer"
MCPSERVER_NAME="test-mcpserver"

parse_common_args "$@"

cleanup() {
    log_info "Cleaning up MCPServer test resources..."
    cleanup_resource mcpserver "$MCPSERVER_NAME"
    log_info "Cleanup complete"
}

if [ "$CLEANUP_ONLY" = true ]; then
    cleanup
    exit 0
fi

log_header "Operator - MCPServer Test"

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

#
# Test 1: Create MCPServer
#
log_section "Test 1: Create MCPServer"

# Use nginx as a simple HTTP server for testing
cat <<MANIFEST | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: $MCPSERVER_NAME
  namespace: $NAMESPACE
spec:
  image: nginx:alpine
  transport: http
  port: 80
  replicas: 1
  healthPath: /
  readinessPath: /
  resources:
    requests:
      cpu: "50m"
      memory: "64Mi"
    limits:
      cpu: "100m"
      memory: "128Mi"
MANIFEST

log_ok "MCPServer resource created"

#
# Test 2: Verify Deployment Created
#
log_section "Test 2: Verify Deployment Created"

if wait_for "Deployment to be created" \
    "kubectl get deployment $MCPSERVER_NAME -n $NAMESPACE --no-headers 2>/dev/null | wc -l | grep -q '1'" 30; then
    log_ok "Deployment created: $MCPSERVER_NAME"
else
    log_fail "Deployment was not created"
    kubectl get mcpserver "$MCPSERVER_NAME" -n "$NAMESPACE" -o yaml
    exit 1
fi

#
# Test 3: Verify Service Created
#
log_section "Test 3: Verify Service Created"

if wait_for "Service to be created" \
    "kubectl get service $MCPSERVER_NAME -n $NAMESPACE --no-headers 2>/dev/null | wc -l | grep -q '1'" 30; then
    log_ok "Service created: $MCPSERVER_NAME"

    # Verify service port
    SVC_PORT=$(kubectl get service "$MCPSERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.ports[0].port}')
    if [ "$SVC_PORT" = "80" ]; then
        log_ok "Service port is correct: $SVC_PORT"
    else
        log_fail "Service port is incorrect: $SVC_PORT (expected 80)"
    fi
else
    log_fail "Service was not created"
    exit 1
fi

#
# Test 4: Verify Deployment has correct labels
#
log_section "Test 4: Verify Deployment Labels"

DEPLOY_LABELS=$(kubectl get deployment "$MCPSERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.labels}')
if echo "$DEPLOY_LABELS" | grep -q "kubemoot-operator"; then
    log_ok "Deployment has managed-by label"
else
    log_fail "Deployment missing managed-by label"
fi

if echo "$DEPLOY_LABELS" | grep -q "mcp-server"; then
    log_ok "Deployment has component label"
else
    log_fail "Deployment missing component label"
fi

#
# Test 5: Wait for MCPServer to be Ready
#
log_section "Test 5: Wait for MCPServer Ready"

if wait_for "MCPServer to be Ready" \
    "kubectl get mcpserver $MCPSERVER_NAME -n $NAMESPACE -o jsonpath='{.status.ready}' | grep -q 'true'" 60; then
    log_ok "MCPServer is ready"
else
    log_warn "MCPServer not ready within timeout"
    kubectl get mcpserver "$MCPSERVER_NAME" -n "$NAMESPACE" -o yaml | tail -20
fi

# Check status fields
PHASE=$(kubectl get mcpserver "$MCPSERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}')
if [ "$PHASE" = "Ready" ]; then
    log_ok "MCPServer phase: $PHASE"
else
    log_warn "MCPServer phase: $PHASE (expected Ready)"
fi

ENDPOINT=$(kubectl get mcpserver "$MCPSERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.status.endpoint}')
if [ -n "$ENDPOINT" ]; then
    log_ok "MCPServer endpoint: $ENDPOINT"
else
    log_warn "MCPServer endpoint not set"
fi

#
# Test 6: Verify Security Context
#
log_section "Test 6: Verify Security Context"

SEC_CONTEXT=$(kubectl get deployment "$MCPSERVER_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].securityContext.runAsNonRoot}')
if [ "$SEC_CONTEXT" = "true" ]; then
    log_ok "Security context runAsNonRoot is set"
else
    log_warn "Security context runAsNonRoot not set"
fi

#
# Test 7: Delete MCPServer
#
log_section "Test 7: Delete MCPServer"

kubectl delete mcpserver "$MCPSERVER_NAME" -n "$NAMESPACE"
log_info "MCPServer resource deleted"

if wait_for "MCPServer to be removed" \
    "! kubectl get mcpserver $MCPSERVER_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "MCPServer removed successfully"
else
    log_fail "MCPServer still exists after deletion"
fi

# Check that deployment was cleaned up (via owner reference)
if wait_for "Deployment to be removed" \
    "! kubectl get deployment $MCPSERVER_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Deployment removed via owner reference"
else
    log_warn "Deployment still exists"
fi

# Check that service was cleaned up
if wait_for "Service to be removed" \
    "! kubectl get service $MCPSERVER_NAME -n $NAMESPACE 2>/dev/null" 30; then
    log_ok "Service removed via owner reference"
else
    log_warn "Service still exists"
fi

# Disable trap since we cleaned up
trap - EXIT

#
# Phase 1 Test: Real MCP Server with MCP Protocol
#
log_section "Phase 1 Test: Real MCP Server"

MCP_REAL_NAME="test-kubernetes-mcp"

# Create ServiceAccount and RBAC for the MCP server
log_info "Creating RBAC for Kubernetes MCP server..."
cat <<RBAC | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubernetes-mcp-test-sa
  namespace: $NAMESPACE
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubernetes-mcp-test-readonly
rules:
  - apiGroups: [""]
    resources: ["pods", "services", "nodes", "namespaces", "events"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubernetes-mcp-test-readonly-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kubernetes-mcp-test-readonly
subjects:
  - kind: ServiceAccount
    name: kubernetes-mcp-test-sa
    namespace: $NAMESPACE
RBAC
log_ok "RBAC created"

# Create real MCP server
log_info "Creating real Kubernetes MCP server..."
cat <<MANIFEST | kubectl apply -f -
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: $MCP_REAL_NAME
  namespace: $NAMESPACE
spec:
  image: ghcr.io/feiskyer/mcp-kubernetes-server:latest
  args:
    - "--transport"
    - "streamable-http"
    - "--host"
    - "0.0.0.0"
    - "--port"
    - "8000"
  transport: http
  port: 8000
  replicas: 1
  serviceAccountName: kubernetes-mcp-test-sa
  resources:
    requests:
      cpu: "100m"
      memory: "256Mi"
    limits:
      cpu: "500m"
      memory: "512Mi"
  capabilities:
    - kubernetes
    - test
MANIFEST
log_ok "MCPServer resource created"

#
# Test: Verify ServiceAccount is assigned
#
log_section "Phase 1 Test: Verify ServiceAccount"

if wait_for "Pod to be created" \
    "kubectl get pods -l app.kubernetes.io/name=$MCP_REAL_NAME -n $NAMESPACE --no-headers 2>/dev/null | wc -l | grep -qv '^0'" 60; then
    SA_NAME=$(kubectl get pods -l "app.kubernetes.io/name=$MCP_REAL_NAME" -n "$NAMESPACE" -o jsonpath='{.items[0].spec.serviceAccountName}')
    if [ "$SA_NAME" = "kubernetes-mcp-test-sa" ]; then
        log_ok "ServiceAccount assigned correctly: $SA_NAME"
    else
        log_fail "ServiceAccount is wrong: $SA_NAME (expected kubernetes-mcp-test-sa)"
    fi
else
    log_fail "Pod was not created"
fi

#
# Test: Verify TCP probes (not HTTP)
#
log_section "Phase 1 Test: Verify TCP Probes"

PROBE_TYPE=$(kubectl get pods -l "app.kubernetes.io/name=$MCP_REAL_NAME" -n "$NAMESPACE" -o jsonpath='{.items[0].spec.containers[0].readinessProbe.tcpSocket.port}' 2>/dev/null)
if [ "$PROBE_TYPE" = "8000" ]; then
    log_ok "TCP probe configured on port 8000"
else
    HTTP_PROBE=$(kubectl get pods -l "app.kubernetes.io/name=$MCP_REAL_NAME" -n "$NAMESPACE" -o jsonpath='{.items[0].spec.containers[0].readinessProbe.httpGet.path}' 2>/dev/null)
    if [ -n "$HTTP_PROBE" ]; then
        log_warn "Using HTTP probe ($HTTP_PROBE) instead of TCP probe"
    else
        log_warn "Could not determine probe type"
    fi
fi

#
# Test: Wait for MCP server to be ready
#
log_section "Phase 1 Test: Wait for MCP Server Ready"

if wait_for "MCPServer pod to be ready" \
    "kubectl get pods -l app.kubernetes.io/name=$MCP_REAL_NAME -n $NAMESPACE -o jsonpath='{.items[0].status.containerStatuses[0].ready}' | grep -q 'true'" 180; then
    log_ok "MCP Server pod is ready"
else
    log_warn "MCP Server pod not ready within timeout"
    kubectl get pods -l "app.kubernetes.io/name=$MCP_REAL_NAME" -n "$NAMESPACE"
    kubectl logs -l "app.kubernetes.io/name=$MCP_REAL_NAME" -n "$NAMESPACE" --tail=20 || true
fi

#
# Test: MCP Protocol Initialize Call
#
log_section "Phase 1 Test: MCP Protocol Initialize"

MCP_REQUEST='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'

MCP_RESPONSE=$(kubectl exec -n "$NAMESPACE" "deploy/$MCP_REAL_NAME" -- \
    curl -s -X POST "http://localhost:8000/mcp" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -d "$MCP_REQUEST" 2>/dev/null || echo "CURL_FAILED")

if echo "$MCP_RESPONSE" | grep -q "protocolVersion"; then
    log_ok "MCP server responds to initialize call"
    if echo "$MCP_RESPONSE" | grep -q "mcp-kubernetes-server"; then
        log_ok "Server identifies as mcp-kubernetes-server"
    fi
    if echo "$MCP_RESPONSE" | grep -q "serverInfo"; then
        log_ok "Server returns serverInfo"
    fi
else
    log_warn "MCP initialize call did not return expected response"
    log_info "Response: $MCP_RESPONSE"
fi

#
# Cleanup Phase 1 resources
#
log_section "Phase 1 Cleanup"

kubectl delete mcpserver "$MCP_REAL_NAME" -n "$NAMESPACE" --ignore-not-found=true
kubectl delete clusterrolebinding kubernetes-mcp-test-readonly-binding --ignore-not-found=true
kubectl delete clusterrole kubernetes-mcp-test-readonly --ignore-not-found=true
kubectl delete serviceaccount kubernetes-mcp-test-sa -n "$NAMESPACE" --ignore-not-found=true
log_ok "Phase 1 resources cleaned up"

exit_with_results
