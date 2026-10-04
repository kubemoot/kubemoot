#!/usr/bin/env bash
#
# Operator Test Library
# Shared utilities for all test scripts
#
# Usage: source this file in test scripts
#   source "$(dirname "$0")/lib.sh"
#

# Prevent double-sourcing
if [[ -n "${_KUBEMOOT_TEST_LIB_LOADED:-}" ]]; then
    return 0
fi
_KUBEMOOT_TEST_LIB_LOADED=1

set -euo pipefail

# Configuration defaults
export NAMESPACE="${NAMESPACE:-kubemoot}"
export OLLAMA_NAMESPACE="${OLLAMA_NAMESPACE:-ollama}"
export TIMEOUT="${TIMEOUT:-120}"

# Colors for output
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[0;33m'
readonly BLUE='\033[0;34m'
readonly NC='\033[0m' # No Color

# Test counters
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0

# Test name for reporting
TEST_NAME="${TEST_NAME:-unknown}"

#
# Logging functions
#
log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_ok() { echo -e "${GREEN}[PASS]${NC} $1"; TESTS_PASSED=$((TESTS_PASSED + 1)); }
log_fail() { echo -e "${RED}[FAIL]${NC} $1"; TESTS_FAILED=$((TESTS_FAILED + 1)); }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_skip() { echo -e "${YELLOW}[SKIP]${NC} $1"; TESTS_SKIPPED=$((TESTS_SKIPPED + 1)); }

log_section() {
    echo ""
    echo "----------------------------------------------"
    echo "  $1"
    echo "----------------------------------------------"
}

log_header() {
    echo "=============================================="
    echo "  $1"
    echo "=============================================="
    echo ""
}

#
# Wait for condition with timeout
#
wait_for() {
    local description="$1"
    local check_cmd="$2"
    local timeout="${3:-$TIMEOUT}"

    log_info "Waiting for: $description (timeout: ${timeout}s)"
    local start_time=$(date +%s)

    while true; do
        if eval "$check_cmd" > /dev/null 2>&1; then
            return 0
        fi

        local elapsed=$(($(date +%s) - start_time))
        if [ $elapsed -ge $timeout ]; then
            return 1
        fi
        sleep 2
    done
}

#
# Pre-flight checks
#
check_cluster() {
    if ! kubectl cluster-info > /dev/null 2>&1; then
        log_fail "Cannot connect to Kubernetes cluster"
        return 1
    fi
    log_ok "Kubernetes cluster accessible"
}

check_namespace() {
    local ns="${1:-$NAMESPACE}"
    if ! kubectl get namespace "$ns" > /dev/null 2>&1; then
        log_fail "Namespace '$ns' does not exist"
        return 1
    fi
    log_ok "Namespace '$ns' exists"
}

# The operator's own pods: the chart gives the liaison, the notification mock, and the
# NATS bootstrap job the same app.kubernetes.io/name, so control-plane picks the operator.
OPERATOR_SELECTOR="${OPERATOR_SELECTOR:-app.kubernetes.io/name=kubemoot-operator,control-plane=controller-manager}"

check_operator() {
    if [ -z "$(kubectl get deploy -n "$NAMESPACE" -l "$OPERATOR_SELECTOR" -o name 2>/dev/null)" ]; then
        log_fail "Kubemoot operator not found in namespace '$NAMESPACE'"
        return 1
    fi
    log_ok "Kubemoot operator is deployed"
    wait_for_operator_leader
}

#
# operator_leader_pod: the operator pod that holds a leader lease in the namespace, if
# that pod is Ready and not shutting down; prints nothing otherwise. Pod readiness alone
# is not enough: during a rollout the new pod is Ready while the old one still holds the
# lease, and no controller reconciles until the lease changes hands. A lease holder is
# <pod name>_<uuid>.
#
operator_leader_pod() {
    local holders ready pod
    holders=$(kubectl get lease -n "$NAMESPACE" \
        -o jsonpath='{range .items[*]}{.spec.holderIdentity}{"\n"}{end}' 2>/dev/null | sed 's/_.*//')
    ready=$(kubectl get pods -n "$NAMESPACE" -l "$OPERATOR_SELECTOR" \
        -o jsonpath='{range .items[*]}{.metadata.name}|{.metadata.deletionTimestamp}|{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' \
        2>/dev/null | awk -F'|' '$2 == "" && $3 == "True" {print $1}')
    for pod in $holders; do
        if grep -qx "$pod" <<<"$ready"; then
            echo "$pod"
            return 0
        fi
    done
    return 1
}

wait_for_operator_leader() {
    if wait_for "a Ready operator pod to hold the leader lease" operator_leader_pod "${LEADER_TIMEOUT:-180}"; then
        log_ok "Kubemoot operator is leading ($(operator_leader_pod))"
        return 0
    fi
    log_fail "No Ready operator pod holds the leader lease"
    return 1
}

check_crd() {
    local crd="$1"
    if ! kubectl get crd "$crd" > /dev/null 2>&1; then
        log_fail "CRD '$crd' not installed"
        return 1
    fi
    log_ok "CRD '$crd' is installed"
}

check_ollama() {
    if ! kubectl get deploy ollama -n "$OLLAMA_NAMESPACE" > /dev/null 2>&1; then
        log_fail "Ollama deployment not found in namespace '$OLLAMA_NAMESPACE'"
        return 1
    fi
    log_ok "Ollama is deployed"
}

check_pgvector() {
    # Check for pgvector in homelab-pilot namespace (primary location)
    if kubectl get svc homelab-pilot-db -n homelab-pilot > /dev/null 2>&1; then
        log_ok "pgvector database is available (homelab-pilot-db)"
        return 0
    fi
    # Fallback: check legacy location
    if kubectl get svc postgresql -n clusteragent-db > /dev/null 2>&1; then
        log_ok "pgvector database is available (clusteragent-db)"
        return 0
    fi
    log_fail "pgvector database not found"
    return 1
}

#
# Ollama helpers
#
ollama_has_model() {
    local model="$1"
    kubectl exec -n "$OLLAMA_NAMESPACE" deploy/ollama -- ollama list 2>/dev/null | grep -q "^${model}"
}

ollama_remove_model() {
    local model="$1"
    kubectl exec -n "$OLLAMA_NAMESPACE" deploy/ollama -- ollama rm "$model" 2>/dev/null || true
}

#
# Resource cleanup helpers
#
cleanup_resource() {
    local kind="$1"
    local name="$2"
    local ns="${3:-$NAMESPACE}"
    kubectl delete "$kind" "$name" -n "$ns" --ignore-not-found=true 2>/dev/null || true
}

#
# Test results
#
print_results() {
    echo ""
    echo "=============================================="
    echo "  Test Results: $TEST_NAME"
    echo "=============================================="
    echo -e "  ${GREEN}Passed:${NC}  $TESTS_PASSED"
    echo -e "  ${RED}Failed:${NC}  $TESTS_FAILED"
    echo -e "  ${YELLOW}Skipped:${NC} $TESTS_SKIPPED"
    echo "=============================================="
}

exit_with_results() {
    print_results
    if [ $TESTS_FAILED -gt 0 ]; then
        exit 1
    fi
    echo ""
    log_ok "All $TEST_NAME tests passed!"
    exit 0
}

#
# Argument parsing helper
#
parse_common_args() {
    DO_CLEANUP=true
    CLEANUP_ONLY=false

    for arg in "$@"; do
        case $arg in
            --cleanup)
                CLEANUP_ONLY=true
                ;;
            --no-cleanup)
                DO_CLEANUP=false
                ;;
            --help|-h)
                echo "Usage: $0 [--cleanup|--no-cleanup]"
                echo "  --cleanup     Only clean up test resources, don't run tests"
                echo "  --no-cleanup  Leave test resources after running tests"
                exit 0
                ;;
        esac
    done
}
