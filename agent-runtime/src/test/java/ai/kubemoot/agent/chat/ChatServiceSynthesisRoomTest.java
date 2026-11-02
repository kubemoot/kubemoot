package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.modelserver.ServerSelector;
import ai.kubemoot.agent.modelserver.ServerState;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.model.chat.ChatModel;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * The room a simple call (synthesis) has in its window for stored tool results: the smallest
 * window among the servers that hold the agent's model, less the prompt (counted dense for numbers)
 * and the reply reserve; unknown when no such server publishes one.
 */
class ChatServiceSynthesisRoomTest {

    private static ServerState server(long window) {
        var state = mock(ServerState.class);
        when(state.contextLengthFor(anyString())).thenReturn(window);
        when(state.hasModel(anyString())).thenReturn(true);
        return state;
    }

    private static ChatService service(ServerSelector selector) {
        return new ChatService(mock(ChatModel.class), mock(RagClient.class), mock(McpClientService.class),
                mock(DiscussionOrchestrator.class), ChatServiceToolLoopTest.stubProperties(8, "coordinator", false, false),
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                selector, null, null, null, null, null, null, null, null);
    }

    private static ServerSelector publishing(long... windows) {
        // Build the servers before stubbing: each does its own stubbing.
        var servers = java.util.Arrays.stream(windows).mapToObj(ChatServiceSynthesisRoomTest::server).toList();
        var selector = mock(ServerSelector.class);
        when(selector.readState()).thenReturn(servers);
        return selector;
    }

    @Test
    void theWindowIsTheSmallestAnyServerPublishesForTheModel() {
        assertEquals(16_384, service(publishing(32_768, 16_384, 65_536)).simpleCallContextTokens().getAsLong());
    }

    @Test
    void aServerThatDoesNotHoldTheModelDoesNotCount() {
        // The CPU embedding server publishes 8,192 for its own model; it cannot run the agent's model,
        // so its window must not set the room of a synthesis that runs on the larger server.
        var embedding = mock(ServerState.class);
        when(embedding.contextLengthFor(anyString())).thenReturn(8_192L);
        when(embedding.hasModel(anyString())).thenReturn(false);
        var big = server(32_768);
        var selector = mock(ServerSelector.class);
        when(selector.readState()).thenReturn(java.util.List.of(embedding, big));

        assertEquals(32_768, service(selector).simpleCallContextTokens().getAsLong());
    }

    @Test
    void aServerThatPublishesNothingDoesNotCount() {
        assertEquals(32_768, service(publishing(0, 32_768)).simpleCallContextTokens().getAsLong());
    }

    @Test
    void noPublishedWindowOrNoSelectorIsUnknown() {
        assertTrue(service(publishing(0, 0)).simpleCallContextTokens().isEmpty());
        assertTrue(service(publishing()).simpleCallRoomTokens("s", "u").isEmpty());
        assertTrue(service(null).simpleCallRoomTokens("s", "u").isEmpty());
    }

    @Test
    void theRoomIsTheWindowLessThePromptAndTheReplyReserve() {
        var service = service(publishing(32_768));
        String system = "s".repeat(3_000);
        String user = "u".repeat(6_000);

        long room = service.simpleCallRoomTokens(system, user).getAsLong();

        // 9,000 characters at 3.0 per token (the estimate never goes above 3.0, whatever the default ratio), two messages of overhead, and the 4,096-token reserve.
        long prompt = 3_000 + 2L * PromptSize.MESSAGE_OVERHEAD_TOKENS;
        assertEquals(32_768 - prompt - ToolResultBudget.replyReserve(32_768), room);
    }

    @Test
    void numericJsonIsCountedAtItsRealDensityNotAtAFlatRatio() {
        var service = service(publishing(32_768));
        String numeric = "{\"cpu\":0.1234567,\"mem\":1048576,\"t\":1761953400}\n".repeat(600);

        long tokens = service.estimateTokens(numeric);

        // About 28,800 characters; a flat 3.0 per token is 9,600, one token per digit is above 20,000.
        assertTrue(tokens >= 20_000, "dense: " + tokens);
        long room = service.simpleCallRoomTokens("system", numeric).getAsLong();
        assertTrue(room < 32_768 - 20_000 - ToolResultBudget.replyReserve(32_768) + 1, "room " + room);
    }

    @Test
    void aPromptThatAlreadyFillsTheWindowLeavesNoRoom() {
        var service = service(publishing(8_192));

        assertEquals(0, service.simpleCallRoomTokens("s", "u".repeat(60_000)).getAsLong());
        assertEquals(0, service.estimateTokens(null));
    }

    @Test
    void theRuntimeEstimatesTheSameTextTheSameWayAsItsToolLoop() {
        var service = service(null);
        String text = "NAME   READY\npod-1  1/1\n".repeat(50);

        // The tool loop counts a user message as characters plus density, plus the per-message overhead.
        var ratio = new CharsPerToken();
        long asAMessage = PromptSize.fullEstimateTokens(
                java.util.List.of(dev.langchain4j.data.message.UserMessage.from(text)), java.util.List.of(), ratio);
        assertEquals(asAMessage - PromptSize.MESSAGE_OVERHEAD_TOKENS,
                service.estimateTokens(text));
    }
}
