package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.modelserver.CandidatePolicy;
import ai.kubemoot.agent.modelserver.CapacityWait;
import ai.kubemoot.agent.modelserver.ChatModelPool;
import ai.kubemoot.agent.modelserver.ModelSpeed;
import ai.kubemoot.agent.modelserver.Pick;
import ai.kubemoot.agent.modelserver.ServerSelector;
import ai.kubemoot.agent.modelserver.ServerState;
import ai.kubemoot.agent.modelserver.Ticket;
import ai.kubemoot.agent.modelserver.TicketManager;
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

import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyDouble;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * The candidate list at work in the runtime: the mulling call and the triage call both
 * choose among the operator's candidates, favoring the model measured faster when the
 * rule asks for it ({@code preferFaster}), and triage never loads or evicts to do so.
 * The scheduler (ServerSelector) is mocked; ChatService's choice is real.
 */
class ChatServiceCandidateRankingTest {

    /** The bound model for both phases in ChatServiceToolLoopTest.stubProperties. */
    private static final String BOUND = "qwen2.5:32b";
    private static final String FAST = "qwen2.5:8b";
    private static final String EP = "http://ollama-a:11434";
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private ServerSelector selector;
    private ChatModelPool pool;
    private TicketManager tickets;
    private ServerState rig;
    private Pick fastPick;

    @BeforeEach
    void setUp() {
        selector = mock(ServerSelector.class);
        pool = mock(ChatModelPool.class);
        tickets = mock(TicketManager.class);
        var driver = new ai.kubemoot.agent.modelserver.OllamaDriver(null);
        when(selector.driver()).thenReturn(driver);
        when(selector.driverFor(any())).thenReturn(driver);
        rig = rig(Map.of(BOUND, speed(20), FAST, speed(60)));
        fastPick = new Pick(rig, new Ticket("t-1", rig.name(), 512, "test", "2026-10-19T00:00:00Z"));
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimWarm(eq(FAST), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(fastPick));
    }

    private static ModelSpeed speed(long generateTokensPerSecond) {
        return new ModelSpeed(1_000, 500, generateTokensPerSecond, 0, 0);
    }

    /** One server that has the fast model loaded and the bound model on disk. */
    private static ServerState rig(Map<String, ModelSpeed> speeds) {
        return new ServerState("ollama-a", EP, 1, 0, 0, List.of(FAST), true, "2026-10-19T00:00:00Z", 32_768,
                Map.of(FAST, 6_000L), Map.of(BOUND, 20_000L), 0L, Map.of(), speeds);
    }

    private static String candidates(long boundScore, long fastScore) {
        return "[{\"model\":\"" + BOUND + "\",\"score\":" + boundScore + "},"
                + "{\"model\":\"" + FAST + "\",\"score\":" + fastScore + "}]";
    }

    private ChatService service(String mulling, String triage, long preferFasterMulling, long preferFasterTriage) {
        var policy = new CandidatePolicy(MAPPER, Optional.ofNullable(mulling), Optional.ofNullable(triage),
                CandidatePolicy.DEFAULT_TOLERANCE, preferFasterMulling, preferFasterTriage);
        var mcp = mock(McpClientService.class);
        when(mcp.getToolSpecifications()).thenReturn(Map.of());
        return new ChatService(mock(ChatModel.class), mock(RagClient.class), mcp, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(3, "tooler", false, false),
                mock(AgentHeartbeatService.class), MAPPER, selector, pool, null, tickets, null,
                null, policy, null, null);
    }

    private void poolServes(String text) {
        var blocking = mock(ChatModel.class);
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage(text));
        when(response.tokenUsage()).thenReturn(new TokenUsage(10, 5));
        when(blocking.chat(any(ChatRequest.class))).thenReturn(response);
        var streaming = mock(StreamingChatModel.class);
        var streamed = ChatResponse.builder().aiMessage(new AiMessage(text)).tokenUsage(new TokenUsage(10, 5)).build();
        org.mockito.Mockito.doAnswer(inv -> {
            StreamingChatResponseHandler handler = inv.getArgument(1);
            handler.onPartialResponse(text);
            handler.onCompleteResponse(streamed);
            return null;
        }).when(streaming).chat(any(ChatRequest.class), any(StreamingChatResponseHandler.class));
        when(pool.forEndpoint(any(), anyString(), anyString(), anyDouble(), anyInt(), any(), any())).thenReturn(blocking);
        when(pool.streamingForEndpoint(any(), anyString(), anyString(), anyDouble(), anyInt(), any(), any()))
                .thenReturn(streaming);
    }

    // ---- triage ----------------------------------------------------------------

    @Test
    void triageWithPreferFasterRunsTheFasterWarmCandidate() {
        poolServes("{}");

        var result = service(null, candidates(70, 60), 0, 50).triageChatWithTokens("system", "conversation");

        verify(pool).streamingForEndpoint(any(), eq(EP), eq(FAST), anyDouble(), anyInt(), any(), any());
        verify(pool, never()).streamingForEndpoint(any(), anyString(), eq(BOUND), anyDouble(), anyInt(), any(), any());
        assertEquals(FAST, result.modelUse().model());
        verify(tickets).recordResidency(eq("ollama-a"), eq(FAST), anyLong());
        verify(tickets).release(fastPick.ticket());
    }

    @Test
    void triageWithoutPreferFasterKeepsTheBoundModelWhenItIsWarmElseUsesAWarmCandidate() {
        // The bound triage model is on disk only, the candidate within tolerance is warm: the
        // call joins the warm candidate instead of loading, as a mulling call does.
        poolServes("{}");

        service(null, candidates(70, 65), 0, 0).triageChatWithTokens("system", "conversation");

        verify(pool).streamingForEndpoint(any(), eq(EP), eq(FAST), anyDouble(), anyInt(), any(), any());
    }

    @Test
    void triageNeverUnloadsToMakeRoomForACandidate() {
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimRoom(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimLoading(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        poolServes("{}");

        service(null, candidates(70, 65), 0, 50).triageChatWithTokens("system", "conversation");

        verify(selector, never()).claimWithEvictions(anyString(), anyLong(), anyLong(), any(), any());
        verify(pool).streamingForEndpoint(any(), eq("http://localhost:11434"), eq(BOUND), anyDouble(), anyInt(), any(), any());
    }

    @Test
    void triageWithNoCandidateListRunsTheBoundModelAsBefore() {
        when(selector.pickAndClaimRoom(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(fastPick));
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(fastPick));
        poolServes("{}");

        var result = service(null, null, 0, 0).triageChatWithTokens("system", "conversation");

        assertEquals(BOUND, result.modelUse().model());
    }

    // ---- mulling ---------------------------------------------------------------

    @Test
    void aMullingCallWithPreferFasterRunsTheFasterWarmCandidate() {
        poolServes("answer");

        var result = service(candidates(70, 60), null, 50, 0)
                .directChat(new ChatService.ChatRequest("conv", "question"), true, CapacityWait.NONE);

        assertEquals(FAST, result.model());
    }

    @Test
    void aMullingCallWithoutPreferFasterKeepsTheBoundModelPreferred() {
        // Fast scores 20 points below the bound model: out of tolerance, so only the bound model qualifies.
        when(selector.pickAndClaimRoom(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(fastPick));
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(fastPick));
        poolServes("answer");

        service(candidates(70, 50), null, 0, 0)
                .directChat(new ChatService.ChatRequest("conv", "question"), true, CapacityWait.NONE);

        verify(selector, never()).pickAndClaimWarm(eq(FAST), anyLong(), anyLong(), anyLong());
    }

    // ---- measured speed --------------------------------------------------------

    @Test
    void measuredSpeedIsTheBestSpeedAReadyServerThatHasTheModelReports() {
        var slow = rig(Map.of(FAST, speed(30)));
        var observed = new ServerState("ollama-b", EP, 1, 0, 0, List.of(FAST), true, "t", 32_768,
                Map.of(FAST, 6_000L), Map.of(), 0L, Map.of(), Map.of(FAST, new ModelSpeed(1, 1, 40, 90, 12)));
        var notReady = new ServerState("ollama-c", EP, 1, 0, 0, List.of(FAST), false, "t", 32_768,
                Map.of(FAST, 6_000L), Map.of(), 0L, Map.of(), Map.of(FAST, speed(500)));
        var withoutTheModel = new ServerState("ollama-d", EP, 1, 0, 0, List.of(), true, "t", 32_768,
                Map.of(), Map.of(), 0L, Map.of(), Map.of(FAST, speed(900)));

        assertEquals(90, ChatService.measuredSpeed(List.of(slow, observed, notReady, withoutTheModel), FAST),
                "observed solo speed replaces the calibrated one; a server not ready or without the model is ignored");
        assertEquals(0, ChatService.measuredSpeed(List.of(slow), "never-measured"));
        assertEquals(0, ChatService.measuredSpeed(List.of(), FAST));
        assertTrue(ChatService.measuredSpeed(List.of(rig(Map.of())), FAST) == 0, "no speeds published is unmeasured");
    }
}
