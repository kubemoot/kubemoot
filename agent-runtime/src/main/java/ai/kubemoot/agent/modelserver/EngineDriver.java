package ai.kubemoot.agent.modelserver;

import java.util.List;

/**
 * The model lifecycle actions the scheduler takes on a server, and how the
 * server behaves under contention and model switching. The planner and the cost
 * model reason only through this interface: load, release, and wake costs, and a
 * concurrency signal. {@link OllamaDriver} is the implementation for Ollama;
 * {@link OperatorLifecycleDriver} serves vLLM by asking the operator to wake and sleep its models.
 */
public interface EngineDriver {

    /** Cold-load, release, and wake costs. */
    SwitchCostProfile costProfile();

    /** True when a new call can start on the server now without queueing behind others. */
    boolean hasFreeCapacity(ServerState server, int inFlightCalls);

    /**
     * Expected seconds until a new call can start on the server, given how long
     * each in-flight call has been running and the recent call latency.
     */
    double expectedWaitSeconds(ServerState server, List<Double> inFlightElapsedSeconds, double callSeconds);

    /** Starts making {@code model} ready for a planned call; does not wait for it. */
    void warm(String endpoint, String model);

    /** Releases a resident model's memory. Returns true when the server confirmed it. */
    boolean release(String endpoint, String model);

    /**
     * {@link #warm(String, String)} for a model on {@code server}: the server is named,
     * for a driver that asks the operator, which finds the server by name.
     */
    default void warm(ServerState server, String model) {
        warm(server.endpointFor(model), model);
    }

    /** {@link #release(String, String)} for a model on {@code server}. */
    default boolean release(ServerState server, String model) {
        return release(server.endpointFor(model), model);
    }

    /**
     * Makes {@code model} ready on {@code server} and returns when the server reports
     * it so, before the call is sent. Ollama loads on demand when the first request
     * arrives, so the default does nothing; a driver for an engine that keeps models asleep
     * waits here.
     *
     * @return true when the driver asked the server to act, so server state read before
     *         is out of date; false when there was nothing to do
     * @throws ModelNotReadyException when the server did not make the model ready
     */
    default boolean ensureReady(ServerState server, String model) {
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
