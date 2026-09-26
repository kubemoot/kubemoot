package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link LearningFitPredictor} — v2 of {@link FitPredictor}.
 * Adds per-{@code (provider, model)} EMA learning on top of the static
 * v1 canServe decision. Tests cover:
 * <ul>
 *   <li>Cold-start (no samples) returns the static result unchanged.</li>
 *   <li>Few samples (below MIN_CONFIDENCE_SAMPLES) reports cold-start.</li>
 *   <li>EMA converges toward observed success rate as samples accumulate.</li>
 *   <li>recordOutcome with mixed results produces a value between
 *       extremes (EMA semantics).</li>
 *   <li>Hard "no" decisions (circuit, slot full, etc.) bypass the
 *       learning overlay.</li>
 * </ul>
 */
class LearningFitPredictorTest {

    private static ProviderState state(String name, int maxParallel, boolean ready,
                                        long totalVramMiB, Map<String, Long> loaded) {
        return new ProviderState(name, "http://" + name, maxParallel, 0, 0,
                List.copyOf(loaded.keySet()), ready, "2026-05-28T00:00:00Z",
                totalVramMiB, loaded);
    }

    private static FitInputs in(ProviderState p, String model, int activeTickets,
                                long coldFootprintMiB, boolean circuitOpen) {
        return new FitInputs(p, model, activeTickets, 0L, coldFootprintMiB, 0L, 0.0, circuitOpen);
    }

    private final LearningFitPredictor pred = new LearningFitPredictor();

    private static final ProviderState RIG0_WARM =
            state("ollama-gpu", 1, true, 32_605L, Map.of("qwen3:32b", 27_797L));

    @Test
    void coldStart_zeroSamples_returnsStaticOptimistic() {
        FitScore s = pred.predict(in(RIG0_WARM, "qwen3:32b", 0, 27_797L, false));
        assertTrue(s.canServe());
        assertEquals(1.0, s.successProbability(), "no samples → optimistic 1.0");
        assertEquals(0, s.sampleSize());
        assertTrue(s.reasoning().contains("bootstrap"));
    }

    @Test
    void fewSamples_belowConfidenceThreshold_stillOptimistic() {
        // Two failures — below MIN_CONFIDENCE_SAMPLES. Predictor should
        // not yet lower its confidence; let the cold-start optimism stand
        // so the provider gets a fair number of chances before judgment.
        pred.recordOutcome("ollama-gpu", "qwen3:32b", 1500L, false);
        pred.recordOutcome("ollama-gpu", "qwen3:32b", 2000L, false);
        FitScore s = pred.predict(in(RIG0_WARM, "qwen3:32b", 0, 27_797L, false));
        assertEquals(1.0, s.successProbability(),
                "below MIN_CONFIDENCE_SAMPLES → still optimistic");
        assertTrue(s.reasoning().contains("bootstrap"));
        assertEquals(2, s.sampleSize());
    }

    @Test
    void enoughSamples_allSuccesses_reportsHighSR() {
        // Feed MIN_CONFIDENCE_SAMPLES successful outcomes; predictor's
        // EMA must converge near 1.0.
        for (int i = 0; i < LearningFitPredictor.MIN_CONFIDENCE_SAMPLES; i++) {
            pred.recordOutcome("ollama-gpu", "qwen3:32b", 2000L, true);
        }
        FitScore s = pred.predict(in(RIG0_WARM, "qwen3:32b", 0, 27_797L, false));
        assertTrue(s.successProbability() > 0.95,
                "all successes → SR near 1.0, got " + s.successProbability());
        assertTrue(s.sampleSize() >= LearningFitPredictor.MIN_CONFIDENCE_SAMPLES);
        assertTrue(s.reasoning().contains("SR=") && s.reasoning().contains("samples"));
    }

    @Test
    void enoughSamples_allFailures_reportsLowSR() {
        for (int i = 0; i < LearningFitPredictor.MIN_CONFIDENCE_SAMPLES + 3; i++) {
            pred.recordOutcome("ollama-rig1", "qwen3:32b", 120_000L, false);
        }
        // Predict on a different provider so the static canServe doesn't
        // hard-no us out (we want to see the EMA value, not the gate).
        var rig1Warm = state("ollama-rig1", 1, true, 24_563L,
                Map.of("qwen3:32b", 23_369L));
        FitScore s = pred.predict(in(rig1Warm, "qwen3:32b", 0, 23_369L, false));
        assertTrue(s.successProbability() < 0.1,
                "all failures → SR near 0.0, got " + s.successProbability());
    }

    @Test
    void mixedOutcomes_emaConvergesBetween() {
        // 4 success + 4 failure → EMA should be somewhere in the middle
        // (not exactly 0.5 due to EMA recency-weighting, but well between
        // the extremes).
        for (int i = 0; i < 4; i++) {
            pred.recordOutcome("ollama-gpu", "qwen3:8b", 1000L, true);
            pred.recordOutcome("ollama-gpu", "qwen3:8b", 5000L, false);
        }
        var p = state("ollama-gpu", 1, true, 32_605L, Map.of("qwen3:8b", 5_000L));
        FitScore s = pred.predict(in(p, "qwen3:8b", 0, 5_000L, false));
        assertTrue(s.successProbability() > 0.05 && s.successProbability() < 0.95,
                "mixed outcomes → EMA in (0.05, 0.95), got " + s.successProbability());
        assertEquals(8, s.sampleSize());
    }

    @Test
    void hardNo_circuitOpen_skipsLearningOverlay() {
        // Even with a perfect history, an open circuit returns hard no
        // and never reaches the learning layer.
        for (int i = 0; i < 20; i++) {
            pred.recordOutcome("ollama-gpu", "qwen3:32b", 1500L, true);
        }
        boolean circuitOpen = true;
        FitScore s = pred.predict(in(RIG0_WARM, "qwen3:32b", 0, 27_797L, circuitOpen));
        assertFalse(s.canServe(), "circuit open trumps learning");
    }

    @Test
    void warmStaysFeasibleAtDeepQueue_noHardDepthCap() {
        // In the weighted-cost scheduler there is no co-schedule depth cap:
        // queueing at a warm home is a ranking COST, never a feasibility refusal.
        // (The thrash fix replaced the depth cap with an eviction weight in the
        // selector; see [[JIT Scheduler Locality Algorithm]].) So a warm provider
        // stays feasible at any queue depth, and the learning overlay still fires.
        for (int i = 0; i < 20; i++) {
            pred.recordOutcome("ollama-gpu", "qwen3:32b", 1500L, true);
        }
        FitScore s = pred.predict(in(RIG0_WARM, "qwen3:32b", 12, 27_797L, false));
        assertTrue(s.canServe(), "warm stays feasible at deep queue - no hard depth cap");
    }

    @Test
    void perModelLearning_isolation() {
        // Learning for (provider, modelA) does NOT bleed into (provider, modelB).
        for (int i = 0; i < 10; i++) {
            pred.recordOutcome("ollama-gpu", "qwen3:32b", 90_000L, false);
        }
        // 8b warm (fits usable); 32b cold (also fits usable, but learned bad).
        // Loaded set must not over-subscribe the card's usable VRAM.
        var pWith8b = state("ollama-gpu", 1, true, 32_605L,
                Map.of("qwen3:8b", 5_000L));
        FitScore sFor32b = pred.predict(in(pWith8b, "qwen3:32b", 0, 27_797L, false));
        FitScore sFor8b = pred.predict(in(pWith8b, "qwen3:8b", 0, 5_000L, false));
        assertTrue(sFor32b.successProbability() < 0.1, "qwen3:32b learned bad");
        assertEquals(1.0, sFor8b.successProbability(),
                "qwen3:8b's prediction unaffected by 32b's failures");
    }

    @Test
    void recordOutcome_nullOrEmpty_isNoop() {
        pred.recordOutcome(null, "qwen3:32b", 1000L, true);
        pred.recordOutcome("", "qwen3:32b", 1000L, true);
        pred.recordOutcome("ollama-gpu", null, 1000L, true);
        pred.recordOutcome("ollama-gpu", "", 1000L, true);
        // None should populate state; cold-start applies.
        assertNull(pred.peekForTest("ollama-gpu", "qwen3:32b"));
        assertNull(pred.peekForTest(null, null));
    }

    @Test
    void latencyEma_smooths_observedDurations() {
        // Three samples with very different latencies; EMA should land
        // between the extremes, biased toward most recent samples per
        // ALPHA (0.3) — exact value not pinned, just bounded.
        pred.recordOutcome("ollama-gpu", "qwen3:32b", 1000L, true);
        pred.recordOutcome("ollama-gpu", "qwen3:32b", 1000L, true);
        pred.recordOutcome("ollama-gpu", "qwen3:32b", 1000L, true);
        pred.recordOutcome("ollama-gpu", "qwen3:32b", 1000L, true);
        pred.recordOutcome("ollama-gpu", "qwen3:32b", 9000L, true);   // recent spike
        FitScore s = pred.predict(in(RIG0_WARM, "qwen3:32b", 0, 27_797L, false));
        assertTrue(s.estimatedLatencyMs() > 1000L && s.estimatedLatencyMs() < 9000L,
                "EMA latency between 1000 and 9000, got " + s.estimatedLatencyMs());
    }
}
