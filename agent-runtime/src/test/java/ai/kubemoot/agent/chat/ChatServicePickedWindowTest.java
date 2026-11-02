package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.modelserver.CandidatePolicy;
import ai.kubemoot.agent.modelserver.ChatModelPool;
import ai.kubemoot.agent.modelserver.EngineDriver;
import ai.kubemoot.agent.modelserver.Pick;
import ai.kubemoot.agent.modelserver.ServerSelector;
import ai.kubemoot.agent.modelserver.ServerState;
import ai.kubemoot.agent.modelserver.Ticket;
import ai.kubemoot.agent.modelserver.TicketManager;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.StreamingChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.chat.response.StreamingChatResponseHandler;
import dev.langchain4j.model.output.TokenUsage;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.OptionalLong;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyDouble;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/**
 * A simple call built after the pick: the prompt-building function is handed the window of the
 * model and server the scheduler picked for that call, not the smallest window any server publishes,
 * and what it returns is what is sent.
 */
class ChatServicePickedWindowTest {

    private static final String MODEL = "qwen2.5:32b";
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private ServerSelector selector;
    private ChatModelPool pool;
    private TicketManager tickets;

    @BeforeEach
    void setUp() {
        selector = mock(ServerSelector.class);
        pool = mock(ChatModelPool.class);
        tickets = mock(TicketManager.class);
        var driver = mock(EngineDriver.class);
        when(driver.ensureReady(any(), anyString())).thenReturn(true);
        when(selector.driver()).thenReturn(driver);
        when(selector.driverFor(any())).thenReturn(driver);
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        var response = ChatResponse.builder().aiMessage(new AiMessage("ok")).tokenUsage(new TokenUsage(10, 5)).build();
        var streaming = mock(StreamingChatModel.class);
        doAnswer(inv -> {
            StreamingChatResponseHandler handler = inv.getArgument(1);
            handler.onCompleteResponse(response);
            return null;
        }).when(streaming).chat(any(ChatRequest.class), any(StreamingChatResponseHandler.class));
        when(pool.streamingForEndpoint(any(), anyString(), anyString(), anyDouble(), anyInt(), any(), any()))
                .thenReturn(streaming);
        when(pool.forEndpoint(any(), anyString(), anyString(), anyDouble(), anyInt(), any(), any()))
                .thenReturn(mock(ChatModel.class));
    }

    private static ServerState server(String name, long window) {
        return new ServerState(name, "", 1, 0, 0, List.of(MODEL), true, "2026-11-02T00:00:00Z", 0L,
                Map.of(MODEL, 0L), Map.of(), window, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://" + name + ":8000"));
    }

    /** A CPU server that holds only an embedding model, with the 8,192 window the diagnosis found. */
    private static ServerState embedding() {
        return new ServerState("vllm-cpu", "", 1, 0, 0, List.of("nomic-embed-text"), true, "2026-11-02T00:00:00Z", 0L,
                Map.of("nomic-embed-text", 0L), Map.of(), 8_192L, Map.of(), Map.of(), "vllm",
                Map.of("nomic-embed-text", "http://vllm-cpu:8000"));
    }

    private ChatService service() {
        var policy = new CandidatePolicy(MAPPER, Optional.empty(), Optional.empty(), CandidatePolicy.DEFAULT_TOLERANCE, 0L, 0L);
        var mcp = mock(McpClientService.class);
        when(mcp.getToolSpecifications()).thenReturn(Map.of());
        return new ChatService(mock(ChatModel.class), mock(RagClient.class), mcp, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(3, "tooler", false, false),
                mock(AgentHeartbeatService.class), MAPPER, selector, pool, null, tickets, null,
                null, policy, null, null);
    }

    private void pickedServerIs(ServerState picked, ServerState... others) {
        var all = new ArrayList<ServerState>(List.of(others));
        all.add(picked);
        when(selector.readState()).thenReturn(all);
        var pick = new Pick(picked, new Ticket("t-1", picked.name(), 512, "test", "2026-11-02T00:00:00Z"));
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pick));
        when(selector.pickAndClaimRoom(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pick));
    }

    @Test
    void thePromptIsBuiltFromTheWindowOfTheServerThePickChose() {
        pickedServerIs(server("vllm-rig0", 32_768), embedding());
        var seen = new ArrayList<OptionalLong>();
        var rooms = new ArrayList<OptionalLong>();

        var result = service().simpleLlmCallFor("system", "question", window -> {
            seen.add(window.tokens());
            rooms.add(window.roomAfter("system", "question"));
            return new SimplePrompt("system", "question");
        });

        assertEquals("ok", result.text());
        assertEquals(List.of(OptionalLong.of(32_768)), seen, "the CPU embedding server's 8,192 is not the window");
        assertTrue(rooms.get(0).getAsLong() < 32_768 && rooms.get(0).getAsLong() > 20_000, "room " + rooms);
    }

    @Test
    void aCallPlacedOnAnotherServerGetsThatServersWindow() {
        var windows = new ArrayList<OptionalLong>();
        pickedServerIs(server("vllm-rig0", 32_768));
        service().simpleLlmCallFor("s", "u", w -> {
            windows.add(w.tokens());
            return new SimplePrompt("s", "u");
        });
        pickedServerIs(server("vllm-rig1", 8_192));
        service().simpleLlmCallFor("s", "u", w -> {
            windows.add(w.tokens());
            return new SimplePrompt("s", "u");
        });
        pickedServerIs(server("vllm-rig2", 131_072));
        service().simpleLlmCallFor("s", "u", w -> {
            windows.add(w.tokens());
            return new SimplePrompt("s", "u");
        });

        assertEquals(List.of(OptionalLong.of(32_768), OptionalLong.of(8_192), OptionalLong.of(131_072)), windows);
    }

    @Test
    void aPickedServerThatPublishesNoWindowIsUnknown() {
        pickedServerIs(server("vllm-rig0", 0));
        var seen = new ArrayList<OptionalLong>();
        var rooms = new ArrayList<OptionalLong>();

        service().simpleLlmCallFor("s", "u", w -> {
            seen.add(w.tokens());
            rooms.add(w.roomAfter("s", "u"));
            return new SimplePrompt("s", "u");
        });

        assertEquals(List.of(OptionalLong.empty()), seen);
        assertEquals(List.of(OptionalLong.empty()), rooms);
    }

    @Test
    void whatThePromptFunctionReturnsIsWhatIsSent() {
        pickedServerIs(server("vllm-rig0", 32_768));
        var sent = new ArrayList<String>();
        var streaming = mock(StreamingChatModel.class);
        doAnswer(inv -> {
            ChatRequest request = inv.getArgument(0);
            sent.add(request.messages().toString());
            StreamingChatResponseHandler handler = inv.getArgument(1);
            handler.onCompleteResponse(ChatResponse.builder().aiMessage(new AiMessage("done")).build());
            return null;
        }).when(streaming).chat(any(ChatRequest.class), any(StreamingChatResponseHandler.class));
        when(pool.streamingForEndpoint(any(), anyString(), anyString(), anyDouble(), anyInt(), any(), any()))
                .thenReturn(streaming);

        service().simpleLlmCallFor("pick system", "pick user", w -> new SimplePrompt("built system", "built user"));

        assertEquals(1, sent.size());
        assertTrue(sent.get(0).contains("built system") && sent.get(0).contains("built user"), sent.get(0));
        assertFalse(sent.get(0).contains("pick user"), sent.get(0));
    }

    @Test
    void thePlainCallSendsItsPromptAsItWas() {
        pickedServerIs(server("vllm-rig0", 32_768));

        assertEquals("ok", service().simpleLlmCallWithTokens("system", "question").text());
    }
}
