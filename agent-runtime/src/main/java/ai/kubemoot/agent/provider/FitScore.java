package ai.kubemoot.agent.provider;

/**
 * Per-call prediction for whether a {@code (ProviderState, model)}
 * pair will serve this inference well, plus the supporting numbers a
 * dashboard or operator can read to understand why.
 *
 * Produced by {@link FitPredictor#predict}; consumed by
 * {@link ProviderSelector#rankCandidates} to filter and rank
 * candidate providers at JIT decision time.
 *
 * v1 (2026-05-28, this commit) is the structural refactor: same inputs,
 * same outcomes, new shape. {@code successProbability} is 1.0 when
 * {@code canServe} is true and 0.0 otherwise; {@code sampleSize} is 0
 * everywhere because no learning data exists yet. v2 layers a per-agent
 * EMA learning loop behind the same interface — see
 * {@code kubemoot/docs/scheduler.md} for the staged plan.
 *
 * <h3>Fields</h3>
 * <ul>
 *   <li>{@code canServe} — hard filter. False means the provider is
 *       excluded from {@code rankCandidates} entirely (no fit, circuit
 *       open, not ready, etc.).</li>
 *   <li>{@code successProbability} — soft signal in [0.0, 1.0]. Used
 *       for ranking and (eventually) for explore/exploit trade-offs.
 *       In v1 collapses to 1.0 / 0.0 because there's no learning yet.</li>
 *   <li>{@code estimatedLatencyMs} — best current guess for the call
 *       duration; the v2.1 observed-latency EMA value in v1.</li>
 *   <li>{@code sampleSize} — how many observations went into the
 *       prediction. Confidence indicator. {@code 0} == no data.</li>
 *   <li>{@code reasoning} — human-readable for dashboards, logs, and
 *       per-call attribution in agent signals. Examples:
 *       <i>"warm, slot 1/2"</i>, <i>"cold-load fits 14000 ≤ 32605"</i>,
 *       <i>"circuit open until 19:30:45"</i>.</li>
 * </ul>
 */
public record FitScore(
        boolean canServe,
        double successProbability,
        long estimatedLatencyMs,
        int sampleSize,
        String reasoning
) {
    /** Convenience for "no" responses with a reason. */
    public static FitScore no(String reasoning) {
        return new FitScore(false, 0.0, 0L, 0, reasoning);
    }

    /** Convenience for v1 "yes" responses (probability collapses to 1.0). */
    public static FitScore yes(long estimatedLatencyMs, String reasoning) {
        return new FitScore(true, 1.0, estimatedLatencyMs, 0, reasoning);
    }
}
