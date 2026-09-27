package ai.kubemoot.agent.provider;

import java.util.Comparator;

/**
 * Near-future demand for one model across every crew, read from the shared
 * demand bucket.
 *
 * @param waiters     agents waiting for a GPU for this model right now
 * @param intents     agents selected for a live thread that are about to request it
 * @param useRate     decaying count of recent call starts (half-life {@link DemandBoard#USE_HALF_LIFE})
 * @param predicted   decaying selection frequency of agents that name this model, summed
 *                    over the crews that usually select them
 * @param lastUsedMs  epoch millis of the last call start, 0 when never seen
 */
public record ModelDemand(int waiters, int intents, double useRate, double predicted, long lastUsedMs) {

    /** No recorded demand. */
    public static final ModelDemand NONE = new ModelDemand(0, 0, 0.0, 0.0, 0L);

    /** Weight of one intent in {@link #expectedReloads()}: an intent is a near-certain request. */
    static final double INTENT_WEIGHT = 1.0;
    /** Weight of recent use and prediction in {@link #expectedReloads()}. */
    static final double RATE_WEIGHT = 0.5;

    /**
     * Expected number of requests for this model in the near future, the factor
     * that scales the cost of unloading it. Waiting agents count fully, intents
     * almost fully, recent use and prediction at half weight.
     */
    public double expectedReloads() {
        return waiters + INTENT_WEIGHT * intents + RATE_WEIGHT * (useRate + predicted);
    }

    /**
     * Victim order, lowest near-future demand first: fewer waiters, then fewer
     * intents, then a lower recent-use rate plus prediction, then least recently used.
     */
    public static final Comparator<ModelDemand> LEAST_NEEDED_FIRST = Comparator
            .comparingInt(ModelDemand::waiters)
            .thenComparingInt(ModelDemand::intents)
            .thenComparingDouble(d -> d.useRate() + d.predicted())
            .thenComparingLong(ModelDemand::lastUsedMs);
}
