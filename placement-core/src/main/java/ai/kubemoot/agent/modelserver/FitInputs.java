package ai.kubemoot.agent.modelserver;

/**
 * Plain bag of inputs the {@link FitPredictor} needs to evaluate one
 * candidate {@code (server, model)} pair. Kept as a record so
 * predictor implementations stay pure and testable — no implicit
 * dependencies on selector state or NATS connections.
 *
 * <h3>What VRAM really costs per call</h3>
 * Model-weight size is only part of the picture. Every in-flight
 * inference also consumes KV-cache VRAM that scales linearly with its
 * (prompt + generated) token count. On a tight-fit server (e.g. a
 * 32B model loaded on a 24 GiB card) the KV cache for two concurrent
 * 8K-token calls can be the difference between "serves both" and
 * "Ollama wedges with no logged error" — see thread d61647e9-… 2026-05-28.
 *
 * So a thorough fit prediction needs:
 * <ol>
 *   <li>Weights size (the {@code coldLoadFootprintMiB} field)</li>
 *   <li>KV cache for this call ({@code thisCallKvCacheEstimateMiB})</li>
 *   <li>KV cache already reserved by in-flight calls
 *       ({@code activeKvCacheReservedMiB})</li>
 *   <li>Whether the model is currently loading on this server via an
 *       in-flight ticket ({@code activeModelsOnServer}) — converges
 *       concurrent selectors on an in-flight cold-load instead of
 *       redundantly cold-loading on other servers</li>
 * </ol>
 *
 * <h3>v1 vs v2+ usage</h3>
 * The {@code thisCall*} and {@code active*KvCache*} fields are added
 * to FitInputs in v1 (this commit) so the record's shape doesn't
 * break when v2 starts using them. v1 callers may pass {@code 0L} for
 * all KV-cache fields — the {@link StaticFitPredictor} v1 impl falls
 * back to model-weights-only sizing (the v2.2 cold-load gate).
 * v2 will populate these from agent-side token counts and an operator-
 * published per-model {@code kvBytesPerToken} constant derived from
 * {@code /api/show} model metadata.
 *
 * @param server                  ModelServer live state (NATS-published)
 * @param modelName                 LLM the call wants to use
 * @param activeTicketCount         in-flight tickets on this server; drives the slot gate
 * @param activeKvCacheReservedMiB  sum of KV-cache VRAM already pledged by in-flight tickets ({@code 0L} if unknown — v1)
 * @param coldLoadFootprintMiB      model weights size if cold-loading
 * @param thisCallKvCacheEstimateMiB KV cache this call would need given its prompt + max-output token count ({@code 0L} if unknown — v1)
 * @param observedLatencyMs         per-agent EMA latency for this server's endpoint
 * @param circuitOpen               true if this server's circuit breaker is currently open
 * @param activeModelsOnServer    set of model names currently held by active tickets on this server — a model in this set is loaded OR being cold-loaded by an in-flight call. {@code null} or empty falls back to "model-load-in-flight not tracked" (pre-2026-05-31 behaviour). Added for [[Cold-Start Model-Load Wedge]].
 */
public record FitInputs(
        ServerState server,
        String modelName,
        int activeTicketCount,
        long activeKvCacheReservedMiB,
        long coldLoadFootprintMiB,
        long thisCallKvCacheEstimateMiB,
        double observedLatencyMs,
        boolean circuitOpen,
        java.util.Set<String> activeModelsOnServer
) {
    /** Backwards-compat constructor for callers that predate the activeModelsOnServer field. */
    public FitInputs(ServerState server, String modelName, int activeTicketCount,
                     long activeKvCacheReservedMiB, long coldLoadFootprintMiB,
                     long thisCallKvCacheEstimateMiB, double observedLatencyMs, boolean circuitOpen) {
        this(server, modelName, activeTicketCount, activeKvCacheReservedMiB,
                coldLoadFootprintMiB, thisCallKvCacheEstimateMiB, observedLatencyMs,
                circuitOpen, java.util.Set.of());
    }

    /**
     * True when the model is loaded on the server OR an active ticket
     * for this (server, model) pair indicates an in-flight cold-load.
     * Selectors should treat both cases the same — picking a warm-OR-
     * loading server over a truly-cold alternative converges concurrent
     * agents on a single cold-load instead of triggering parallel loads.
     */
    public boolean modelWarmOrLoading() {
        if (server != null && server.hasModelLoaded(modelName)) return true;
        return activeModelsOnServer != null
                && modelName != null
                && activeModelsOnServer.contains(modelName);
    }
}
