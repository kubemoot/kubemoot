package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.CallPlanner.Placement;
import ai.kubemoot.agent.modelserver.CallPlanner.PlacementRequest;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/**
 * The learned policy places a call where it is predicted to finish soonest: a fast
 * accelerator by default, a slow server only when the fast ones are busy enough
 * that it finishes first, and never a long prompt on a slow server when a fast one
 * finishes first. It keeps the crew's model choice and has no opinion without
 * measurements.
 */
class LearnedPlacementTest {

    private static final String GPU = "ollama-gpu";
    private static final String CPU = "ollama-cpu";
    private static final String SMALL = "qwen3:8b";
    private static final String OTHER = "qwen3:14b";
    private static final long SMALL_MIB = 5_120;

    /** Speeds of the same model on a fast accelerator and on a CPU. */
    private static final ModelSpeed GPU_SPEED = new ModelSpeed(2_892, 15_162, 235, 0, 0);
    private static final ModelSpeed CPU_SPEED = new ModelSpeed(4_297, 162, 7, 0, 0);

    private static ServerState server(String name, List<String> loaded, Map<String, ModelSpeed> speeds) {
        return new ServerState(name, "http://" + name, 1, 0, 0, loaded, true, "2026-01-01T00:00:00Z",
                32_000, Map.of(), Map.of(SMALL, SMALL_MIB, OTHER, 9_000L), 0L, Map.of(), speeds);
    }

    private static PlacementRequest request(long promptTokens) {
        return new PlacementRequest(SMALL, List.of(OTHER), List.of(), m -> SMALL_MIB, m -> 0L, promptTokens);
    }

    /** A selector whose options are fixed and whose claims are recorded. */
    private static class FixedSelector extends ServerSelector {
        final Map<String, List<Option>> options;
        final List<String> claims = new ArrayList<>();
        boolean refuse;

        FixedSelector(Map<String, List<Option>> options) {
            super(null, new ObjectMapper(), null, null, new OllamaDriver(null));
            this.options = options;
        }

        @Override
        public List<Option> options(String model, long fp, long kv, long promptTokens) {
            return options.getOrDefault(model, List.of());
        }

        @Override
        public Optional<Pick> pickAndClaimOn(String server, Start start, String model, long fp, long kv,
                                             long promptTokens) {
            claims.add(model + "@" + server + " " + start);
            if (refuse) return Optional.empty();
            ServerState p = options.get(model).stream().map(Option::server)
                    .filter(s -> s.name().equals(server)).findFirst().orElseThrow();
            return Optional.of(new Pick(p, null, null, false));
        }
    }

    /** A fallback that records whether it was asked. */
    private static final class Fallback implements PlacementPolicy {
        int calls;

        @Override
        public PlacementMode mode() {
            return PlacementMode.ALGORITHM;
        }

        @Override
        public Optional<Placement> place(PlacementRequest request) {
            calls++;
            return Optional.empty();
        }
    }

    private static ServerSelector.Option idle(ServerState p, boolean warm) {
        return new ServerSelector.Option(p, warm, true, 0.0);
    }

    private static ServerSelector.Option busy(ServerState p, double waitSeconds) {
        return new ServerSelector.Option(p, true, false, waitSeconds);
    }

    private final ServerState gpuCold = server(GPU, List.of(), Map.of(SMALL, GPU_SPEED));
    private final ServerState gpuWarm = server(GPU, List.of(SMALL), Map.of(SMALL, GPU_SPEED));
    private final ServerState cpuWarm = server(CPU, List.of(SMALL), Map.of(SMALL, CPU_SPEED));

    @Test
    void anIdleAcceleratorBeatsAWarmCpu_evenWhenItMustLoad() {
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), idle(gpuCold, false))));
        var learned = new LearnedPlacement(new Fallback(), selector);
        Optional<LearnedPlacement.Proposal> p = learned.propose(request(500));
        assertTrue(p.isPresent());
        assertEquals(GPU, p.get().server());
        assertTrue(p.get().reason().contains("load 2.9"), p.get().reason());
    }

    @Test
    void theCpuWinsOnlyWhenTheAcceleratorQueueIsLong() {
        var longQueue = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), busy(gpuWarm, 120))));
        var fallback = new Fallback();
        assertEquals(CPU, new LearnedPlacement(fallback, longQueue).propose(request(200)).orElseThrow().server());
        assertTrue(new LearnedPlacement(fallback, longQueue).place(request(200)).isPresent());
        assertEquals(List.of(SMALL + "@" + CPU + " SLOT"), longQueue.claims);

        // A short queue on the accelerator still finishes first; waiting is the fallback's to decide.
        var shortQueue = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), busy(gpuWarm, 5))));
        assertEquals(GPU, new LearnedPlacement(fallback, shortQueue).propose(request(200)).orElseThrow().server());
        new LearnedPlacement(fallback, shortQueue).place(request(200));
        assertTrue(shortQueue.claims.isEmpty(), "the learned policy never queues itself");
        assertEquals(1, fallback.calls);
    }

    @Test
    void aLongPromptNeverGoesToTheCpuWhenTheAcceleratorFinishesFirst() {
        // 8000 prompt tokens: about 49s of prompt processing on the CPU, under 1s on the GPU.
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), busy(gpuWarm, 40))));
        assertEquals(GPU, new LearnedPlacement(new Fallback(), selector).propose(request(8_000)).orElseThrow().server());
    }

    @Test
    void anUnmeasuredOptionLeavesTheCallToTheFallback() {
        ServerState unmeasured = server("new-gpu", List.of(SMALL), Map.of());
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(unmeasured, true), idle(cpuWarm, true))));
        assertTrue(new LearnedPlacement(new Fallback(), selector).propose(request(100)).isEmpty(),
                "an unmeasured server must not lose to a measured slow one, nor win by guess");
    }

    @Test
    void aServerWithoutTheModelIsNeitherAnOptionNorAReasonForNoOpinion() {
        // The CPU has not downloaded OTHER: it can neither serve the call nor stop an opinion.
        ServerState cpuWithout = new ServerState(CPU, "http://" + CPU, 1, 0, 0, List.of(SMALL), true,
                "2026-01-01T00:00:00Z", 32_000, Map.of(), Map.of(SMALL, SMALL_MIB), 0L, Map.of(), Map.of());
        ServerState gpuWithOther = server(GPU, List.of(OTHER), Map.of(OTHER, new ModelSpeed(1, 8_000, 130, 0, 0)));
        var selector = new FixedSelector(Map.of(OTHER, List.of(idle(cpuWithout, false), idle(gpuWithOther, true))));
        var request = new PlacementRequest(OTHER, List.of(), List.of(), m -> 9_000L, m -> 0L, 100L);
        assertEquals(GPU, new LearnedPlacement(new Fallback(), selector).propose(request).orElseThrow().server());
        assertTrue(gpuWithOther.hasModel(OTHER));
        assertFalse(cpuWithout.hasModel(OTHER));
        assertTrue(cpuWithout.hasModel(SMALL));
    }

    @Test
    void aStateWithNoDownloadedListCountsOnlyWhatIsLoaded() {
        ServerState noList = new ServerState(CPU, "http://" + CPU, 1, 0, 0, List.of(SMALL), true,
                "2026-01-01T00:00:00Z", 32_000, Map.of(), null, 0L, Map.of(), Map.of());
        assertTrue(noList.hasModel(SMALL));
        assertFalse(noList.hasModel(OTHER));
    }

    @Test
    void itKeepsThePreferredModel_andUsesAnAlternativeOnlyWhenThePreferredIsUnmeasured() {
        ServerState fastOther = server(GPU, List.of(OTHER), Map.of(OTHER, new ModelSpeed(1, 99_999, 999, 0, 0)));
        var both = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true)), OTHER, List.of(idle(fastOther, true))));
        assertEquals(SMALL, new LearnedPlacement(new Fallback(), both).propose(request(100)).orElseThrow().model(),
                "a faster alternative model must not replace the crew's preferred model");

        ServerState unmeasured = server(CPU, List.of(SMALL), Map.of());
        var preferredUnmeasured = new FixedSelector(Map.of(SMALL, List.of(idle(unmeasured, true)),
                OTHER, List.of(idle(fastOther, true))));
        assertTrue(new LearnedPlacement(new Fallback(), preferredUnmeasured).propose(request(100)).isEmpty(),
                "a placeable preferred model is not swapped for a measured alternative");

        var preferredNowhere = new FixedSelector(Map.of(OTHER, List.of(idle(fastOther, true))));
        assertEquals(OTHER, new LearnedPlacement(new Fallback(), preferredNowhere).propose(request(100)).orElseThrow().model());
    }

    @Test
    void observedSpeedReplacesCalibratedSpeedOnceTrusted() {
        ServerState slowedGpu = server(GPU, List.of(SMALL), Map.of(SMALL, new ModelSpeed(2_892, 15_162, 235, 3, 12)));
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(slowedGpu, true), idle(cpuWarm, true))));
        assertEquals(CPU, new LearnedPlacement(new Fallback(), selector).propose(request(100)).orElseThrow().server());
    }

    @Test
    void placeClaimsTheChosenServerTheWayItCanStart() {
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), idle(gpuCold, false))));
        var fallback = new Fallback();
        Optional<Placement> placed = new LearnedPlacement(fallback, selector).place(request(100));
        assertTrue(placed.isPresent());
        assertEquals(List.of(SMALL + "@" + GPU + " LOAD"), selector.claims);
        assertEquals(SMALL, placed.get().model());
        assertEquals(0, fallback.calls);
    }

    @Test
    void theFallbackPlacesWhenThereIsNoOpinionOrTheClaimIsLost() {
        var fallback = new Fallback();
        new LearnedPlacement(fallback, new FixedSelector(Map.of())).place(request(100));
        new LearnedPlacement(fallback).place(request(100));
        var lost = new FixedSelector(Map.of(SMALL, List.of(idle(gpuWarm, true))));
        lost.refuse = true;
        new LearnedPlacement(fallback, lost).place(request(100));
        assertEquals(3, fallback.calls);
        assertEquals(List.of(SMALL + "@" + GPU + " SLOT"), lost.claims);
    }

    @Test
    void startOfMapsEachOption() {
        assertEquals(ServerSelector.Start.LOAD, LearnedPlacement.startOf(idle(gpuCold, false)));
        assertEquals(ServerSelector.Start.SLOT, LearnedPlacement.startOf(idle(gpuWarm, true)));
    }

    @Test
    void theModelSpeedPrefersObservedOnceTrusted() {
        assertEquals(235, GPU_SPEED.generateSpeed());
        assertEquals(40, new ModelSpeed(1, 2, 235, 40, 12).generateSpeed());
    }

    @Test
    void serverStateParsesModelSpeeds() throws Exception {
        ObjectMapper json = new ObjectMapper();
        var full = ServerSelector.speedsField(json.readTree("""
                {"modelSpeeds":{"qwen3:8b":{"loadMs":2892,"promptTokensPerSecond":15162,
                 "generateTokensPerSecond":235,"observedGenerateTokensPerSecond":220,"observedSamples":9},
                 "broken":"not an object"}}"""));
        assertEquals(new ModelSpeed(2892, 15162, 235, 220, 9), full.get(SMALL));
        assertFalse(full.containsKey("broken"));
        assertTrue(ServerSelector.speedsField(json.readTree("{}")).isEmpty());
        assertTrue(ServerSelector.speedsField(json.readTree("{\"modelSpeeds\":[1,2]}")).isEmpty());
        var partial = ServerSelector.speedsField(json.readTree("{\"modelSpeeds\":{\"m\":{\"generateTokensPerSecond\":5}}}"));
        assertEquals(new ModelSpeed(0, 0, 5, 0, 0), partial.get("m"));
        assertTrue(ServerSelector.fromJson(json.readTree("{\"name\":\"p\"}")).speedOf(SMALL).isEmpty());
    }

    @Test
    void predictNeedsBothSpeeds() {
        ServerState noPrompt = server(GPU, List.of(SMALL), Map.of(SMALL, new ModelSpeed(1, 0, 100, 0, 0)));
        assertTrue(LearnedPlacement.predict(idle(noPrompt, true), SMALL, 10).isEmpty());
        ServerState noGenerate = server(GPU, List.of(SMALL), Map.of(SMALL, new ModelSpeed(1, 100, 0, 0, 0)));
        assertTrue(LearnedPlacement.predict(idle(noGenerate, true), SMALL, 10).isEmpty());
        var warm = LearnedPlacement.predict(idle(gpuWarm, true), SMALL, -5).orElseThrow();
        assertEquals(0.0, warm.load());
        assertEquals(0.0, warm.prompt(), "a negative prompt size counts as none");
    }

    // ---- the decision each placement carries ----

    /** A policy that places on a fixed server with the algorithm's fit explanation. */
    private static final class Placing implements PlacementPolicy {
        final PlacementMode mode;
        final ServerState on;

        Placing(PlacementMode mode, ServerState on) {
            this.mode = mode;
            this.on = on;
        }

        @Override
        public PlacementMode mode() {
            return mode;
        }

        @Override
        public Optional<Placement> place(PlacementRequest request) {
            return Optional.of(new Placement(new Pick(on, null, FitScore.yes(0, "warm, slot 1/2")), SMALL, SMALL_MIB,
                    List.of()));
        }
    }

    @Test
    void aLearnedPlacementCarriesItsPredictionItsReasonAndTheOtherOptions() {
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), idle(gpuCold, false))));

        PlacementDecision d = new LearnedPlacement(new Fallback(), selector).place(request(500)).orElseThrow().decision();

        assertEquals(PlacementDecision.LEARNED, d.policy());
        assertTrue(d.reason().startsWith("predicted "), d.reason());
        assertEquals(2.892, d.predicted().load(), 1e-9, "a cold start predicts the load");
        assertEquals(1, d.alternatives().size());
        var cpu = d.alternatives().get(0);
        assertEquals(CPU, cpu.server());
        assertTrue(cpu.warm() && cpu.startsNow());
        assertTrue(cpu.predicted().total() > d.predicted().total(), "the CPU would have finished later");
    }

    @Test
    void whenTheLearnedChoiceHasToWaitTheAlgorithmsPlacementSaysSo() {
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), busy(gpuWarm, 5))));

        PlacementDecision d = new LearnedPlacement(new Placing(PlacementMode.ALGORITHM, cpuWarm), selector)
                .place(request(200)).orElseThrow().decision();

        assertEquals(PlacementDecision.ALGORITHM, d.policy());
        assertTrue(d.reason().startsWith("warm, slot 1/2"), d.reason());
        assertTrue(d.reason().contains("has to wait for a slot"), d.reason());
        assertNotNull(d.predicted(), "the CPU's own prediction is kept for scoring");
        assertEquals(1, d.alternatives().size());
        assertEquals(GPU, d.alternatives().get(0).server());
    }

    @Test
    void whenTheLearnedPolicyHasNoOpinionTheDecisionSaysWhy() {
        ServerState unmeasured = server("new-gpu", List.of(SMALL), Map.of());
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(unmeasured, true), idle(cpuWarm, true))));

        PlacementDecision d = new LearnedPlacement(new Placing(PlacementMode.ALGORITHM, cpuWarm), selector)
                .place(request(100)).orElseThrow().decision();

        assertEquals(PlacementDecision.ALGORITHM, d.policy());
        assertTrue(d.reason().contains("no opinion"), d.reason());
        assertNotNull(d.predicted(), "the measured CPU still has a prediction");
        assertNull(d.alternatives().get(0).predicted(), "the unmeasured server has none");
    }

    @Test
    void whenTheLearnedChoiceWasTakenFirstTheDecisionSaysWhich() {
        var lost = new FixedSelector(Map.of(SMALL, List.of(idle(gpuWarm, true), idle(cpuWarm, true))));
        lost.refuse = true;

        PlacementDecision d = new LearnedPlacement(new Placing(PlacementMode.ALGORITHM, cpuWarm), lost)
                .place(request(100)).orElseThrow().decision();

        assertTrue(d.reason().contains("was taken first"), d.reason());
        assertTrue(d.reason().contains(SMALL + "@" + GPU), d.reason());
    }

    @Test
    void theCallPlannerExplainsAnAlgorithmPlacementWithoutChangingIt() {
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), idle(gpuWarm, true))));
        var planner = new CallPlanner(selector, null, null, new Placing(PlacementMode.ALGORITHM, cpuWarm));

        Placement placed = planner.place(request(200)).orElseThrow();

        assertEquals(CPU, placed.pick().server().name(), "the explanation never moves the call");
        PlacementDecision d = placed.decision();
        assertEquals(PlacementDecision.ALGORITHM, d.policy());
        assertEquals("warm, slot 1/2", d.reason(), "the algorithm's reason, with nothing about the learned policy");
        assertEquals(LearnedPlacement.predict(idle(cpuWarm, true), SMALL, 200).orElseThrow(), d.predicted());
        assertEquals(GPU, d.alternatives().get(0).server());
    }

    @Test
    void underShadowTheDecisionIsLabeledAndNamesWhatTheLearnedPolicyWouldChoose() {
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), idle(gpuWarm, true))));
        var planner = new CallPlanner(selector, null, null, new Placing(PlacementMode.SHADOW, cpuWarm));

        PlacementDecision d = planner.place(request(200)).orElseThrow().decision();

        assertEquals(PlacementDecision.SHADOW, d.policy());
        assertTrue(d.reason().contains("learned policy would choose " + SMALL + "@" + GPU), d.reason());
    }

    @Test
    void underShadowAgreementIsSaid() {
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), idle(gpuWarm, true))));
        var planner = new CallPlanner(selector, null, null, new Placing(PlacementMode.SHADOW, gpuWarm));

        assertTrue(planner.place(request(200)).orElseThrow().decision().reason().contains("learned policy agrees"));
    }

    @Test
    void aFailureToSurveyLeavesThePlacementWithoutAPrediction() {
        var broken = new FixedSelector(Map.of()) {
            @Override
            public List<Option> options(String model, long fp, long kv, long promptTokens) {
                throw new IllegalStateException("state unreadable");
            }
        };
        var planner = new CallPlanner(broken, null, null, new Placing(PlacementMode.ALGORITHM, cpuWarm));

        PlacementDecision d = planner.place(request(100)).orElseThrow().decision();

        assertNull(d.predicted());
        assertTrue(d.alternatives().isEmpty());
        assertEquals("warm, slot 1/2", d.reason());
    }

    @Test
    void aPlacementOnAServerTheSurveyDidNotWeighHasNoPrediction() {
        ServerState other = server("other", List.of(SMALL), Map.of(SMALL, GPU_SPEED));
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(gpuWarm, true))));
        var planner = new CallPlanner(selector, null, null, new Placing(PlacementMode.ALGORITHM, other));

        assertNull(planner.place(request(100)).orElseThrow().decision().predicted());
    }

    @Test
    void theAlgorithmsReasonNamesWhatItUnloadedAndWhenTheCallQueued() {
        var unloaded = new Placement(new Pick(cpuWarm, null, FitScore.yes(0, "cold-load fits")), SMALL, 1,
                List.of(OTHER));
        assertEquals("cold-load fits; unloaded " + OTHER + " to make room", PlacementDecisions.algorithmReason(unloaded));

        var queued = new Placement(new Pick(cpuWarm, null, FitScore.yes(0, "warm"), true), SMALL, 1, List.of());
        assertEquals("warm; joined a busy copy and may wait for a slot", PlacementDecisions.algorithmReason(queued));

        var unexplained = new Placement(new Pick(cpuWarm, null, null, false), SMALL, 1, List.of());
        assertEquals("placed by the algorithm", PlacementDecisions.algorithmReason(unexplained));
    }

    // ---- the selector's side, with real tickets ----

    private static ServerSelector realSelector(List<ServerState> states, TicketManager tickets) {
        return new ServerSelector(null, new ObjectMapper(), tickets, null, new OllamaDriver(null)) {
            @Override
            public List<ServerState> readState() {
                return states;
            }
        };
    }

    private static TicketManager realTickets(InMemoryKv bucket) {
        return new TicketManager(bucket.server(TicketManager.TICKETS_BUCKET), new ObjectMapper());
    }

    private static long ticketsOn(InMemoryKv bucket, String server) {
        return bucket.data.keySet().stream().filter(k -> k.startsWith(TicketManager.keyPrefixFor(server))).count();
    }

    @Test
    void optionsClaimNothing_andAColdLoadGoesToTheChosenServerWhileACopyIsResidentAndBusyElsewhere() {
        InMemoryKv bucket = new InMemoryKv();
        TicketManager tickets = realTickets(bucket);
        ServerSelector selector = realSelector(List.of(cpuWarm, gpuCold), tickets);
        // A call is running on the CPU's resident copy: that is not a load, and must not stop one on the GPU.
        assertTrue(selector.pickAndClaimOn(CPU, ServerSelector.Start.SLOT, SMALL, SMALL_MIB, 0L, 100L).isPresent());
        long before = bucket.data.size();

        List<ServerSelector.Option> options = selector.options(SMALL, SMALL_MIB, 0L, 100L);
        assertEquals(2, options.size(), options.toString());
        assertEquals(before, bucket.data.size(), "listing options must not claim");

        Optional<Pick> pick = selector.pickAndClaimOn(GPU, ServerSelector.Start.LOAD, SMALL, SMALL_MIB, 0L, 100L);
        assertTrue(pick.isPresent(), "a busy resident copy on the CPU must not block a load on the GPU");
        assertEquals(GPU, pick.get().server().name());
        assertEquals(1, ticketsOn(bucket, GPU));
        assertTrue(selector.pickAndClaimOn(CPU, ServerSelector.Start.LOAD, SMALL, SMALL_MIB, 0L, 100L).isEmpty(),
                "a cold load is not a way to start where the model is resident");
    }

    @Test
    void aColdLoadYieldsToALoadAlreadyInProgressOnAnotherServer() {
        ServerState otherGpuCold = server("ollama-rig1", List.of(), Map.of(SMALL, GPU_SPEED));
        InMemoryKv bucket = new InMemoryKv();
        ServerSelector selector = realSelector(List.of(gpuCold, otherGpuCold), realTickets(bucket));
        assertTrue(selector.pickAndClaimOn(GPU, ServerSelector.Start.LOAD, SMALL, SMALL_MIB, 0L, 100L).isPresent());
        assertTrue(selector.pickAndClaimOn("ollama-rig1", ServerSelector.Start.LOAD, SMALL, SMALL_MIB, 0L, 100L).isEmpty(),
                "a second concurrent load of the same model must converge on the first");
        assertEquals(0, ticketsOn(bucket, "ollama-rig1"));
    }

    @Test
    void aSlotClaimOnAFullCopyIsRefused() {
        InMemoryKv bucket = new InMemoryKv();
        ServerSelector selector = realSelector(List.of(gpuWarm), realTickets(bucket));
        assertTrue(selector.pickAndClaimOn(GPU, ServerSelector.Start.SLOT, SMALL, SMALL_MIB, 0L, 100L).isPresent());
        assertTrue(selector.pickAndClaimOn(GPU, ServerSelector.Start.SLOT, SMALL, SMALL_MIB, 0L, 100L).isEmpty(),
                "the server runs one call at a time");
        assertEquals(1, ticketsOn(bucket, GPU));
        assertFalse(selector.options(SMALL, SMALL_MIB, 0L, 100L).get(0).startsNow());
    }

    @Test
    void shadowRecordsADifferenceBetweenTheAlgorithmAndTheLearnedPolicy() {
        InMemoryKv bucket = new InMemoryKv();
        TicketManager tickets = realTickets(bucket);
        ServerSelector selector = realSelector(List.of(cpuWarm, gpuCold), tickets);
        AlgorithmPlacement algorithm = new AlgorithmPlacement(selector, tickets, null);
        List<ShadowPlacement.Decision> decisions = new ArrayList<>();
        PlacementPolicy shadow = PlacementPolicies.create("shadow", algorithm, selector, decisions::add);

        Optional<Placement> placed = shadow.place(new PlacementRequest(SMALL, List.of(), List.of(cpuWarm, gpuCold),
                m -> SMALL_MIB, m -> 0L, 100L));
        assertTrue(placed.isPresent());
        assertEquals(CPU, placed.get().pick().server().name(), "the algorithm takes the warm copy");
        assertEquals(1, decisions.size());
        assertEquals(GPU, decisions.get(0).learned().server(), "the learned policy predicts the GPU finishes first");
        assertEquals(ShadowPlacement.Agreement.DIFFER, decisions.get(0).agreement());
    }
}
