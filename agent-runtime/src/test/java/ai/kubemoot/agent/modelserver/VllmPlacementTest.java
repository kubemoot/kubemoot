package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.CallPlanner.Placement;
import ai.kubemoot.agent.modelserver.CallPlanner.PlacementRequest;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * With one model per accelerator on vLLM servers, a call for that model goes to the
 * vLLM server: it is ranked, picked over a CPU Ollama, and claimed without a memory
 * budget, though it publishes no VRAM total, no footprint, and no measured speed.
 */
class VllmPlacementTest {

    private static final String MODEL = "qwen3:14b";
    private static final long FOOTPRINT_MIB = 9_000L;
    private static final String VLLM = "vllm-rig1";
    private static final String CPU = "ollama-cpu";

    private TicketManager tickets;
    private List<ServerState> states;
    private ServerSelector selector;

    private static ServerState cpu(List<String> loaded) {
        Map<String, Long> resident = new java.util.HashMap<>();
        loaded.forEach(m -> resident.put(m, m.equals(MODEL) ? 9_000L : 5_000L));
        return new ServerState(CPU, "http://cpu:11434", 1, 0, 0, loaded, true, "2026-10-19T00:00:00Z", 24_000L,
                resident, Map.of());
    }

    private static ServerState vllm(boolean awake, Integer waiting, Double kv, Map<String, ModelSpeed> speeds) {
        return new ServerState(VLLM, "", 1, 0, 0, awake ? List.of(MODEL) : List.of(), true, "2026-10-19T00:00:00Z",
                0L, awake ? Map.of(MODEL, 0L) : Map.of(), awake ? Map.of() : Map.of(MODEL, 0L), 0L, Map.of(), speeds,
                "vllm", Map.of(MODEL, "http://vllm-14b:8000"), 0, waiting, kv);
    }

    private ServerSelector selectorOver() {
        return new ServerSelector(null, new ObjectMapper(), tickets, null, new OllamaDriver(null)) {
            @Override
            public List<ServerState> readState() {
                return states;
            }
        };
    }

    private static PlacementRequest request(List<ServerState> all) {
        return new PlacementRequest(MODEL, List.of(), all, m -> FOOTPRINT_MIB, m -> 0L, 500L);
    }

    @BeforeEach
    void setUp() {
        tickets = mock(TicketManager.class);
        when(tickets.claimSlot(anyString(), anyLong(), anyLong(), anyLong(), anyString(), any()))
                .thenAnswer(inv -> Optional.of(new Ticket("t", inv.getArgument(0), inv.getArgument(1), "agent",
                        "2026-10-19T00:00:00Z")));
        when(tickets.claim(anyString(), anyLong(), anyLong(), anyLong(), anyString()))
                .thenAnswer(inv -> Optional.of(new Ticket("t", inv.getArgument(0), inv.getArgument(1), "agent",
                        "2026-10-19T00:00:00Z")));
        states = List.of(cpu(List.of("qwen3:8b")), vllm(true, 0, 0.1, Map.of()));
        selector = selectorOver();
    }

    @Test
    void theVllmServerIsAnOption_startingNow_withNoVramTotalAndNoFootprint() {
        List<ServerSelector.Option> options = selector.options(MODEL, FOOTPRINT_MIB, 0L, 500L);
        assertEquals(1, options.size(), "only the server that hosts the model");
        ServerSelector.Option o = options.get(0);
        assertEquals(VLLM, o.server().name());
        assertTrue(o.warm());
        assertTrue(o.startsNow());
        assertEquals(0.0, o.waitSeconds());
    }

    @Test
    void aWaitingRequestMakesItBusyWithAWait_notExcluded() {
        states = List.of(vllm(true, 4, 0.5, Map.of()));
        ServerSelector.Option o = selectorOver().options(MODEL, FOOTPRINT_MIB, 0L, 0L).get(0);
        assertFalse(o.startsNow());
        assertTrue(o.waitSeconds() > 0.0);
    }

    @Test
    void aSleepingHostedModelIsAnOptionThatMustWake() {
        states = List.of(vllm(false, 0, 0.0, Map.of()));
        ServerSelector.Option o = selectorOver().options(MODEL, FOOTPRINT_MIB, 0L, 0L).get(0);
        assertFalse(o.warm());
        assertTrue(o.startsNow());
    }

    @Test
    void aVllmServerIsRankedAndPickedOverAWarmCpuHostingTheSameModel() {
        states = List.of(cpu(List.of(MODEL)), vllm(true, 0, 0.1, Map.of()));
        var ranked = ServerSelector.rankCandidates(states, MODEL, FOOTPRINT_MIB,
                ServerSelector.LoadView.of(p -> 0, p -> 0L, p -> 0.0, n -> false), new StaticFitPredictor());
        assertEquals(2, ranked.size());
        assertEquals(VLLM, ranked.get(0).server().name());

        Optional<Placement> placed = new AlgorithmPlacement(selectorOver(), tickets, null).place(request(states));
        assertEquals(VLLM, placed.orElseThrow().pick().server().name());
    }

    @Test
    void theAlgorithmClaimsOnVllmWithAnUnboundedBudget_notTheMissingVramHeadroom() {
        Optional<Placement> placed = new AlgorithmPlacement(selector, tickets, null).place(request(states));
        assertEquals(VLLM, placed.orElseThrow().pick().server().name());
        org.mockito.Mockito.verify(tickets).claimSlot(eq(VLLM), anyLong(), anyLong(),
                eq(ServedModelFit.UNBOUNDED_HEADROOM_MIB), eq(MODEL), any());
    }

    @Test
    void aBusyVllmIsNotClaimedForAFreeSlot_theCallQueuesOnItsWarmCopy() {
        states = List.of(cpu(List.of("qwen3:8b")), vllm(true, 6, 0.4, Map.of()));
        Optional<Placement> placed = new AlgorithmPlacement(selectorOver(), tickets, null)
                .place(new PlacementRequest(MODEL, List.of(), states, m -> FOOTPRINT_MIB, m -> 0L, 500L, false));
        assertTrue(placed.orElseThrow().queued(), "joined the warm copy without a counted slot");
        org.mockito.Mockito.verify(tickets, org.mockito.Mockito.never())
                .claimSlot(anyString(), anyLong(), anyLong(), anyLong(), anyString(), any());
    }

    @Test
    void anUnmeasuredVllmGivesLearnedPlacementNoOpinion_andTheFallbackPicksIt() {
        var learned = new LearnedPlacement(new AlgorithmPlacement(selector, tickets, null), selector);
        assertTrue(learned.propose(request(states)).isEmpty(), "never guess an unmeasured server fast or slow");
        Placement placed = learned.place(request(states)).orElseThrow();
        assertEquals(VLLM, placed.pick().server().name());
    }

    @Test
    void aMeasuredVllmIsPlacedByTheLearnedPolicyItself() {
        ModelSpeed fast = new ModelSpeed(30_000, 20_000, 120, 0, 0);
        states = List.of(vllm(true, 0, 0.1, Map.of(MODEL, fast)));
        selector = selectorOver();
        var learned = new LearnedPlacement(new Fallback(), selector);
        assertEquals(VLLM, learned.propose(request(states)).orElseThrow().server());
        assertEquals(VLLM, learned.place(request(states)).orElseThrow().pick().server().name());
    }

    @Test
    void ollamaPlacementIsUnchanged_theGpuWithAVramTotalStillNeedsRoom() {
        ServerState tiny = new ServerState("ollama-tiny", "http://tiny:11434", 1, 0, 0, List.of(), true,
                "2026-10-19T00:00:00Z", 4_000L, Map.of(), Map.of(MODEL, 8_855L));
        states = List.of(tiny);
        assertTrue(selectorOver().options(MODEL, FOOTPRINT_MIB, 0L, 0L).isEmpty(),
                "an Ollama GPU that cannot fit the model is still no option");
    }

    @Test
    void anAsleepModelWithAnObservedWakeCostsLessThanOneWithNone() {
        ServerState observed = new ServerState("vllm-a", "", 1, 0, 0, List.of(), true, "2026-10-19T00:00:00Z",
                0L, Map.of(), Map.of(MODEL, 0L), 0L, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://a:8000"),
                0, 0, 0.0, Map.of(MODEL, 1_500L));
        ServerState unobserved = new ServerState("vllm-b", "", 1, 0, 0, List.of(), true, "2026-10-19T00:00:00Z",
                0L, Map.of(), Map.of(MODEL, 0L), 0L, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://b:8000"),
                0, 0, 0.0);
        var ranked = ServerSelector.rankCandidates(List.of(unobserved, observed), MODEL, FOOTPRINT_MIB,
                ServerSelector.LoadView.of(p -> 0, p -> 0L, p -> 0.0, n -> false), new StaticFitPredictor());
        assertEquals("vllm-a", ranked.get(0).server().name(), "a known 1.5 s wake beats the default load penalty");
    }

    @Test
    void theLearnedPredictionUsesTheObservedWakeForASleepingModel() {
        ModelSpeed speed = new ModelSpeed(30_000, 20_000, 120, 0, 0);
        ServerState asleep = new ServerState(VLLM, "", 1, 0, 0, List.of(), true, "2026-10-19T00:00:00Z",
                0L, Map.of(), Map.of(MODEL, 0L), 0L, Map.of(), Map.of(MODEL, speed), "vllm",
                Map.of(MODEL, "http://v:8000"), 0, 0, 0.0, Map.of(MODEL, 2_000L));
        var option = new ServerSelector.Option(asleep, false, true, 0.0);
        var prediction = LearnedPlacement.predict(option, MODEL, 0L).orElseThrow();
        assertEquals(2.0, prediction.load(), 1e-9, "the observed wake, not the calibrated 30 s load");
    }

    private static final class Fallback implements PlacementPolicy {
        @Override
        public PlacementMode mode() {
            return PlacementMode.ALGORITHM;
        }

        @Override
        public Optional<Placement> place(PlacementRequest request) {
            return Optional.empty();
        }
    }
}
