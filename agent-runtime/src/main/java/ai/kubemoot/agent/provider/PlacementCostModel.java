package ai.kubemoot.agent.provider;

import java.util.List;
import java.util.Optional;

/**
 * The one place that decides, when no GPU has room for a call right now, whether
 * the agent queues for a loaded copy, unloads idle models to load its own, or
 * waits for memory to free up. Costs are in seconds of expected delay.
 *
 * <ul>
 *   <li><b>Queue</b>: the expected wait for a slot on a loaded copy of the requested
 *       model or an in-tolerance candidate, from the in-flight calls there and the
 *       recent call latency. No load, no unload.</li>
 *   <li><b>Evict</b>: the requested model's cold-load time plus, for each victim, its
 *       release time and its wake time scaled by its expected near-future requests
 *       ({@link ModelDemand#expectedReloads()}), from the provider's
 *       {@link SwitchCostProfile}. A large model is slow to bring back, so releasing
 *       one that is still wanted is expensive; a cheaper release or faster wake makes
 *       eviction cheaper.</li>
 * </ul>
 *
 * The cheaper option wins; with neither available the agent waits for memory.
 */
public final class PlacementCostModel {

    private PlacementCostModel() {}

    /** Call latency assumed for a provider with no observed calls yet. */
    public static final double DEFAULT_CALL_SECONDS = 30.0;

    /** A queued call on a loaded copy. */
    public record QueueOption(String model, String provider, double waitSeconds) {}

    /** One idle resident model chosen for unloading. */
    public record Victim(String model, long footprintMiB, ModelDemand demand) {}

    /** Unload {@code victims} on {@code provider} to load the requested model; {@code costSeconds} per {@link #evictionCostSeconds}. */
    public record EvictionPlan(String provider, List<Victim> victims, double costSeconds) {
        public List<String> victimModels() {
            return victims.stream().map(Victim::model).toList();
        }
    }

    public enum Action { QUEUE, EVICT, WAIT_FOR_MEMORY }

    public record Decision(Action action, Optional<QueueOption> queue, Optional<EvictionPlan> eviction) {}

    /**
     * The requested model's cold-load time plus, for each victim, the time to
     * release it and its wake time weighted by its expected near-future requests,
     * all on the provider's {@code profile}.
     */
    public static double evictionCostSeconds(SwitchCostProfile profile, long requestedFootprintMiB,
                                             List<Victim> victims) {
        double cost = profile.coldLoadSeconds(requestedFootprintMiB);
        for (Victim v : victims) {
            cost += profile.releaseSeconds() + profile.wakeSeconds(v.footprintMiB()) * v.demand().expectedReloads();
        }
        return cost;
    }

    /** Queue when a loaded copy's wait costs no more than the eviction; evict when only it helps or it is cheaper; else wait for memory. */
    public static Decision decide(Optional<QueueOption> queue, Optional<EvictionPlan> eviction) {
        boolean queueWins = queue.isPresent()
                && (eviction.isEmpty() || queue.get().waitSeconds() <= eviction.get().costSeconds());
        if (queueWins) {
            return new Decision(Action.QUEUE, queue, eviction);
        }
        if (eviction.isPresent()) {
            return new Decision(Action.EVICT, queue, eviction);
        }
        return new Decision(Action.WAIT_FOR_MEMORY, queue, eviction);
    }
}
