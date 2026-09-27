package ai.kubemoot.agent.provider;

import java.time.Duration;
import java.util.concurrent.atomic.AtomicInteger;

/** In-memory {@link CapacitySignal} for tests: {@link #nudge()} stands in for a KV change. */
public class FakeCapacitySignal implements CapacitySignal {

    private final Object monitor = new Object();
    private final boolean watchable;
    private long version;
    /** How many times a waiter blocked for a change. */
    public final AtomicInteger awaits = new AtomicInteger();

    public FakeCapacitySignal() {
        this(true);
    }

    public FakeCapacitySignal(boolean watchable) {
        this.watchable = watchable;
    }

    @Override
    public boolean ensureWatching() {
        return watchable;
    }

    @Override
    public long version() {
        synchronized (monitor) {
            return version;
        }
    }

    @Override
    public boolean awaitChange(long seen, Duration max) throws InterruptedException {
        awaits.incrementAndGet();
        long deadline = System.nanoTime() + max.toNanos();
        synchronized (monitor) {
            while (version == seen) {
                long ms = (deadline - System.nanoTime()) / 1_000_000L;
                if (ms <= 0) {
                    return false;
                }
                monitor.wait(ms);
            }
            return true;
        }
    }

    @Override
    public void nudge() {
        synchronized (monitor) {
            version++;
            monitor.notifyAll();
        }
    }
}
