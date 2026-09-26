package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link KvCacheEstimator} — Phase D's conservative
 * per-call KV-cache VRAM estimator.
 */
class KvCacheEstimatorTest {

    // ---- bytesPerTokenFor: model-family heuristic ----

    @Test
    void bytesPerToken_32bClass() {
        assertEquals(256 * 1024L, KvCacheEstimator.bytesPerTokenFor("qwen3:32b"));
        assertEquals(256 * 1024L, KvCacheEstimator.bytesPerTokenFor("llama-32b-instruct"));
    }

    @Test
    void bytesPerToken_14bClass() {
        assertEquals(192 * 1024L, KvCacheEstimator.bytesPerTokenFor("qwen3:14b"));
    }

    @Test
    void bytesPerToken_8bClass() {
        assertEquals(128 * 1024L, KvCacheEstimator.bytesPerTokenFor("qwen3:8b"));
        assertEquals(128 * 1024L, KvCacheEstimator.bytesPerTokenFor("llama3-8b"));
    }

    @Test
    void bytesPerToken_embeddingsAreSmall() {
        assertEquals(64 * 1024L, KvCacheEstimator.bytesPerTokenFor("nomic-embed-text:latest"));
    }

    @Test
    void bytesPerToken_unknownDefaults() {
        // Unknown model (no parseable parameter count, not an embedding) →
        // middle-of-the-road default so the estimate still gates something
        // rather than degenerating to 0. null is treated the same as unknown.
        assertEquals(128 * 1024L, KvCacheEstimator.bytesPerTokenFor("unknown-model"));
        assertEquals(128 * 1024L, KvCacheEstimator.bytesPerTokenFor(null));
    }

    @Test
    void bytesPerToken_parsesParamCountFamilyAgnostic() {
        // Size is parsed from the name, not matched against specific models.
        assertEquals(512 * 1024L, KvCacheEstimator.bytesPerTokenFor("llama-3.1-70b-instruct"));
        assertEquals(256 * 1024L, KvCacheEstimator.bytesPerTokenFor("qwen3:32b"));
        assertEquals(192 * 1024L, KvCacheEstimator.bytesPerTokenFor("gemma2:14b"));
        assertEquals(128 * 1024L, KvCacheEstimator.bytesPerTokenFor("mistral:7b"));
        assertEquals(96 * 1024L, KvCacheEstimator.bytesPerTokenFor("qwen3:4b"));
        // The family-version digit must not be mistaken for the size.
        assertEquals(128 * 1024L, KvCacheEstimator.bytesPerTokenFor("qwen3:8b"));
    }

    // ---- estimateColdOccupancyMiB: cold-start footprint fallback ----

    @Test
    void coldOccupancy_fromParamCount_familyAgnostic() {
        // Used only when no provider has observed the real footprint yet; keeps the
        // JIT scheduler engaged instead of bypassing to the static endpoint.
        assertEquals(8L * 800 + 1024, KvCacheEstimator.estimateColdOccupancyMiB("qwen3:8b"));
        assertEquals(32L * 800 + 1024, KvCacheEstimator.estimateColdOccupancyMiB("qwen3:32b"));
        assertEquals(70L * 800 + 1024, KvCacheEstimator.estimateColdOccupancyMiB("llama-3.1-70b-instruct"));
        // 32B estimate must stay under a 5090's usable VRAM (so it remains feasible
        // there) and over a 4090's usable (so best-fit/feasibility keeps it off the
        // 4090) - sanity that the ballpark lands in the right band.
        assertTrue(KvCacheEstimator.estimateColdOccupancyMiB("qwen3:32b") < 28_000);
        assertTrue(KvCacheEstimator.estimateColdOccupancyMiB("qwen3:32b") > 21_616);
    }

    @Test
    void coldOccupancy_unparseable_returnsZero_soCallerDegradesToStatic() {
        assertEquals(0L, KvCacheEstimator.estimateColdOccupancyMiB("mystery-model"));
        assertEquals(0L, KvCacheEstimator.estimateColdOccupancyMiB(null));
    }

    // ---- estimateMiB: full per-call estimate ----

    @Test
    void estimate_qwen32b_8kPrompt_2kOutput() {
        // 256 KiB/token × 10000 tokens = 2.44 GiB + 256 MiB safety = ~2.7 GiB
        long mib = KvCacheEstimator.estimateMiB("qwen3:32b", 8000L, 2000L);
        assertTrue(mib > 2500L && mib < 3000L,
                "32b + 10k tokens expected ~2.7 GiB, got " + mib + " MiB");
    }

    @Test
    void estimate_qwen8b_modestPrompt() {
        // 128 KiB/token × 2000 tokens = 250 MiB + 256 safety = ~500 MiB
        long mib = KvCacheEstimator.estimateMiB("qwen3:8b", 1500L, 500L);
        assertTrue(mib > 400L && mib < 600L,
                "8b + 2k tokens expected ~500 MiB, got " + mib + " MiB");
    }

    @Test
    void estimate_safetyMarginAlwaysAdded() {
        // Even a zero-token call gets the 256 MiB safety margin so the
        // gate never under-reserves.
        long mib = KvCacheEstimator.estimateMiB("qwen3:8b", 0L, 0L);
        assertEquals(KvCacheEstimator.SAFETY_MARGIN_MIB, mib);
    }

    @Test
    void estimate_defensive_negativeArgs() {
        long mib = KvCacheEstimator.estimateMiB("qwen3:8b", -100L, -50L);
        assertEquals(KvCacheEstimator.SAFETY_MARGIN_MIB, mib,
                "negative token counts treated as zero");
    }

    // ---- estimateTokensFromChars ----

    @Test
    void tokenEstimate_roughlyCharsOver4_withSafetyPad() {
        // 4000 chars → ~1000 tokens × 1.25 pad = 1250 tokens
        long t = KvCacheEstimator.estimateTokensFromChars(4000);
        assertTrue(t >= 1200 && t <= 1300, "expected ~1250 tokens, got " + t);
    }

    @Test
    void tokenEstimate_zeroOrNegativeChars_returnsZero() {
        assertEquals(0, KvCacheEstimator.estimateTokensFromChars(0));
        assertEquals(0, KvCacheEstimator.estimateTokensFromChars(-100));
    }
}
