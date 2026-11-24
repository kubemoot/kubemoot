package kubemoot.ai.mcpgateway.controller;

import kubemoot.ai.mcpgateway.model.FeedbackEntry;
import kubemoot.ai.mcpgateway.model.RegisterServerRequest;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import kubemoot.ai.mcpgateway.model.ToolSearchResult;
import kubemoot.ai.mcpgateway.service.McpClientManager;
import kubemoot.ai.mcpgateway.service.ToolSearchService;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpStatus;
import org.springframework.web.server.ResponseStatusException;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The admin endpoints: a missing server is a 404 and nothing is touched; the rest answer from the manager. */
class AdminControllerTest {

    private final McpClientManager manager = mock(McpClientManager.class);
    private final ToolSearchService search = mock(ToolSearchService.class);
    private final AdminController controller = new AdminController(manager, search);
    private final ServerRegistration server = mock(ServerRegistration.class);

    @Test
    void registeringAServerReturns201WithTheRegistration() {
        when(manager.registerServer(eq("n"), eq("http://x"), eq("http"), any(), any(), any())).thenReturn(server);
        var response = controller.registerServer(new RegisterServerRequest("n", "http://x", "http", null, null, null));
        assertEquals(HttpStatus.CREATED, response.getStatusCode());
        assertEquals(server, response.getBody());
    }

    @Test
    void anUnknownServerIs404ForGetDeleteToolsAndFeedback() {
        when(manager.getServer("gone")).thenReturn(null);
        assertEquals(HttpStatus.NOT_FOUND, controller.getServer("gone").getStatusCode());
        assertEquals(HttpStatus.NOT_FOUND, controller.unregisterServer("gone").getStatusCode());
        assertEquals(HttpStatus.NOT_FOUND, controller.listServerTools("gone").getStatusCode());
        assertEquals(HttpStatus.NOT_FOUND, controller.getServerFeedback("gone").getStatusCode());
        verify(manager, never()).unregisterServer(any());
        verify(manager, never()).consumeFeedback(any());
    }

    @Test
    void aKnownServerIsReturnedUnregisteredListedAndItsFeedbackConsumed() {
        when(manager.getServer("s1")).thenReturn(server);
        List<ToolInfo> tools = List.of(new ToolInfo("t", "d", Map.of(), "s1", "n"));
        List<FeedbackEntry> feedback = List.of(FeedbackEntry.success("n", "s1", "discover", 1));
        when(manager.getToolsForServer("s1")).thenReturn(tools);
        when(manager.consumeFeedback("s1")).thenReturn(feedback);

        assertEquals(server, controller.getServer("s1").getBody());
        assertEquals(tools, controller.listServerTools("s1").getBody());
        assertEquals(feedback, controller.getServerFeedback("s1").getBody());
        assertEquals(HttpStatus.NO_CONTENT, controller.unregisterServer("s1").getStatusCode());
        verify(manager).unregisterServer("s1");
    }

    @Test
    void allToolsAreCounted() {
        when(manager.getAllTools()).thenReturn(List.of(new ToolInfo("t", "d", Map.of(), "s1", "n")));
        assertEquals(1, controller.listTools().get("count"));
    }

    @Test
    void searchAnswersFromTheIndexOrIs503WhenThereIsNone() {
        List<ToolSearchResult> hits = List.of(new ToolSearchResult("n", "t", "d", 0.5, Map.of()));
        when(search.searchTools("q", 4)).thenReturn(hits);
        assertEquals(hits, controller.searchTools("q", 4));

        ResponseStatusException refused = assertThrows(ResponseStatusException.class,
            () -> new AdminController(manager, null).searchTools("q", 4));
        assertEquals(HttpStatus.SERVICE_UNAVAILABLE, refused.getStatusCode());
    }
}
