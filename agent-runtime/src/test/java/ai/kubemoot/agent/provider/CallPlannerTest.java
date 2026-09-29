package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.Timeout;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyMap;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

/** Placement decisions, planning at selection, and releasing plans. */
class CallPlannerTest {

    private static final String BIG = "ollama-5090";
    private static final String SMALL = "ollama-4090";

    private ProviderSelector selector;
    private TicketManager tickets;
    private FakeEngineDriver driver;

    @BeforeEach
    void setUp() {
        selector = mock(ProviderSelector.class);
        tickets = mock(TicketManager.class);
        driver = new FakeEngineDriver(OllamaDriver.PROFILE);
        when(selector.driver()).thenReturn(driver);
        when(selector.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaimLoading(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selector.queueOption(anyList(), anyList(), anyLong())).thenReturn(Optional.empty());
        when(selector.planEviction(anyString(), anyLong(), anyList(), anyMap(), anyLong())).thenReturn(Optional.empty());
    }

    private static ProviderState gpu(String name, long vram, Map<String, Long> loaded) {
        return new ProviderState(name, "http://" + name, 1, 0, 0, List.copyOf(loaded.keySet()), true,
                "2026-09-27T00:00:00Z", vram, loaded);
    }

    private static Pick pickOn(ProviderState p, String id) {
        return new Pick(p, new Ticket(id, p.name(), 512, "test", "2026-09-27T00:00:00Z"));
    }

    private static CallPlanner.PlacementRequest request(String preferred, List<String> alternatives,
                                                        List<ProviderState> states) {
        return new CallPlanner.PlacementRequest(preferred, alternatives, states, m -> 12_000L, m -> 256L);
    }

    private DemandBoard demandOn(InMemoryKv bucket, String agent) {
        var props = mock(AgentProperties.class);
        when(props.agentName()).thenReturn(agent);
        when(props.namespace()).thenReturn(Optional.of("ns"));
        when(props.crew()).thenReturn(Optional.of("crew"));
        return new DemandBoard(bucket.provider(DemandBoard.BUCKET), new ObjectMapper(), props);
    }

    @Test
    void busyWarmCopyOnTheSmallGpu_queuesThere_andNothingIsUnloaded() {
        // The 5090 holds a warm qwen3:32b; the 4090's qwen3:14b is busy. The weighted
        // pick queues on the warm 14b copy, so no eviction is even considered.
        var big = gpu(BIG, 32_768, Map.of("qwen3:32b", 27_000L));
        var small = gpu(SMALL, 24_564, Map.of("qwen3:14b", 12_224L));
        when(selector.pickAndClaim(eq("qwen3:14b"), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(small, "q")));
        var planner = new CallPlanner(selector, tickets, null);

        var placement = planner.place(request("qwen3:14b", List.of(), List.of(big, small))).orElseThrow();

        assertEquals(SMALL, placement.pick().provider().name());
        assertTrue(placement.evicted().isEmpty());
        verify(selector, never()).planEviction(anyString(), anyLong(), anyList(), anyMap(), anyLong());
        assertTrue(driver.released.isEmpty());
    }

    @Test
    void noRoomAndAShortWaitElsewhere_queues_insteadOfUnloading() {
        var big = gpu(BIG, 32_768, Map.of("qwen3:32b", 27_000L));
        var victim = new PlacementCostModel.Victim("qwen3:32b", 27_000, new ModelDemand(0, 0, 1.0, 0.0, 1L));
        when(selector.queueOption(anyList(), anyList(), anyLong()))
                .thenReturn(Optional.of(new PlacementCostModel.QueueOption("qwen3:14b", SMALL, 15.0)));
        when(selector.planEviction(anyString(), anyLong(), anyList(), anyMap(), anyLong())).thenReturn(Optional.of(
                new PlacementCostModel.EvictionPlan(BIG, List.of(victim),
                        PlacementCostModel.evictionCostSeconds(OllamaDriver.PROFILE, 12_224, List.of(victim)))));
        var planner = new CallPlanner(selector, tickets, null);

        assertTrue(planner.place(request("qwen3:14b", List.of(), List.of(big))).isEmpty(), "the caller waits for the slot");
        verify(selector, never()).claimWithEvictions(anyString(), anyLong(), anyLong(), any(), anyList());
        assertTrue(driver.released.isEmpty());
    }

    @Test
    void evictionDecision_claimsFirst_thenReleasesTheVictims_andRecordsThem() {
        var big = gpu(BIG, 32_768, Map.of("qwen3:8b", 6_000L));
        var plan = new PlacementCostModel.EvictionPlan(BIG,
                List.of(new PlacementCostModel.Victim("qwen3:8b", 6_000, ModelDemand.NONE)), 13.0);
        when(selector.planEviction(anyString(), anyLong(), anyList(), anyMap(), anyLong())).thenReturn(Optional.of(plan));
        when(selector.claimWithEvictions(eq("qwen3:32b"), anyLong(), anyLong(), eq(plan), anyList()))
                .thenReturn(Optional.of(pickOn(big, "e")));
        var planner = new CallPlanner(selector, tickets, null);

        var placement = planner.place(request("qwen3:32b", List.of(), List.of(big))).orElseThrow();

        assertEquals(List.of("qwen3:8b"), placement.evicted());
        assertEquals(List.of("qwen3:8b"), driver.released);
        verify(tickets).clearResidency(BIG, "qwen3:8b");
        var order = inOrder(selector);
        order.verify(selector).claimWithEvictions(anyString(), anyLong(), anyLong(), any(), anyList());
        order.verify(selector).invalidateCache();
    }

    @Test
    void lostEvictionClaim_releasesNothing() {
        var plan = new PlacementCostModel.EvictionPlan(BIG,
                List.of(new PlacementCostModel.Victim("qwen3:8b", 6_000, ModelDemand.NONE)), 13.0);
        when(selector.planEviction(anyString(), anyLong(), anyList(), anyMap(), anyLong())).thenReturn(Optional.of(plan));
        var planner = new CallPlanner(selector, tickets, null);

        assertTrue(planner.place(request("qwen3:32b", List.of(), List.of(gpu(BIG, 32_768, Map.of())))).isEmpty());
        assertTrue(driver.released.isEmpty(), "another planner won the victim's memory");
    }

    @Test
    void agentsWithTheSameCandidate_convergeOnTheCopyBeingLoaded() {
        var big = gpu(BIG, 32_768, Map.of());
        when(selector.pickAndClaimLoading(eq("qwen3:32b"), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(big, "c")));
        var planner = new CallPlanner(selector, tickets, null);

        var placement = planner.place(request("qwen3:14b", List.of("qwen3:32b"), List.of(big))).orElseThrow();

        assertEquals("qwen3:32b", placement.model());
        verify(selector, never()).pickAndClaim(eq("qwen3:14b"), anyLong(), anyLong(), anyLong());
    }

    @Test
    void planAtSelection_claimsCapacity_startsTheLoad_andTheFirstCallUsesIt() {
        var big = gpu(BIG, 32_768, Map.of());
        var bucket = new InMemoryKv();
        var planner = new CallPlanner(selector, tickets, demandOn(bucket, "analyst"));
        when(selector.pickAndClaim(eq("qwen3:32b"), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(big, "p")));

        planner.commit("t1", List.of("qwen3:32b"), () -> planner.place(request("qwen3:32b", List.of(), List.of(big))));

        assertTrue(planner.holds("t1"));
        assertEquals(List.of("qwen3:32b"), driver.warmed, "the load overlaps the agent's triage");
        assertFalse(bucket.data.isEmpty(), "the intent is published");
        var held = planner.takeHeld("t1").orElseThrow();
        assertEquals("p", held.pick().ticket().ticketId());
        assertTrue(planner.takeHeld("t1").isEmpty(), "handed over once");
        verify(tickets, never()).release(any());
    }

    @Test
    void standAsideOrThreadEnd_releasesThePlansClaim_andClearsIntents() {
        var big = gpu(BIG, 32_768, Map.of("qwen3:32b", 27_000L));
        var bucket = new InMemoryKv();
        var demand = demandOn(bucket, "analyst");
        var planner = new CallPlanner(selector, tickets, demand);
        var pick = pickOn(big, "held");
        when(selector.pickAndClaimWarm(eq("qwen3:32b"), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pick));
        planner.commit("t1", List.of("qwen3:32b"), () -> planner.place(request("qwen3:32b", List.of(), List.of(big))));
        assertTrue(driver.warmed.isEmpty(), "a warm plan needs no load");

        planner.release("t1");

        verify(tickets).release(pick.ticket());
        assertFalse(planner.holds("t1"));
        assertEquals(0, demand.snapshot().getOrDefault("qwen3:32b", ModelDemand.NONE).intents());
    }

    @Test
    void forecastAloneNeverLoadsAnything() {
        var bucket = new InMemoryKv();
        var demand = demandOn(bucket, "analyst");
        var cold = gpu(BIG, 32_768, Map.of());
        when(selector.pickAndClaim(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(cold, "x")));
        var planner = new CallPlanner(selector, tickets, demand);

        for (int i = 0; i < 5; i++) {
            demand.recordSelection(List.of("qwen3:32b"));
            demand.recordUse("qwen3:32b");
        }
        planner.place(request("qwen3:8b", List.of(), List.of(cold)));
        planner.callStarted(null, "qwen3:8b");

        assertTrue(demand.snapshot().get("qwen3:32b").predicted() > 1.0);
        assertTrue(driver.warmed.isEmpty(), "only a selected agent's plan starts a load");
    }

    @Test
    void planWithoutAClaim_holdsNothing() {
        var planner = new CallPlanner(selector, tickets, null);
        planner.commit("t1", List.of("m"), Optional::empty);
        assertFalse(planner.holds("t1"));
        planner.release("t1");
        verify(tickets, never()).release(any());
    }

    @Test
    @Timeout(value = 10, unit = TimeUnit.SECONDS)
    void waitingAgentResumes_whenAnEvictionFreesRoom() throws Exception {
        var signal = new FakeCapacitySignal();
        var freed = new AtomicBoolean();
        driver.whenReleased(() -> { freed.set(true); signal.nudge(); });
        var big = gpu(BIG, 32_768, Map.of("qwen3:8b", 6_000L));

        // Agent B waits for room for qwen3:14b.
        var selectorB = mock(ProviderSelector.class);
        when(selectorB.driver()).thenReturn(driver);
        when(selectorB.pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selectorB.pickAndClaimLoading(anyString(), anyLong(), anyLong(), anyLong())).thenReturn(Optional.empty());
        when(selectorB.pickAndClaim(eq("qwen3:14b"), anyLong(), anyLong(), anyLong()))
                .thenAnswer(inv -> freed.get() ? Optional.of(pickOn(big, "b")) : Optional.empty());
        when(selectorB.queueOption(anyList(), anyList(), anyLong())).thenReturn(Optional.empty());
        when(selectorB.planEviction(anyString(), anyLong(), anyList(), anyMap(), anyLong())).thenReturn(Optional.empty());
        var plannerB = new CallPlanner(selectorB, tickets, null);
        var waiter = new GpuCapacityWaiter(signal, 60, 90);
        var wait = new RecordingCapacityWait();
        var b = CompletableFuture.supplyAsync(() ->
                waiter.await("qwen3:14b", () -> plannerB.place(request("qwen3:14b", List.of(), List.of(big))), wait));
        while (wait.waiting.isEmpty()) {
            Thread.onSpinWait();
        }

        // Agent A unloads the idle qwen3:8b for its own load.
        var plan = new PlacementCostModel.EvictionPlan(BIG,
                List.of(new PlacementCostModel.Victim("qwen3:8b", 6_000, ModelDemand.NONE)), 13.0);
        when(selector.planEviction(anyString(), anyLong(), anyList(), anyMap(), anyLong())).thenReturn(Optional.of(plan));
        when(selector.claimWithEvictions(anyString(), anyLong(), anyLong(), any(), anyList()))
                .thenReturn(Optional.of(pickOn(big, "a")));
        new CallPlanner(selector, tickets, null).place(request("qwen3:4b", List.of(), List.of(big)));

        assertEquals("b", b.get(5, TimeUnit.SECONDS).pick().ticket().ticketId());
        assertEquals(List.of("qwen3:14b"), wait.capacity);
    }

    @Test
    void planThatFinishesAfterTheFirstCallStarted_isDroppedAtOnce() {
        var big = gpu(BIG, 32_768, Map.of("qwen3:32b", 27_000L));
        var pick = pickOn(big, "late");
        when(selector.pickAndClaimWarm(eq("qwen3:32b"), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pick));
        var planner = new CallPlanner(selector, tickets, null);

        planner.commit("t1", List.of("qwen3:32b"), () -> {
            planner.callStarted("t1", "qwen3:32b"); // the agent's own call got there first
            return planner.place(request("qwen3:32b", List.of(), List.of(big)));
        });

        assertFalse(planner.holds("t1"));
        verify(tickets).release(pick.ticket());
    }

    @Test
    void standAsideBeforeThePlanStarted_meansNoPlan_butAnEarlierRoundDoesNot() {
        var big = gpu(BIG, 32_768, Map.of("qwen3:32b", 27_000L));
        when(selector.pickAndClaimWarm(eq("qwen3:32b"), anyLong(), anyLong(), anyLong())).thenReturn(Optional.of(pickOn(big, "p")));
        var planner = new CallPlanner(selector, tickets, null);
        long selectedAt = System.currentTimeMillis();

        planner.release("t1"); // the agent stood aside before its plan task ran
        planner.commit("t1", selectedAt, List.of("qwen3:32b"),
                () -> planner.place(request("qwen3:32b", List.of(), List.of(big))));
        assertFalse(planner.holds("t1"));
        verify(selector, never()).pickAndClaimWarm(anyString(), anyLong(), anyLong(), anyLong());

        planner.commit("t1", System.currentTimeMillis() + 1, List.of("qwen3:32b"),
                () -> planner.place(request("qwen3:32b", List.of(), List.of(big))));
        assertTrue(planner.holds("t1"), "a later selection in the same thread plans again");
        planner.release("t1");
    }
}
