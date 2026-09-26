#!/usr/bin/env bash
# Post-deploy smoke test for Kubemoot operator stack.
# Usage: ./kubemoot/scripts/smoke-test.sh [namespace]
# Exit code = number of failures.

set -euo pipefail

NS="${1:-kubemoot}"
FAILURES=0

pass() { echo "  ✓ $1"; }
fail() { echo "  ✗ $1"; FAILURES=$((FAILURES + 1)); }

echo "Kubemoot smoke test — namespace: $NS"
echo "---"

# 1. Operator pod running
if kubectl -n "$NS" get pods -l app.kubernetes.io/name=kubemoot-operator -o jsonpath='{.items[0].status.phase}' 2>/dev/null | grep -q Running; then
    pass "Operator pod running"
else
    fail "Operator pod not running"
fi

# 2. KubemootConfig exists
if kubectl get kubemootconfig default &>/dev/null; then
    pass "KubemootConfig exists"
else
    fail "KubemootConfig 'default' not found"
fi

# 3. Coordinator agent phase = Running
COORD_PHASE=$(kubectl -n "$NS" get agent homelab-coordinator -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
if [ "$COORD_PHASE" = "Running" ]; then
    pass "Coordinator agent phase=Running"
else
    fail "Coordinator agent phase=$COORD_PHASE (expected Running)"
fi

# 4. Coordinator rollout status
if kubectl -n "$NS" rollout status deployment/homelab-coordinator --timeout=10s &>/dev/null; then
    pass "Coordinator deployment healthy"
else
    fail "Coordinator deployment rollout not healthy"
fi

# 5. At least one ModelProvider ready
READY_PROVIDERS=$(kubectl -n "$NS" get modelprovider -o jsonpath='{range .items[*]}{.status.ready}{"\n"}{end}' 2>/dev/null | grep -c true || true)
if [ "$READY_PROVIDERS" -ge 1 ]; then
    pass "ModelProvider(s) ready: $READY_PROVIDERS"
else
    fail "No ModelProviders ready"
fi

# 6. NATS pod running
if kubectl -n nats get pods -l app.kubernetes.io/name=nats -o jsonpath='{.items[0].status.phase}' 2>/dev/null | grep -q Running; then
    pass "NATS pod running"
else
    fail "NATS pod not running"
fi

# 7. Coordinator readiness endpoint
COORD_POD=$(kubectl -n "$NS" get pods -l app=homelab-coordinator -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$COORD_POD" ]; then
    if kubectl -n "$NS" exec "$COORD_POD" -- wget -qO- http://localhost:8080/q/health/ready 2>/dev/null | grep -q '"status"'; then
        pass "Coordinator readiness endpoint responds"
    else
        fail "Coordinator readiness endpoint unreachable"
    fi
else
    fail "Coordinator pod not found for readiness check"
fi

# 8. MCP gateway healthy
GW_PODS=$(kubectl -n "$NS" get pods -l app=mcp-gateway -o jsonpath='{.items[*].status.phase}' 2>/dev/null || echo "")
if echo "$GW_PODS" | grep -q Running; then
    pass "MCP gateway healthy"
else
    fail "MCP gateway not running"
fi

# 9. No agents in Error state
ERROR_AGENTS=$(kubectl -n "$NS" get agents -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' 2>/dev/null | grep -c Error || true)
if [ "$ERROR_AGENTS" -eq 0 ]; then
    pass "No agents in Error state"
else
    fail "$ERROR_AGENTS agent(s) in Error state"
fi

# 10. Operator zero restarts
RESTARTS=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=kubemoot-operator -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}' 2>/dev/null || echo "-1")
if [ "$RESTARTS" = "0" ]; then
    pass "Operator zero restarts"
else
    fail "Operator has $RESTARTS restart(s)"
fi

echo "---"
if [ "$FAILURES" -eq 0 ]; then
    echo "All 10 checks passed."
else
    echo "$FAILURES check(s) failed."
fi

exit "$FAILURES"
