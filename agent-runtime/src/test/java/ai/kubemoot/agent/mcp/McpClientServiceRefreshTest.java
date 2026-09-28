package ai.kubemoot.agent.mcp;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.gateway.GatewayClient;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * The gateway tool list is re-read when it is empty or stale, so a tool server that
 * registers after the agent started is used without restarting the agent.
 */
class McpClientServiceRefreshTest {

    private GatewayClient gateway;
    private McpClientService service;

    private static GatewayClient.ToolInfo tool(String name) {
        return new GatewayClient.ToolInfo(name, "does " + name, null);
    }

    private static List<String> names(McpClientService s) {
        return s.listTools().stream().map(McpClientService.ToolInfo::name).sorted().toList();
    }

    @BeforeEach
    void setUp() {
        var props = mock(AgentProperties.class);
        var gatewayProps = mock(AgentProperties.Gateway.class);
        when(props.gateway()).thenReturn(gatewayProps);
        when(gatewayProps.enabled()).thenReturn(true);
        when(gatewayProps.endpoint()).thenReturn(Optional.of("http://gateway:8080"));
        when(props.mcpServers()).thenReturn(Optional.empty());
        when(props.enabledTools()).thenReturn(Optional.empty());
        when(props.disabledTools()).thenReturn(Optional.empty());

        gateway = mock(GatewayClient.class);
        when(gateway.isConfigured()).thenReturn(true);
        service = new McpClientService(props);
        service.gatewayClient = gateway;
    }

    @Test
    void anEmptyListIsReadAgainAndPicksUpALateServer() {
        when(gateway.listTools()).thenReturn(List.of());
        service.initialize();
        assertEquals(List.of(), names(service));

        when(gateway.listTools()).thenReturn(List.of(tool("fetch")));
        var soon = Instant.now();
        assertFalse(service.refreshGatewayToolsIfStale(soon), "an empty list is not re-read on every turn");
        verify(gateway, times(1)).listTools();

        var afterRetry = soon.plus(McpClientService.EMPTY_TOOL_LIST_RETRY).plusSeconds(1);
        assertTrue(service.refreshGatewayToolsIfStale(afterRetry));
        assertEquals(List.of("fetch"), names(service));
    }

    @Test
    void aFreshListIsNotReadAgainBeforeItsTimeToLive() {
        when(gateway.listTools()).thenReturn(List.of(tool("fetch")));
        service.initialize();

        assertFalse(service.refreshGatewayToolsIfStale(Instant.now()));
        verify(gateway, times(1)).listTools();
    }

    @Test
    void aStaleListFollowsAddedAndRemovedTools() {
        when(gateway.listTools()).thenReturn(List.of(tool("fetch"), tool("search")));
        service.initialize();

        when(gateway.listTools()).thenReturn(List.of(tool("fetch"), tool("pods_list")));
        var later = Instant.now().plus(McpClientService.TOOL_LIST_TTL).plusSeconds(1);
        assertTrue(service.refreshGatewayToolsIfStale(later));
        assertEquals(List.of("fetch", "pods_list"), names(service));
    }

    @Test
    void anUnchangedStaleListReportsNoChange() {
        when(gateway.listTools()).thenReturn(List.of(tool("fetch")));
        service.initialize();

        var later = Instant.now().plus(McpClientService.TOOL_LIST_TTL).plusSeconds(1);
        assertFalse(service.refreshGatewayToolsIfStale(later));
        verify(gateway, times(2)).listTools();
    }

    @Test
    void anEmptyOrFailedReadKeepsTheKnownTools() {
        when(gateway.listTools()).thenReturn(List.of(tool("fetch")));
        service.initialize();
        var later = Instant.now().plus(McpClientService.TOOL_LIST_TTL).plusSeconds(1);

        when(gateway.listTools()).thenReturn(List.of());
        assertFalse(service.refreshGatewayToolsIfStale(later));
        assertEquals(List.of("fetch"), names(service));

        when(gateway.listTools()).thenThrow(new IllegalStateException("gateway down"));
        assertFalse(service.refreshGatewayToolsIfStale(later.plus(McpClientService.TOOL_LIST_TTL).plusSeconds(1)));
        assertEquals(List.of("fetch"), names(service));
    }

    @Test
    void nothingIsReadWhenTheGatewayIsNotConfigured() {
        when(gateway.listTools()).thenReturn(List.of());
        service.initialize();
        when(gateway.isConfigured()).thenReturn(false);

        assertFalse(service.refreshGatewayToolsIfStale(Instant.now().plus(McpClientService.TOOL_LIST_TTL).plusSeconds(1)));
        verify(gateway, times(1)).listTools();
    }
}
