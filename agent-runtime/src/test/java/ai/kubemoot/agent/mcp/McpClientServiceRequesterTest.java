package ai.kubemoot.agent.mcp;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.gateway.GatewayClient;
import ai.kubemoot.agent.requester.Requester;
import ai.kubemoot.agent.requester.RequesterScope;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** A tool call through the gateway carries the running turn's requester as MCP {@code _meta}. */
class McpClientServiceRequesterTest {

    private GatewayClient gateway;
    private McpClientService service;

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
        when(gateway.listTools()).thenReturn(List.of());
        when(gateway.callTool(any(), any(), any()))
                .thenReturn(new GatewayClient.ToolCallResult("t", true, "ok", null));
        service = new McpClientService(props);
        service.gatewayClient = gateway;
        service.initialize();
    }

    private Map<String, Object> metaSent() {
        @SuppressWarnings("unchecked")
        ArgumentCaptor<Map<String, Object>> meta = ArgumentCaptor.forClass(Map.class);
        verify(gateway).callTool(eq("t"), any(), meta.capture());
        return meta.getValue();
    }

    @Test
    void theRequestersZoneAndDateAreSent() {
        var requester = Requester.of("America/Los_Angeles", "2026-10-24T09:00:00").orElseThrow();
        try (var scope = RequesterScope.bind(requester)) {
            service.callTool("t", Map.of());
        }
        assertEquals(Map.of("timeZone", "America/Los_Angeles", "localDateTime", "2026-10-24T09:00:00",
                "requestDate", "2026-10-24"), metaSent());
    }

    @Test
    void aTurnWithoutARequesterSendsNoMeta() {
        service.callTool("t", Map.of());
        assertTrue(metaSent().isEmpty());
    }

    @Test
    void anInvalidClientZoneSendsNoMeta() {
        try (var scope = RequesterScope.bind(Requester.of("Mars/Base", "soon").orElse(null))) {
            service.callTool("t", Map.of());
        }
        assertTrue(metaSent().isEmpty());
    }

    @Test
    void mergeKeepsExistingEntriesAndTheRequesterWinsOnConflict() {
        var merged = McpClientService.mergeRequesterMeta(
                Map.of("progressToken", "p", "timeZone", "UTC"), Map.of("timeZone", "Europe/Paris"));
        assertEquals(Map.of("progressToken", "p", "timeZone", "Europe/Paris"), merged);
    }

    @Test
    void mergeToleratesNoExistingMeta() {
        assertEquals(Map.of("timeZone", "UTC"), McpClientService.mergeRequesterMeta(null, Map.of("timeZone", "UTC")));
    }
}
