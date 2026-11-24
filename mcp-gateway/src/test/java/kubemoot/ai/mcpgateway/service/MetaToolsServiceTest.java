package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import kubemoot.ai.mcpgateway.model.ToolSearchResult;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The two meta-tools an agent uses to find and load tools, and what each answers. */
class MetaToolsServiceTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final McpClientManager manager = mock(McpClientManager.class);
    private final ToolSearchService search = mock(ToolSearchService.class);
    private final McpGatewayProperties properties = new McpGatewayProperties();

    private MetaToolsService service(ToolSearchService searchService) {
        return new MetaToolsService(searchService, manager, properties, MAPPER, WebClient.builder());
    }

    private JsonNode run(MetaToolsService service, String tool, Map<String, Object> args) throws Exception {
        return MAPPER.readTree(service.executeMetaTool(tool, args));
    }

    @Test
    void listsBothMetaToolsAndRecognisesOnlyThoseNames() {
        MetaToolsService service = service(search);
        List<String> names = service.getMetaTools().stream().map(ToolInfo::name).toList();
        assertEquals(List.of("search_tools", "load_tools"), names);
        assertTrue(service.isMetaTool("search_tools"));
        assertTrue(service.isMetaTool("load_tools"));
        assertFalse(service.isMetaTool("echo"));
        assertFalse(service.isMetaTool(null));
    }

    @Test
    void anUnknownMetaToolIsRefused() {
        assertThrows(IllegalArgumentException.class, () -> service(search).executeMetaTool("nope", Map.of()));
    }

    @Test
    void searchReturnsEachMatchWithItsServerAndScore() throws Exception {
        when(search.searchTools("weather", 3)).thenReturn(
            List.of(new ToolSearchResult("wx", "forecast", "Gets a forecast", 0.9, Map.of())));
        JsonNode out = run(service(search), "search_tools", Map.of("query", "weather", "top_k", 3));
        assertEquals(1, out.get("count").asInt());
        assertEquals("wx", out.get("results").get(0).get("server").asText());
        assertEquals("forecast", out.get("results").get(0).get("tool").asText());
        assertEquals(0.9, out.get("results").get(0).get("score").asDouble());
    }

    @Test
    void searchWithoutTopKOrWithNonPositiveTopKUsesTheConfiguredMaximum() throws Exception {
        properties.getMetaTools().setMaxResultsPerSearch(7);
        when(search.searchTools(anyString(), anyInt())).thenReturn(List.of());
        MetaToolsService service = service(search);

        run(service, "search_tools", Map.of("query", "a", "top_k", 0));
        verify(search).searchTools("a", 7);

        // A top_k that is not a number is the default of 10, which is positive and used as given.
        run(service, "search_tools", Map.of("query", "b", "top_k", "many"));
        verify(search).searchTools("b", 10);
    }

    @Test
    void searchWithNoIndexSaysSoInsteadOfFailing() throws Exception {
        JsonNode out = run(service(null), "search_tools", Map.of("query", "x"));
        assertEquals("Tool search not available", out.get("error").asText());
        assertTrue(out.get("message").asText().contains("tool-index"));
    }

    @Test
    void anEmptySearchWithoutDiscoveryReturnsAnEmptyList() throws Exception {
        when(search.searchTools(anyString(), anyInt())).thenReturn(List.of());
        JsonNode out = run(service(search), "search_tools", Map.of("query", "x"));
        assertEquals(0, out.get("count").asInt());
        assertEquals(0, out.get("results").size());
    }

    @Test
    void anEmptySearchWithDiscoveryTellsTheAgentToRetry() throws Exception {
        properties.getMetaTools().getDiscovery().setEnabled(true);
        // Nothing listens here; the request is fire and forget and its failure is only logged.
        properties.getMetaTools().getDiscovery().setAgentEndpoint("http://127.0.0.1:1");
        when(search.searchTools(anyString(), anyInt())).thenReturn(List.of());
        JsonNode out = run(service(search), "search_tools", Map.of("query", "x"));
        assertEquals("searching", out.get("status").asText());
        assertEquals("x", out.get("query").asText());
        assertEquals(0, out.get("count").asInt());
    }

    @Test
    void loadReportsLoadedEmptyAndFailedServersSeparately() throws Exception {
        when(manager.getToolsForServer("a")).thenReturn(List.of(
            new ToolInfo("t1", "d", Map.of(), "a", "a"), new ToolInfo("t2", "d", Map.of(), "a", "a")));
        when(manager.getToolsForServer("b")).thenReturn(List.of());
        when(manager.getToolsForServer("c")).thenThrow(new IllegalStateException("gone"));
        JsonNode servers = run(service(search), "load_tools", Map.of("servers", List.of("a", "b", "c"))).get("servers");

        assertEquals("loaded", servers.get("a").get("status").asText());
        assertEquals(2, servers.get("a").get("toolCount").asInt());
        assertEquals("empty", servers.get("b").get("status").asText());
        assertEquals("error", servers.get("c").get("status").asText());
        assertEquals("gone", servers.get("c").get("message").asText());
        verify(manager).getToolsForServer(eq("a"));
    }

    @Test
    void loadWithNoServersIsAnError() throws Exception {
        MetaToolsService service = service(search);
        assertEquals("No servers specified", run(service, "load_tools", Map.of("servers", List.of())).get("error").asText());
        assertEquals("No servers specified", run(service, "load_tools", new HashMap<>()).get("error").asText());
    }
}
