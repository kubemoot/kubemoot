package ai.kubemoot.agent.provider;

import jakarta.enterprise.context.ApplicationScoped;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.Locale;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

/**
 * v2 implementation of {@link FitPredictor} — adds per-{@code (provider,
 * model)} EMA learning on top of the static v1 canServe decision.
 *
 * <h3>What "learning" means here</h3>
 * Every completed inference call feeds {@link #recordOutcome} with a
 * success/failure flag + observed latency. The predictor maintains
 * an exponentially-weighted moving average per (provider, model) pair
 * for both metrics; {@link #predict} returns those averages as the
 * {@link FitScore#successProbability} and
 * {@link FitScore#estimatedLatencyMs} fields.
 *
 * Selector ranking can then prefer reliable providers without making
 * the decision binary. A provider that's been flaky for THIS model
 * gets a lower successProbability and slips down the rank, but isn't
 * hard-excluded (that's the circuit breaker's job for hard wedges).
 *
 * <h3>Bootstrap / cold-start</h3>
 * With zero samples the predictor returns the static result unchanged
 * (optimistic 1.0 probability) so unfamiliar (provider, model) pairs
 * get a fair chance to be tried. Confidence builds with each sample;
 * once {@link #MIN_CONFIDENCE_SAMPLES} are accumulated the predictor
 * starts surfacing the learned EMA values.
 *
 * <h3>Scope of learning</h3>
 * Per-agent, in-memory. Each agent process learns its own observations.
 * Cluster-wide aggregation via NATS would be v3; persistence across
 * restarts would also live there. v2 in-memory is fine because the
 * cluster has 20+ agents and any one's learning loss on restart is
 * dilutional, not catastrophic.
 *
 * <h3>What v2 deliberately does NOT do</h3>
 * <ul>
 *   <li>No KV-cache modeling — Phase D will populate FitInputs'
 *       {@code activeKvCacheReservedMiB} and
 *       {@code thisCallKvCacheEstimateMiB} and add gating logic that
 *       uses them. v2 still ignores those fields.</li>
 *   <li>No exploration policy — every call routes by current best
 *       prediction; a provider that's been bad once won't be
 *       periodically re-probed beyond the half-open circuit-breaker
 *       window. Multi-armed-bandit exploration is a future stage.</li>
 *   <li>No per-context-size segmentation — a prompt with 500 tokens
 *       and one with 20000 tokens contribute to the same EMA. The
 *       Phase D KV-cache work is the right place to add this.</li>
 * </ul>
 */
@ApplicationScoped
public class LearningFitPredictor implements FitPredictor {

    private static final Logger log = LoggerFactory.getLogger(LearningFitPredictor.class);

    /** EMA smoothing factor — α. Higher = react faster to recent samples; lower = more inertia. */
    static final double ALPHA = 0.3;
    /** Below this many samples the predictor reports the static prediction unchanged (cold start). */
    static final int MIN_CONFIDENCE_SAMPLES = 5;

    private final StaticFitPredictor baseline = new StaticFitPredictor();
    /** Per-(provider, model) running EMA state. */
    private final Map<Key, EmaState> learnings = new ConcurrentHashMap<>();

    @Override
    public FitScore predict(FitInputs in) {
        FitScore base = baseline.predict(in);
        // Hard no — circuit open, slot saturated, can't fit, etc. — don't
        // overlay learning. Predict honestly returns the structural no.
        if (!base.canServe() || in == null || in.provider() == null || in.modelName() == null) {
            return base;
        }

        EmaState ema = learnings.get(new Key(in.provider().name(), in.modelName()));
        if (ema == null || ema.sampleCount < MIN_CONFIDENCE_SAMPLES) {
            // Cold-start: return the static optimistic 1.0 + the sample
            // count we have (0 if no key). Caller's sort still sees a
            // valid score; the warm/cold tiebreak carries the decision.
            int samples = ema == null ? 0 : ema.sampleCount;
            return new FitScore(true, 1.0, base.estimatedLatencyMs(), samples,
                    base.reasoning() + " | bootstrap (" + samples + " samples)");
        }

        long latencyMs = (long) ema.latencyMs;
        String reason = String.format(Locale.ROOT,
                "%s | SR=%.2f over %d samples, EMA latency %dms",
                base.reasoning(), ema.successRate, ema.sampleCount, latencyMs);
        return new FitScore(true, ema.successRate, latencyMs, ema.sampleCount, reason);
    }

    @Override
    public void recordOutcome(String provider, String modelName, long latencyMs, boolean success) {
        if (provider == null || provider.isEmpty() || modelName == null || modelName.isEmpty()) return;
        if (latencyMs < 0) latencyMs = 0;  // defensive

        final long lat = latencyMs;
        final double sampleSR = success ? 1.0 : 0.0;
        EmaState updated = learnings.compute(new Key(provider, modelName), (k, prev) -> {
            if (prev == null) {
                // First sample seeds the EMA at the sample value.
                return new EmaState(sampleSR, lat, 1);
            }
            double newSR = ALPHA * sampleSR + (1.0 - ALPHA) * prev.successRate;
            double newLat = ALPHA * lat + (1.0 - ALPHA) * prev.latencyMs;
            return new EmaState(newSR, newLat, prev.sampleCount + 1);
        });
        log.debug("recordOutcome provider={} model={} success={} latencyMs={} → SR={} EMA-lat={}ms samples={}",
                provider, modelName, success, latencyMs,
                String.format(Locale.ROOT, "%.3f", updated.successRate),
                (long) updated.latencyMs, updated.sampleCount);
    }

    /** Test-only accessor for the per-key EMA state. */
    EmaState peekForTest(String provider, String modelName) {
        return learnings.get(new Key(provider, modelName));
    }

    /** Per-(provider, model) sample bucket. Immutable record — compute() replaces atomically. */
    record EmaState(double successRate, double latencyMs, int sampleCount) {}

    /** Composite key for the learnings map. */
    record Key(String provider, String model) {}
}
