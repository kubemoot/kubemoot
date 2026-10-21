package ai.kubemoot.placement;

import jakarta.annotation.PreDestroy;
import jakarta.inject.Inject;
import jakarta.inject.Singleton;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.function.LongSupplier;

/**
 * One replica at a time is the placement service's leader and its single writer of tickets. The
 * leader is the holder of a lease in the NATS KV leader bucket, the same system that holds the
 * mirror: a replica that cannot reach NATS can neither renew the lease nor mirror a ticket, so it
 * must not keep answering, and it does not.
 *
 * <p>The protocol, with lease L (equal to the bucket's TTL):</p>
 * <ul>
 *   <li>A follower tries to create the lease every L/3. The create fails while a leader's entry exists.</li>
 *   <li>The leader renews it every L/3 with a compare-and-set on the revision it last wrote, so a
 *       renewal can never overwrite another holder's lease.</li>
 *   <li>The bucket removes an entry that is not renewed for L. A crashed leader's lease therefore
 *       lapses after at most L, and a follower takes it within another L/3.</li>
 *   <li><b>Self-fencing.</b> The leader treats itself as deposed once 2L/3 pass without a successful
 *       renewal, whatever it believes about NATS. That is before the bucket can hand the lease to
 *       anyone else, so two replicas never answer as leader at once. {@link #isLeader()} applies this
 *       on every request.</li>
 *   <li>A leader that shuts down gives the lease up, so a follower takes over within L/3 instead of L.</li>
 * </ul>
 *
 * <p>A replica becomes the leader (and starts answering) only after {@link Callbacks#elected} has
 * recovered the tickets the previous leader left in the mirror.</p>
 */
@Singleton
public class Leadership {

    private static final Logger log = LoggerFactory.getLogger(Leadership.class);

    /** What the service does when this replica gains or loses the lease. */
    interface Callbacks {
        /** Recovers the previous leader's state; a failure here gives the lease back. */
        void elected() throws Exception;

        /**
         * Stops answering and forgets the state. {@code leaseLost} is true when the lease may already
         * be another replica's, so nothing more may be written to the mirror; false on a clean
         * shutdown, where what is queued can still be written before the lease is handed over.
         */
        void demoted(boolean leaseLost);
    }

    /** Opens the lease store, or fails when NATS is not reachable. */
    interface StoreSource {
        LeaseStore open() throws Exception;
    }

    private final ServiceSettings settings;
    private final StoreSource storeSource;
    private final LongSupplier nanoClock;
    private final Duration lease;
    private final Duration fence;
    private Callbacks callbacks = new Callbacks() {
        @Override public void elected() { /* nothing until the service registers */ }
        @Override public void demoted(boolean leaseLost) { /* nothing until the service registers */ }
    };
    private LeaseStore store;
    private ScheduledExecutorService ticker;
    private volatile boolean leader;
    private volatile long lastRenewNanos;
    private long revision;

    @Inject
    public Leadership(ServiceSettings settings, NatsConnection nats) {
        this(settings, () -> new KvLeaseStore(nats.require()), System::nanoTime);
    }

    Leadership(ServiceSettings settings, StoreSource storeSource, LongSupplier nanoClock) {
        this.settings = settings;
        this.storeSource = storeSource;
        this.nanoClock = nanoClock;
        this.lease = settings.lease();
        this.fence = lease.multipliedBy(2).dividedBy(3);
    }

    /** Registers the service's reactions; call before {@link #start}. */
    void register(Callbacks callbacks) {
        this.callbacks = callbacks;
    }

    /** Starts contending for the lease. */
    public void start() {
        long periodMs = Math.max(250, lease.toMillis() / 3);
        ticker = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "placement-leadership");
            t.setDaemon(true);
            return t;
        });
        ticker.scheduleWithFixedDelay(this::tick, 0, periodMs, TimeUnit.MILLISECONDS);
    }

    /** True while this replica holds the lease and has renewed it within the fence. */
    public boolean isLeader() {
        return leader && nanoClock.getAsLong() - lastRenewNanos < fence.toNanos();
    }

    /** One step of the protocol; the ticker calls it every L/3. */
    synchronized void tick() {
        try {
            if (leader) {
                keep();
            } else {
                contend();
            }
        } catch (RuntimeException e) {
            log.warn("Leadership step failed: {}", e.getMessage());
        }
    }

    private void keep() {
        if (nanoClock.getAsLong() - lastRenewNanos >= fence.toNanos()) {
            stepDown("the lease was not renewed within " + fence.toSeconds() + "s");
            return;
        }
        // The bucket's clock for this lease starts when it processes the write, which is after the
        // time taken here: stamp the lease with the time the request was sent, never the time the
        // answer came back, so a slow answer shortens the fence instead of extending it.
        long sentAt = nanoClock.getAsLong();
        try {
            long renewed = store().renew(settings.holderId(), revision);
            if (renewed == LeaseStore.NOT_HELD) {
                stepDown("another replica holds the lease");
                return;
            }
            revision = renewed;
            lastRenewNanos = sentAt;
        } catch (Exception e) {
            // The fence ends this leader if renewals keep failing.
            log.warn("Renewing the lease failed: {}", e.getMessage());
            store = null;
        }
    }

    private void contend() {
        try {
            long sentAt = nanoClock.getAsLong();
            long acquired = store().acquire(settings.holderId());
            if (acquired == LeaseStore.NOT_HELD) {
                return;
            }
            revision = acquired;
            lastRenewNanos = sentAt;
            callbacks.elected();
            // The lease is not renewed while the previous leader's state is recovered. A recovery that
            // outlasts the fence ends with a lease another replica may already hold: do not serve.
            if (nanoClock.getAsLong() - sentAt >= fence.toNanos()) {
                log.warn("Recovery took longer than the lease fence; giving the lease back");
                giveBack();
                return;
            }
            leader = true;
            log.info("This replica is now the placement leader");
        } catch (Exception e) {
            log.warn("Taking over as leader failed: {}", e.getMessage());
            giveBack();
        }
    }

    private LeaseStore store() throws Exception {
        if (store == null) {
            store = storeSource.open();
        }
        return store;
    }

    private void stepDown(String why) {
        log.warn("This replica is no longer the placement leader: {}", why);
        leader = false;
        callbacks.demoted(true);
        store = null;
    }

    /** Releases a lease taken but not served (recovery failed), so a follower can try. */
    private void giveBack() {
        leader = false;
        try {
            callbacks.demoted(true);
            if (store != null) {
                store.release(revision);
            }
        } catch (Exception e) {
            log.warn("Giving the lease back failed: {}", e.getMessage());
        }
        store = null;
    }

    /** Stops contending and, when leading, hands the lease over at once. */
    @PreDestroy
    synchronized void shutdown() {
        if (ticker != null) {
            ticker.shutdownNow();
        }
        if (!leader) {
            return;
        }
        // The drain runs while this replica still counts as leader, so the fenced mirror writes the queue.
        try {
            callbacks.demoted(false);
        } finally {
            leader = false;
        }
        try {
            if (store != null) {
                store.release(revision);
            }
            log.info("Gave the placement lease up for a quick handover");
        } catch (Exception e) {
            log.warn("Releasing the lease failed (it lapses by itself): {}", e.getMessage());
        }
    }
}
