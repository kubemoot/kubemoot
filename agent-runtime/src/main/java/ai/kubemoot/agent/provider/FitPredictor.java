package ai.kubemoot.agent.provider;

/**
 * Modular per-call fit/health predictor. Answers one question at JIT
 * decision time for a candidate {@code (provider, model)} pair: how
 * likely is the call to serve well, with what latency, and why?
 *
 * Composes the existing structural inputs (warm vs cold, slot
 * saturation, circuit state) with — eventually — learned per-(provider,
 * model) success-rate and latency history. v1 (the static impl) covers
 * only the structural inputs; v2 adds per-agent EMA learning behind
 * the same interface.
 *
 * Plugs into {@link ProviderSelector#rankCandidates} as the
 * canServe/score function — replaces the previous inline warm/cold
 * gate. See {@code kubemoot/docs/scheduler.md}.
 *
 * <h3>Two-call contract</h3>
 * <ul>
 *   <li>{@link #predict(FitInputs)} — pure, no side effects. Called
 *       per candidate per selection.</li>
 *   <li>{@link #recordOutcome} — fire after each completed call so the
 *       predictor learns. No-op in v1.</li>
 * </ul>
 */
public interface FitPredictor {

    /**
     * Predict the {@link FitScore} for one candidate provider/model
     * pair. Pure function — no NATS reads, no state mutation. The
     * selector calls this once per candidate per selection and uses
     * {@code score.canServe()} as a hard filter and the other fields
     * for ranking.
     */
    FitScore predict(FitInputs inputs);

    /**
     * Record the actual outcome of an inference call. Caller is
     * {@code ChatService}'s finally block (the same site that already
     * feeds the circuit breaker via
     * {@link ProviderSelector#recordSuccess}/{@link ProviderSelector#recordFailure}).
     *
     * No-op in v1 — learning lands in v2.
     */
    void recordOutcome(String provider, String modelName, long latencyMs, boolean success);
}
