package ai.kubemoot.placement;

import jakarta.annotation.PreDestroy;
import jakarta.inject.Inject;
import jakarta.inject.Singleton;

import java.time.Duration;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

/**
 * A change counter over what decides whether a call fits: the tickets, and the server state the
 * operator publishes. A caller whose call found no room asks for the version, and waits until it
 * moves; it retries its placement exactly when the picture changed, not on a timer.
 *
 * <p>A wait is a request that stays open, completed by the next change, so it holds no thread.
 * {@code max} bounds it as a safety net for a caller that vanished; the normal end is a change.</p>
 */
@Singleton
public class CapacityVersion implements TicketTable.Listener {

    private final AtomicLong version = new AtomicLong();
    private final Set<CompletableFuture<Long>> waiters = ConcurrentHashMap.newKeySet();
    /** Wakes the waiters off the thread that made the change, which may hold the table's lock. */
    private final ExecutorService waker = Executors.newSingleThreadExecutor(r -> {
        Thread t = new Thread(r, "capacity-wakeup");
        t.setDaemon(true);
        return t;
    });
    private final AtomicBoolean wakePending = new AtomicBoolean();

    @Inject
    public CapacityVersion(TicketTable table) {
        table.addListener(this);
    }

    /** The current version. */
    public long current() {
        return version.get();
    }

    @Override
    public void changed(TicketTable.Change change) {
        bump();
    }

    /** Advances the version and wakes every waiter, on another thread. */
    public void bump() {
        version.incrementAndGet();
        if (wakePending.compareAndSet(false, true)) {
            waker.execute(() -> {
                wakePending.set(false);
                long now = version.get();
                for (CompletableFuture<Long> w : waiters) {
                    w.complete(now);
                }
            });
        }
    }

    @PreDestroy
    void close() {
        waker.shutdownNow();
    }

    /**
     * Completes with the version once it differs from {@code seen}, or with the current version when
     * {@code max} passes first.
     */
    public CompletionStage<Long> awaitChange(long seen, Duration max) {
        long now = version.get();
        if (now != seen) {
            return CompletableFuture.completedFuture(now);
        }
        CompletableFuture<Long> waiter = new CompletableFuture<>();
        waiters.add(waiter);
        waiter.whenComplete((v, e) -> waiters.remove(waiter));
        // A change between the check above and the registration must not be missed.
        long after = version.get();
        if (after != seen) {
            waiter.complete(after);
        }
        return waiter.completeOnTimeout(version.get(), max.toMillis(), TimeUnit.MILLISECONDS);
    }

    /** How many waits are open. */
    public int waiting() {
        return waiters.size();
    }
}
