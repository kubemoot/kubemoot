package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;

/**
 * The rules for an engine that reserved its accelerator memory when its server started
 * (vLLM): a server that hosts the model serves it, memory figures do not matter, and
 * capacity is the engine's own queue and cache.
 */
class ServedModelFitTest {

    private static final String MODEL = "qwen3:14b";

    /** A vLLM server hosting {@code awake} models and {@code asleep} ones, with the engine's load. */
    private static ServerState vllm(List<String> awake, List<String> asleep, boolean ready, Integer running,
                                      Integer waiting, Double kv) {
        Map<String, Long> hostedAsleep = new java.util.HashMap<>();
        asleep.forEach(m -> hostedAsleep.put(m, 0L));
        Map<String, Long> awakeFootprints = new java.util.HashMap<>();
        awake.forEach(m -> awakeFootprints.put(m, 0L));
        return new ServerState("vllm-rig1", "", 1, 0, 0, awake, ready, "2026-10-19T00:00:00Z", 0L,
                awakeFootprints, hostedAsleep, 0L, Map.of(), Map.of(), "vllm",
                Map.of(MODEL, "http://vllm-14b:8000"), running, waiting, kv);
    }

    private static ServerState idle() {
        return vllm(List.of(MODEL), List.of(), true, 0, 0, 0.1);
    }

    private static FitInputs inputs(ServerState p, String model, boolean circuitOpen) {
        return new FitInputs(p, model, 0, 0L, 0L, 0L, 0.0, circuitOpen);
    }

    private final FitPredictor predictor = new StaticFitPredictor();

    // ---- fit: a server that hosts the model serves it ----

    @Test
    void aServerThatHostsTheModelServesIt_withNoVramTotalAndNoFootprint() {
        FitScore score = predictor.predict(inputs(idle(), MODEL, false));
        assertTrue(score.canServe(), score.reasoning());
    }

    @Test
    void aServerThatDoesNotHostTheModelCannotServeIt() {
        FitScore score = predictor.predict(inputs(idle(), "qwen3:32b", false));
        assertFalse(score.canServe());
        assertTrue(score.reasoning().contains("does not host"), score.reasoning());
    }

    @Test
    void aHostedButSleepingModelIsServedAtTheCostOfAWake() {
        ServerState asleep = vllm(List.of(), List.of(MODEL), true, 0, 0, 0.0);
        FitScore score = predictor.predict(inputs(asleep, MODEL, false));
        assertTrue(score.canServe(), score.reasoning());
        assertTrue(score.reasoning().contains("waking"), score.reasoning());
    }

    @Test
    void aModelALoadingTicketNamesCountsAsHosted() {
        ServerState other = vllm(List.of("qwen3:8b"), List.of(), true, 0, 0, 0.0);
        FitInputs in = new FitInputs(other, MODEL, 1, 0L, 0L, 0L, 0.0, false, Set.of(MODEL));
        assertTrue(predictor.predict(in).canServe());
    }

    @Test
    void anOpenCircuitOrAnUnreadyServerIsRefused() {
        assertFalse(predictor.predict(inputs(idle(), MODEL, true)).canServe());
        assertFalse(predictor.predict(inputs(vllm(List.of(MODEL), List.of(), false, 0, 0, 0.0), MODEL, false))
                .canServe());
    }

    @Test
    void theColdLoadFootprintAndUnknownVramNeverExcludeIt() {
        ServerState p = idle();
        assertEquals(0L, p.totalVramMiB());
        assertEquals(0L, p.coldLoadFootprintMiB(MODEL));
        FitInputs huge = new FitInputs(p, MODEL, 0, 0L, 900_000L, 0L, 0.0, false);
        assertTrue(predictor.predict(huge).canServe(), "a footprint larger than any accelerator is irrelevant here");
    }

    @Test
    void anOllamaServerIsStillJudgedByMemory() {
        ServerState ollama = new ServerState("ollama-cpu", "http://cpu:11434", 1, 0, 0, List.of(), true,
                "2026-10-19T00:00:00Z", 0L, Map.of(), Map.of(MODEL, 8_855L));
        assertFalse(predictor.predict(inputs(ollama, MODEL, false)).canServe(), "unknown total VRAM refuses a cold load");
    }

    // ---- capacity: the engine's queue and cache, not slots ----

    @Test
    void waitingRequestsMeanBusy() {
        ServerState busy = vllm(List.of(MODEL), List.of(), true, 40, 3, 0.4);
        assertFalse(ServedModelFit.hasFreeCapacity(busy));
    }

    @Test
    void aFullCacheMeansBusyEvenWithNoQueue() {
        assertFalse(ServedModelFit.hasFreeCapacity(vllm(List.of(MODEL), List.of(), true, 40, 0, 0.95)));
        assertFalse(ServedModelFit.hasFreeCapacity(vllm(List.of(MODEL), List.of(), true, 40, 0, ServedModelFit.KV_CACHE_HIGH)));
        assertTrue(ServedModelFit.hasFreeCapacity(vllm(List.of(MODEL), List.of(), true, 40, 0, 0.89)));
    }

    @Test
    void noWaitingRequestsMeansRoom_howeverManyRunAndWhateverMaxParallelSays() {
        ServerState batching = vllm(List.of(MODEL), List.of(), true, 64, 0, 0.5);
        assertEquals(1, batching.maxParallel(), "the operator still publishes the minimum");
        assertTrue(ServedModelFit.hasFreeCapacity(batching));
    }

    @Test
    void anEngineThatReportsNoLoadIsReadAsHavingRoom() {
        assertTrue(ServedModelFit.hasFreeCapacity(vllm(List.of(MODEL), List.of(), true, null, null, null)));
    }

    @Test
    void theDriverAnswersFromTheEnginesLoad_notFromInFlightCalls() {
        EngineDriver driver = new OperatorLifecycleDriver(new OllamaDriver(null), "vllm");
        ServerState p = idle();
        assertTrue(driver.hasFreeCapacity(p, 50), "fifty calls in flight batch; they do not fill slots");
        assertFalse(driver.hasFreeCapacity(vllm(List.of(MODEL), List.of(), true, 8, 2, 0.2), 0));
    }

    @Test
    void expectedWaitIsZeroWithRoomAndGrowsWithTheQueue() {
        assertEquals(0.0, ServedModelFit.expectedWaitSeconds(idle(), List.of(), 30.0));
        ServerState queued = vllm(List.of(MODEL), List.of(), true, 10, 5, 0.5);
        assertEquals(15.0, ServedModelFit.expectedWaitSeconds(queued, List.of(), 30.0), 1e-9);
        ServerState cacheFullNoQueue = vllm(List.of(MODEL), List.of(), true, 10, 0, 0.99);
        assertEquals(3.0, ServedModelFit.expectedWaitSeconds(cacheFullNoQueue, List.of(), 30.0), 1e-9);
    }

    // ---- crowding, tickets, footprint ----

    @Test
    void contentionIsTheEnginesQueueAndCache_withALightTouchForLocalCalls() {
        ServerState p = vllm(List.of(MODEL), List.of(), true, 10, 2, 0.5);
        assertEquals(2.5, p.engineFit().contention(p, 0), 1e-9);
        assertEquals(2.5 + 0.1, p.engineFit().contention(p, 10), 1e-9);
        assertEquals(0.0, vllm(List.of(MODEL), List.of(), true, null, null, null).engineFit().contention(idleNoLoad(), 0));
    }

    private static ServerState idleNoLoad() {
        return vllm(List.of(MODEL), List.of(), true, null, null, null);
    }

    @Test
    void ticketsNeverRefuseACallOnAnEngineThatBudgetsNoMemory() {
        assertTrue(idle().engineFit().ticketHeadroomMiB(idle()) > 1_000_000_000L);
        assertTrue(TicketManager.withinBudget(10_000_000L, ServedModelFit.TICKET_MARKER_MIB,
                idle().engineFit().ticketHeadroomMiB(idle()), 0L));
    }

    @Test
    void aModelOnlyAnEngineReservedServerHostsCarriesTheTicketMarkerNotZero() {
        List<ServerState> states = List.of(idle());
        assertEquals(ServedModelFit.TICKET_MARKER_MIB, EngineFits.placementFootprintMiB(states, MODEL, 0L));
        assertEquals(9_000L, EngineFits.placementFootprintMiB(states, MODEL, 9_000L), "a reported footprint wins");
        assertEquals(0L, EngineFits.placementFootprintMiB(states, "qwen3:32b", 0L), "an unhosted model stays unknown");
        assertEquals(0L, EngineFits.placementFootprintMiB(List.of(), MODEL, 0L));
    }

    @Test
    void aVllmHostedModelCanAlwaysBeHeld_aHostlessOversizedOneStillCannot() {
        ServerState small = new ServerState("o", "http://o", 1, 0, 0, List.of(), true, "", 16_000L, Map.of());
        assertTrue(ServerSelector.canEverHold(List.of(small, idle()), MODEL, 90_000L));
        assertFalse(ServerSelector.canEverHold(List.of(small, idle()), "qwen3:235b", 90_000L));
    }

    // ---- engine dispatch and the published fields ----

    @Test
    void theEngineTypePicksTheFit_andAgreesWithTheChatEngine() {
        for (String engine : new String[] {null, "", " ", "ollama", " OLLAMA ", "vllm", "sglang", "openai"}) {
            boolean ollamaChat = ChatEngines.forType(engine) == ChatEngines.OLLAMA;
            assertEquals(ollamaChat, EngineFits.forType(engine) == OllamaFit.INSTANCE, "engine " + engine);
        }
    }

    @Test
    void theEngineReportedLoadIsParsed_andAReportedZeroStaysZero() throws Exception {
        ObjectMapper json = new ObjectMapper();
        ServerState p = ServerSelector.fromJson(json.readTree("""
                {"name":"v","engine":"vllm","endpoint":"","ready":true,
                 "requestsRunning":0,"requestsWaiting":0,"kvCacheUsage":0.0}"""));
        assertEquals(0, p.requestsRunning());
        assertEquals(0, p.requestsWaiting());
        assertEquals(0.0, p.kvCacheUsage());

        ServerState absent = ServerSelector.fromJson(json.readTree("{\"name\":\"o\",\"endpoint\":\"http://o\"}"));
        assertNull(absent.requestsRunning());
        assertNull(absent.requestsWaiting());
        assertNull(absent.kvCacheUsage());
    }

    // ---- the other places memory figures could drop a hosting server ----

    @Test
    void contextFitIgnoresMemoryForAnEngineThatBudgetsNone() {
        ServerState declared = new ServerState("vllm-rig1", "", 1, 0, 0, List.of(MODEL), true, "", 8_000L,
                Map.of(MODEL, 0L), Map.of(), 0L, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://v:8000"),
                0, 0, 0.0);
        assertTrue(ContextFit.anyCanRun(List.of(declared), List.of(MODEL), 100L, m -> 22_000L),
                "a declared memory budget smaller than the Ollama footprint must not refuse a hosting server");
        ServerState ollama = new ServerState("o", "http://o", 1, 0, 0, List.of(), true, "", 8_000L, Map.of());
        assertFalse(ContextFit.anyCanRun(List.of(ollama), List.of(MODEL), 100L, m -> 22_000L));
    }

    @Test
    void noEvictionIsEverPlannedOnAnEngineThatBudgetsNoMemory() {
        ServerState declared = new ServerState("vllm-rig1", "", 1, 0, 0, List.of(MODEL), true, "", 64_000L,
                Map.of(MODEL, 0L), Map.of(), 0L, Map.of(), Map.of(), "vllm", Map.of(MODEL, "http://v:8000"),
                0, 0, 0.0);
        var selector = new ServerSelector(null, new ObjectMapper(), org.mockito.Mockito.mock(TicketManager.class),
                null, new OllamaDriver(null));
        assertTrue(selector.planEviction("qwen3:32b", 20_000L, List.of(declared), Map.of()).isEmpty());
    }

    @Test
    void driverForANonOllamaEngineIsCachedPerEngine_andAMissingEngineIsOllama() {
        var selector = new ServerSelector(null, new ObjectMapper(), null, null, new OllamaDriver(null));
        assertSame(selector.driverFor(idle()), selector.driverFor(idle()));
        assertNotSame(selector.driver(), selector.driverFor(idle()));
        ServerState noEngine = new ServerState("o", "http://o", 1, 0, 0, List.of(), true, "", 0L, Map.of());
        assertSame(selector.driver(), selector.driverFor(noEngine));
    }

    @Test
    void optionalNumbersAreNullWhenAbsentOrNotNumbers() throws Exception {
        var node = new ObjectMapper().readTree("{\"a\":\"x\",\"b\":2,\"c\":0.5}");
        assertNull(ServerSelector.optionalInt(node, "a"));
        assertNull(ServerSelector.optionalInt(node, "missing"));
        assertEquals(2, ServerSelector.optionalInt(node, "b"));
        assertNull(ServerSelector.optionalDouble(node, "a"));
        assertEquals(0.5, ServerSelector.optionalDouble(node, "c"));
    }

    @Test
    void placementFootprintKeepsAReportedValueAndToleratesNoStates() {
        assertEquals(9_000L, EngineFits.placementFootprintMiB(null, MODEL, 9_000L));
        assertEquals(0L, EngineFits.placementFootprintMiB(null, MODEL, 0L));
    }
}
