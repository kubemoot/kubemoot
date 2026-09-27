package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.provider.CandidatePolicy;
import ai.kubemoot.agent.provider.CapacityWait;
import ai.kubemoot.agent.provider.ChatModelPool;
import ai.kubemoot.agent.provider.FakeCapacitySignal;
import ai.kubemoot.agent.provider.GpuCapacityWaiter;
import ai.kubemoot.agent.provider.NoFitException;
import ai.kubemoot.agent.provider.Pick;
import ai.kubemoot.agent.provider.ProviderSelector;
import ai.kubemoot.agent.provider.ProviderState;
import ai.kubemoot.agent.provider.RecordingCapacityWait;
import ai.kubemoot.agent.provider.Ticket;
import ai.kubemoot.agent.provider.TicketManager;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.Timeout;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyDouble;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

/**
 * The mulling call's GPU choice: warm candidate models, waiting for capacity
 * when the cluster is busy, and the model-too-large refusal. The scheduler
 * (ProviderSelector) is mocked; ChatService's placement logic is real.
 */
class ChatServiceCapacityTest {

    /** The bound (preferred) model in ChatServiceToolLoopTest.stubProperties. */
    private static final String PREFERRED = "qwen2.5:32b";
    private static final String WARM_CANDIDATE = "qwen2.5:14b";
    private static final String EP = "http://ollama-a:11434";
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private ChatModel staticModel;
    private McpClientService mcpClient;
    private ProviderSelector selector;
    private ChatModelPool pool;
    private TicketManager tickets;

    @BeforeEach
    void setUp() {
        staticModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        when(mcpClient.getToolSpecifications()).thenReturn(Map.of());
        selector = mock(ProviderSelector.class);
        pool = mock(ChatModelPool.class);
        tickets = mock(TicketManager.class);
    }

    /** A provider with the given VRAM, the candidate model resident, the preferred model on disk. */
    private static ProviderState provider(long totalVramMiB) {
        return new ProviderState("ollama-a", EP, 1, 0, 0, List.of(WARM_CANDIDATE), true,
                "2026-09-27T00:00:00Z", totalVramMiB, Map.of(WARM_CANDIDATE, 10_000L),
                Map.of(PREFERRED, 20_000L));
    }

    private static Pick pickOn(ProviderState p) {
        return new Pick(p, new Ticket("t-1", p.name(), 512, "test", "2026-09-27T00:00:00Z"));
    }

    private static ChatModel answering(String text) {
        var model = mock(ChatModel.class);
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage(text));
        when(response.tokenUsage()).thenReturn(new TokenUsage(10, 5));
        when(model.chat(any(ChatRequest.class))).thenReturn(response);
        return model;
    }

    private void poolServes(String model, ChatModel chat) {
        when(pool.forEndpoint(eq(EP), eq(model), anyDouble(), anyInt(), any(), any())).thenReturn(chat);
    }

    private ChatService service(GpuCapacityWaiter waiter, String candidatesJson) {
        var policy = new CandidatePolicy(MAPPER, Optional.ofNullable(candidatesJson), CandidatePolicy.DEFAULT_TOLERANCE);
        return new ChatService(staticModel, mock(RagClient.class), mcpClient, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(3, "tooler", false, false),
                mock(AgentHeartbeatService.class), MAPPER, selector, pool, null, tickets, null,
                waiter, policy);
    }

    private static ChatService.ChatRequest request() {
        return new ChatService.ChatRequest("conv-gpu", "What is using the GPU?");
    }

    private static String candidates(long preferredScore, long candidateScore) {
        return "[{\"model\":\"" + PREFERRED + "\",\"score\":" + preferredScore + "},"
                + "{\"model\":\"" + WARM_CANDIDATE + "\",\"score\":" + candidateScore + "}]";
    }

    @Test
    void warmCandidateWithinTolerance_beatsColdLoadingThePreferredModel() {
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        var warmChat = answering("warm answer");
        poolServes(WARM_CANDIDATE, warmChat);

        var result = service(null, candidates(70, 60)).directChat(request(), true, CapacityWait.NONE);

        assertEquals("warm answer", result.response());
        assertEquals(WARM_CANDIDATE, result.model(), "the result reports the model the call ran on");
        verify(pool).forEndpoint(eq(EP), eq(WARM_CANDIDATE), anyDouble(), anyInt(), any(), any());
        verify(selector, never()).pickAndClaim(anyString(), anyLong(), anyLong());
        verify(tickets).recordResidency("ollama-a", WARM_CANDIDATE, 10_000L);
        verifyNoInteractions(staticModel);
    }

    @Test
    void warmCandidateOutOfTolerance_keepsThePreferredModel() {
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("quality answer"));

        var result = service(null, candidates(70, 30)).directChat(request(), true, CapacityWait.NONE);

        assertEquals("quality answer", result.response());
        assertEquals(PREFERRED, result.model());
        verify(selector, never()).pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong());
        verify(pool).forEndpoint(eq(EP), eq(PREFERRED), anyDouble(), anyInt(), any(), any());
    }

    @Test
    void preferredModelWarmWithRoom_isUsedEvenWhenACandidateIsAlsoWarm() {
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("preferred answer"));

        var result = service(null, candidates(70, 70)).directChat(request(), true, CapacityWait.NONE);

        assertEquals(PREFERRED, result.model());
        verify(selector, never()).pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong());
    }

    @Test
    void modelNoGpuCanHold_standsAsideAsModelTooLarge_withoutWaiting() {
        when(selector.readState()).thenReturn(List.of(provider(16_384)));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong())).thenReturn(Optional.empty());
        var signal = new FakeCapacitySignal();
        var wait = new RecordingCapacityWait();

        var nfe = assertThrows(NoFitException.class, () -> service(new GpuCapacityWaiter(signal, 60, 90), null)
                .directChat(request(), true, wait));

        assertEquals(NoFitException.REASON_MODEL_TOO_LARGE, nfe.reason());
        assertEquals(PREFERRED, nfe.model());
        assertTrue(wait.waiting.isEmpty(), "a model that never fits does not wait");
        assertEquals(0, signal.awaits.get());
    }

    @Test
    @Timeout(value = 10, unit = TimeUnit.SECONDS)
    void saturatedCluster_waitsThenRunsWhenCapacityArrives() {
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong()))
                .thenReturn(Optional.empty(), Optional.empty(), Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("after the wait"));
        var signal = new FakeCapacitySignal();
        // Another agent's ticket release arrives as a KV change once this one waits.
        var wait = new RecordingCapacityWait().whenWaiting(signal::nudge);

        var result = service(new GpuCapacityWaiter(signal, 60, 90), null).directChat(request(), true, wait);

        assertEquals("after the wait", result.response());
        assertEquals(List.of(PREFERRED), wait.waiting);
        assertEquals(List.of(PREFERRED), wait.capacity);
        verify(selector, atLeastOnce()).invalidateCache();
    }

    @Test
    void saturatedCluster_withoutWaiter_standsAsideGpuBusyAtOnce() {
        when(selector.readState()).thenReturn(List.of(provider(32_768)));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong())).thenReturn(Optional.empty());

        var nfe = assertThrows(NoFitException.class, () -> service(null, null)
                .directChat(request(), true, new RecordingCapacityWait()));

        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
        assertEquals(PREFERRED, nfe.model());
    }

    @Test
    void callThatCannotWait_standsAsideGpuBusy_evenWithAWaiter() {
        when(selector.readState()).thenReturn(List.of(provider(32_768)));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong())).thenReturn(Optional.empty());

        var nfe = assertThrows(NoFitException.class, () -> service(new GpuCapacityWaiter(new FakeCapacitySignal(), 60, 90), null)
                .directChat(request(), true));

        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
    }
}
