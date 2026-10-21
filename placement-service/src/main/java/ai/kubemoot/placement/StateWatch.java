package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.ServerSelector;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.api.KeyValueWatchOption;
import io.nats.client.api.KeyValueWatcher;
import jakarta.inject.Inject;
import jakarta.inject.Singleton;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.UUID;

/**
 * Advances the {@link CapacityVersion} on every change to the server state the operator publishes (a
 * probe reporting new residency, a server going away), so a caller waiting for capacity retries when
 * the picture changes. It carries metadata only and skips the initial replay.
 */
@Singleton
public class StateWatch {

    private static final Logger log = LoggerFactory.getLogger(StateWatch.class);

    private final NatsConnection nats;
    private final CapacityVersion capacity;
    private AutoCloseable subscription;

    @Inject
    public StateWatch(NatsConnection nats, CapacityVersion capacity) {
        this.nats = nats;
        this.capacity = capacity;
    }

    /** Starts watching; a failure is logged and the next election retries. */
    synchronized void start() throws Exception {
        stop();
        subscription = nats.require().keyValue(ServerSelector.STATE_BUCKET)
                .watchAll(new Bump(), KeyValueWatchOption.UPDATES_ONLY, KeyValueWatchOption.META_ONLY);
    }

    /** Stops watching. */
    synchronized void stop() {
        if (subscription == null) {
            return;
        }
        try {
            subscription.close();
        } catch (Exception e) {
            log.debug("Closing the server state watch failed: {}", e.getMessage());
        }
        subscription = null;
    }

    private final class Bump implements KeyValueWatcher {
        private final String consumerPrefix = "kubemoot-placement-state-" + UUID.randomUUID().toString().substring(0, 8);

        @Override
        public void watch(KeyValueEntry entry) {
            capacity.bump();
        }

        @Override
        public void endOfData() {
            // UPDATES_ONLY: nothing is replayed.
        }

        @Override
        public String getConsumerNamePrefix() {
            return consumerPrefix;
        }
    }
}
