package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.provider.CallPlanner.Placement;
import ai.kubemoot.agent.provider.CallPlanner.PlacementRequest;
import ai.kubemoot.agent.provider.LearnedPlacement.Proposal;
import ai.kubemoot.agent.provider.ShadowPlacement.Agreement;
import ai.kubemoot.agent.provider.ShadowPlacement.Decision;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/** The placement seam: modes, the toggle, and shadow mode leaving placement to the algorithm. */
class PlacementPolicyTest {

    private static final ProviderState GPU = new ProviderState("gpu-a", "http://gpu-a", 1, 0, 0,
            List.of("qwen3:14b"), true, "2026-10-16T00:00:00Z", 24_000, Map.of("qwen3:14b", 12_000L));

    private static PlacementRequest request() {
        return new PlacementRequest("qwen3:14b", List.of(), List.of(GPU), m -> 12_000L, m -> 256L);
    }

    private static Placement placementOn(String model) {
        return new Placement(new Pick(GPU, new Ticket("t", GPU.name(), 512, "test", "2026-10-16T00:00:00Z")),
                model, 12_000L, List.of());
    }

    /** A policy that always returns {@code result} and counts its calls. */
    private static final class FixedPolicy implements PlacementPolicy {
        private final Optional<Placement> result;
        final AtomicInteger calls = new AtomicInteger();

        FixedPolicy(Optional<Placement> result) {
            this.result = result;
        }

        @Override
        public PlacementMode mode() {
            return PlacementMode.ALGORITHM;
        }

        @Override
        public Optional<Placement> place(PlacementRequest request) {
            calls.incrementAndGet();
            return result;
        }
    }

    /** A learned policy with a fixed proposal. */
    private static LearnedPlacement proposing(PlacementPolicy fallback, Optional<Proposal> proposal) {
        return new LearnedPlacement(fallback) {
            @Override
            public Optional<Proposal> propose(PlacementRequest request) {
                return proposal;
            }
        };
    }

    // ---- PlacementMode ----

    @Test
    void parse_acceptsEachModeInAnyCase() {
        assertEquals(Optional.of(PlacementMode.ALGORITHM), PlacementMode.parse("algorithm"));
        assertEquals(Optional.of(PlacementMode.LEARNED), PlacementMode.parse(" Learned "));
        assertEquals(Optional.of(PlacementMode.SHADOW), PlacementMode.parse("SHADOW"));
    }

    @Test
    void parse_rejectsBlankNullAndUnknown() {
        assertTrue(PlacementMode.parse(null).isEmpty());
        assertTrue(PlacementMode.parse("  ").isEmpty());
        assertTrue(PlacementMode.parse("greedy").isEmpty());
    }

    @Test
    void configName_isLowerCase() {
        assertEquals("shadow", PlacementMode.SHADOW.configName());
    }

    // ---- the toggle ----

    @Test
    void create_selectsThePolicyForEachMode() {
        var algorithm = new FixedPolicy(Optional.empty());
        assertSame(algorithm, PlacementPolicies.create("algorithm", algorithm, d -> { }));
        assertEquals(PlacementMode.LEARNED, PlacementPolicies.create("learned", algorithm, d -> { }).mode());
        assertEquals(PlacementMode.SHADOW, PlacementPolicies.create("shadow", algorithm, d -> { }).mode());
    }

    @Test
    void create_unknownOrBlankValue_fallsBackToTheAlgorithm() {
        var algorithm = new FixedPolicy(Optional.empty());
        assertSame(algorithm, PlacementPolicies.create("fastest", algorithm, d -> { }));
        assertSame(algorithm, PlacementPolicies.create("", algorithm, d -> { }));
        assertSame(algorithm, PlacementPolicies.create(null, algorithm, d -> { }));
    }

    @Test
    void callPlanner_placesThroughTheConfiguredPolicy() {
        var placed = placementOn("qwen3:14b");
        var policy = new FixedPolicy(Optional.of(placed));
        var planner = new CallPlanner(mock(ProviderSelector.class), mock(TicketManager.class), null, policy);

        assertSame(placed, planner.place(request()).orElseThrow());
        assertEquals(1, policy.calls.get());
        assertEquals(PlacementMode.ALGORITHM, planner.mode());
    }

    @Test
    void callPlanner_withoutAPolicy_usesTheAlgorithm() {
        var planner = new CallPlanner(mock(ProviderSelector.class), mock(TicketManager.class), null);
        assertEquals(PlacementMode.ALGORITHM, planner.mode());
    }

    // ---- learned ----

    @Test
    void learned_withoutHistory_hasNoOpinion_andPlacesWithTheFallback() {
        var placed = placementOn("qwen3:14b");
        var fallback = new FixedPolicy(Optional.of(placed));
        var learned = new LearnedPlacement(fallback);

        assertTrue(learned.propose(request()).isEmpty());
        assertSame(placed, learned.place(request()).orElseThrow());
        assertEquals(1, fallback.calls.get());
    }

    // ---- shadow ----

    @Test
    void shadow_placesWithTheAlgorithm_andRecordsBothChoices() {
        var placed = placementOn("qwen3:14b");
        var algorithm = new FixedPolicy(Optional.of(placed));
        var proposal = new Proposal("qwen3:8b", "gpu-b", "sooner finish");
        List<Decision> recorded = new ArrayList<>();
        var shadow = new ShadowPlacement(algorithm, proposing(algorithm, Optional.of(proposal)), recorded::add);

        assertSame(placed, shadow.place(request()).orElseThrow());
        assertEquals(1, algorithm.calls.get(), "the learned policy never places in shadow mode");
        assertEquals(1, recorded.size());
        assertEquals(proposal, recorded.get(0).learned());
        assertEquals(Agreement.DIFFER, recorded.get(0).agreement());
    }

    @Test
    void shadow_learnedFailure_neverChangesPlacement() {
        var placed = placementOn("qwen3:14b");
        var algorithm = new FixedPolicy(Optional.of(placed));
        var broken = new LearnedPlacement(algorithm) {
            @Override
            public Optional<Proposal> propose(PlacementRequest request) {
                throw new IllegalStateException("no history");
            }
        };
        List<Decision> recorded = new ArrayList<>();
        var shadow = new ShadowPlacement(algorithm, broken, recorded::add);

        assertSame(placed, shadow.place(request()).orElseThrow());
        assertEquals(Agreement.NO_OPINION, recorded.get(0).agreement());
    }

    @Test
    void shadow_recorderFailure_neverChangesPlacement() {
        var placed = placementOn("qwen3:14b");
        var algorithm = new FixedPolicy(Optional.of(placed));
        var shadow = new ShadowPlacement(algorithm, new LearnedPlacement(algorithm), d -> {
            throw new IllegalStateException("metrics down");
        });

        assertSame(placed, shadow.place(request()).orElseThrow());
    }

    @Test
    void shadow_algorithmWaits_isRecordedAsAWait() {
        var algorithm = new FixedPolicy(Optional.empty());
        List<Decision> recorded = new ArrayList<>();
        var shadow = new ShadowPlacement(algorithm, new LearnedPlacement(algorithm), recorded::add);

        assertTrue(shadow.place(request()).isEmpty());
        assertNull(recorded.get(0).actual());
    }

    @Test
    void agreement_comparesModelAndProvider() {
        var placed = placementOn("qwen3:14b");
        assertEquals(Agreement.AGREE, new Decision("qwen3:14b", placed,
                new Proposal("qwen3:14b", "gpu-a", "r")).agreement());
        assertEquals(Agreement.DIFFER, new Decision("qwen3:14b", placed,
                new Proposal("qwen3:14b", "gpu-b", "r")).agreement());
        assertEquals(Agreement.DIFFER, new Decision("qwen3:14b", placed,
                new Proposal("qwen3:8b", "gpu-a", "r")).agreement());
        assertEquals(Agreement.DIFFER, new Decision("qwen3:14b", null,
                new Proposal("qwen3:14b", "gpu-a", "r")).agreement());
        assertEquals(Agreement.NO_OPINION, new Decision("qwen3:14b", placed, null).agreement());
    }

    @Test
    void shadowRecorder_countsDecisionsByAgreement() {
        var registry = new SimpleMeterRegistry();
        var recorder = PlacementPolicies.shadowRecorder(registry);
        var placed = placementOn("qwen3:14b");

        recorder.accept(new Decision("qwen3:14b", placed, null));
        recorder.accept(new Decision("qwen3:14b", placed, new Proposal("qwen3:14b", "gpu-a", "r")));

        assertEquals(1.0, registry.counter(PlacementPolicies.METRIC_SHADOW_DECISIONS, "agreement", "no_opinion").count());
        assertEquals(1.0, registry.counter(PlacementPolicies.METRIC_SHADOW_DECISIONS, "agreement", "agree").count());
    }

    @Test
    void shadowRecorder_withoutARegistry_onlyLogs() {
        var recorder = PlacementPolicies.shadowRecorder(null);
        assertDoesNotThrow(() -> recorder.accept(new Decision("qwen3:14b", null, null)));
    }
}
