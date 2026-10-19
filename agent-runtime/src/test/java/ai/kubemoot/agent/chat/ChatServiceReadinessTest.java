package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.provider.CandidatePolicy;
import ai.kubemoot.agent.provider.CapacityWait;
import ai.kubemoot.agent.provider.ChatModelPool;
import ai.kubemoot.agent.provider.EngineDriver;
import ai.kubemoot.agent.provider.ModelNotReadyException;
import ai.kubemoot.agent.provider.Pick;
import ai.kubemoot.agent.provider.ProviderSelector;
import ai.kubemoot.agent.provider.ProviderState;
import ai.kubemoot.agent.provider.Ticket;
import ai.kubemoot.agent.provider.TicketManager;
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
import org.mockito.InOrder;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Pattern;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyDouble;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

/**
 * Every call path that sends to a model on an engine with a lifecycle driver (vLLM) first
 * makes the model ready through the driver, which asks the operator: tool loop, evaluation,
 * synthesis (the no-tool calls) and triage. A path that sent to a sleeping server would hang
 * until the call timed out.
 */
class ChatServiceReadinessTest {

    private static final String MODEL = "qwen2.5:32b";
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private ProviderSelector selector;
    private ChatModelPool pool;
    private TicketManager tickets;
    private EngineDriver driver;
    private ProviderState asleep;
    private Pick pick;

    @BeforeEach
    void setUp() {
        selector = mock(ProviderSelector.class);
        pool = mock(ChatModelPool.class);
        tickets = mock(TicketManager.class);
        driver = mock(EngineDriver.class);
        when(driver.ensureReady(any(), anyString())).thenReturn(true);
        when(selector.driver()).thenReturn(driver);
        when(selector.driverFor(any())).thenReturn(driver);
        // A vLLM server that is asleep: the provider hosts the model but does not list it as loaded.
        asleep = new ProviderState("vllm-rig1", "", 1, 0, 0, List.of(), true, "2026-10-19T00:00:00Z", 0L,
                Map.of(), Map.of(MODEL, 0L), 0L, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://vllm-rig1:8000"));
        pick = new Pick(asleep, new Ticket("t-1", asleep.name(), 512, "test", "2026-10-19T00:00:00Z"));
        when(selector.readState()).thenReturn(List.of(asleep));
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pick));
        when(selector.pickAndClaimRoom(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pick));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
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

    private static ChatModel answering(String text) {
        var model = mock(ChatModel.class);
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage(text));
        when(response.tokenUsage()).thenReturn(new TokenUsage(10, 5));
        when(model.chat(any(ChatRequest.class))).thenReturn(response);
        return model;
    }

    private static StreamingChatModel streamingAnswering(String text) {
        var model = mock(StreamingChatModel.class);
        var response = ChatResponse.builder().aiMessage(new AiMessage(text)).tokenUsage(new TokenUsage(10, 5)).build();
        doAnswer(inv -> {
            StreamingChatResponseHandler handler = inv.getArgument(1);
            handler.onPartialResponse(text);
            handler.onCompleteResponse(response);
            return null;
        }).when(model).chat(any(ChatRequest.class), any(StreamingChatResponseHandler.class));
        return model;
    }

    private void poolServes(String text) {
        ChatModel blocking = answering(text);
        StreamingChatModel streaming = streamingAnswering(text);
        when(pool.forEndpoint(any(), anyString(), anyString(), anyDouble(), anyInt(), any(), any()))
                .thenReturn(blocking);
        when(pool.streamingForEndpoint(any(), anyString(), anyString(), anyDouble(), anyInt(), any(), any()))
                .thenReturn(streaming);
    }

    private void assertAskedBeforeSending() {
        InOrder order = inOrder(driver, pool);
        order.verify(driver).ensureReady(asleep, MODEL);
        order.verify(pool, atLeastOnce()).streamingForEndpoint(any(), eq("http://vllm-rig1:8000"), eq(MODEL),
                anyDouble(), anyInt(), any(), any());
    }

    @Test
    void triageWakesASleepingModelBeforeSending() {
        poolServes("{}");
        service().triageChatWithTokens("system", "conversation");

        assertAskedBeforeSending();
    }

    @Test
    void aNoToolCallWakesASleepingModelBeforeSending() {
        poolServes("synthesis");
        service().simpleLlmCallWithTokens("system", "question");

        assertAskedBeforeSending();
    }

    @Test
    void theToolLoopWakesASleepingModelBeforeSending() {
        poolServes("answer");
        service().directChat(new ChatService.ChatRequest("conv", "question"), true, CapacityWait.NONE);

        InOrder order = inOrder(driver, pool);
        order.verify(driver).ensureReady(asleep, MODEL);
        order.verify(pool).forEndpoint(any(), eq("http://vllm-rig1:8000"), eq(MODEL), anyDouble(), anyInt(), any(),
                any());
    }

    @Test
    void anAwakeModelAsksNothing() {
        var awake = new ProviderState("vllm-rig1", "", 1, 0, 0, List.of(MODEL), true, "2026-10-19T00:00:00Z", 0L,
                Map.of(MODEL, 0L), Map.of(), 0L, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://vllm-rig1:8000"));
        when(selector.readState()).thenReturn(List.of(awake));
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong()))
                .thenReturn(Optional.of(new Pick(awake, pick.ticket())));
        when(selector.pickAndClaimRoom(anyString(), anyLong(), anyLong(), anyLong()))
                .thenReturn(Optional.of(new Pick(awake, pick.ticket())));
        poolServes("{}");

        service().triageChatWithTokens("system", "conversation");

        verify(driver, never()).ensureReady(any(), anyString());
    }

    @Test
    void aWakeThatFailsReleasesTheTicketAndNothingIsSent() {
        when(driver.ensureReady(any(), anyString())).thenThrow(new ModelNotReadyException("the server has no sleep mode"));
        poolServes("{}");

        var e = assertThrows(IllegalStateException.class, () -> service().triageChatWithTokens("system", "c"));

        assertTrue(e.getMessage().contains("the server has no sleep mode"), e.getMessage());
        verify(tickets).release(pick.ticket());
        verify(selector).recordFailure("vllm-rig1");
        verify(pool, never()).streamingForEndpoint(any(), anyString(), anyString(), anyDouble(), anyInt(), any(), any());
    }

    /**
     * Every place ChatService builds a model to send to must be after the readiness step
     * (ModelReadiness.ensure, in buildScheduledMullingPick and scheduledTriage). This
     * counts them so a new call path cannot be added without someone deciding where its
     * readiness step is: the degraded paths below talk to Ollama endpoints only.
     */
    @Test
    void everyPlaceAModelIsBuiltIsAccountedFor() throws Exception {
        String source = Files.readString(Path.of("src", "main", "java", "ai", "kubemoot", "agent", "chat",
                "ChatService.java"));
        long builds = Pattern.compile("chatModelPool\\.(forEndpoint|streamingForEndpoint)\\(").matcher(source).results()
                .count();
        long readinessSteps = Pattern.compile("ModelReadiness\\.ensure\\(").matcher(source).results().count();
        assertEquals(2, readinessSteps, "the scheduled mulling pick and the triage placement each wake a model");
        // scheduled mulling pick (forEndpoint), its no-tool send and triage (streamingForEndpoint),
        // and the degraded Ollama-only direct-probe pick (forEndpoint).
        assertEquals(4, builds, "a new place that builds a chat model needs a ModelReadiness.ensure before it, "
                + "or a reason it only reaches Ollama");
    }
}
