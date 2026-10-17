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
import static org.mockito.ArgumentMatchers.longThat;
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
    private ai.kubemoot.agent.provider.PerformanceHistory history;

    @BeforeEach
    void setUp() {
        staticModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        when(mcpClient.getToolSpecifications()).thenReturn(Map.of());
        selector = mock(ProviderSelector.class);
        pool = mock(ChatModelPool.class);
        tickets = mock(TicketManager.class);
        when(selector.driver()).thenReturn(new ai.kubemoot.agent.provider.OllamaDriver(null));
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
        return service(waiter, candidatesJson, null);
    }

    /** A service whose calls are placed by {@code planner}; null uses the algorithm over {@code selector}. */
    private ChatService service(GpuCapacityWaiter waiter, String candidatesJson,
                                ai.kubemoot.agent.provider.CallPlanner planner) {
        var policy = new CandidatePolicy(MAPPER, Optional.ofNullable(candidatesJson), CandidatePolicy.DEFAULT_TOLERANCE);
        return new ChatService(staticModel, mock(RagClient.class), mcpClient, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(3, "tooler", false, false),
                mock(AgentHeartbeatService.class), MAPPER, selector, pool, null, tickets, null,
                waiter, policy, planner, history);
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
        when(selector.pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        var warmChat = answering("warm answer");
        poolServes(WARM_CANDIDATE, warmChat);

        var result = service(null, candidates(70, 60)).directChat(request(), true, CapacityWait.NONE);

        assertEquals("warm answer", result.response());
        assertEquals(WARM_CANDIDATE, result.model(), "the result reports the model the call ran on");
        assertEquals(List.of(new ModelUse(WARM_CANDIDATE, "ollama-a")), result.modelsUsed());
        assertEquals("ollama-a", result.providerName());
        verify(pool).forEndpoint(eq(EP), eq(WARM_CANDIDATE), anyDouble(), anyInt(), any(), any());
        verify(selector, never()).pickAndClaim(anyString(), anyLong(), anyLong(), anyLong());
        verify(tickets).recordResidency("ollama-a", WARM_CANDIDATE, 10_000L);
        verifyNoInteractions(staticModel);
    }

    @Test
    void aScheduledCall_recordsEachRequestOnTheProviderItRanOn() {
        history = mock(ai.kubemoot.agent.provider.PerformanceHistory.class);
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        when(tickets.activeCountFor("ollama-a")).thenReturn(2);
        poolServes(WARM_CANDIDATE, answering("warm answer"));

        service(null, candidates(70, 60)).directChat(request(), true, CapacityWait.NONE);

        var seen = org.mockito.ArgumentCaptor.forClass(ai.kubemoot.agent.provider.CallObservation.class);
        verify(history).record(seen.capture());
        var o = seen.getValue();
        assertEquals("ollama-a", o.provider());
        assertEquals(WARM_CANDIDATE, o.model());
        assertEquals(ai.kubemoot.agent.provider.CallObservation.KIND_TOOL_TURN, o.kind());
        assertTrue(o.ok());
        assertFalse(o.coldStart(), "the candidate was resident when the call was placed");
        assertEquals(10, o.promptTokens());
        assertEquals(2, o.activeCalls());
    }

    @Test
    void aCallPlacedWhereTheModelIsNotResident_isRecordedCold() {
        history = mock(ai.kubemoot.agent.provider.PerformanceHistory.class);
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("cold answer"));

        service(null, null).directChat(request(), true, CapacityWait.NONE);

        var seen = org.mockito.ArgumentCaptor.forClass(ai.kubemoot.agent.provider.CallObservation.class);
        verify(history).record(seen.capture());
        assertEquals(PREFERRED, seen.getValue().model());
        assertTrue(seen.getValue().coldStart());
    }

    @Test
    void warmCandidateOutOfTolerance_keepsThePreferredModel() {
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("quality answer"));

        var result = service(null, candidates(70, 30)).directChat(request(), true, CapacityWait.NONE);

        assertEquals("quality answer", result.response());
        assertEquals(PREFERRED, result.model());
        verify(selector, never()).pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong(), anyLong());
        verify(pool).forEndpoint(eq(EP), eq(PREFERRED), anyDouble(), anyInt(), any(), any());
    }

    @Test
    void preferredModelWarmWithRoom_isUsedEvenWhenACandidateIsAlsoWarm() {
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("preferred answer"));

        var result = service(null, candidates(70, 70)).directChat(request(), true, CapacityWait.NONE);

        assertEquals(PREFERRED, result.model());
        verify(selector, never()).pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong(), anyLong());
    }

    @Test
    void modelNoGpuCanHold_standsAsideAsModelTooLarge_withoutWaiting() {
        when(selector.readState()).thenReturn(List.of(provider(16_384)));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
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
        history = mock(ai.kubemoot.agent.provider.PerformanceHistory.class);
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong()))
                .thenReturn(Optional.empty(), Optional.empty(), Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("after the wait"));
        var signal = new FakeCapacitySignal();
        // Another agent's ticket release arrives as a KV change once this one waits.
        var wait = new RecordingCapacityWait().whenWaiting(signal::nudge);

        var result = service(new GpuCapacityWaiter(signal, 60, 90), null).directChat(request(), true, wait);

        assertEquals("after the wait", result.response());
        verify(history).recordWait(eq("ollama-a"), eq(PREFERRED), longThat(ms -> ms >= 0));
        assertEquals(List.of(PREFERRED), wait.waiting);
        assertEquals(List.of(PREFERRED), wait.capacity);
        verify(selector, atLeastOnce()).invalidateCache();
    }

    @Test
    void saturatedCluster_withoutWaiter_standsAsideGpuBusyAtOnce() {
        when(selector.readState()).thenReturn(List.of(provider(32_768)));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());

        var nfe = assertThrows(NoFitException.class, () -> service(null, null)
                .directChat(request(), true, new RecordingCapacityWait()));

        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
        assertEquals(PREFERRED, nfe.model());
    }

    @Test
    void callThatCannotWait_standsAsideGpuBusy_evenWithAWaiter() {
        when(selector.readState()).thenReturn(List.of(provider(32_768)));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());

        var nfe = assertThrows(NoFitException.class, () -> service(new GpuCapacityWaiter(new FakeCapacitySignal(), 60, 90), null)
                .directChat(request(), true));

        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
    }

    @Test
    void planMadeAtSelection_isUsedByTheFirstCall() {
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimLoading(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("planned answer"));
        var service = service(null, null);

        service.commitToThread("t-plan", System.currentTimeMillis());
        var result = service.directChat(new ChatService.ChatRequest("conv", "q", null, "t-plan", "q"),
                true, CapacityWait.NONE);

        assertEquals("planned answer", result.response());
        verify(selector, times(1)).pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong());
        verify(tickets).release(any());
    }

    /** A pick that joined a busy copy without a counted slot, as the weighted cost's queue does. */
    private static Pick queuedPickOn(ProviderState p) {
        return new Pick(p, new Ticket("t-queued", p.name(), 512, "test", "2026-09-27T00:00:00Z"),
                ai.kubemoot.agent.provider.FitScore.yes(0L, "warm"), true);
    }

    private static ChatService.ChatRequest onThread(String threadId) {
        return new ChatService.ChatRequest("conv", "q", null, threadId, "q");
    }

    @Test
    void queuedPlanMadeAtSelection_isPlacedAgain_andLandsOnAnAlternativeWithAFreeSlot() {
        var rig = provider(32_768);
        var queued = queuedPickOn(rig);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong(), anyLong()))
                .thenReturn(Optional.empty(), Optional.of(pickOn(rig)));
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(queued));
        poolServes(WARM_CANDIDATE, answering("alternative answer"));
        var service = service(null, candidates(70, 60));

        service.commitToThread("t-queue", System.currentTimeMillis());
        var result = service.directChat(onThread("t-queue"), true, CapacityWait.NONE);

        assertEquals("alternative answer", result.response());
        assertEquals(WARM_CANDIDATE, result.model(), "the slot freed after selection is used");
        assertEquals(List.of(new ModelUse(WARM_CANDIDATE, "ollama-a")), result.modelsUsed(),
                "the result names the model and provider the re-planned call ran on, not the preferred model");
        verify(tickets).release(queued.ticket());
        verify(selector, times(1)).pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong());
    }

    @Test
    void planOwningASlot_isUsedUnchanged_evenWhenAnAlternativeHasRoomAtCallTime() {
        var rig = provider(32_768);
        var owned = pickOn(rig);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(owned));
        when(selector.pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong(), anyLong()))
                .thenReturn(Optional.of(pickOn(rig)));
        poolServes(PREFERRED, answering("planned answer"));
        var service = service(null, candidates(70, 60));

        service.commitToThread("t-slot", System.currentTimeMillis());
        var result = service.directChat(onThread("t-slot"), true, CapacityWait.NONE);

        assertEquals(PREFERRED, result.model());
        verify(selector, times(1)).pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong(), anyLong());
        verify(selector, never()).pickAndClaimWarm(eq(WARM_CANDIDATE), anyLong(), anyLong(), anyLong());
    }

    @Test
    void queuedPlan_withStillNoRoomAtCallTime_queuesAsBefore() {
        var rig = provider(32_768);
        var atSelection = queuedPickOn(rig);
        var atCall = new Pick(rig, new Ticket("t-queued-2", rig.name(), 512, "test", "2026-09-27T00:00:00Z"),
                ai.kubemoot.agent.provider.FitScore.yes(0L, "warm"), true);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong()))
                .thenReturn(Optional.of(atSelection), Optional.of(atCall));
        poolServes(PREFERRED, answering("queued answer"));
        var service = service(null, null);

        service.commitToThread("t-busy", System.currentTimeMillis());
        var result = service.directChat(onThread("t-busy"), true, CapacityWait.NONE);

        assertEquals("queued answer", result.response());
        assertEquals(PREFERRED, result.model());
        verify(tickets).release(atSelection.ticket());
        verify(selector, times(2)).pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong());
    }

    @Test
    void releasePlan_releasesAnUnusedPlan() {
        var rig = provider(32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaimWarm(eq(PREFERRED), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(rig)));
        var service = service(null, null);

        service.commitToThread("t-gone", System.currentTimeMillis());
        service.releasePlan("t-gone");

        verify(tickets, times(1)).release(any());
    }

    /** A provider like {@link #provider} whose models get {@code contextTokens} per request. */
    private static ProviderState providerWithContext(long totalVramMiB, long contextTokens) {
        return new ProviderState("ollama-a", EP, 1, 0, 0, List.of(WARM_CANDIDATE), true,
                "2026-09-27T00:00:00Z", totalVramMiB, Map.of(WARM_CANDIDATE, 10_000L),
                Map.of(PREFERRED, 20_000L), contextTokens, Map.of());
    }

    @Test
    void promptLargerThanEveryContext_standsAsidePromptTooLarge_withoutWaiting() {
        when(selector.readState()).thenReturn(List.of(providerWithContext(32_768, 1_024)));
        var signal = new FakeCapacitySignal();
        var wait = new RecordingCapacityWait();

        var nfe = assertThrows(NoFitException.class, () -> service(new GpuCapacityWaiter(signal, 60, 90), null)
                .directChat(new ChatService.ChatRequest("conv", "q".repeat(20_000)), true, wait));

        assertEquals(NoFitException.REASON_PROMPT_TOO_LARGE, nfe.reason());
        assertTrue(nfe.predictorReason().contains("1024 tokens"), nfe.predictorReason());
        assertTrue(wait.waiting.isEmpty(), "a prompt that fits no context does not wait");
        assertEquals(0, signal.awaits.get());
    }

    @Test
    void planMadeAtSelection_whoseContextCannotHoldTheRealPrompt_isReleased() {
        // The plan is made for an assumed prompt (~5K tokens) that fits the 8K
        // context; the real prompt (~12K tokens) does not, so the plan is dropped.
        var rig = providerWithContext(32_768, 8_192);
        when(selector.readState()).thenReturn(List.of(rig));
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong()))
                .thenReturn(Optional.of(pickOn(rig)), Optional.empty());
        var service = service(null, null);

        service.commitToThread("t-big", System.currentTimeMillis());
        var nfe = assertThrows(NoFitException.class, () -> service.directChat(
                new ChatService.ChatRequest("conv", "q".repeat(40_000), null, "t-big", "q"), true, CapacityWait.NONE));

        assertEquals(NoFitException.REASON_PROMPT_TOO_LARGE, nfe.reason());
        verify(tickets, times(1)).release(any());
        verify(pool, never()).forEndpoint(anyString(), anyString(), anyDouble(), anyInt(), any(), any());
    }

    @Test
    void coordinatorCall_promptLargerThanEveryContext_isRefused_notSentToTheStaticEndpoint() {
        when(selector.readState()).thenReturn(List.of(providerWithContext(32_768, 1_024)));

        var nfe = assertThrows(NoFitException.class,
                () -> service(null, null).simpleLlmCallWithTokens("system", "q".repeat(20_000)));

        assertEquals(NoFitException.REASON_PROMPT_TOO_LARGE, nfe.reason());
        verifyNoInteractions(staticModel);
    }

    @Test
    void coordinatorCall_everyGpuBusy_stillFallsBackToTheStaticEndpoint() {
        when(selector.readState()).thenReturn(List.of(providerWithContext(32_768, 32_768)));
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage("synthesis"));
        when(response.tokenUsage()).thenReturn(new TokenUsage(10, 5));
        when(staticModel.chat(any(ChatRequest.class))).thenReturn(response);

        var result = service(null, null).simpleLlmCallWithTokens("system", "short");

        assertEquals("synthesis", result.text());
        assertEquals(new ModelUse(PREFERRED, ""), result.modelUse(), "the static endpoint has no provider");
    }

    @Test
    void triage_promptLargerThanEveryContext_isRefused() {
        when(selector.readState()).thenReturn(List.of(providerWithContext(32_768, 1_024)));

        var nfe = assertThrows(NoFitException.class,
                () -> service(null, null).triageChat("system", "c".repeat(20_000)));

        assertEquals(NoFitException.REASON_PROMPT_TOO_LARGE, nfe.reason());
        verify(selector, never()).pickAndClaim(anyString(), anyLong(), anyLong(), anyLong());
    }

    @Test
    void triage_isPlacedByTheConfiguredPolicy_andNeverUnloadsModels() {
        when(selector.readState()).thenReturn(List.of(providerWithContext(32_768, 32_768)));
        var asked = new java.util.ArrayList<ai.kubemoot.agent.provider.CallPlanner.PlacementRequest>();
        ai.kubemoot.agent.provider.PlacementPolicy recording = new ai.kubemoot.agent.provider.PlacementPolicy() {
            @Override
            public ai.kubemoot.agent.provider.PlacementMode mode() {
                return ai.kubemoot.agent.provider.PlacementMode.LEARNED;
            }

            @Override
            public Optional<ai.kubemoot.agent.provider.CallPlanner.Placement> place(
                    ai.kubemoot.agent.provider.CallPlanner.PlacementRequest r) {
                asked.add(r);
                return Optional.empty();
            }
        };
        var service = service(null, null, new ai.kubemoot.agent.provider.CallPlanner(selector, tickets, null, recording));

        // With no placement the call goes to the static endpoint, unreachable in a unit test.
        assertThrows(RuntimeException.class, () -> service.triageChat("system", "c".repeat(7_000)));

        assertEquals(1, asked.size(), "triage must go through the placement policy");
        assertEquals(PREFERRED, asked.get(0).preferred());
        assertTrue(asked.get(0).alternatives().isEmpty(), "triage keeps its own model");
        assertFalse(asked.get(0).mayUnload(), "a triage call never unloads models to make room");
        verify(selector, never()).pickAndClaim(anyString(), anyLong(), anyLong(), anyLong());
    }

    @Test
    void triage_passesThePromptSizeToThePlacement() {
        when(selector.readState()).thenReturn(List.of(providerWithContext(32_768, 32_768)));
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());

        // The static triage endpoint is unreachable in a unit test; only the placement matters here.
        assertThrows(RuntimeException.class, () -> service(null, null).triageChat("system", "c".repeat(7_000)));

        verify(selector).pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), eq(2_002L));
    }

    @Test
    void triage_reportsTheTriageModelAndTheProviderThatServedIt() throws Exception {
        var server = com.sun.net.httpserver.HttpServer.create(
                new java.net.InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/api/chat", exchange -> {
            byte[] body = "{\"message\":{\"content\":\"CONTRIBUTE\"},\"prompt_eval_count\":7,\"eval_count\":1}"
                    .getBytes(java.nio.charset.StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(200, body.length);
            try (var out = exchange.getResponseBody()) {
                out.write(body);
            }
        });
        server.start();
        try {
            String endpoint = "http://127.0.0.1:" + server.getAddress().getPort();
            var rig = new ProviderState("ollama-b", endpoint, 1, 0, 0, List.of(PREFERRED), true,
                    "2026-09-27T00:00:00Z", 32_768, Map.of(PREFERRED, 20_000L), Map.of(PREFERRED, 20_000L),
                    32_768, Map.of());
            when(selector.readState()).thenReturn(List.of(rig));
            when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), anyLong()))
                    .thenReturn(Optional.of(pickOn(rig)));

            var result = service(null, null).triageChatWithTokens("system", "conversation");

            assertEquals("CONTRIBUTE", result.text());
            assertEquals(7, result.inputTokens());
            assertEquals(new ModelUse(PREFERRED, "ollama-b"), result.modelUse());
            assertEquals(PREFERRED, service(null, null).getTriageModelId(),
                    "with no triage model configured, triage runs on the agent's model");
        } finally {
            server.stop(0);
        }
    }

    @Test
    void planAtSelection_isSizedFromTheConversation() {
        var rig = providerWithContext(32_768, 32_768);
        when(selector.readState()).thenReturn(List.of(rig));
        var tokens = org.mockito.ArgumentCaptor.forClass(Long.class);
        when(selector.pickAndClaim(eq(PREFERRED), anyLong(), anyLong(), tokens.capture())).thenReturn(Optional.empty());
        var service = service(null, null);

        service.commitToThread("t-small", System.currentTimeMillis(), "short question");
        service.commitToThread("t-large", System.currentTimeMillis(), "c".repeat(70_000));

        service.commitToThread("t-unknown", System.currentTimeMillis(), null);
        service.commitToThread("t-empty", System.currentTimeMillis(), "");

        var sizes = tokens.getAllValues();
        assertEquals(4, sizes.size(), sizes.toString());
        assertTrue(sizes.get(0) < 1_000, "a short conversation plans a small prompt: " + sizes);
        assertEquals(4_572L, sizes.get(2), "an unknown conversation plans the 16,000-character default");
        assertTrue(sizes.get(3) <= sizes.get(0), "an empty conversation plans no more than a short one: " + sizes);
        assertTrue(sizes.get(1) >= 20_000, "a 70,000-character conversation plans ~20K tokens: " + sizes);
    }
}
