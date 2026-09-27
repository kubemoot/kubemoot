package ai.kubemoot.agent.provider;

import java.time.Duration;

/**
 * A change counter over the scheduler's shared state. Every change to provider
 * state or tickets advances {@link #version()}; a waiter records the version,
 * retries its pick, and then blocks in {@link #awaitChange} until the version
 * moves past what it saw.
 */
public interface CapacitySignal {

    /** Starts watching when not yet watching. False when the watch cannot be established. */
    boolean ensureWatching();

    /** The current change count. */
    long version();

    /**
     * Blocks until {@link #version()} differs from {@code seen} or {@code max}
     * elapses. Returns true when a change arrived.
     */
    boolean awaitChange(long seen, Duration max) throws InterruptedException;

    /** Advances the version without a KV change, waking every waiter to re-check. */
    void nudge();
}
