package ai.kubemoot.agent.modelserver;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.List;
import java.util.concurrent.Executor;
import java.util.concurrent.Executors;

/**
 * The driver for vLLM, an engine that keeps its models asleep until a call needs them. The
 * operator accepts requests for vLLM servers only.
 * The runtime never wakes or sleeps a server itself and never sends Ollama's load and
 * unload calls to another engine's server: it asks the operator, whose engine adapter is
 * the one place that wakes and sleeps ({@link ModelLifecycle}).
 *
 * <ul>
 *   <li>{@link #ensureReady} asks and waits until the server reports the model awake,
 *       before the call is sent.</li>
 *   <li>{@link #warm(ServerState, String)} asks without waiting, so a wake overlaps an
 *       agent's triage; the call's own {@code ensureReady} then returns at once.</li>
 *   <li>{@link #release(ServerState, String)} asks and waits until the model sleeps.</li>
 * </ul>
 *
 * <p>Cost questions come from the base driver. Capacity is the engine's own: such a server
 * batches requests and has no fixed slots, so it has room while it holds no waiting request
 * and its cache is not nearly full ({@link ServedModelFit}), whatever {@code maxParallel} says.</p>
 */
final class OperatorLifecycleDriver implements EngineDriver {

    private static final Logger log = LoggerFactory.getLogger(OperatorLifecycleDriver.class);

    private final EngineDriver base;
    private final String engine;
    private final ModelLifecycle lifecycle;
    private final Executor background;

    OperatorLifecycleDriver(EngineDriver base, String engine, ModelLifecycle lifecycle, Executor background) {
        this.base = base;
        this.engine = engine;
        this.lifecycle = lifecycle;
        this.background = background;
    }

    /**
     * Background requests each block until the operator answers, so they run on their own
     * threads, not on the common pool other work shares.
     */
    private static final Executor BACKGROUND = Executors.newCachedThreadPool(r -> {
        Thread t = new Thread(r, "model-lifecycle-warm");
        t.setDaemon(true);
        return t;
    });

    /** A driver whose background requests run on {@link #BACKGROUND}. */
    OperatorLifecycleDriver(EngineDriver base, String engine, ModelLifecycle lifecycle) {
        this(base, engine, lifecycle, BACKGROUND);
    }

    /** A driver with no way to reach the operator: every request fails. */
    OperatorLifecycleDriver(EngineDriver base, String engine) {
        this(base, engine, new ModelLifecycle() {
            @Override
            public Reply ready(String server, String model) {
                return Reply.failure("no operator connection");
            }

            @Override
            public Reply release(String server, String model) {
                return Reply.failure("no operator connection");
            }
        });
    }

    @Override
    public SwitchCostProfile costProfile() {
        return base.costProfile();
    }

    @Override
    public boolean hasFreeCapacity(ServerState server, int inFlightCalls) {
        return ServedModelFit.hasFreeCapacity(server);
    }

    @Override
    public double expectedWaitSeconds(ServerState server, List<Double> inFlightElapsedSeconds, double callSeconds) {
        return ServedModelFit.expectedWaitSeconds(server, inFlightElapsedSeconds, callSeconds);
    }

    /** Without a server the operator cannot be asked: nothing is sent to the server. */
    @Override
    public void warm(String endpoint, String model) {
        log.debug("Not warming {} for engine '{}': the request names no server", model, engine);
    }

    @Override
    public boolean release(String endpoint, String model) {
        log.debug("Not releasing {} for engine '{}': the request names no server", model, engine);
        return false;
    }

    @Override
    public void warm(ServerState server, String model) {
        background.execute(() -> {
            ModelLifecycle.Reply reply = lifecycle.ready(server.name(), model);
            if (!reply.ok()) {
                log.info("Warming {} on {} failed: {}", model, server.name(), reply.error());
            }
        });
    }

    @Override
    public boolean release(ServerState server, String model) {
        ModelLifecycle.Reply reply = lifecycle.release(server.name(), model);
        if (!reply.ok()) {
            log.info("Releasing {} on {} failed: {}", model, server.name(), reply.error());
        }
        return reply.ok();
    }

    @Override
    public boolean ensureReady(ServerState server, String model) {
        ModelLifecycle.Reply reply = lifecycle.ready(server.name(), model);
        if (!reply.ok()) {
            throw new ModelNotReadyException("server " + server.name() + " could not make " + model
                    + " ready: " + reply.error());
        }
        return true;
    }
}
