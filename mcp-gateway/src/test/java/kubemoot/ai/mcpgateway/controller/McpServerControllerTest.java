package kubemoot.ai.mcpgateway.controller;

import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.service.ToolRouter;
import org.junit.jupiter.api.Test;
import org.springframework.http.codec.ServerSentEvent;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

import java.time.Duration;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** The MCP endpoint's SSE session: its first event names the message endpoint, and replies are mirrored to it. */
class McpServerControllerTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final ToolRouter router = mock(ToolRouter.class);
    private final McpServerController controller = new McpServerController(router, MAPPER);

    private static String sessionIdIn(ServerSentEvent<String> endpointEvent) throws Exception {
        String endpoint = MAPPER.readTree(endpointEvent.data()).get("endpoint").asText();
        assertTrue(endpoint.startsWith("/mcp/messages?sessionId="), endpoint);
        return endpoint.substring(endpoint.indexOf('=') + 1);
    }

    @Test
    void theFirstEventNamesTheMessageEndpointForThatSession() throws Exception {
        var first = controller.connectSse().next().block();
        assertEquals("endpoint", first.event());
        assertTrue(sessionIdIn(first).length() > 10);
    }

    @Test
    void aReplyIsAlsoSentOnTheOpenSession() throws Exception {
        McpMessage request = McpMessage.request(1, "tools/list", Map.of());
        McpMessage reply = McpMessage.response(1, Map.of("tools", java.util.List.of()));
        when(router.handleRequest(request)).thenReturn(Mono.just(reply));

        var events = controller.connectSse().replay().autoConnect();
        String session = sessionIdIn(events.blockFirst());

        assertEquals(reply, controller.handleMessage(session, request).block());

        var second = events.skip(1).blockFirst();
        assertEquals("message", second.event());
        assertEquals(MAPPER.writeValueAsString(reply), second.data());
    }

    @Test
    void aReplyWithoutASessionOrForAnotherSessionIsReturnedAndNotStreamedToAnOpenOne() {
        McpMessage request = McpMessage.request(2, "initialize", Map.of());
        McpMessage reply = McpMessage.response(2, Map.of());
        when(router.handleRequest(request)).thenReturn(Mono.just(reply));

        StepVerifier.create(controller.connectSse())
            .expectNextMatches(event -> "endpoint".equals(event.event()))
            .then(() -> {
                assertEquals(reply, controller.handleMessage(null, request).block());
                assertEquals(reply, controller.handleMessage("no-such-session", request).block());
            })
            .expectNoEvent(Duration.ofMillis(200))
            .thenCancel()
            .verify();
    }
}
