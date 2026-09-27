package ai.kubemoot.agent.provider;

import java.util.List;

/**
 * The model lifecycle actions the scheduler takes on a provider, and how the
 * provider behaves under contention and model switching. The planner and the cost
 * model reason only through this interface: load, release, and wake costs, and a
 * concurrency signal. {@link OllamaDriver} is the implementation.
 */
public interface EngineDriver {

    /** Cold-load, release, and wake costs. */
    SwitchCostProfile costProfile();

    /** True when a new call can start on the provider now without queueing behind others. */
    boolean hasFreeCapacity(ProviderState provider, int inFlightCalls);

    /**
     * Expected seconds until a new call can start on the provider, given how long
     * each in-flight call has been running and the recent call latency.
     */
    double expectedWaitSeconds(ProviderState provider, List<Double> inFlightElapsedSeconds, double callSeconds);

    /** Starts making {@code model} ready for a planned call; does not wait for it. */
    void warm(String endpoint, String model);

    /** Releases a resident model's memory. Returns true when the provider confirmed it. */
    boolean release(String endpoint, String model);
}
