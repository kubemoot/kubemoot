package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.provider.CallPlanner.Placement;
import ai.kubemoot.agent.provider.CallPlanner.PlacementRequest;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/**
 * The learned policy places a call where it is predicted to finish soonest: a fast
 * accelerator by default, a slow provider only when the fast ones are busy enough
 * that it finishes first, and never a long prompt on a slow provider when a fast one
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

    private static ProviderState provider(String name, List<String> loaded, Map<String, ModelSpeed> speeds) {
        return new ProviderState(name, "http://" + name, 1, 0, 0, loaded, true, "2026-01-01T00:00:00Z",
                32_000, Map.of(), Map.of(SMALL, SMALL_MIB, OTHER, 9_000L), 0L, Map.of(), speeds);
    }

    private static PlacementRequest request(long promptTokens) {
        return new PlacementRequest(SMALL, List.of(OTHER), List.of(), m -> SMALL_MIB, m -> 0L, promptTokens);
    }

    /** A selector whose options are fixed and whose claims are recorded. */
    private static final class FixedSelector extends ProviderSelector {
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
        public Optional<Pick> pickAndClaimOn(String provider, Start start, String model, long fp, long kv,
                                             long promptTokens) {
            claims.add(model + "@" + provider + " " + start);
            if (refuse) return Optional.empty();
            ProviderState p = options.get(model).stream().map(Option::provider)
                    .filter(s -> s.name().equals(provider)).findFirst().orElseThrow();
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

    private static ProviderSelector.Option idle(ProviderState p, boolean warm) {
        return new ProviderSelector.Option(p, warm, true, 0.0);
    }

    private static ProviderSelector.Option busy(ProviderState p, double waitSeconds) {
        return new ProviderSelector.Option(p, true, false, waitSeconds);
    }

    private final ProviderState gpuCold = provider(GPU, List.of(), Map.of(SMALL, GPU_SPEED));
    private final ProviderState gpuWarm = provider(GPU, List.of(SMALL), Map.of(SMALL, GPU_SPEED));
    private final ProviderState cpuWarm = provider(CPU, List.of(SMALL), Map.of(SMALL, CPU_SPEED));

    @Test
    void anIdleAcceleratorBeatsAWarmCpu_evenWhenItMustLoad() {
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), idle(gpuCold, false))));
        var learned = new LearnedPlacement(new Fallback(), selector);
        Optional<LearnedPlacement.Proposal> p = learned.propose(request(500));
        assertTrue(p.isPresent());
        assertEquals(GPU, p.get().provider());
        assertTrue(p.get().reason().contains("load 2.9"), p.get().reason());
    }

    @Test
    void theCpuWinsOnlyWhenTheAcceleratorQueueIsLong() {
        var longQueue = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), busy(gpuWarm, 120))));
        var fallback = new Fallback();
        assertEquals(CPU, new LearnedPlacement(fallback, longQueue).propose(request(200)).orElseThrow().provider());
        assertTrue(new LearnedPlacement(fallback, longQueue).place(request(200)).isPresent());
        assertEquals(List.of(SMALL + "@" + CPU + " SLOT"), longQueue.claims);

        // A short queue on the accelerator still finishes first; waiting is the fallback's to decide.
        var shortQueue = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), busy(gpuWarm, 5))));
        assertEquals(GPU, new LearnedPlacement(fallback, shortQueue).propose(request(200)).orElseThrow().provider());
        new LearnedPlacement(fallback, shortQueue).place(request(200));
        assertTrue(shortQueue.claims.isEmpty(), "the learned policy never queues itself");
        assertEquals(1, fallback.calls);
    }

    @Test
    void aLongPromptNeverGoesToTheCpuWhenTheAcceleratorFinishesFirst() {
        // 8000 prompt tokens: about 49s of prompt processing on the CPU, under 1s on the GPU.
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true), busy(gpuWarm, 40))));
        assertEquals(GPU, new LearnedPlacement(new Fallback(), selector).propose(request(8_000)).orElseThrow().provider());
    }

    @Test
    void anUnmeasuredOptionLeavesTheCallToTheFallback() {
        ProviderState unmeasured = provider("new-gpu", List.of(SMALL), Map.of());
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(unmeasured, true), idle(cpuWarm, true))));
        assertTrue(new LearnedPlacement(new Fallback(), selector).propose(request(100)).isEmpty(),
                "an unmeasured provider must not lose to a measured slow one, nor win by guess");
    }

    @Test
    void itKeepsThePreferredModel_andUsesAnAlternativeOnlyWhenThePreferredIsUnmeasured() {
        ProviderState fastOther = provider(GPU, List.of(OTHER), Map.of(OTHER, new ModelSpeed(1, 99_999, 999, 0, 0)));
        var both = new FixedSelector(Map.of(SMALL, List.of(idle(cpuWarm, true)), OTHER, List.of(idle(fastOther, true))));
        assertEquals(SMALL, new LearnedPlacement(new Fallback(), both).propose(request(100)).orElseThrow().model(),
                "a faster alternative model must not replace the crew's preferred model");

        ProviderState unmeasured = provider(CPU, List.of(SMALL), Map.of());
        var preferredUnmeasured = new FixedSelector(Map.of(SMALL, List.of(idle(unmeasured, true)),
                OTHER, List.of(idle(fastOther, true))));
        assertTrue(new LearnedPlacement(new Fallback(), preferredUnmeasured).propose(request(100)).isEmpty(),
                "a placeable preferred model is not swapped for a measured alternative");

        var preferredNowhere = new FixedSelector(Map.of(OTHER, List.of(idle(fastOther, true))));
        assertEquals(OTHER, new LearnedPlacement(new Fallback(), preferredNowhere).propose(request(100)).orElseThrow().model());
    }

    @Test
    void observedSpeedReplacesCalibratedSpeedOnceTrusted() {
        ProviderState slowedGpu = provider(GPU, List.of(SMALL), Map.of(SMALL, new ModelSpeed(2_892, 15_162, 235, 3, 12)));
        var selector = new FixedSelector(Map.of(SMALL, List.of(idle(slowedGpu, true), idle(cpuWarm, true))));
        assertEquals(CPU, new LearnedPlacement(new Fallback(), selector).propose(request(100)).orElseThrow().provider());
    }

    @Test
    void placeClaimsTheChosenProviderTheWayItCanStart() {
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
        assertEquals(ProviderSelector.Start.LOAD, LearnedPlacement.startOf(idle(gpuCold, false)));
        assertEquals(ProviderSelector.Start.SLOT, LearnedPlacement.startOf(idle(gpuWarm, true)));
    }

    @Test
    void theModelSpeedPrefersObservedOnceTrusted() {
        assertEquals(235, GPU_SPEED.generateSpeed());
        assertEquals(40, new ModelSpeed(1, 2, 235, 40, 12).generateSpeed());
    }

    @Test
    void providerStateParsesModelSpeeds() throws Exception {
        ObjectMapper json = new ObjectMapper();
        var full = ProviderSelector.speedsField(json.readTree("""
                {"modelSpeeds":{"qwen3:8b":{"loadMs":2892,"promptTokensPerSecond":15162,
                 "generateTokensPerSecond":235,"observedGenerateTokensPerSecond":220,"observedSamples":9},
                 "broken":"not an object"}}"""));
        assertEquals(new ModelSpeed(2892, 15162, 235, 220, 9), full.get(SMALL));
        assertFalse(full.containsKey("broken"));
        assertTrue(ProviderSelector.speedsField(json.readTree("{}")).isEmpty());
        assertTrue(ProviderSelector.speedsField(json.readTree("{\"modelSpeeds\":[1,2]}")).isEmpty());
        var partial = ProviderSelector.speedsField(json.readTree("{\"modelSpeeds\":{\"m\":{\"generateTokensPerSecond\":5}}}"));
        assertEquals(new ModelSpeed(0, 0, 5, 0, 0), partial.get("m"));
        assertTrue(ProviderSelector.fromJson(json.readTree("{\"name\":\"p\"}")).speedOf(SMALL).isEmpty());
    }

    @Test
    void predictNeedsBothSpeeds() {
        ProviderState noPrompt = provider(GPU, List.of(SMALL), Map.of(SMALL, new ModelSpeed(1, 0, 100, 0, 0)));
        assertTrue(LearnedPlacement.predict(idle(noPrompt, true), SMALL, 10).isEmpty());
        ProviderState noGenerate = provider(GPU, List.of(SMALL), Map.of(SMALL, new ModelSpeed(1, 100, 0, 0, 0)));
        assertTrue(LearnedPlacement.predict(idle(noGenerate, true), SMALL, 10).isEmpty());
        var warm = LearnedPlacement.predict(idle(gpuWarm, true), SMALL, -5).orElseThrow();
        assertEquals(0.0, warm.load());
        assertEquals(0.0, warm.prompt(), "a negative prompt size counts as none");
    }

    // ---- the selector's side, with real tickets ----

    private static ProviderSelector realSelector(List<ProviderState> states, TicketManager tickets) {
        return new ProviderSelector(null, new ObjectMapper(), tickets, null, new OllamaDriver(null)) {
            @Override
            public List<ProviderState> readState() {
                return states;
            }
        };
    }

    private static TicketManager realTickets(InMemoryKv bucket) {
        return new TicketManager(bucket.provider(TicketManager.TICKETS_BUCKET), new ObjectMapper());
    }

    private static long ticketsOn(InMemoryKv bucket, String provider) {
        return bucket.data.keySet().stream().filter(k -> k.startsWith(TicketManager.keyPrefixFor(provider))).count();
    }

    @Test
    void optionsClaimNothing_andAColdLoadGoesToTheChosenProviderWhileACopyIsResidentAndBusyElsewhere() {
        InMemoryKv bucket = new InMemoryKv();
        TicketManager tickets = realTickets(bucket);
        ProviderSelector selector = realSelector(List.of(cpuWarm, gpuCold), tickets);
        // A call is running on the CPU's resident copy: that is not a load, and must not stop one on the GPU.
        assertTrue(selector.pickAndClaimOn(CPU, ProviderSelector.Start.SLOT, SMALL, SMALL_MIB, 0L, 100L).isPresent());
        long before = bucket.data.size();

        List<ProviderSelector.Option> options = selector.options(SMALL, SMALL_MIB, 0L, 100L);
        assertEquals(2, options.size(), options.toString());
        assertEquals(before, bucket.data.size(), "listing options must not claim");

        Optional<Pick> pick = selector.pickAndClaimOn(GPU, ProviderSelector.Start.LOAD, SMALL, SMALL_MIB, 0L, 100L);
        assertTrue(pick.isPresent(), "a busy resident copy on the CPU must not block a load on the GPU");
        assertEquals(GPU, pick.get().provider().name());
        assertEquals(1, ticketsOn(bucket, GPU));
        assertTrue(selector.pickAndClaimOn(CPU, ProviderSelector.Start.LOAD, SMALL, SMALL_MIB, 0L, 100L).isEmpty(),
                "a cold load is not a way to start where the model is resident");
    }

    @Test
    void aColdLoadYieldsToALoadAlreadyInProgressOnAnotherProvider() {
        ProviderState otherGpuCold = provider("ollama-rig1", List.of(), Map.of(SMALL, GPU_SPEED));
        InMemoryKv bucket = new InMemoryKv();
        ProviderSelector selector = realSelector(List.of(gpuCold, otherGpuCold), realTickets(bucket));
        assertTrue(selector.pickAndClaimOn(GPU, ProviderSelector.Start.LOAD, SMALL, SMALL_MIB, 0L, 100L).isPresent());
        assertTrue(selector.pickAndClaimOn("ollama-rig1", ProviderSelector.Start.LOAD, SMALL, SMALL_MIB, 0L, 100L).isEmpty(),
                "a second concurrent load of the same model must converge on the first");
        assertEquals(0, ticketsOn(bucket, "ollama-rig1"));
    }

    @Test
    void aSlotClaimOnAFullCopyIsRefused() {
        InMemoryKv bucket = new InMemoryKv();
        ProviderSelector selector = realSelector(List.of(gpuWarm), realTickets(bucket));
        assertTrue(selector.pickAndClaimOn(GPU, ProviderSelector.Start.SLOT, SMALL, SMALL_MIB, 0L, 100L).isPresent());
        assertTrue(selector.pickAndClaimOn(GPU, ProviderSelector.Start.SLOT, SMALL, SMALL_MIB, 0L, 100L).isEmpty(),
                "the provider runs one call at a time");
        assertEquals(1, ticketsOn(bucket, GPU));
        assertFalse(selector.options(SMALL, SMALL_MIB, 0L, 100L).get(0).startsNow());
    }

    @Test
    void shadowRecordsADifferenceBetweenTheAlgorithmAndTheLearnedPolicy() {
        InMemoryKv bucket = new InMemoryKv();
        TicketManager tickets = realTickets(bucket);
        ProviderSelector selector = realSelector(List.of(cpuWarm, gpuCold), tickets);
        AlgorithmPlacement algorithm = new AlgorithmPlacement(selector, tickets, null);
        List<ShadowPlacement.Decision> decisions = new ArrayList<>();
        PlacementPolicy shadow = PlacementPolicies.create("shadow", algorithm, selector, decisions::add);

        Optional<Placement> placed = shadow.place(new PlacementRequest(SMALL, List.of(), List.of(cpuWarm, gpuCold),
                m -> SMALL_MIB, m -> 0L, 100L));
        assertTrue(placed.isPresent());
        assertEquals(CPU, placed.get().pick().provider().name(), "the algorithm takes the warm copy");
        assertEquals(1, decisions.size());
        assertEquals(GPU, decisions.get(0).learned().provider(), "the learned policy predicts the GPU finishes first");
        assertEquals(ShadowPlacement.Agreement.DIFFER, decisions.get(0).agreement());
    }
}
