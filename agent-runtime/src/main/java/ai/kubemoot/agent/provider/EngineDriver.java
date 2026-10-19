package ai.kubemoot.agent.provider;

import java.util.List;

/**
 * The model lifecycle actions the scheduler takes on a provider, and how the
 * provider behaves under contention and model switching. The planner and the cost
 * model reason only through this interface: load, release, and wake costs, and a
 * concurrency signal. {@link OllamaDriver} is the implementation for Ollama;
 * {@link OperatorLifecycleDriver} serves vLLM by asking the operator to wake and sleep its models.
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

    /**
     * {@link #warm(String, String)} for a model on {@code provider}: the provider is named,
     * for a driver that asks the operator, which finds the provider by name.
     */
    default void warm(ProviderState provider, String model) {
        warm(provider.endpointFor(model), model);
    }

    /** {@link #release(String, String)} for a model on {@code provider}. */
    default boolean release(ProviderState provider, String model) {
        return release(provider.endpointFor(model), model);
    }

    /**
     * Makes {@code model} ready on {@code provider} and returns when the provider reports
     * it so, before the call is sent. Ollama loads on demand when the first request
     * arrives, so the default does nothing; a driver for an engine that keeps models asleep
     * waits here.
     *
     * @return true when the driver asked the provider to act, so provider state read before
     *         is out of date; false when there was nothing to do
     * @throws ModelNotReadyException when the provider did not make the model ready
     */
    default boolean ensureReady(ProviderState provider, String model) {
        return false; // load on demand: the call itself loads the model
    }

    /**
     * Whether this driver can free a resident model. A driver that cannot (an engine with
     * no runtime driver) is never planned an eviction, because the memory would be
     * recorded as free while the server still holds it.
     */
    default boolean canRelease() {
        return true;
    }
}
