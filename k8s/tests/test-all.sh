#!/usr/bin/env bash
#
# Operator - Full Test Suite
# Runs all resource tests in sequence
#
# Usage:
#   ./test-all.sh              # Run all tests
#   ./test-all.sh --cleanup    # Clean up all test resources
#   ./test-all.sh --no-cleanup # Leave resources after tests
#   ./test-all.sh --fast       # Skip Model test (slow due to pull)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[0;33m'
readonly BLUE='\033[0;34m'
readonly BOLD='\033[1m'
readonly NC='\033[0m'

# Test results
declare -A TEST_RESULTS
TOTAL_PASSED=0
TOTAL_FAILED=0
TOTAL_SKIPPED=0

# Options
DO_CLEANUP=true
CLEANUP_ONLY=false
FAST_MODE=false

# Parse arguments
for arg in "$@"; do
    case $arg in
        --cleanup)
            CLEANUP_ONLY=true
            ;;
        --no-cleanup)
            DO_CLEANUP=false
            ;;
        --fast)
            FAST_MODE=true
            ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --cleanup     Only clean up all test resources"
            echo "  --no-cleanup  Leave resources after running tests"
            echo "  --fast        Skip slow tests (Model pull)"
            echo "  --help        Show this help message"
            exit 0
            ;;
    esac
done

# Build cleanup args
CLEANUP_ARGS=""
if [ "$DO_CLEANUP" = false ]; then
    CLEANUP_ARGS="--no-cleanup"
fi

echo ""
echo -e "${BOLD}=============================================="
echo "  Operator - Full Test Suite"
echo "==============================================${NC}"
echo ""
echo "  Namespace: ${NAMESPACE:-kubemoot}"
echo "  Cleanup:   $DO_CLEANUP"
echo "  Fast mode: $FAST_MODE"
echo ""

if [ "$CLEANUP_ONLY" = true ]; then
    echo -e "${BLUE}[INFO]${NC} Running cleanup for all tests..."
    "$SCRIPT_DIR/test-modelprovider.sh" --cleanup
    "$SCRIPT_DIR/test-model.sh" --cleanup
    "$SCRIPT_DIR/test-mcpserver.sh" --cleanup
    "$SCRIPT_DIR/test-embeddingmodel.sh" --cleanup
    "$SCRIPT_DIR/test-ragsource.sh" --cleanup
    "$SCRIPT_DIR/test-ragsource-schedule.sh" --cleanup
    "$SCRIPT_DIR/test-rag-query.sh" --cleanup
    "$SCRIPT_DIR/test-ragsource-queryservice.sh" --cleanup
    "$SCRIPT_DIR/test-agent.sh" --cleanup
    "$SCRIPT_DIR/test-mcp-gateway.sh" --cleanup
    "$SCRIPT_DIR/test-agent-mcp.sh" --cleanup
    "$SCRIPT_DIR/test-mcpqualitypolicy.sh" --cleanup
    "$SCRIPT_DIR/test-mcpcatalog.sh" --cleanup
    "$SCRIPT_DIR/test-internal-agents.sh" --cleanup
    echo ""
    echo -e "${GREEN}[DONE]${NC} All test resources cleaned up"
    exit 0
fi

run_test() {
    local name="$1"
    local script="$2"
    local skip="${3:-false}"

    echo ""
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BOLD}  Running: $name${NC}"
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

    if [ "$skip" = true ]; then
        echo -e "${YELLOW}[SKIP]${NC} Skipped (--fast mode)"
        TEST_RESULTS[$name]="SKIPPED"
        TOTAL_SKIPPED=$((TOTAL_SKIPPED + 1))
        return 0
    fi

    if "$SCRIPT_DIR/$script" $CLEANUP_ARGS; then
        TEST_RESULTS[$name]="PASSED"
        TOTAL_PASSED=$((TOTAL_PASSED + 1))
    else
        TEST_RESULTS[$name]="FAILED"
        TOTAL_FAILED=$((TOTAL_FAILED + 1))
    fi
}

#
# Run individual tests
#
run_test "ModelProvider" "test-modelprovider.sh"

if [ "$FAST_MODE" = true ]; then
    run_test "Model" "test-model.sh" true
else
    run_test "Model" "test-model.sh"
fi

run_test "MCPServer" "test-mcpserver.sh"

if [ "$FAST_MODE" = true ]; then
    run_test "EmbeddingModel" "test-embeddingmodel.sh" true
else
    run_test "EmbeddingModel" "test-embeddingmodel.sh"
fi

run_test "RAGSource" "test-ragsource.sh"

run_test "RAGSource-Schedule" "test-ragsource-schedule.sh"

run_test "RAG-Query" "test-rag-query.sh"

run_test "RAGSource-QueryService" "test-ragsource-queryservice.sh"

run_test "Agent" "test-agent.sh"

run_test "MCPGateway" "test-mcp-gateway.sh"

run_test "Agent-MCP" "test-agent-mcp.sh"

run_test "MCPQualityPolicy" "test-mcpqualitypolicy.sh"

run_test "MCPCatalog" "test-mcpcatalog.sh"

run_test "InternalAgents" "test-internal-agents.sh"

#
# Print summary
#
echo ""
echo -e "${BOLD}=============================================="
echo "  Full Test Suite Results"
echo "==============================================${NC}"
echo ""

for test_name in "ModelProvider" "Model" "MCPServer" "EmbeddingModel" "RAGSource" "RAGSource-Schedule" "RAG-Query" "RAGSource-QueryService" "Agent" "MCPGateway" "Agent-MCP" "MCPQualityPolicy" "MCPCatalog" "InternalAgents"; do
    result="${TEST_RESULTS[$test_name]:-UNKNOWN}"
    case $result in
        PASSED)
            echo -e "  $test_name: ${GREEN}$result${NC}"
            ;;
        FAILED)
            echo -e "  $test_name: ${RED}$result${NC}"
            ;;
        SKIPPED)
            echo -e "  $test_name: ${YELLOW}$result${NC}"
            ;;
        *)
            echo -e "  $test_name: $result"
            ;;
    esac
done

echo ""
echo "----------------------------------------------"
echo -e "  ${GREEN}Passed:${NC}  $TOTAL_PASSED"
echo -e "  ${RED}Failed:${NC}  $TOTAL_FAILED"
echo -e "  ${YELLOW}Skipped:${NC} $TOTAL_SKIPPED"
echo "=============================================="

if [ $TOTAL_FAILED -gt 0 ]; then
    echo ""
    echo -e "${RED}[FAIL]${NC} Some tests failed!"
    exit 1
fi

echo ""
echo -e "${GREEN}[PASS]${NC} All tests passed!"
exit 0
