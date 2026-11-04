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

/**
 * A tool call through the gateway names the discussion thread in {@code _meta} for the tools that
 * read stored results, with the requester's fields beside it, and writes the thread into the paths
 * of {@code execute_code}.
 */
class McpClientServiceWorkspaceTest {

    private static final String PREFIX = "crew-homelab-pilot/homelab-pilot/0071d018-24cb-4532-a560-fc12a788e654/";

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

    @SuppressWarnings("unchecked")
    private Map<String, Object> metaSentFor(String tool) {
        ArgumentCaptor<Map<String, Object>> meta = ArgumentCaptor.forClass(Map.class);
        verify(gateway).callTool(eq(tool), any(), meta.capture());
        return meta.getValue();
    }

    @SuppressWarnings("unchecked")
    private Map<String, Object> argumentsSentFor(String tool) {
        ArgumentCaptor<Map<String, Object>> arguments = ArgumentCaptor.forClass(Map.class);
        verify(gateway).callTool(eq(tool), arguments.capture(), any());
        return arguments.getValue();
    }

    @Test
    void theQueryToolGetsTheThreadsPrefixInMeta() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            service.callTool("workspace_query", Map.of("sql", "SELECT 1"));
        }
        assertEquals(Map.of("workspacePrefix", PREFIX), metaSentFor("workspace_query"));
    }

    @Test
    void anArtifactToolGetsItAndTheRequestersFieldsStayBesideIt() {
        var requester = Requester.of("America/New_York", "2026-11-04T09:00:00").orElseThrow();
        try (var r = RequesterScope.bind(requester); var scope = WorkspaceScope.bind(PREFIX)) {
            service.callTool("artifact_head", Map.of("key", "k8s-workloads/001-pods_list"));
        }
        var meta = metaSentFor("artifact_head");
        assertEquals(PREFIX, meta.get("workspacePrefix"));
        assertEquals("America/New_York", meta.get("timeZone"));
        assertEquals("2026-11-04", meta.get("requestDate"));
    }

    @Test
    void aToolThatDoesNotReadStoredResultsDoesNotLearnTheThread() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            service.callTool("resources_list", Map.of("kind", "Job"));
        }
        assertTrue(metaSentFor("resources_list").isEmpty());
    }

    @Test
    void outsideAThreadNoPrefixIsSent() {
        service.callTool("workspace_query", Map.of("sql", "SELECT 1"));
        assertTrue(metaSentFor("workspace_query").isEmpty());
    }

    @Test
    void theKeysAgentsTypeAreSentAsTheyAreToTheQueryTool() {
        var artifacts = Map.of("jobs", "k8s-workloads/002-resources_list");
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            service.callTool("workspace_query", Map.of("artifacts", artifacts, "sql", "SELECT 1"));
        }
        assertEquals(artifacts, argumentsSentFor("workspace_query").get("artifacts"),
                "the tool resolves the key, the runtime does not rewrite it");
    }

    @Test
    void executeCodeGetsItsShortArtifactPathsWrittenOut() {
        try (var scope = WorkspaceScope.bind(PREFIX)) {
            service.callTool("execute_code", Map.of("language", "python",
                    "code", "print(open('/artifacts/k8s-workloads/002-resources_list').read())"));
        }
        assertEquals("print(open('/artifacts/" + PREFIX + "k8s-workloads/002-resources_list').read())",
                argumentsSentFor("execute_code").get("code"));
    }
}
