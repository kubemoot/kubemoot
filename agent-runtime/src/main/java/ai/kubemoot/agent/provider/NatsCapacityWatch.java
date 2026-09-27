package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.nats.NatsConnectionProvider;
import io.nats.client.Connection;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.api.KeyValueWatchOption;
import io.nats.client.api.KeyValueWatcher;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;

/**
 * {@link CapacitySignal} backed by NATS KV watches on the provider-state and
 * ticket buckets. The operator's provider probes, and every ticket claim and
 * release, arrive as KV updates; each one advances the version, so a waiting
 * agent retries its pick exactly when the GPU picture changes.
 *
 * <p>The watches start on first use and stay open for the life of the process.
 * They carry metadata only (no values) and skip the initial replay.</p>
 */
@ApplicationScoped
public class NatsCapacityWatch implements CapacitySignal {

    private static final Logger log = LoggerFactory.getLogger(NatsCapacityWatch.class);

    private final NatsConnectionProvider natsProvider;
    /** Guards the version counter; watch callbacks take only this lock. */
    private final Object monitor = new Object();
    /** Guards watch setup, kept apart from {@link #monitor} so a callback never waits on setup. */
    private final Object setupLock = new Object();
    private final List<AutoCloseable> subscriptions = new ArrayList<>();
    private long version;
    private volatile boolean watching;

    @Inject
    public NatsCapacityWatch(NatsConnectionProvider natsProvider) {
        this.natsProvider = natsProvider;
    }

    @Override
    public boolean ensureWatching() {
        synchronized (setupLock) {
            if (watching) {
                return true;
            }
            Connection conn = natsProvider == null ? null : natsProvider.getConnection();
            if (conn == null) {
                return false;
            }
            try {
                watchBucket(conn, ProviderSelector.STATE_BUCKET);
                watchBucket(conn, TicketManager.TICKETS_BUCKET);
                watching = true;
                log.info("Watching {} and {} for GPU capacity changes",
                        ProviderSelector.STATE_BUCKET, TicketManager.TICKETS_BUCKET);
            } catch (Exception e) {
                closeSubscriptions();
                log.warn("Cannot watch GPU capacity buckets: {}", e.getMessage());
            }
            return watching;
        }
    }

    private void watchBucket(Connection conn, String bucket) throws Exception {
        subscriptions.add(conn.keyValue(bucket).watchAll(new BumpOnChange(),
                KeyValueWatchOption.UPDATES_ONLY, KeyValueWatchOption.META_ONLY));
    }

    private void closeSubscriptions() {
        for (AutoCloseable c : subscriptions) {
            try {
                c.close();
            } catch (Exception e) {
                log.debug("Closing capacity watch failed: {}", e.getMessage());
            }
        }
        subscriptions.clear();
    }

    @Override
    public long version() {
        synchronized (monitor) {
            return version;
        }
    }

    @Override
    public boolean awaitChange(long seen, Duration max) throws InterruptedException {
        long deadline = System.nanoTime() + max.toNanos();
        synchronized (monitor) {
            while (version == seen) {
                long remainingMs = (deadline - System.nanoTime()) / 1_000_000L;
                if (remainingMs <= 0) {
                    return false;
                }
                monitor.wait(remainingMs);
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

    /**
     * Consumer name prefix for this process's watches, random per process at startup,
     * so a watch consumer can never share a name with another process's consumer even
     * if jnats's generated names repeat across processes (a second guard beside the
     * native build initializing NUID at run time).
     */
    private final String consumerPrefix = "kubemoot-capacity-" + java.util.UUID.randomUUID().toString().substring(0, 8);

    /** Advances the version on every KV update the watch delivers. */
    private final class BumpOnChange implements KeyValueWatcher {
        @Override
        public void watch(KeyValueEntry entry) {
            nudge();
        }

        @Override
        public String getConsumerNamePrefix() {
            return consumerPrefix;
        }

        @Override
        public void endOfData() {
            // UPDATES_ONLY: nothing is replayed, so there is no initial data to finish.
        }
    }
}
