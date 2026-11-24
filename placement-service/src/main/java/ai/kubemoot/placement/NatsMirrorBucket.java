package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.KvTicketStore;
import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.api.KeyValueOperation;
import io.nats.client.api.KeyValuePurgeOptions;
import io.nats.client.api.KeyValueWatcher;

import java.io.IOException;
import java.util.UUID;

/** {@link MirrorBucket} over the NATS KV tickets bucket. */
final class NatsMirrorBucket implements MirrorBucket {

    private final KeyValue kv;

    NatsMirrorBucket(Connection connection) throws IOException {
        this.kv = connection.keyValue(KvTicketStore.BUCKET);
    }

    @Override
    public void put(String key, byte[] value) throws IOException {
        try {
            kv.put(key, value);
        } catch (JetStreamApiException e) {
            throw new IOException(e.getMessage(), e);
        }
    }

    @Override
    public void remove(String key) throws IOException {
        try {
            kv.delete(key);
        } catch (JetStreamApiException e) {
            throw new IOException(e.getMessage(), e);
        }
    }

    @Override
    public void sweepMarkers() throws IOException {
        try {
            kv.purgeDeletes(KeyValuePurgeOptions.builder().deleteMarkersNoThreshold().build());
        } catch (JetStreamApiException e) {
            throw new IOException(e.getMessage(), e);
        } catch (InterruptedException e) {
            throw interrupted("sweeping the removal markers", e);
        }
    }

    @Override
    public AutoCloseable watch(Watcher watcher) throws IOException {
        try {
            return kv.watchAll(new Adapter(watcher));
        } catch (JetStreamApiException e) {
            throw new IOException(e.getMessage(), e);
        } catch (InterruptedException e) {
            throw interrupted("opening the tickets watch", e);
        }
    }

    private static IOException interrupted(String doing, InterruptedException e) {
        Thread.currentThread().interrupt();
        return new IOException("interrupted while " + doing, e);
    }

    /** Hands each bucket entry to the mirror's watcher; the consumer name is random per replica. */
    private static final class Adapter implements KeyValueWatcher {
        private final Watcher target;
        private final String consumerPrefix = "kubemoot-placement-" + UUID.randomUUID().toString().substring(0, 8);

        Adapter(Watcher target) {
            this.target = target;
        }

        @Override
        public void watch(KeyValueEntry entry) {
            boolean removal = entry.getOperation() != KeyValueOperation.PUT;
            long createdAt = entry.getCreated() == null ? System.currentTimeMillis()
                    : entry.getCreated().toInstant().toEpochMilli();
            target.item(new Item(entry.getKey(), removal ? null : entry.getValue(), createdAt));
        }

        @Override
        public void endOfData() {
            target.caughtUp();
        }

        @Override
        public String getConsumerNamePrefix() {
            return consumerPrefix;
        }
    }
}
