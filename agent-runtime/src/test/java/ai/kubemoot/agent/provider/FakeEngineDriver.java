package ai.kubemoot.agent.provider;

import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;

/** A lifecycle driver with a configurable cost profile that records warm and release calls. */
public class FakeEngineDriver implements EngineDriver {

    public final List<String> warmed = new CopyOnWriteArrayList<>();
    public final List<String> released = new CopyOnWriteArrayList<>();
    private final SwitchCostProfile profile;
    private volatile Runnable onRelease = () -> { };

    public FakeEngineDriver(SwitchCostProfile profile) {
        this.profile = profile;
    }

    public FakeEngineDriver whenReleased(Runnable hook) {
        this.onRelease = hook;
        return this;
    }

    @Override public SwitchCostProfile costProfile() { return profile; }
    @Override public boolean hasFreeCapacity(ProviderState p, int inFlight) { return inFlight < Math.max(1, p.maxParallel()); }
    @Override public double expectedWaitSeconds(ProviderState p, List<Double> elapsed, double callSeconds) {
        return OllamaDriver.slotWaitSeconds(elapsed, p.maxParallel(), callSeconds);
    }
    @Override public void warm(String endpoint, String model) { warmed.add(model); }
    @Override public boolean release(String endpoint, String model) {
        released.add(model);
        onRelease.run();
        return true;
    }
}
