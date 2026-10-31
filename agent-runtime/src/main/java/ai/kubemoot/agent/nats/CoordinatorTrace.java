package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.List;
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

    /** The most events kept for the check at close; a thread past it is not checked. */
    static final int MAX_KEPT_EVENTS = 2048;

    private long lastSequence;
    private volatile boolean fenced;
    private boolean overflowed;
    private final List<CoordinatorEvent> kept = new ArrayList<>();
    private final Map<String, String> lastEventIds = new ConcurrentHashMap<>();
    private volatile String unloggedAt;

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

    /** The sequence of the last event this writer stored on the thread's subject; 0 before the first. */
    long lastSequence() {
        synchronized (lock) {
            return lastSequence;
        }
    }

    /** Records an event the log acknowledged at {@code sequence}. */
    void appended(CoordinatorEvent event, long sequence) {
        synchronized (lock) {
            lastSequence = sequence;
            lastEventIds.put(event.type(), event.eventId());
            if (kept.size() < MAX_KEPT_EVENTS) {
                kept.add(event);
            } else {
                overflowed = true;
            }
        }
    }

    /**
     * An append failed: the thread's log is incomplete from {@code eventId} on, so no later
     * event is appended (it would mislead a resume) and the thread is not resumable.
     * Returns true for the first call only, so the loss is reported once per thread.
     */
    boolean markUnlogged(String eventId) {
        synchronized (lock) {
            if (unloggedAt != null) {
                return false;
            }
            unloggedAt = eventId;
            return true;
        }
    }

    /** True once an append failed for this thread. */
    boolean unlogged() {
        return unloggedAt != null;
    }

    /** The id of the event of {@code type} written last, or null when none was. */
    String lastEventId(String type) {
        return lastEventIds.get(type);
    }

    /** Another writer owns the thread: nothing more is written or done for it. */
    void fence() {
        fenced = true;
    }

    boolean fenced() {
        return fenced;
    }

    /** The events the log acknowledged, in order; null when there were too many to keep. */
    List<CoordinatorEvent> kept() {
        synchronized (lock) {
            return overflowed ? null : List.copyOf(kept);
        }
    }
}
