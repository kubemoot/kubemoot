package ai.kubemoot.agent.provider;

/**
 * Conservative per-call KV-cache VRAM estimator. The "what Ollama
 * doesn't tell us" piece — see {@code kubemoot/docs/scheduler.md}.
 *
 * <h3>Why this exists</h3>
 * Model-weight size alone doesn't predict whether a call will fit on
 * a provider. Each in-flight inference also reserves KV-cache VRAM
 * that scales linearly with (prompt + generated) token count. On a
 * tight-fit provider (e.g. qwen3:32b at 23 GiB loaded on a 24 GiB
 * card), the KV cache for one 8K-token call (~2 GiB) can be the
 * difference between "serves" and "Ollama wedges silently" — the
 * exact failure mode of thread d61647e9 on 2026-05-28.
 *
 * <h3>Estimation formula</h3>
 * KV cache for one call ≈ {@code bytes_per_token × (promptTokens + maxOutputTokens)}
 *
 * {@code bytes_per_token} is a per-model constant derivable from
 * model architecture: {@code 2 (K+V) × num_kv_heads × head_dim ×
 * num_layers × dtype_bytes}. For qwen3:32b at FP16 KV cache,
 * that's roughly 256 KiB per token; for qwen3:8b roughly 64 KiB.
 *
 * <h3>v1 (this commit): conservative defaults</h3>
 * Phase D ships a default-per-model-family table. Phase D2 will add
 * an operator-side {@code /api/show} probe to compute the per-model
 * constant precisely from each model's {@code model_info} block, and
 * publish via {@code ProviderState} or a dedicated KV bucket. v1's
 * defaults are deliberately conservative (overestimate slightly) so
 * the gate fails safe — better to under-route than to OOM.
 *
 * <h3>What's NOT modeled</h3>
 * <ul>
 *   <li>Compute graph + activation scratch — typically tens of MiB,
 *       small relative to the rest. Add a flat 256 MiB safety margin.</li>
 *   <li>Per-runner buffers — Ollama allocates these on load, already
 *       reflected in {@code loadedModelFootprintsMiB}.</li>
 *   <li>Output token count beyond {@code maxOutputTokens} — bounded
 *       by config; conservative if the actual output is shorter.</li>
 * </ul>
 */
public final class KvCacheEstimator {

    private KvCacheEstimator() {}

    /** Flat safety margin added to every per-call estimate, in MiB. */
    static final long SAFETY_MARGIN_MIB = 256;

    /**
     * Default KV-cache bytes per token, banded by the model's PARAMETER COUNT
     * parsed from its name (family-agnostic - works for any model whose name
     * carries an "Nb" size token). Values rounded UP to fail safe. Used until an
     * operator-side per-model probe can compute the exact per-model constant from
     * each model's architecture.
     *
     * KV cache per token scales with model depth, which tracks parameter count.
     * Conservative bands (from typical GQA architectures at FP16 KV):
     * <ul>
     *   <li>&gt;= 60B params -> 512 KiB/tok</li>
     *   <li>&gt;= 30B params -> 256 KiB/tok</li>
     *   <li>&gt;= 13B params -> 192 KiB/tok</li>
     *   <li>&gt;=  7B params -> 128 KiB/tok</li>
     *   <li>&lt;   7B params -> 96 KiB/tok</li>
     *   <li>embedding models / no parseable size -> 64-128 KiB/tok</li>
     * </ul>
     */
    public static long bytesPerTokenFor(String modelName) {
        if (modelName == null) return 128L * 1024;  // unknown - middle-ground default
        String m = modelName.toLowerCase();
        long params = extractParamBillions(m);
        if (params <= 0) {
            if (m.contains("embed")) return 64L * 1024;  // embedding models: tiny KV // NOSONAR S2259: m = modelName.toLowerCase() after the null-return guard above, so it is non-null here
            return 128L * 1024;                          // unknown size - middle ground
        }
        // KV cache per token grows with model depth, which tracks the parameter
        // count. Coarse, conservative (rounded UP) bands keyed ONLY on the
        // parameter count parsed from the name - model-family-agnostic, no
        // specific model names baked in. Replace with an operator-side /api/show
        // probe of each model's architecture when tighter accuracy is needed.
        if (params >= 60) return 512L * 1024;
        if (params >= 30) return 256L * 1024;
        if (params >= 13) return 192L * 1024;
        if (params >= 7)  return 128L * 1024;
        return 96L * 1024;  // small (<7B) models
    }

    /**
     * Rough cold-load VRAM occupancy estimate (MiB) from a model name's parameter
     * count, family-agnostic. Used ONLY when no provider has observed the model's
     * real footprint yet (cold start, before the operator's /api/ps or /api/tags
     * publish) so the JIT scheduler can still gate and place it, instead of
     * bypassing to the static endpoint (which skips best-fit/fit entirely). ~800
     * MiB per billion params approximates a q4-quantized loaded footprint plus a
     * small context/runtime allowance; deliberately a touch high so the gate fails
     * safe. Returns 0 when no parameter count can be parsed (caller then degrades
     * to the static endpoint as a last resort). Superseded by the real observed
     * footprint the moment the model loads anywhere.
     */
    public static long estimateColdOccupancyMiB(String modelName) {
        long params = extractParamBillions(modelName);
        if (params <= 0) return 0L;
        return params * 800L + 1024L;
    }

    private static final java.util.regex.Pattern PARAM_PATTERN =
            java.util.regex.Pattern.compile("(\\d+(?:\\.\\d+)?)\\s*b\\b");

    /**
     * Parse the parameter count in billions from a model name, family-agnostic:
     * "qwen3:32b" -> 32, "llama-3.1-70b-instruct" -> 70, "gemma2:9b" -> 9. Returns
     * 0 when no "&lt;number&gt;b" token is present. Takes the largest match so a
     * family-version digit (e.g. the "3" in qwen3) is never mistaken for the size.
     */
    static long extractParamBillions(String modelName) {
        if (modelName == null) return 0;
        java.util.regex.Matcher mt = PARAM_PATTERN.matcher(modelName.toLowerCase());
        long best = 0;
        while (mt.find()) {
            try {
                long v = (long) Math.ceil(Double.parseDouble(mt.group(1)));
                if (v > best) best = v;
            } catch (NumberFormatException ignored) {
                // skip unparseable match
            }
        }
        return best;
    }

    /**
     * Estimate the KV cache VRAM this single call would consume, in MiB.
     * Conservative — uses a per-model bytes-per-token constant and
     * adds a flat safety margin. Caller passes the prompt's token
     * count and the configured generation cap.
     */
    public static long estimateMiB(String modelName, long promptTokens, long maxOutputTokens) {
        if (promptTokens < 0) promptTokens = 0;
        if (maxOutputTokens < 0) maxOutputTokens = 0;
        long bytes = bytesPerTokenFor(modelName) * (promptTokens + maxOutputTokens);
        long mib = (bytes + (1024L * 1024 - 1)) / (1024L * 1024);  // ceil-div to MiB
        return mib + SAFETY_MARGIN_MIB;
    }

    /**
     * Crude prompt token count from raw character length.
     * GPT/LLaMA-family tokenization averages ~4 chars per token in
     * English; this is the standard rough estimate. Off by ~20-30%
     * either way but adequate for VRAM gating where we'd rather
     * overestimate slightly than underestimate. Replace with a real
     * tokenizer (e.g. langchain4j-tokenization or jtokkit) when
     * tighter accuracy matters.
     */
    public static long estimateTokensFromChars(int totalChars) {
        if (totalChars <= 0) return 0;
        // +25% safety pad to fail-safe — token counts on a structured
        // prompt with system + tools + history can run higher than
        // the chars/4 rule of thumb suggests.
        return (long) Math.ceil(totalChars / 4.0 * 1.25);
    }
}
