package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.lease.KvLeaseStore;
import ai.kubemoot.agent.lease.LeaseStore;
import org.eclipse.microprofile.config.ConfigProvider;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.function.Consumer;
import java.util.function.LongSupplier;
import java.util.function.Predicate;
import java.util.function.Supplier;

/**
 * The leases that let more than one coordinator replica run: one per thread a replica owns, and
 * one for the crew's request pump. A lease is a key in the {@value #BUCKET} KV bucket (the chart
 * creates it with a TTL equal to {@link #LEASE}). A replica takes a lease with a create, renews it
 * every {@link #RENEW_EVERY} with a compare-and-set, and gives it up when the thread closes or the
 * replica stops. A replica that dies stops renewing, and the bucket drops the key within the TTL.
 *
 * <p>The lease only avoids wasted work: the fenced append to the thread's log decides who may act.
 * A holder that has not renewed within the fence ({@code 2/3} of the lease, before the bucket can
 * hand the key to anyone else) no longer {@link #holds} the lease, and the orchestrator stops
 * acting on the thread at its next step, whether or not the renewal pass has run.
 *
 * <p>Only the map of held leases is guarded by the monitor. Every call to the store runs outside
 * it, so a slow bucket delays the caller that asked and nobody else.
 */
final class CoordinatorLeases {

    /** The setting that turns leases on: environment variable {@code KUBEMOOT_COORDINATOR_LEASE}. */
    static final String CONFIG_KEY = "kubemoot.coordinator.lease";
    /** The KV bucket the chart creates for the leases. */
    static final String BUCKET = "kubemoot_coordinator_leases";
    /** The name of the crew's request pump lease; no thread id looks like it. */
    static final String PUMP = "request-pump";
    /** How long a lease lives without a renewal: the TTL of the bucket. */
    static final Duration LEASE = Duration.ofSeconds(15);
    static final Duration RENEW_EVERY = LEASE.dividedBy(3);
    private static final Duration FENCE = LEASE.multipliedBy(2).dividedBy(3);

    private static final Logger log = LoggerFactory.getLogger(CoordinatorLeases.class);

    /** Opens the lease store, or fails when NATS is not reachable. */
    interface StoreSource {
        LeaseStore open() throws Exception;
    }

    private record Held(long revision, long renewedAtNanos, long takenAtNanos) {
    }

    private final boolean enabled;
    private final StoreSource storeSource;
    private final Supplier<String> keyPrefix;
    private final String holder;
    private final LongSupplier nanoClock;
    private final Predicate<String> stillNeeded;
    private final Consumer<String> onLost;
    private final Map<String, Held> held = new HashMap<>();
    private volatile LeaseStore store;

    /**
     * @param keyPrefix   goes before a name to make the key (the namespace and crew, so crews do not meet)
     * @param holder      who this replica is: its pod name
     * @param stillNeeded false for a thread that closed: its lease is given up at the next renewal
     * @param onLost      told the name of a lease this replica no longer holds, so it stops acting on it
     */
    CoordinatorLeases(StoreSource storeSource, Supplier<String> keyPrefix, String holder, LongSupplier nanoClock,
                      Predicate<String> stillNeeded, Consumer<String> onLost) {
        this(true, storeSource, keyPrefix, holder, nanoClock, stillNeeded, onLost);
    }

    private CoordinatorLeases(boolean enabled, StoreSource storeSource, Supplier<String> keyPrefix, String holder,
                              LongSupplier nanoClock, Predicate<String> stillNeeded, Consumer<String> onLost) {
        this.enabled = enabled;
        this.storeSource = storeSource;
        this.keyPrefix = keyPrefix;
        this.holder = holder;
        this.nanoClock = nanoClock;
        this.stillNeeded = stillNeeded;
        this.onLost = onLost;
    }

    /** Leases switched off: every thread is owned by the one replica, as before. */
    static CoordinatorLeases off() {
        return new CoordinatorLeases(false, null, () -> "", "", System::nanoTime, name -> true, name -> { });
    }

    /** The leases for this runtime over the KV bucket, held as this pod. */
    static CoordinatorLeases forRuntime(NatsConnectionProvider natsProvider, Predicate<String> stillNeeded,
                                        Consumer<String> onLost) {
        Supplier<String> prefix = () -> {
            CrewScope scope = natsProvider.scope();
            return scope.namespace() + "." + (scope.hasCrew() ? scope.crew() + "." : "");
        };
        return new CoordinatorLeases(() -> {
            var conn = natsProvider.getConnection();
            if (conn == null) {
                throw new java.io.IOException("no NATS connection");
            }
            return new KvLeaseStore(conn, BUCKET);
        }, prefix, podName(), System::nanoTime, stillNeeded, onLost);
    }

    /** True when the leases setting is on. */
    static boolean configured() {
        return ConfigProvider.getConfig().getOptionalValue(CONFIG_KEY, Boolean.class).orElse(false);
    }

    private static String podName() {
        String host = System.getenv("HOSTNAME");
        return host == null || host.isBlank() ? "coordinator-" + UUID.randomUUID() : host;
    }

    boolean enabled() {
        return enabled;
    }

    /** Renews the leases every {@link #RENEW_EVERY} on {@code scheduler}. */
    void start(ScheduledExecutorService scheduler) {
        if (enabled) {
            scheduler.scheduleWithFixedDelay(this::tick, RENEW_EVERY.toMillis(), RENEW_EVERY.toMillis(),
                    TimeUnit.MILLISECONDS);
        }
    }

    /**
     * Takes the lease called {@code name} unless another holder has it. True when this replica
     * holds it afterwards, including when it already did.
     */
    boolean acquire(String name) {
        if (!enabled || holds(name)) {
            return true;
        }
        long sentAt = nanoClock.getAsLong();
        try {
            long revision = store().acquire(key(name), holder);
            if (revision == LeaseStore.NOT_HELD) {
                return false;
            }
            synchronized (this) {
                held.put(name, new Held(revision, sentAt, sentAt));
            }
            return true;
        } catch (Exception e) {
            log.warn("Taking the lease {} failed: {}", name, e.getMessage());
            store = null;
            return false;
        }
    }

    /** True while this replica holds {@code name} and has renewed it within the fence. Always true when leases are off. */
    synchronized boolean holds(String name) {
        if (!enabled) {
            return true;
        }
        Held lease = held.get(name);
        return lease != null && nanoClock.getAsLong() - lease.renewedAtNanos() < FENCE.toNanos();
    }

    /** Gives {@code name} up if this replica holds it. */
    void release(String name) {
        Held lease;
        synchronized (this) {
            lease = held.remove(name);
        }
        if (!enabled || lease == null) {
            return;
        }
        try {
            store().release(key(name), lease.revision());
        } catch (Exception e) {
            log.warn("Releasing the lease {} failed (it lapses by itself): {}", name, e.getMessage());
            store = null;
        }
    }

    /** Gives every lease up, so another replica takes the threads at once instead of after the TTL. */
    void releaseAll() {
        names().forEach(this::release);
    }

    private synchronized List<String> names() {
        return new ArrayList<>(held.keySet());
    }

    /**
     * One renewal pass: renews what is still needed, gives up what is not, drops what was lost and
     * reports it after the pass. A lease taken less than one renewal period ago is renewed whatever
     * {@code stillNeeded} says: its thread may not be registered yet.
     */
    void tick() {
        List<String> lost = new ArrayList<>();
        for (String name : names()) {
            Held lease;
            synchronized (this) {
                lease = held.get(name);
            }
            if (lease == null) {
                continue;
            }
            if (isNeeded(name, lease)) {
                renew(name, lease, lost);
            } else {
                release(name);
            }
        }
        lost.forEach(onLost);
    }

    private boolean isNeeded(String name, Held lease) {
        return PUMP.equals(name) || nanoClock.getAsLong() - lease.takenAtNanos() < RENEW_EVERY.toNanos()
                || stillNeeded.test(name);
    }

    private void renew(String name, Held lease, List<String> lost) {
        long sentAt = nanoClock.getAsLong();
        try {
            long revision = store().renew(key(name), holder, lease.revision());
            synchronized (this) {
                if (revision == LeaseStore.NOT_HELD) {
                    forget(name, lease, "another replica holds it", lost);
                } else if (held.get(name) == lease) {
                    held.put(name, new Held(revision, sentAt, lease.takenAtNanos()));
                }
            }
        } catch (Exception e) {
            log.warn("Renewing the lease {} failed: {}", name, e.getMessage());
            store = null;
            if (sentAt - lease.renewedAtNanos() >= FENCE.toNanos()) {
                synchronized (this) {
                    forget(name, lease, "it was not renewed within " + FENCE.toSeconds() + "s", lost);
                }
            }
        }
    }

    /** Drops {@code lease} if it is still the one held under {@code name}, and queues the report. The caller holds the monitor. */
    private void forget(String name, Held lease, String why, List<String> lost) {
        if (held.get(name) == lease) {
            held.remove(name);
            log.warn("Lease {} is lost: {}", name, why);
            lost.add(name);
        }
    }

    private String key(String name) {
        return keyPrefix.get() + name;
    }

    private LeaseStore store() throws Exception {
        LeaseStore current = store;
        if (current == null) {
            current = storeSource.open();
            store = current;
        }
        return current;
    }
}
