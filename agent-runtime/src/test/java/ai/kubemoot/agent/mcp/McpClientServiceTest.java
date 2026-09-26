package ai.kubemoot.agent.mcp;

import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Unit tests for the pure per-server tool filtering helper extracted from
 * connectToServer. Verifies the enabled/disabled allow rules used to decide
 * which MCP tools a direct-mode server contributes.
 */
class McpClientServiceTest {

    @Test
    void allowsToolWhenNoFilters() {
        assertTrue(McpClientService.isServerToolAllowed("get_pods", List.of(), List.of()));
    }

    @Test
    void allowsToolPresentInEnabledList() {
        assertTrue(McpClientService.isServerToolAllowed("get_pods", List.of("get_pods"), List.of()));
    }

    @Test
    void blocksToolAbsentFromNonEmptyEnabledList() {
        assertFalse(McpClientService.isServerToolAllowed("delete_pod", List.of("get_pods"), List.of()));
    }

    @Test
    void blocksToolPresentInDisabledList() {
        assertFalse(McpClientService.isServerToolAllowed("delete_pod", List.of(), List.of("delete_pod")));
    }

    @Test
    void disabledTakesPrecedenceOverEnabled() {
        assertFalse(McpClientService.isServerToolAllowed(
                "delete_pod", List.of("delete_pod"), List.of("delete_pod")));
    }

    @Test
    void allowsToolNotInDisabledListWhenEnabledEmpty() {
        assertTrue(McpClientService.isServerToolAllowed("get_pods", List.of(), List.of("delete_pod")));
    }
}
