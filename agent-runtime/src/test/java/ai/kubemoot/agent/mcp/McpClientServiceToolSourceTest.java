package ai.kubemoot.agent.mcp;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.gateway.GatewayClient;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** The description of where tools come from, used when a declared tool is missing. */
class McpClientServiceToolSourceTest {

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
        gateway = mock(GatewayClient.class);
        when(gateway.isConfigured()).thenReturn(true);
        when(gateway.endpoint()).thenReturn("http://gateway:8080");
        when(gateway.listTools()).thenReturn(List.of());
        when(gateway.lastListError()).thenReturn(Optional.empty());
        service = new McpClientService(props);
        service.gatewayClient = gateway;
        service.initialize();
    }

    @Test
    void aReachableGateway_isNamedByAddress() {
        assertEquals("the MCP gateway at http://gateway:8080", service.describeToolSource());
    }

    private static McpClientService unconfigured(boolean gatewayEnabled, GatewayClient client) {
        var props = mock(AgentProperties.class);
        var gatewayProps = mock(AgentProperties.Gateway.class);
        when(props.gateway()).thenReturn(gatewayProps);
        when(gatewayProps.enabled()).thenReturn(gatewayEnabled);
        when(gatewayProps.endpoint()).thenReturn(Optional.of("http://gateway:8080"));
        when(props.mcpServers()).thenReturn(Optional.empty());
        var service = new McpClientService(props);
        service.gatewayClient = client;
        service.initialize();
        return service;
    }

    @Test
    void directModeWithNoConnectedServer_saysSo() {
        assertEquals("no MCP server is connected", unconfigured(false, null).describeToolSource());
    }

    @Test
    void aGatewayWithNoAddress_saysSo() {
        var client = mock(GatewayClient.class);
        when(client.isConfigured()).thenReturn(false);
        assertEquals("the MCP gateway (no address configured)", unconfigured(true, client).describeToolSource());
    }

    @Test
    void anUnreachableGateway_carriesItsError() {
        when(gateway.lastListError()).thenReturn(Optional.of("gateway unreachable: ConnectException"));
        var source = service.describeToolSource();
        assertTrue(source.contains("http://gateway:8080"), source);
        assertTrue(source.contains("gateway unreachable: ConnectException"), source);
    }
}
