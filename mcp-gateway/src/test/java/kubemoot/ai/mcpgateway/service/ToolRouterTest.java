package kubemoot.ai.mcpgateway.service;

import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import reactor.core.publisher.Mono;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** How the MCP endpoint answers each method, and where a tool call goes. */
class ToolRouterTest {

    private final McpClientManager manager = mock(McpClientManager.class);
    private final MetaToolsService meta = mock(MetaToolsService.class);

    private McpMessage handle(ToolRouter router, String method, Map<String, Object> params) {
        return router.handleRequest(McpMessage.request(1, method, params)).block();
    }

    private static int code(McpMessage message) {
        assertNotNull(message.error());
        return message.error().code();
    }

    @Test
    void aRequestWithoutAMethodIsInvalid() {
        assertEquals(-32600, code(new ToolRouter(manager, null).handleRequest(
            McpMessage.request(1, null, Map.of())).block()));
    }

    @Test
    void anUnknownMethodIsNotFound() {
        assertEquals(-32601, code(handle(new ToolRouter(manager, null), "bogus", Map.of())));
    }

    @Test
    void initializeAdvertisesToolsResourcesAndPrompts() {
        McpMessage reply = handle(new ToolRouter(manager, null), "initialize", Map.of());
        @SuppressWarnings("unchecked")
        Map<String, Object> result = (Map<String, Object>) reply.result();
        assertEquals("2024-11-05", result.get("protocolVersion"));
        assertEquals(Map.of("tools", Map.of("listChanged", true),
            "resources", Map.of("subscribe", false, "listChanged", true),
            "prompts", Map.of("listChanged", true)), result.get("capabilities"));
    }

    @Test
    void initializedAndTheEmptyListsAnswerWithEmptyResults() {
        ToolRouter router = new ToolRouter(manager, null);
        assertEquals(Map.of(), handle(router, "initialized", Map.of()).result());
        assertEquals(Map.of("resources", List.of()), handle(router, "resources/list", Map.of()).result());
        assertEquals(Map.of("prompts", List.of()), handle(router, "prompts/list", Map.of()).result());
    }

    @Test
    void readingResourcesAndGettingPromptsAreNotImplemented() {
        ToolRouter router = new ToolRouter(manager, null);
        assertEquals(-32601, code(handle(router, "resources/read", Map.of())));
        assertEquals(-32601, code(handle(router, "prompts/get", Map.of())));
    }

    @Test
    void toolsListFillsMissingDescriptionAndSchemaAndAppendsMetaTools() {
        when(manager.getAllTools()).thenReturn(List.of(new ToolInfo("echo", null, null, "s1", "s1")));
        when(meta.getMetaTools()).thenReturn(List.of(new ToolInfo("search_tools", "find", Map.of("a", 1), "_meta", "_meta")));
        @SuppressWarnings("unchecked")
        Map<String, Object> result = (Map<String, Object>) handle(new ToolRouter(manager, meta), "tools/list", Map.of()).result();
        assertEquals(List.of(
            Map.of("name", "echo", "description", "", "inputSchema", Map.of()),
            Map.of("name", "search_tools", "description", "find", "inputSchema", Map.of("a", 1))),
            result.get("tools"));
    }

    @Test
    void toolsCallWithoutAParamsNameIsInvalid() {
        ToolRouter router = new ToolRouter(manager, null);
        assertEquals(-32602, code(handle(router, "tools/call", Map.of("arguments", Map.of()))));
        assertEquals(-32602, code(router.handleRequest(McpMessage.request(1, "tools/call", null)).block()));
    }

    @Test
    void aToolNoServerOffersIsNotFoundAndNothingIsForwarded() {
        when(manager.findServerForTool("ghost")).thenReturn(null);
        ToolRouter router = new ToolRouter(manager, null);
        assertEquals(-32602, code(handle(router, "tools/call", Map.of("name", "ghost"))));
        verify(manager, never()).forwardRequest(anyString(), any(), any());
    }

    @Test
    void aMetaToolIsAnsweredHereAndNeverForwarded() {
        when(meta.isMetaTool("search_tools")).thenReturn(true);
        when(meta.executeMetaTool("search_tools", Map.of("query", "x"))).thenReturn("{\"count\":0}");
        McpMessage reply = handle(new ToolRouter(manager, meta), "tools/call",
            Map.of("name", "search_tools", "arguments", Map.of("query", "x")));
        assertEquals(Map.of("content", List.of(Map.of("type", "text", "text", "{\"count\":0}"))), reply.result());
        verify(manager, never()).forwardRequest(anyString(), any(), any());
    }

    @Test
    void aNonMapArgumentsValueBecomesEmptyArguments() {
        when(manager.findServerForTool("t")).thenReturn("s1");
        when(manager.forwardRequest(anyString(), any(), any())).thenReturn(Mono.just(McpMessage.response(1, Map.of())));
        handle(new ToolRouter(manager, null), "tools/call", Map.of("name", "t", "arguments", "oops"));
        var captor = ArgumentCaptor.forClass(McpMessage.class);
        verify(manager).forwardRequest(anyString(), captor.capture(), any());
        assertEquals(Map.of(), captor.getValue().params().get("arguments"));
        assertNull(captor.getValue().params().get("_meta"));
    }
}
