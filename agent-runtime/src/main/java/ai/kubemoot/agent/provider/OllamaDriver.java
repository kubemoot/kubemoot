package ai.kubemoot.agent.provider;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/**
 * Ollama: several models resident per GPU, a fixed number of parallel slots per
 * provider (maxParallel, from OLLAMA_NUM_PARALLEL), release by unloading
 * ({@link OllamaModelControl#unload}), and a released model reloads from disk at the
 * same speed as a cold load.
 */
@ApplicationScoped
public class OllamaDriver implements EngineDriver {

    /** About 1 GiB per second from local disk into GPU memory. */
    static final double LOAD_MIB_PER_SECOND = 1024.0;
    /** An unload request returns in about a second. */
    static final double RELEASE_SECONDS = 1.0;

    static final SwitchCostProfile PROFILE =
            new SwitchCostProfile(LOAD_MIB_PER_SECOND, RELEASE_SECONDS, LOAD_MIB_PER_SECOND);

    private final OllamaModelControl control;

    @Inject
    public OllamaDriver(OllamaModelControl control) {
        this.control = control;
    }

    @Override
    public SwitchCostProfile costProfile() {
        return PROFILE;
    }

    @Override
    public boolean hasFreeCapacity(ProviderState provider, int inFlightCalls) {
        return inFlightCalls < Math.max(1, provider.maxParallel());
    }

    @Override
    public double expectedWaitSeconds(ProviderState provider, List<Double> inFlightElapsedSeconds, double callSeconds) {
        return slotWaitSeconds(inFlightElapsedSeconds, provider.maxParallel(), callSeconds);
    }

    /**
     * Seconds until one of {@code slots} slots frees: zero with a slot free; else the
     * soonest in-flight call's remaining time, plus one call latency per call already
     * queued beyond the slots, spread across the slots.
     */
    static double slotWaitSeconds(List<Double> elapsedSeconds, int slots, double callSeconds) {
        int s = Math.max(1, slots);
        if (elapsedSeconds.size() < s) {
            return 0.0;
        }
        List<Double> remaining = new ArrayList<>();
        for (double e : elapsedSeconds) {
            remaining.add(Math.max(0.0, callSeconds - e));
        }
        Collections.sort(remaining);
        int queuedBeyondSlots = elapsedSeconds.size() - s;
        return remaining.get(0) + queuedBeyondSlots * callSeconds / s;
    }

    @Override
    public void warm(String endpoint, String model) {
        if (control != null) {
            control.startLoad(endpoint, model);
        }
    }

    @Override
    public boolean release(String endpoint, String model) {
        return control != null && control.unload(endpoint, model);
    }
}
