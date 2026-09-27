package ai.kubemoot.agent.provider;

/**
 * The caller's side of a wait for GPU capacity. The discussion subscriber
 * implements it per thread: it knows whether the thread has ended, publishes
 * the {@code waiting} signal, and wakes the waiter when the thread ends.
 */
public interface CapacityWait {

    /** True once the discussion this call serves has ended (synthesis, close, cancel). */
    boolean threadEnded();

    /** Called once when the agent starts waiting for {@code model}. */
    void onWaiting(String model);

    /** Called when a wait that announced itself ends with a claimed GPU for {@code model}. */
    void onCapacity(String model);

    /** Registers {@code wake} to run when the thread ends, so a waiter notices at once. */
    void wakeOnEnd(Runnable wake);

    /** A call that must not wait: it gives up at once when no GPU has room. */
    CapacityWait NONE = new CapacityWait() {
        @Override public boolean threadEnded() { return true; }
        @Override public void onWaiting(String model) { /* never waits */ }
        @Override public void onCapacity(String model) { /* never waits */ }
        @Override public void wakeOnEnd(Runnable wake) { /* never waits */ }
    };
}
