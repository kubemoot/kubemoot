package ai.kubemoot.agent.provider;

import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;

/** {@link CapacityWait} that records what the waiter announced. */
public class RecordingCapacityWait implements CapacityWait {

    public final List<String> waiting = new CopyOnWriteArrayList<>();
    public final List<String> capacity = new CopyOnWriteArrayList<>();
    private final List<Runnable> wakers = new CopyOnWriteArrayList<>();
    private volatile boolean ended;
    private volatile Runnable onWaitingHook = () -> { };

    /** Runs {@code hook} each time the waiter announces it is waiting. */
    public RecordingCapacityWait whenWaiting(Runnable hook) {
        this.onWaitingHook = hook;
        return this;
    }

    /** Ends the thread and runs every registered wake-up. */
    public void endThread() {
        ended = true;
        wakers.forEach(Runnable::run);
    }

    @Override public boolean threadEnded() { return ended; }
    @Override public void onWaiting(String model) { waiting.add(model); onWaitingHook.run(); }
    @Override public void onCapacity(String model) { capacity.add(model); }
    @Override public void wakeOnEnd(Runnable wake) { wakers.add(wake); }
}
