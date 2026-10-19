package ai.kubemoot.agent.provider;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * The driver for an engine the runtime has no model lifecycle driver for (vLLM today).
 * {@code warm} and {@code release} do nothing, so the runtime never sends Ollama's load
 * and unload calls to another engine's server; models on such a provider stay as they
 * are until the operator carries out the wake and sleep. Cost questions come from the
 * base driver. Capacity is the engine's own: such a server batches requests and has no
 * fixed slots, so it has room while it holds no waiting request and its cache is not
 * nearly full ({@link ServedModelFit}), whatever {@code maxParallel} says.
 */
final class NoLifecycleDriver implements EngineDriver {

    private static final Logger log = LoggerFactory.getLogger(NoLifecycleDriver.class);

    private final EngineDriver base;
    private final String engine;
    private final AtomicInteger logged = new AtomicInteger();

    NoLifecycleDriver(EngineDriver base, String engine) {
        this.base = base;
        this.engine = engine;
    }

    @Override
    public SwitchCostProfile costProfile() {
        return base.costProfile();
    }

    @Override
    public boolean hasFreeCapacity(ProviderState provider, int inFlightCalls) {
        return ServedModelFit.hasFreeCapacity(provider);
    }

    @Override
    public double expectedWaitSeconds(ProviderState provider, List<Double> inFlightElapsedSeconds, double callSeconds) {
        return ServedModelFit.expectedWaitSeconds(provider, inFlightElapsedSeconds, callSeconds);
    }

    @Override
    public void warm(String endpoint, String model) {
        noteOnce();
    }

    @Override
    public boolean release(String endpoint, String model) {
        noteOnce();
        return false;
    }

    @Override
    public boolean canRelease() {
        return false;
    }

    /** How many times the no-op was reported (at most one); read by tests. */
    int reports() {
        return logged.get();
    }

    private void noteOnce() {
        if (logged.compareAndSet(0, 1)) {
            log.info("No runtime model driver for engine '{}': not loading or releasing models on it", engine);
        }
    }
}
