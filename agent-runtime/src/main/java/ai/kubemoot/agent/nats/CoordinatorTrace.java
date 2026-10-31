package ai.kubemoot.agent.nats;

import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * What the coordinator needs to remember about writing one thread's log: whether the
 * thread has been announced, which round it is in, and how many times each phase has
 * been entered in the round. These give an event its natural id, so the same decision
 * made twice has the same id.
 */
final class CoordinatorTrace {

    private volatile boolean opened;
    private final AtomicInteger round = new AtomicInteger();
    private final Map<String, Integer> phaseEntries = new ConcurrentHashMap<>();

    private final Object lock = new Object();

    /**
     * Held while an event is built and handed to the log, so the events of one thread
     * reach the stream in the order they were built, whichever thread builds them.
     */
    Object lock() {
        return lock;
    }

    /** True once {@code thread_opened} was written; events for a thread never opened are not written. */
    boolean opened() {
        return opened;
    }

    void markOpened() {
        opened = true;
    }

    /** The round: 0 until a human reply reopens the thread. */
    int round() {
        return round.get();
    }

    /** Starts the next round and returns its number. */
    int nextRound() {
        return round.incrementAndGet();
    }

    /** Counts an entry into {@code phase} in the current round; the first entry is 1. */
    int enter(String phase) {
        return phaseEntries.merge(round.get() + ":" + phase, 1, Integer::sum);
    }
}
