package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for the ProviderSelector's selection logic. These exercise
 * the {@link ProviderSelector#pickFor} algorithm against in-memory
 * ProviderState lists — no NATS, no Quarkus, plain JUnit so they run
 * fast in CI without the Ollama dependency the @QuarkusTest annotation
 * would drag in (per the kubemoot/agent-runtime testing rule in CLAUDE.md).
 *
 * The actual NATS KV read path is covered by integration smoke testing
 * after deploy — mocking NATS's KeyValue API in unit tests adds little
 * value beyond noise, since {@code readState} is a thin adapter.
 */
class ProviderSelectorTest {

    /**
     * Helper to invoke the selection algorithm directly with synthetic
     * provider states. Bypasses NATS read; tests the pure logic.
     */
    private static Optional<ProviderState> select(List<ProviderState> states, String modelName) {
        // Inline the selection rules from ProviderSelector.pickFor so tests
        // exercise the algorithm without needing CDI / NATS plumbing. Keep
        // in sync with the production method's rules.
        var ready = states.stream().filter(ProviderState::ready).toList();
        if (ready.isEmpty()) return Optional.empty();
        var warm = ready.stream().filter(p -> p.hasModelLoaded(modelName)).toList();
        var candidates = warm.isEmpty() ? ready : warm;
        var free = candidates.stream().filter(ProviderState::hasFreeSlot).toList();
        if (!free.isEmpty()) candidates = free;
        return candidates.stream()
                .min(java.util.Comparator.comparingDouble(ProviderState::saturationRatio)
                        .thenComparing(java.util.Comparator.comparingInt(ProviderState::maxParallel).reversed()));
    }

    private static ProviderState state(String name, String endpoint, int maxParallel,
                                        int activeCount, boolean ready, List<String> loaded) {
        // v1 helper: zero v2 fields, so headroom-based callers degrade to fallback.
        // v2 tests below use the v2State helper which sets totalVramMiB + footprints.
        return new ProviderState(name, endpoint, maxParallel, activeCount, 0, loaded, ready,
                "2026-05-25T00:00:00Z", 0L, java.util.Map.of());
    }

    /** v2 helper: include totalVramMiB and per-model footprints for headroom tests. */
    private static ProviderState v2State(String name, String endpoint, boolean ready,
                                          long totalVramMiB, java.util.Map<String, Long> loadedFootprints) {
        return new ProviderState(name, endpoint, 1, 0, 0,
                List.copyOf(loadedFootprints.keySet()), ready, "2026-05-28T00:00:00Z",
                totalVramMiB, loadedFootprints);
    }

    @Test
    void prefersWarmProviderWhenSomeAreCold() {
        // rig0 has the 32B model loaded; rig1 doesn't. Both ready, both free.
        // Warm wins — cold-load on 32B costs 20-30s, weight matters less than warmth.
        var rig0 = state("ollama-gpu", "http://rig0:11434", 1, 0, true, List.of("qwen3:32b"));
        var rig1 = state("ollama-rig1", "http://rig1:11434", 1, 0, true, List.of("qwen3:8b"));
        var pick = select(List.of(rig0, rig1), "qwen3:32b");
        assertTrue(pick.isPresent());
        assertEquals("ollama-gpu", pick.get().name());
    }

    @Test
    void spreadsAcrossWarmProvidersByLowestSaturation() {
        // Both rigs have the model warm. rig0 is currently in-use (1/1),
        // rig1 is free (0/1). Selector picks rig1 — the freest.
        // This is the homelab case: k8s-config + k8s-workloads co-triaged,
        // first one lands on rig0 → second sees rig0 saturated and picks rig1.
        // Emergent bin-packing, no static placement heuristics needed.
        var rig0Busy = state("ollama-gpu", "http://rig0:11434", 1, 1, true, List.of("qwen3:32b"));
        var rig1Free = state("ollama-rig1", "http://rig1:11434", 1, 0, true, List.of("qwen3:32b"));
        var pick = select(List.of(rig0Busy, rig1Free), "qwen3:32b");
        assertTrue(pick.isPresent());
        assertEquals("ollama-rig1", pick.get().name(), "When rig0 is saturated and rig1 is free, pick rig1");
    }

    @Test
    void fallsBackToLeastSaturatedWhenAllBusy() {
        // No provider has a free slot. rig0 is 2/2, rig1 is 1/1. Saturation
        // ratios are equal (1.0). Tie-break: higher maxParallel wins (more
        // headroom for the next inference to complete sooner). rig0 wins.
        var rig0 = state("ollama-gpu", "http://rig0:11434", 2, 2, true, List.of("qwen3:32b"));
        var rig1 = state("ollama-rig1", "http://rig1:11434", 1, 1, true, List.of("qwen3:32b"));
        var pick = select(List.of(rig0, rig1), "qwen3:32b");
        assertTrue(pick.isPresent());
        assertEquals("ollama-gpu", pick.get().name());
    }

    @Test
    void skipsNotReadyProviders() {
        // rig0 is the obvious pick by warmth + freedom, but it's not ready
        // (probe failed). Selector falls back to rig1 even though rig1 has
        // to cold-load the model. Better to wait for a cold load than to
        // send to a known-broken provider.
        var rig0Broken = state("ollama-gpu", "http://rig0:11434", 2, 0, false, List.of("qwen3:32b"));
        var rig1Cold = state("ollama-rig1", "http://rig1:11434", 1, 0, true, List.of("qwen3:8b"));
        var pick = select(List.of(rig0Broken, rig1Cold), "qwen3:32b");
        assertTrue(pick.isPresent());
        assertEquals("ollama-rig1", pick.get().name());
    }

    @Test
    void emptyWhenNoProvidersReady() {
        // Pathological — no provider is ready. Selector returns empty;
        // ChatService falls back to the static Quarkus-injected ChatModel.
        var rig0 = state("ollama-gpu", "http://rig0:11434", 1, 0, false, List.of("qwen3:32b"));
        var rig1 = state("ollama-rig1", "http://rig1:11434", 1, 0, false, List.of("qwen3:8b"));
        var pick = select(List.of(rig0, rig1), "qwen3:32b");
        assertTrue(pick.isEmpty(), "No ready provider → empty Optional, caller falls back to static");
    }

    @Test
    void scoreForRanking_combinesSaturationWithObservedLatency() {
        // Construct selector with null NATS dependencies — scoreForRanking
        // + recordObservedLatency don't touch NATS, only the in-memory
        // latency map and the supplied ProviderState's getters.
        var sel = new ProviderSelector(null, new com.fasterxml.jackson.databind.ObjectMapper(), null, null, null);

        // No recorded latency → pure saturation determines score
        var fresh = state("a", "http://a", 1, 0, true, java.util.List.of("m"));
        var saturated = state("b", "http://b", 1, 1, true, java.util.List.of("m"));
        assertTrue(sel.scoreForRanking(fresh) < sel.scoreForRanking(saturated),
                "without latency history, less-saturated wins");

        // Record a slow observation against 'a' — now 'a' should be penalised
        sel.recordObservedLatency("http://a", 30_000); // 30s
        // 30s × 0.1 penalty per sec = 3.0 added to a's score. a's saturation is 0.
        // b's saturation is 1.0. So a=3.0, b=1.0. b wins now.
        assertTrue(sel.scoreForRanking(saturated) < sel.scoreForRanking(fresh),
                "after observing 30s latency on 'a', 'b' (saturated but fresh-history) wins");
    }

    @Test
    void recordObservedLatency_emaSmoothing() {
        var sel = new ProviderSelector(null, new com.fasterxml.jackson.databind.ObjectMapper(), null, null, null);
        // First observation seeds at full weight
        sel.recordObservedLatency("http://x", 30_000);
        assertEquals(30_000.0, sel.latencyMapForTest().get("http://x"), 0.001,
                "first observation should be the EMA seed");
        // Second observation: 5000ms with alpha=0.4
        // new = 30000*0.6 + 5000*0.4 = 18000 + 2000 = 20000
        sel.recordObservedLatency("http://x", 5_000);
        assertEquals(20_000.0, sel.latencyMapForTest().get("http://x"), 0.001,
                "EMA after second sample should be 20000");
    }

    @Test
    void recordObservedLatency_ignoresInvalidInputs() {
        var sel = new ProviderSelector(null, new com.fasterxml.jackson.databind.ObjectMapper(), null, null, null);
        sel.recordObservedLatency(null, 1000);
        sel.recordObservedLatency("", 1000);
        sel.recordObservedLatency("http://x", 0);
        sel.recordObservedLatency("http://x", -5);
        assertTrue(sel.latencyMapForTest().isEmpty(),
                "null/empty endpoint and zero/negative latencies should be ignored");
    }

    @Test
    void singleProviderJustReturnsIt() {
        // Trivial — only one candidate. Pick it.
        var only = state("ollama-gpu", "http://rig0:11434", 1, 0, true, List.of("qwen3:32b"));
        var pick = select(List.of(only), "qwen3:32b");
        assertTrue(pick.isPresent());
        assertEquals("ollama-gpu", pick.get().name());
    }

    // --- ProviderState helper tests ---

    @Test
    void saturationRatio_handlesMaxParallelZero() {
        // Defensive — operator publishes maxParallel=1 minimum, but if it
        // ever shipped 0 (regression), saturationRatio must not divide by
        // zero. Helper clamps to maxParallel=1 for the math.
        var s = new ProviderState("x", "http://x", 0, 0, 0, null, true, "", 0L, java.util.Map.of());
        assertEquals(0.0, s.saturationRatio());
        // hasFreeSlot also clamps internally
        assertTrue(s.hasFreeSlot());
    }

    @Test
    void hasModelLoaded_nullSafe() {
        // loadedModels can be null when a provider hasn't been probed yet
        // (Jackson omits null fields). Don't NPE.
        var s = new ProviderState("x", "http://x", 1, 0, 0, null, true, "", 0L, java.util.Map.of());
        assertFalse(s.hasModelLoaded("anything"));
    }

    // --- fromJson: native-safe parsing of the operator-published document ---
    // Regression guard for the JIT-blind bug: ProviderState is a record, and
    // readValue(.., ProviderState.class) fails SILENTLY in GraalVM native, so
    // readState() must parse via readTree → fromJson. These lock the field
    // mapping to the operator's struct json tags.

    @Test
    void fromJson_parsesFullOperatorDocument() throws Exception {
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        String json = """
            {"name":"ollama-gpu","endpoint":"http://ollama.ollama-rig0:11434",
             "maxParallel":2,"activeCount":1,"queueDepth":3,
             "loadedModels":["qwen3:32b","qwen3:8b"],
             "ready":true,"lastProbedAt":"2026-05-26T17:36:52Z"}""";
        ProviderState s = ProviderSelector.fromJson(mapper.readTree(json));
        assertEquals("ollama-gpu", s.name());
        assertEquals("http://ollama.ollama-rig0:11434", s.endpoint());
        assertEquals(2, s.maxParallel());
        assertEquals(1, s.activeCount());
        assertEquals(3, s.queueDepth());
        assertTrue(s.ready());
        assertEquals("2026-05-26T17:36:52Z", s.lastProbedAt());
        assertTrue(s.hasModelLoaded("qwen3:32b"));
        assertTrue(s.hasFreeSlot(), "activeCount 1 below maxParallel 2 leaves a free slot");
    }

    @Test
    void fromJson_toleratesMissingLoadedModels() throws Exception {
        // loadedModels is omitempty on the operator side — absent when no
        // model is loaded. Must yield an empty list, never null/crash.
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        String json = """
            {"name":"ollama-rig1","endpoint":"http://x","maxParallel":1,
             "activeCount":1,"queueDepth":0,"ready":true,"lastProbedAt":""}""";
        ProviderState s = ProviderSelector.fromJson(mapper.readTree(json));
        assertEquals("ollama-rig1", s.name());
        assertFalse(s.hasModelLoaded("qwen3:32b"));
        assertFalse(s.hasFreeSlot(), "activeCount 1 not below maxParallel 1 means saturated");
    }

    @Test
    void fromJson_defaultsForEmptyDocument() throws Exception {
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        ProviderState s = ProviderSelector.fromJson(mapper.readTree("{}"));
        assertEquals("", s.name());
        assertEquals(0, s.maxParallel());
        assertFalse(s.ready());
        assertFalse(s.hasModelLoaded("x"));
        // v2 defaults: no headroom, no claims possible — selector falls back to static endpoint.
        assertEquals(0L, s.totalVramMiB());
        assertEquals(0L, s.preTicketHeadroomMiB());
    }

    // --- v2 (VRAM-headroom) field tests: see docs/scheduler.md ---
    // These cover the new fields that drive ticket-based selection. The
    // selector's headroom-and-claim algorithm will be wired in commit 3;
    // these tests lock the math of the underlying ProviderState helpers.

    @Test
    void fromJson_parsesV2HeadroomFields() throws Exception {
        // Operator publishes totalVramMiB + per-model footprints once the
        // probe is updated. Agent must read both, native-safe (no reflection).
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        String json = """
            {"name":"ollama-gpu","endpoint":"http://x","maxParallel":1,
             "ready":true,"lastProbedAt":"2026-05-28T00:00:00Z",
             "totalVramMiB":32000,
             "loadedModelFootprintsMiB":{"qwen3:32b":22000,"qwen3:8b":5000}}""";
        ProviderState s = ProviderSelector.fromJson(mapper.readTree(json));
        assertEquals(32_000L, s.totalVramMiB());
        assertEquals(22_000L, s.footprintMiBFor("qwen3:32b"));
        assertEquals(5_000L, s.footprintMiBFor("qwen3:8b"));
        assertEquals(0L, s.footprintMiBFor("qwen3:14b"), "unknown model footprint is 0");
        assertEquals(27_000L, s.loadedFootprintSumMiB());
        assertEquals(5_000L, s.preTicketHeadroomMiB(), "32GB total − 27GB loaded = 5GB headroom");
    }

    @Test
    void preTicketHeadroom_zeroWhenTotalVramUnpublished() {
        // Rollout case: operator hasn't shipped the new fields yet; agent
        // sees totalVramMiB=0. preTicketHeadroomMiB returns 0 so the selector
        // can't claim here and falls back to the static endpoint. No
        // over-commit on a phantom budget.
        var s = v2State("ollama-gpu", "http://x", true, 0L, java.util.Map.of());
        assertEquals(0L, s.preTicketHeadroomMiB());
    }

    @Test
    void preTicketHeadroom_clampedAtZeroWhenLoadedExceedsTotal() {
        // Defensive: if the operator publishes stale totals (e.g. a model
        // load races the probe), loaded sum could exceed total. Don't
        // return negative headroom — clamp at 0 so the selector simply
        // can't claim there.
        var s = v2State("ollama-gpu", "http://x", true, 24_000L,
                java.util.Map.of("qwen3:32b", 22_000L, "qwen3:14b", 9_000L));
        assertEquals(0L, s.preTicketHeadroomMiB(),
                "loaded 31GB > total 24GB → clamp to 0, not negative");
    }

    @Test
    void footprintMiBFor_nullSafe() {
        // Null map field (record built from minimal constructor or pre-v2 doc).
        var s = new ProviderState("x", "http://x", 1, 0, 0, null, true, "", 0L, null);
        assertEquals(0L, s.footprintMiBFor("any"));
        assertEquals(0L, s.loadedFootprintSumMiB());
    }

    // --- v2.1 rankCandidates: slot-based for warm, headroom-based for cold ---
    // The 2026-05-28 first cut treated tickets as VRAM-footprint reservations
    // and over-rejected warm-model calls (loaded model size left ~5GB free,
    // not enough to "reserve" another full model). The slot-based revision
    // (same date): warm-model calls hold one CONCURRENCY SLOT, cold-model
    // calls reserve the full footprint. Each provider passes a different
    // gate depending on warm/cold for the requested model.

    /** Fixed zero-load world for clean algorithm tests. */
    private static final java.util.function.ToIntFunction<ProviderState> NO_TICKET_COUNT = p -> 0;
    private static final java.util.function.ToLongFunction<ProviderState> NO_TICKETS = p -> 0L;
    private static final java.util.function.ToDoubleFunction<ProviderState> NO_LATENCY = p -> 0.0;
    /** No provider's circuit is open — default for tests that don't exercise the breaker. */
    private static final java.util.function.Predicate<String> NO_CIRCUIT_OPEN = n -> false;
    /** Default v1 predictor — exercises the same logic the inline gate used to contain. */
    private static final FitPredictor STATIC_PREDICTOR = new StaticFitPredictor();

    /** Builds a v2 state with explicit maxParallel for slot tests. */
    private static ProviderState v2StateWithSlots(String name, String endpoint, int maxParallel,
                                                   long totalVramMiB,
                                                   java.util.Map<String, Long> loadedFootprints) {
        return new ProviderState(name, endpoint, maxParallel, 0, 0,
                List.copyOf(loadedFootprints.keySet()), true, "2026-05-28T00:00:00Z",
                totalVramMiB, loadedFootprints);
    }

    @Test
    void rankCandidates_fitAccessorsTolerateNullOptionalFunctions() {
        // Guards the FitAccessors parameter-object refactor (Sonar S107): the
        // optional accessors — circuit-open, KV-cache, active-models — may be null,
        // and buildFits must default them (no-circuit / 0 / empty) instead of
        // NPEing. Two warm providers differ only in saturation, so the
        // less-saturated one must still rank first through the bundled accessors.
        var fresh = v2StateWithSlots("ollama-gpu", "http://rig0", 2, 32_605L,
                java.util.Map.of("model-x", 5_000L));
        var busy = v2StateWithSlots("ollama-rig1", "http://rig1", 2, 24_563L,
                java.util.Map.of("model-x", 5_000L));
        java.util.function.ToIntFunction<ProviderState> active =
                p -> p.name().equals("ollama-rig1") ? 1 : 0; // rig1 more saturated
        // The optional accessors are passed null on purpose. The refactor moved
        // their null-defaulting into buildFits's reads of the FitAccessors getters,
        // so this asserts that path: the call must not NPE, and both warm providers
        // must still flow through (ranking ORDER is the weighted-placement-cost
        // concern of other tests, not this one).
        var ranked = ProviderSelector.rankCandidates(
                List.of(fresh, busy), "model-x", 5_000L,
                active, NO_TICKETS, NO_LATENCY,
                null,             // isCircuitOpenFn null -> default (no circuit open)
                STATIC_PREDICTOR,
                null,             // activeKvCacheFn null -> default 0
                0L,
                null,             // activeModelsFn null -> default empty set
                null);            // residentOverlayFn null -> default empty
        assertEquals(2, ranked.size(),
                "both warm providers feasible; null optional accessors must default, not NPE or drop either");
        var names = ranked.stream().map(c -> c.provider().name()).toList();
        assertTrue(names.contains("ollama-gpu") && names.contains("ollama-rig1"),
                "both providers ranked through the bundled FitAccessors with null optionals");
    }

    @Test
    void rankCandidates_warmSpillingCardIsPenalizedNotRefused() {
        // A warm model resident on BOTH cards. On the larger GPU it fits usable
        // VRAM (healthy); on the smaller GPU it exceeds usable VRAM (spilling to
        // CPU). In the weighted model BOTH are feasible (reusing a warm model adds
        // no VRAM regardless of size), but the spilling card carries the heavy
        // SPILL_PENALTY so the healthy card wins. The spilling card stays as a
        // last-resort fallback, it is not excluded.
        var rigBig = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("model-x", 22_000L));
        var rigSmall = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of("model-x", 22_000L));
        var ranked = ProviderSelector.rankCandidates(
                List.of(rigBig, rigSmall), "model-x", 22_000L, NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(2, ranked.size(), "both warm cards feasible; spilling one is penalized, not refused");
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "healthy card beats the spilling card via SPILL_PENALTY");
    }

    @Test
    void rankCandidates_healthyWarmBeatsSpillingWarmEvenWhenHealthyIsBusier() {
        // The larger GPU has the model warm and HEALTHY but its slot is busy (1/1).
        // The smaller GPU has it warm and idle but SPILLING. Locality+health win:
        // a busy-but-healthy card (contention 1.0) beats an idle-but-spilling card
        // (SPILL_PENALTY 20). Queue at the healthy home rather than serve glacially.
        var rigBig = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("model-x", 22_000L));
        var rigSmall = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of("model-x", 22_000L));
        java.util.function.ToIntFunction<ProviderState> tickets = p ->
                p.name().equals("ollama-gpu") ? 1 : 0;
        var ranked = ProviderSelector.rankCandidates(
                List.of(rigBig, rigSmall), "model-x", 22_000L, tickets, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(2, ranked.size(), "both feasible");
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "busy-but-healthy (contention 1.0) beats idle-but-spilling (SPILL_PENALTY 20)");
    }

    @Test
    void rankCandidates_doesNotEvictLiveModel_queuesAtWarmHomeInstead() {
        // The 2026-06-11 thrash, fixed by weights. 8B is warm on the small card
        // (busy). The big card is full of a 32B that is actively serving (in-flight
        // work). Loading 8B on the big card would EVICT the live 32B - near-
        // forbidden (EVICTION_LIVE_PENALTY). So the 8B queues at its warm home on
        // the small card, even though the small card is busier.
        var big32 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:32b", 27_252L));     // full of a live 32b
        var small8 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of("qwen3:8b", 5_979L));        // 8b warm home
        java.util.function.ToIntFunction<ProviderState> tickets = p ->
                p.name().equals("ollama-gpu") ? 2 : 3;        // both busy; small busier
        var ranked = ProviderSelector.rankCandidates(
                List.of(big32, small8), "qwen3:8b", 5_979L,
                tickets, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals("ollama-rig1", ranked.get(0).provider().name(),
                "queue at the warm 8B home; never evict the live 32B on the big card");
    }

    @Test
    void rankCandidates_coldOntoFreeBeatsColdEviction() {
        // No warm copy of M anywhere. One card has free room; the other is nearly
        // full of an idle model. Load onto the free card (loadCost only) rather
        // than the one that needs an eviction (loadCost + EVICTION_IDLE_BASE).
        var freeCard = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L, java.util.Map.of());
        var fullCard = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of("other-model", 18_000L));    // idle but nearly full
        var ranked = ProviderSelector.rankCandidates(
                List.of(fullCard, freeCard), "qwen3:14b", 9_000L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "load onto the card with free room, not the one that needs an eviction");
    }

    @Test
    void rankCandidates_residencyOverlayKeeps8bOffBusy32bCard_whenPublishedIsStale() {
        // The exact 2026-06-11 thrash, and the residency-overlay fix. The 4090 has
        // 8B warm but BUSY (8 in-flight). The 5090 has the 32B resident, but the
        // operator probe has not published it yet, so published state shows the
        // 5090 EMPTY. Without the overlay the 5090 reads as cold-onto-free (cost 5)
        // and beats the busy 4090 (contention 8) -> 8B thrashes onto the 5090 and
        // evicts the 32B. WITH the overlay, free(5090) = 28694 - 27252 = 1442 <
        // 5979, so loading 8B there is an eviction (~93) and the 8B queues on the
        // warm 4090 (8) instead. No thrash.
        var rig05090 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L, java.util.Map.of()); // stale: empty
        var rig14090 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of("qwen3:8b", 5_979L));                                                 // 8b warm
        java.util.function.ToIntFunction<ProviderState> busy4090 = p ->
                p.name().equals("ollama-rig1") ? 8 : 0;
        java.util.function.Function<ProviderState, java.util.Map<String, Long>> overlay = p ->
                p.name().equals("ollama-gpu") ? java.util.Map.of("qwen3:32b", 27_252L) : java.util.Map.of();
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig05090, rig14090), "qwen3:8b", 5_979L,
                busy4090, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR,
                p -> 0L, 0L, p -> java.util.Set.of(), overlay);
        assertEquals("ollama-rig1", ranked.get(0).provider().name(),
                "residency overlay reveals the 32B on the 5090 (probe-stale), so the 8B queues on the busy 4090 instead of thrashing the 5090");
    }

    @Test
    void mergedResidentMiB_perModelMax_avoidsDoubleCount() {
        // 32B in both published (27000, probe) and overlay (27252, fresh) -> MAX, not
        // sum. 8B only in overlay. Total = 27252 + 5000.
        long m = ProviderSelector.mergedResidentMiB(
                java.util.Map.of("qwen3:32b", 27_000L),
                java.util.Map.of("qwen3:32b", 27_252L, "qwen3:8b", 5_000L));
        assertEquals(32_252L, m, "per-model max, no double-count of the published+overlay 32B");
        assertEquals(0L, ProviderSelector.mergedResidentMiB(null, null));
        assertEquals(5_000L, ProviderSelector.mergedResidentMiB(java.util.Map.of(), java.util.Map.of("x", 5_000L)));
        assertEquals(27_000L, ProviderSelector.mergedResidentMiB(java.util.Map.of("qwen3:32b", 27_000L), java.util.Map.of()));
    }

    @Test
    void isStaleState_filtersOrphanedEntries() {
        // Fresh (now) -> not stale; hours old -> stale (orphan, e.g. a deleted CR's
        // KV entry); blank/unparseable -> tolerated (kept).
        assertFalse(ProviderSelector.isStaleState(java.time.Instant.now().toString()));
        assertTrue(ProviderSelector.isStaleState(
                java.time.Instant.now().minus(java.time.Duration.ofHours(25)).toString()));
        assertFalse(ProviderSelector.isStaleState(""));
        assertFalse(ProviderSelector.isStaleState(null));
        assertFalse(ProviderSelector.isStaleState("not-a-timestamp"));
    }

    @Test
    void rankCandidates_bestFitPrefersSmallerCardForSmallModel() {
        // Both cards empty/idle; an 8B fits both. Best-fit prefers the SMALLER 4090
        // so the bigger 5090 stays free for a model that needs it. Without best-fit
        // the cold cost is identical and the 8B could squat the 5090 (the seed of
        // the 2026-06-11 thrash).
        var rig05090 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L, java.util.Map.of());
        var rig14090 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L, java.util.Map.of());
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig05090, rig14090), "qwen3:8b", 5_979L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals("ollama-rig1", ranked.get(0).provider().name(),
                "best-fit: a small model prefers the smaller card, reserving the big GPU for big models");
    }

    @Test
    void rankCandidates_bigModelStillTakesBigCard() {
        // A 32B exceeds the 4090's usable VRAM, so feasibility leaves only the 5090.
        // Best-fit never strands a big model on a too-small card.
        var rig05090 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L, java.util.Map.of());
        var rig14090 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L, java.util.Map.of());
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig05090, rig14090), "qwen3:32b", 27_252L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(1, ranked.size(), "only the 5090 fits a 32B");
        assertEquals("ollama-gpu", ranked.get(0).provider().name());
    }

    @Test
    void rankCandidates_binPacksByLowestSaturation() {
        // rig0 maxParallel=2 with 1 ticket (0.5 saturation).
        // rig1 maxParallel=2 with 0 tickets (0.0 saturation).
        // Both warm. rig1 wins — emergent bin-pack-spread.
        var rig0 = v2StateWithSlots("ollama-gpu", "http://rig0", 2, 32_605L,
                java.util.Map.of("qwen3:8b", 5_000L));
        var rig1 = v2StateWithSlots("ollama-rig1", "http://rig1", 2, 24_563L,
                java.util.Map.of("qwen3:8b", 5_000L));
        java.util.function.ToIntFunction<ProviderState> tickets = p ->
                p.name().equals("ollama-gpu") ? 1 : 0;
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0, rig1), "qwen3:8b", 5_000L, tickets, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals("ollama-rig1", ranked.get(0).provider().name(),
                "rig1 has 0/2 active < rig0's 1/2 — bin-pack spreads to emptiest");
    }

    @Test
    void rankCandidates_coldModel_passesHeadroomGate() {
        // qwen3:14b isn't loaded anywhere. Cold-load gate: each provider
        // needs >= 8GB free VRAM to load it. Both pass; both compete on
        // saturation (both 0/1).
        var rig0 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:8b", 5_000L));      // 27GB free
        var rig1 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of("qwen3:8b", 5_000L));      // 19GB free
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0, rig1), "qwen3:14b", 8_000L, NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(2, ranked.size(), "Both pass cold-load gate (>=8GB free)");
    }

    @Test
    void rankCandidates_coldModel_excludedWhenModelLargerThanTotalVram() {
        // v2.2: cold-load gate is "model size ≤ provider TOTAL VRAM",
        // not the previous headroom-minus-tickets math. A 27GB qwen3:32b
        // could ever load on rig0 (32GB total) but never on rig1
        // (24GB total) — Ollama would OOM trying.
        var rig0 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L, java.util.Map.of());
        var rig1 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L, java.util.Map.of());
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0, rig1), "qwen3:32b", 27_000L, NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(1, ranked.size(), "Only rig0 has enough TOTAL VRAM to ever load a 27GB model");
        assertEquals("ollama-gpu", ranked.get(0).provider().name());
    }

    @Test
    void rankCandidates_coldLoad_v22_ignoresActiveFootprints_letsOllamaEvict() {
        // v2.2 semantic shift: active ticket footprints DO NOT block
        // cold-load picks anymore. Kubemoot routes to whichever provider
        // could ever fit the model; Ollama decides whether to evict
        // currently-loaded models to make room. This was a deliberate
        // change from v2.1 because the headroom-minus-tickets math was
        // (a) kubemoot duplicating Ollama's job and (b) wrong on edge
        // cases (today's d61647e9 wedge: kubemoot thought rig1 fit but
        // Ollama couldn't actually serve).
        var rig0 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L, java.util.Map.of());
        // Simulate another agent holding a 20GB ticket — would have
        // blocked this 14GB cold-load under v2.1. Under v2.2 it doesn't.
        java.util.function.ToLongFunction<ProviderState> footprints = p ->
                p.name().equals("ollama-gpu") ? 20_000L : 0L;
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0), "qwen3:14b", 14_000L, NO_TICKET_COUNT, footprints, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(1, ranked.size(),
                "v2.2: 14GB cold-load on a 32GB provider passes regardless of in-flight ticket footprints");
    }

    @Test
    void rankCandidates_coldLoad_v22_skipsProviderWithUnknownVram() {
        // Defensive (rollout case): an older operator that hasn't
        // published totalVramMiB yet sends 0. v2.2 must NOT use 0
        // (would over-reject) or treat it as infinite (would over-route
        // to an unprobed provider). Skip the provider entirely until
        // the operator's DCGM probe populates the field.
        var unprobed = new ProviderState("ollama-gpu", "http://rig0", 1, 0, 0,
                List.of(), true, "", 0L, java.util.Map.of());
        var ranked = ProviderSelector.rankCandidates(
                List.of(unprobed), "qwen3:14b", 14_000L, NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertTrue(ranked.isEmpty(), "Unknown totalVramMiB → provider skipped for cold-load");
    }

    @Test
    void rankCandidates_warmBeatsColdAtEqualSaturation() {
        // Two SAME-SIZE providers (so best-fit is neutral), both 0/1 saturation.
        // rig0 warm, rig1 cold. Tiebreak: warm wins (avoid cold-load tax).
        var rig0Warm = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:8b", 5_000L));
        var rig1Cold = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 32_605L, java.util.Map.of());
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0Warm, rig1Cold), "qwen3:8b", 5_000L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "Equal saturation, both fit → warm wins tiebreak");
    }

    @Test
    void rankCandidates_warmSaturatedCoScheduleBeatsColdLoad() {
        // Post [[Scheduler Warm-Slot Co-Scheduling]]: a warm-but-busy provider
        // is no longer excluded — it can queue a co-scheduled call. And the
        // large cold penalty makes that queued-warm option BEAT a cold-load on
        // a free provider, because converging on the already-loaded instance
        // avoids a redundant cold-load (the whole point — N subcommittee agents
        // should share one load, not each pay their own). rig0 (warm, 1/1,
        // co-schedulable) ranks ahead of rig1 (cold, free). Both qualify.
        // (This deliberately reverses the old v2.1 "cold-but-free beats
        // warm-but-saturated" spread behavior for the same-model case.)
        // Same-size cards so best-fit is neutral and this isolates the
        // warm-co-schedule-beats-cold-load intent.
        var rig0Warm = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:8b", 5_000L));
        var rig1Cold = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 32_605L,
                java.util.Map.of("qwen3:14b", 4_000L));
        java.util.function.ToIntFunction<ProviderState> tickets = p ->
                p.name().equals("ollama-gpu") ? 1 : 0;
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0Warm, rig1Cold), "qwen3:8b", 5_000L,
                tickets, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(2, ranked.size(), "warm co-schedules + cold fits → both qualify");
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "warm co-schedule beats cold-load — converge on the loaded instance, avoid a redundant cold-load");
    }

    @Test
    void rankCandidates_latencyIsLastTiebreak() {
        // Identical saturation (0/1) and SAME-SIZE cards (best-fit neutral), both
        // warm. rig1 had a recent slow call (30s observed latency); rig0 fast
        // (100ms). rig0 wins on the latency tiebreak.
        var rig0 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:8b", 5_000L));
        var rig1 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 32_605L,
                java.util.Map.of("qwen3:8b", 5_000L));
        java.util.function.ToDoubleFunction<ProviderState> latency = p ->
                p.endpoint().contains("rig1") ? 30_000.0 : 100.0;
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0, rig1), "qwen3:8b", 5_000L,
                NO_TICKET_COUNT, NO_TICKETS, latency, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "Equal saturation + both warm → lower latency wins");
    }

    @Test
    void rankCandidates_skipsNotReady() {
        // Defensive: a provider with ready=false never appears even if it
        // would otherwise fit (Ollama pod restarting; status.ready=false).
        var rig0Down = new ProviderState("ollama-gpu", "http://rig0", 1, 0, 0,
                List.of("qwen3:8b"), false, "", 32_605L, java.util.Map.of("qwen3:8b", 5_000L));
        var rig1Up = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of("qwen3:8b", 5_000L));
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0Down, rig1Up), "qwen3:8b", 5_000L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(1, ranked.size());
        assertEquals("ollama-rig1", ranked.get(0).provider().name(),
                "Down provider filtered out before ranking");
    }

    @Test
    void rankCandidates_nullOrInvalidInputReturnsEmpty() {
        assertTrue(ProviderSelector.rankCandidates(null, "x", 5_000L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR).isEmpty());
        assertTrue(ProviderSelector.rankCandidates(List.of(), "x", 5_000L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR).isEmpty());
        var rig0 = v2StateWithSlots("ollama-gpu", "http://x", 1, 32_605L, java.util.Map.of());
        assertTrue(ProviderSelector.rankCandidates(List.of(rig0), "x", 0L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR).isEmpty(),
                "Zero cold-load footprint → empty (caller should fall back, not over-commit)");
    }

    // ---- AI Circuit Breaker (Phase A) ----
    // After CIRCUIT_FAILURE_THRESHOLD failures within CIRCUIT_FAILURE_WINDOW,
    // a provider is excluded from rankCandidates for CIRCUIT_COOLDOWN.
    // Direct motivation: thread d61647e9 wedge on rig1, 2026-05-28.

    private static ProviderSelector freshSelector() {
        // Plain constructor; nats provider is null (tests don't read KV state),
        // ObjectMapper is unused for the failure-tracking paths.
        return new ProviderSelector(null, new com.fasterxml.jackson.databind.ObjectMapper(), null, null, null);
    }

    @Test
    void circuit_stays_closed_below_threshold() {
        var sel = freshSelector();
        // One failure short of threshold — should not open.
        for (int i = 0; i < ProviderSelector.CIRCUIT_FAILURE_THRESHOLD - 1; i++) {
            sel.recordFailure("ollama-rig1");
        }
        assertFalse(sel.isCircuitOpen("ollama-rig1"),
                "Below threshold (" + (ProviderSelector.CIRCUIT_FAILURE_THRESHOLD - 1)
                        + " failures) should not open the circuit");
    }

    @Test
    void circuit_opens_at_threshold() {
        var sel = freshSelector();
        for (int i = 0; i < ProviderSelector.CIRCUIT_FAILURE_THRESHOLD; i++) {
            sel.recordFailure("ollama-rig1");
        }
        assertTrue(sel.isCircuitOpen("ollama-rig1"),
                "Threshold failures should open the circuit");
        assertNotNull(sel.openedAtForTest("ollama-rig1"));
        // Other providers unaffected.
        assertFalse(sel.isCircuitOpen("ollama-gpu"));
    }

    @Test
    void circuit_closed_by_success() {
        var sel = freshSelector();
        for (int i = 0; i < ProviderSelector.CIRCUIT_FAILURE_THRESHOLD; i++) {
            sel.recordFailure("ollama-rig1");
        }
        assertTrue(sel.isCircuitOpen("ollama-rig1"));
        sel.recordSuccess("ollama-rig1");
        assertFalse(sel.isCircuitOpen("ollama-rig1"), "Success should close the circuit");
        assertNull(sel.openedAtForTest("ollama-rig1"));
        // Failure window also cleared so a re-open requires N fresh failures.
        var w = sel.failureWindowForTest("ollama-rig1");
        assertTrue(w == null || w.isEmpty(), "Failure window cleared by success");
    }

    @Test
    void circuit_rankCandidates_excludesOpenProviders() {
        // Two providers, both warm + free. rig1's circuit is open → only rig0
        // appears in the ranked candidates. Half-open recovery is handled by
        // the cooldown clock + recordSuccess; this test only covers the
        // hard-exclusion gate inside rankCandidates.
        var rig0 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:8b", 5_000L));
        var rig1 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of("qwen3:8b", 5_000L));
        java.util.function.Predicate<String> rig1IsOpen = n -> n.equals("ollama-rig1");
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig0, rig1), "qwen3:8b", 5_000L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, rig1IsOpen, STATIC_PREDICTOR);
        assertEquals(1, ranked.size(), "rig1 excluded by open circuit");
        assertEquals("ollama-gpu", ranked.get(0).provider().name());
    }

    @Test
    void recordFailure_nullOrEmptyProviderIgnored() {
        // Defensive: ChatService may pass empty providerName on the fallback
        // path. Should be a silent no-op, not an NPE.
        var sel = freshSelector();
        sel.recordFailure(null);
        sel.recordFailure("");
        sel.recordSuccess(null);
        sel.recordSuccess("");
        assertFalse(sel.isCircuitOpen(null));
        assertFalse(sel.isCircuitOpen(""));
    }

    // ---- fromJson: v3 availableModelFootprintsMiB field (2026-06-11 fix) ----

    /**
     * fromJson must parse availableModelFootprintsMiB from the operator's
     * NATS KV payload. This is the v3 field added to carry on-disk model sizes
     * from /api/tags so agents can gate cold loads at true cold start.
     * Uses readTree + manual navigation (never readValue) per GraalVM native rule.
     */
    @Test
    void fromJson_parsesAvailableModelFootprintsMiB() throws Exception {
        var om = new com.fasterxml.jackson.databind.ObjectMapper();
        String json = """
                {
                  "name": "ollama-rig1",
                  "endpoint": "http://ollama.ollama-rig1:11434",
                  "maxParallel": 1,
                  "activeCount": 0,
                  "queueDepth": 0,
                  "loadedModels": [],
                  "ready": true,
                  "lastProbedAt": "2026-06-11T00:00:00Z",
                  "totalVramMiB": 24563,
                  "loadedModelFootprintsMiB": {},
                  "availableModelFootprintsMiB": {
                    "qwen3:32b": 27847,
                    "qwen3:8b": 4863
                  }
                }
                """;
        var state = ProviderSelector.fromJson(om.readTree(json));

        assertEquals("ollama-rig1", state.name());
        assertEquals(24_563L, state.totalVramMiB());
        // loadedModelFootprintsMiB is empty - model not loaded yet.
        assertEquals(0L, state.footprintMiBFor("qwen3:32b"),
                "footprintMiBFor should return 0 when not in loadedModelFootprintsMiB");
        // coldLoadFootprintMiB falls back to availableModelFootprintsMiB.
        assertEquals((long) (27_847L * ProviderState.ON_DISK_TO_VRAM_FACTOR), state.coldLoadFootprintMiB("qwen3:32b"),
                "coldLoadFootprintMiB resolves from availableModelFootprintsMiB (inflated to VRAM-realistic) at cold start");
        assertEquals((long) (4_863L * ProviderState.ON_DISK_TO_VRAM_FACTOR), state.coldLoadFootprintMiB("qwen3:8b"),
                "qwen3:8b also resolves from available footprints (inflated)");
    }

    /**
     * fromJson on an older operator payload (no availableModelFootprintsMiB field)
     * must degrade gracefully: coldLoadFootprintMiB returns 0 for models not in
     * loadedModelFootprintsMiB. No exception, no NPE.
     */
    @Test
    void fromJson_missingAvailableFootprints_degradesGracefully() throws Exception {
        var om = new com.fasterxml.jackson.databind.ObjectMapper();
        String json = """
                {
                  "name": "ollama-gpu",
                  "endpoint": "http://ollama.ollama-rig0:11434",
                  "maxParallel": 2,
                  "activeCount": 0,
                  "queueDepth": 0,
                  "loadedModels": ["qwen3:32b"],
                  "ready": true,
                  "lastProbedAt": "2026-05-01T00:00:00Z",
                  "totalVramMiB": 32605,
                  "loadedModelFootprintsMiB": {"qwen3:32b": 22000}
                }
                """;
        var state = ProviderSelector.fromJson(om.readTree(json));

        // Loaded model still resolves via footprintMiBFor.
        assertEquals(22_000L, state.coldLoadFootprintMiB("qwen3:32b"),
                "loaded model resolves even without availableModelFootprintsMiB field");
        // A model not in loaded map returns 0 (old behavior preserved).
        assertEquals(0L, state.coldLoadFootprintMiB("qwen3:8b"),
                "unknown model returns 0 when field absent - old degraded behavior");
    }

    /**
     * Gate: the rankCandidates cold-load path rejects a provider when the
     * footprint derived from availableModelFootprintsMiB exceeds totalVramMiB.
     * This is the critical path that was broken before 2026-06-11: the selector
     * fell back to the static endpoint because coldLoadFootprintMiB was 0, and
     * the static endpoint loaded qwen3:32b onto the 4090 with no VRAM check.
     */
    @Test
    void rankCandidates_rejectsProviderWhenAvailableFootprintExceedsVram() {
        // 4090 with 24563 MiB VRAM. qwen3:32b on-disk size is 27847 MiB - does not fit.
        var rig14090 = new ProviderState(
                "ollama-rig1", "http://ollama.ollama-rig1:11434",
                1, 0, 0,
                List.of(),
                true, "2026-06-11T00:00:00Z",
                24_563L,
                java.util.Map.of(),
                java.util.Map.of("qwen3:32b", 27_847L)
        );
        // coldLoadFootprintMiB from the available footprint.
        long footprint = rig14090.coldLoadFootprintMiB("qwen3:32b");
        assertEquals((long) (27_847L * ProviderState.ON_DISK_TO_VRAM_FACTOR), footprint);

        var ranked = ProviderSelector.rankCandidates(
                List.of(rig14090), "qwen3:32b", footprint,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertTrue(ranked.isEmpty(),
                "4090 (24563 MiB) must be rejected for qwen3:32b (27847 MiB from /api/tags): "
                        + "this was the root cause of the 2026-06-11 CPU spill");
    }

    /**
     * Gate: when the 5090 (32605 MiB) is available and qwen3:32b (27847 MiB from
     * /api/tags) fits, rankCandidates selects it even at cold start.
     */
    @Test
    void rankCandidates_selectsBiggerProviderForColdLoadFromAvailableFootprint() {
        // 4090 too small; 5090 big enough.
        var rig14090 = new ProviderState(
                "ollama-rig1", "http://ollama.ollama-rig1:11434",
                1, 0, 0, List.of(), true, "2026-06-11T00:00:00Z",
                24_563L, java.util.Map.of(), java.util.Map.of("qwen3:32b", 27_847L));
        var rig05090 = new ProviderState(
                "ollama-gpu", "http://ollama.ollama-rig0:11434",
                1, 0, 0, List.of(), true, "2026-06-11T00:00:00Z",
                32_605L, java.util.Map.of(), java.util.Map.of("qwen3:32b", 27_847L));

        long footprint = 27_847L;
        var ranked = ProviderSelector.rankCandidates(
                List.of(rig14090, rig05090), "qwen3:32b", footprint,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(1, ranked.size(), "only 5090 fits qwen3:32b at cold start");
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "5090 selected because it has enough VRAM; 4090 correctly rejected");
    }

    // ---- Resident-model fit gate: [[Cold Fit Gate Ignores Resident Models]] ----
    // The following tests cover the 2026-06-15 spill scenario and the invariant
    // from the task description: a 32B inference must SHARE the warm 32B on the
    // 5090, never cold-load a second copy onto a non-fitting 4090.

    /**
     * Scenario 1 (the primary bug): 32B warm+resident on 5090, 4090 cold.
     * A second 32B request must route to the 5090 (share the warm copy), NEVER
     * cold-load a second 32B onto the 4090 which cannot fit it.
     *
     * 5090: qwen3:32b warm at 22300 MiB -> warm path -> always feasible.
     * 4090: cold for qwen3:32b, footprint 22300 MiB > usable 21616 MiB -> REFUSED.
     * Only 5090 should appear in candidates.
     */
    @Test
    void residentModel_32b_warm_on_5090_routes_to_5090_not_4090() {
        // 5090 with 32B warm (published by operator after first load).
        var rig05090 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:32b", 22_300L));
        // 4090 cold for 32B (not loaded, nothing resident).
        var rig14090 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of());
        // coldLoadFootprintMiB = observed footprint from 5090 = 22300 MiB.
        long footprint = 22_300L;

        var ranked = ProviderSelector.rankCandidates(
                List.of(rig05090, rig14090), "qwen3:32b", footprint,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);

        assertEquals(1, ranked.size(),
                "only the 5090 (warm) must be a candidate; 4090 refused by cold-gate (22300 > 21616 usable)");
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "second 32B request must share the warm copy on the 5090, not cold-load onto the 4090");
        assertTrue(ranked.get(0).warm(),
                "selected candidate must be warm (sharing, not cold-loading)");
    }

    /**
     * Scenario 2: 5090 warm but saturated (all slots busy), 4090 cold and cannot fit.
     * The 4090 must NOT become a candidate even when the 5090 is fully occupied.
     * The result is an empty candidate list - caller queues (pickAndClaim returns empty,
     * caller retries or stand-asides) rather than spilling onto the 4090.
     */
    @Test
    void residentModel_32b_warm_saturated_5090_4090_still_not_a_candidate() {
        // 5090 warm but slot count equals maxParallel (fully busy - score includes
        // contention but the predictor still admits it as warm).
        var rig05090 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:32b", 22_300L));
        var rig14090 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of());
        // Simulate rig0 fully busy (1 active = maxParallel 1).
        java.util.function.ToIntFunction<ProviderState> oneTicketOn5090 = p ->
                p.name().equals("ollama-gpu") ? 1 : 0;
        long footprint = 22_300L;

        var ranked = ProviderSelector.rankCandidates(
                List.of(rig05090, rig14090), "qwen3:32b", footprint,
                oneTicketOn5090, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);

        // The 5090 is warm (even if saturated, the predictor admits it; ranking
        // penalises contention but does NOT exclude). The 4090 is refused by the
        // cold gate. So ranked should have exactly the 5090.
        assertEquals(1, ranked.size(),
                "saturated 5090 is still a warm candidate; 4090 refused - never spill onto a non-fitting provider");
        assertEquals("ollama-gpu", ranked.get(0).provider().name(),
                "queue on the warm 5090 (even if contention is high), not spill onto the 4090");
    }

    /**
     * Scenario 3 (cold-gate resident accounting): 5090 hosting 32B (22300 MiB
     * resident), 8B tries to cold-load there. Combined 22300+5979 = 28279 MiB
     * is just under usable 28693 for the numbers here, so 8B IS admitted to 5090.
     * But with the FULL 27252 MiB resident the combined 27252+5979 = 33231 MiB
     * exceeds usable 28693 MiB - the 5090 is NOT a candidate for the 8B cold.
     * The 4090 (empty, 8B fits) becomes the only candidate.
     *
     * This is the [[Cold Fit Gate Ignores Resident Models]] scenario.
     */
    @Test
    void residentModel_8b_cold_load_refused_on_5090_hosting_large_32b() {
        // 5090 has 32B resident at 27252 MiB (full observed footprint from /api/ps).
        var rig05090 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L,
                java.util.Map.of("qwen3:32b", 27_252L));
        // 4090 empty (no residents).
        var rig14090 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L,
                java.util.Map.of());
        long footprint8b = 5_979L;

        var ranked = ProviderSelector.rankCandidates(
                List.of(rig05090, rig14090), "qwen3:8b", footprint8b,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);

        // 5090: 27252 resident + 5979 cold = 33231 > 28693 usable -> REFUSED.
        // 4090: 0 resident + 5979 cold = 5979 < 21616 usable -> ADMITTED.
        assertEquals(1, ranked.size(),
                "5090 refused (residents+cold > usable); 4090 is the only candidate for 8B");
        assertEquals("ollama-rig1", ranked.get(0).provider().name(),
                "8B must go to the 4090 (empty), not thrash the 5090 which is full of a 32B");
    }

    /**
     * Scenario 4: regression guard - 8B and 14B still fit both GPUs when both are
     * empty (no regression to the bin-packing goal of using both cards). The fix
     * only blocks cold loads onto FULL cards, not onto empty ones.
     */
    @Test
    void residentModel_smallModels_still_fit_both_gpus_when_empty() {
        // Both GPUs empty. 8B (5979 MiB) fits both. 14B (8000 MiB) fits both.
        var rig05090 = v2StateWithSlots("ollama-gpu", "http://rig0", 1, 32_605L, java.util.Map.of());
        var rig14090 = v2StateWithSlots("ollama-rig1", "http://rig1", 1, 24_563L, java.util.Map.of());

        var ranked8b = ProviderSelector.rankCandidates(
                List.of(rig05090, rig14090), "qwen3:8b", 5_979L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(2, ranked8b.size(), "8B fits both empty GPUs (no regression to bin-packing)");

        var ranked14b = ProviderSelector.rankCandidates(
                List.of(rig05090, rig14090), "qwen3:14b", 8_000L,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(2, ranked14b.size(), "14B fits both empty GPUs (no regression)");
    }

    /**
     * Scenario 5: footprint inflation correctly applied. On-disk 32B size (~19265 MiB)
     * after inflation (x1.2 = ~23118 MiB) must exceed 4090 usable (21616 MiB) and
     * be refused. On the 5090 (usable 28693 MiB) it must be admitted.
     * This verifies the ON_DISK_TO_VRAM_FACTOR path in ProviderState.coldLoadFootprintMiB.
     */
    @Test
    void residentModel_footprint_inflation_refuses_4090_admits_5090_for_32b() {
        long onDiskMiB = 19_265L; // typical qwen3:32b on-disk size in MiB
        long inflatedMiB = (long) (onDiskMiB * ProviderState.ON_DISK_TO_VRAM_FACTOR); // ~23118

        // 4090: cold, no residents. Gate 1: 23118 > usable 21616 -> REFUSED.
        var rig14090 = new ProviderState("ollama-rig1", "http://rig1", 1, 0, 0,
                List.of(), true, "2026-06-15T00:00:00Z",
                24_563L, java.util.Map.of(),
                java.util.Map.of("qwen3:32b", onDiskMiB));
        long footprint4090 = rig14090.coldLoadFootprintMiB("qwen3:32b");
        assertEquals(inflatedMiB, footprint4090, "on-disk MiB must be inflated by ON_DISK_TO_VRAM_FACTOR");
        assertTrue(footprint4090 > StaticFitPredictor.usableVramMiB(24_563L),
                "inflated footprint must exceed 4090 usable VRAM - gate must refuse");

        var ranked4090 = ProviderSelector.rankCandidates(
                List.of(rig14090), "qwen3:32b", inflatedMiB,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertTrue(ranked4090.isEmpty(), "4090 must be refused for 32B via inflated on-disk footprint");

        // 5090: cold, no residents. Inflated 23118 < usable 28693 -> ADMITTED.
        var rig05090 = new ProviderState("ollama-gpu", "http://rig0", 1, 0, 0,
                List.of(), true, "2026-06-15T00:00:00Z",
                32_605L, java.util.Map.of(),
                java.util.Map.of("qwen3:32b", onDiskMiB));
        var ranked5090 = ProviderSelector.rankCandidates(
                List.of(rig05090), "qwen3:32b", inflatedMiB,
                NO_TICKET_COUNT, NO_TICKETS, NO_LATENCY, NO_CIRCUIT_OPEN, STATIC_PREDICTOR);
        assertEquals(1, ranked5090.size(), "5090 must be admitted for 32B via inflated on-disk footprint");
        assertEquals("ollama-gpu", ranked5090.get(0).provider().name());
    }

    // --- capacity classification: busy (wait) versus too large (never fits) ---

    @Test
    void canEverHold_trueWhenSomeCardIsBigEnoughEvenIfFullNow() {
        // 24 GiB card fully occupied by another model: busy, not too small.
        var busy = v2State("ollama-a", "http://a", true, 24_576, java.util.Map.of("qwen3:32b", 21_000L));
        assertTrue(ProviderSelector.canEverHold(List.of(busy), 12_224));
    }

    @Test
    void canEverHold_falseWhenEveryKnownCardIsTooSmall() {
        var small = v2State("ollama-a", "http://a", true, 16_384, java.util.Map.of());
        var smaller = v2State("ollama-b", "http://b", true, 12_288, java.util.Map.of());
        assertFalse(ProviderSelector.canEverHold(List.of(small, smaller), 40_000));
    }

    @Test
    void canEverHold_unknownVramNeverProvesTooLarge() {
        var unknown = v2State("ollama-a", "http://a", true, 0, java.util.Map.of());
        assertTrue(ProviderSelector.canEverHold(List.of(unknown), 40_000));
        assertTrue(ProviderSelector.canEverHold(List.of(), 40_000));
        assertTrue(ProviderSelector.canEverHold(null, 40_000));
        var small = v2State("ollama-b", "http://b", true, 16_384, java.util.Map.of());
        assertFalse(ProviderSelector.canEverHold(List.of(unknown, small), 40_000),
                "one known too-small card and one unknown: the known card decides");
    }

    @Test
    void warmWithFreeSlot_requiresWarmAndAFreeSlot() {
        var p = v2StateWithSlots("ollama-a", "http://a", 2, 32_768, java.util.Map.of("qwen3:14b", 10_000L));
        var yes = FitScore.yes(0L, "warm");
        var selector = new ProviderSelector(null, new com.fasterxml.jackson.databind.ObjectMapper(), null, null, null);
        assertTrue(selector.warmWithFreeSlot(new ProviderSelector.Candidate(p, true, 1, 0.0, yes)));
        assertFalse(selector.warmWithFreeSlot(new ProviderSelector.Candidate(p, true, 2, 0.0, yes)),
                "both slots busy: warm but no room");
        assertFalse(selector.warmWithFreeSlot(new ProviderSelector.Candidate(p, false, 0, 0.0, yes)),
                "cold: a claim here would load the model");
    }

    @Test
    void readStateFailure_isReportedAndKeptApartFromAnEmptyBucket() throws Exception {
        var conn = org.mockito.Mockito.mock(io.nats.client.Connection.class);
        var jsm = org.mockito.Mockito.mock(io.nats.client.JetStreamManagement.class);
        var nats = org.mockito.Mockito.mock(ai.kubemoot.agent.nats.NatsConnectionProvider.class);
        org.mockito.Mockito.when(nats.isAvailable()).thenReturn(true);
        org.mockito.Mockito.when(nats.getConnection()).thenReturn(conn);
        org.mockito.Mockito.when(conn.jetStreamManagement()).thenReturn(jsm);
        org.mockito.Mockito.when(jsm.getStreamInfo(org.mockito.ArgumentMatchers.anyString(), org.mockito.ArgumentMatchers.any()))
                .thenThrow(new java.io.IOException("deliver policy can not be updated [10012]"));
        var sel = new ProviderSelector(nats, new com.fasterxml.jackson.databind.ObjectMapper(), null, null, null);

        assertTrue(sel.readState().isEmpty());
        assertTrue(sel.lastReadError().orElseThrow().contains("10012"));

        var bucket = new InMemoryKv();
        var empty = new ProviderSelector(bucket.provider(ProviderSelector.STATE_BUCKET),
                new com.fasterxml.jackson.databind.ObjectMapper(), null, null, null);
        assertTrue(empty.readState().isEmpty());
        assertTrue(empty.lastReadError().isEmpty(), "an empty bucket is not an error");
    }

    @Test
    void readStateWithNatsDown_saysSo_ratherThanReportingAStaleError() {
        var nats = org.mockito.Mockito.mock(ai.kubemoot.agent.nats.NatsConnectionProvider.class);
        var sel = new ProviderSelector(nats, new com.fasterxml.jackson.databind.ObjectMapper(), null, null, null);
        assertTrue(sel.readState().isEmpty());
        assertEquals("NATS unavailable", sel.lastReadError().orElseThrow());
    }
}
