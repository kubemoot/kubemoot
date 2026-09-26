package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;

import java.util.List;
import java.util.Map;

/**
 * Mirror of the JSON document the kubemoot operator publishes to the NATS
 * KV bucket {@code kubemoot_provider_state}, one entry per ModelProvider.
 * Source of truth: {@code operator/internal/controller/modelprovider_controller.go}
 * (ProviderState struct + ProviderStateBucket constant).
 *
 * Read by {@link ProviderSelector} on each inference call to compute
 * live VRAM headroom and pick the freest provider — JIT GPU scheduling,
 * see {@code kubemoot/docs/scheduler.md} ("JIT GPU Scheduling → Update
 * (2026-05-28): VRAM-headroom tickets") and [[feedback_jit_gpu_scheduling]].
 *
 * <h3>v1 vs v2 fields</h3>
 * {@code maxParallel}, {@code activeCount}, {@code queueDepth},
 * {@code loadedModels} are v1 inputs (saturation-based scoring) and
 * remain on the record for backward compatibility and observability,
 * but are NOT used in v2 selection scoring. v2 uses:
 * <ul>
 *   <li>{@link #totalVramMiB} — total VRAM available on the GPU
 *       (operator probes DCGM or Ollama {@code /api/show})</li>
 *   <li>{@link #loadedModelFootprintsMiB} — VRAM each loaded model
 *       currently consumes (operator probes Ollama {@code /api/ps})</li>
 * </ul>
 * Combined with in-flight ticket footprints (read separately from the
 * {@code kubemoot_provider_tickets} bucket via {@link TicketManager}),
 * these give the per-call decision the live picture it needs.
 *
 * <h3>v3 field</h3>
 * {@link #availableModelFootprintsMiB} holds on-disk sizes from
 * {@code /api/tags} for models not currently loaded. The operator populates it
 * for models in {@code DiscoveredCapacity.AvailableModels} that are NOT already
 * in {@code LoadedModelFootprintsMiB}. It is the cold-load footprint proxy when
 * the model has never been loaded into VRAM yet; agents prefer
 * {@link #loadedModelFootprintsMiB} (actual VRAM size) over it. This is the
 * fallback at true cold start so the fit-gate can reject an oversized model
 * before it loads. Null or absent when the operator pre-dates this field
 * (graceful degradation to the old behavior).
 *
 * {@code @JsonIgnoreProperties(ignoreUnknown=true)} so adding fields on
 * the operator side never breaks deserialisation on the agent side; the
 * inverse case (operator hasn't published a new field yet) is handled by
 * the manual tree-mapping in {@link ProviderSelector#fromJson} which
 * supplies defaults for missing keys.
 */
@JsonIgnoreProperties(ignoreUnknown = true)
public record ProviderState(
        String name,
        String endpoint,
        int maxParallel,
        int activeCount,
        int queueDepth,
        List<String> loadedModels,
        boolean ready,
        String lastProbedAt,
        long totalVramMiB,
        Map<String, Long> loadedModelFootprintsMiB,
        Map<String, Long> availableModelFootprintsMiB
) {
    /**
     * Backward-compat constructor for callers that pre-date the
     * {@code availableModelFootprintsMiB} field (v3). Passes an empty map so
     * existing code compiles without change; tests and production code built
     * before the v3 operator rolled out degrade gracefully to the pre-v3
     * behavior (no cold-start footprint from /api/tags).
     */
    public ProviderState(String name, String endpoint, int maxParallel,
                         int activeCount, int queueDepth, List<String> loadedModels,
                         boolean ready, String lastProbedAt, long totalVramMiB,
                         Map<String, Long> loadedModelFootprintsMiB) {
        this(name, endpoint, maxParallel, activeCount, queueDepth, loadedModels,
                ready, lastProbedAt, totalVramMiB, loadedModelFootprintsMiB,
                java.util.Map.of());
    }

    /**
     * v1 capacity ratio used in the saturation-based scorer. Retained for
     * backward compatibility but not consulted by the v2 headroom path —
     * activeCount is hardcoded to 0 in the operator probe (Ollama doesn't
     * expose in-flight count), so this ratio is always 0/maxParallel = 0
     * for every provider. v2 uses {@link #headroomMiB} instead.
     */
    public double saturationRatio() {
        int parallel = Math.max(maxParallel, 1);
        return (double) activeCount / parallel;
    }

    /** v1 free-slot check, retained for backward compatibility. v2 uses headroom. */
    public boolean hasFreeSlot() {
        return activeCount < Math.max(maxParallel, 1);
    }

    /**
     * True when the provider has the named model already loaded. v2 uses
     * this as a TIEBREAK only (cold-but-free beats warm-but-saturated);
     * v1 treated it as a hard filter.
     */
    public boolean hasModelLoaded(String modelName) {
        if (modelName == null || loadedModels == null) return false;
        return loadedModels.contains(modelName);
    }

    /**
     * Sum of all loaded models' VRAM footprints on this provider, in MiB.
     * Subtracted from {@link #totalVramMiB} to get the budget available
     * for ticket claims. Returns 0 when no footprint data has been
     * published (operator pre-upgrade or no models loaded).
     */
    public long loadedFootprintSumMiB() {
        if (loadedModelFootprintsMiB == null || loadedModelFootprintsMiB.isEmpty()) return 0L;
        long sum = 0L;
        for (Long v : loadedModelFootprintsMiB.values()) {
            if (v != null) sum += v;
        }
        return sum;
    }

    /**
     * VRAM budget available for new ticket claims, in MiB:
     * {@code totalVramMiB − Σ(loadedModelFootprintsMiB)}. Does NOT
     * account for in-flight tickets — callers subtract those separately
     * via {@link TicketManager#activeFootprintFor}, because tickets live
     * in a different bucket and change between probe cycles.
     *
     * Returns 0 when {@code totalVramMiB} is unset (operator pre-upgrade)
     * — selectors treat 0 headroom as "can't claim here, try elsewhere
     * or fall back to static endpoint". Graceful degradation during
     * rollout when not all providers have published the new fields yet.
     */
    public long preTicketHeadroomMiB() {
        if (totalVramMiB <= 0) return 0L;
        long h = totalVramMiB - loadedFootprintSumMiB();
        return Math.max(h, 0L);
    }

    /**
     * Footprint of a specific loaded model on this provider in MiB, or 0
     * when not loaded / unknown. Used as the call-footprint estimate when
     * the desired model is already warm here — the most accurate source.
     */
    public long footprintMiBFor(String modelName) {
        if (modelName == null || loadedModelFootprintsMiB == null) return 0L;
        Long v = loadedModelFootprintsMiB.get(modelName);
        return v == null ? 0L : v;
    }

    /**
     * Best available cold-load footprint estimate for a model on this provider,
     * in MiB. Resolution order (most accurate first):
     * <ol>
     *   <li>{@link #loadedModelFootprintsMiB} - actual VRAM size from /api/ps.
     *       Available once the model has been loaded at least once.</li>
     *   <li>{@link #availableModelFootprintsMiB} - on-disk size from /api/tags,
     *       converted to MiB. Available even at true cold start (model downloaded
     *       but never loaded). On-disk size is a reasonable upper bound for VRAM;
     *       quant models typically use similar VRAM and disk sizes.</li>
     *   <li>0 - footprint genuinely unknown (model not downloaded on this provider,
     *       or operator pre-dates v3 publishing). Callers treat 0 as "unknown".</li>
     * </ol>
     *
     * This is the fix for the JIT cold-start bug verified 2026-06-11:
     * {@code footprintMiBFor} returned 0 at true cold start (nothing loaded yet)
     * so {@code pickMullingChatModel} fell back to the static endpoint with no
     * VRAM check, loading qwen3:32b onto the 4090 (24 GiB) and spilling 20% to
     * CPU. With this method, the cold-load footprint is known from /api/tags
     * and the fit-gate rejects oversized models before they start loading.
     */
    public long coldLoadFootprintMiB(String modelName) {
        // Prefer the observed VRAM size from /api/ps when the model was previously
        // loaded. This is accurate (actual VRAM occupancy) - no inflation.
        long fromLoaded = footprintMiBFor(modelName);
        if (fromLoaded > 0L) return fromLoaded;
        // Fall back to on-disk size from /api/tags as a proxy. On-disk size
        // UNDERESTIMATES the loaded VRAM footprint (weights expand in VRAM and
        // context buffers add on top): observed 2026-06-11, qwen3:32b is ~19.3 GiB
        // on disk vs ~22.3 GiB loaded (~16% larger). Inflate so the cold-load fit
        // gate uses a VRAM-realistic estimate and does not admit a model that
        // would spill. See [[JIT Fit-Gate Degraded Mode Can Spill]].
        if (modelName == null || availableModelFootprintsMiB == null) return 0L;
        Long v = availableModelFootprintsMiB.get(modelName);
        return v == null ? 0L : (long) (v * ON_DISK_TO_VRAM_FACTOR);
    }

    /**
     * Multiplier converting an on-disk model size (from /api/tags) into a
     * VRAM-realistic cold-load footprint estimate. On-disk quantized weights
     * occupy more once loaded (de-padding + context scratch); ~1.2 matches the
     * observed qwen3:32b ratio (~19.3 GiB disk -> ~22.3 GiB VRAM) with a small
     * safety bias. Only applied to the on-disk fallback, never to an observed
     * /api/ps footprint.
     */
    static final double ON_DISK_TO_VRAM_FACTOR = 1.2;
}
