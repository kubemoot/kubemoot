package ai.kubemoot.agent.lease;

import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.api.KeyValueOperation;

import java.io.IOException;
import java.nio.charset.StandardCharsets;

/**
 * {@link LeaseStore} over a NATS KV bucket that the chart creates with a TTL equal to the lease and
 * history 1. Taking a lease is a create, which fails while the key exists; renewing and releasing
 * are compare-and-set on the last revision, which fail when another holder has written since.
 */
public final class KvLeaseStore implements LeaseStore {

    /** JetStream answers a write whose expected last sequence does not match with this error code. */
    private static final int WRONG_LAST_SEQUENCE = 10071;

    private final KeyValue kv;

    public KvLeaseStore(Connection connection, String bucket) throws IOException {
        this.kv = connection.keyValue(bucket);
    }

    @Override
    public long acquire(String key, String holder) throws IOException, JetStreamApiException {
        try {
            return kv.create(key, holder.getBytes(StandardCharsets.UTF_8));
        } catch (JetStreamApiException e) {
            if (lostRace(e)) {
                return NOT_HELD;
            }
            throw e;
        }
    }

    @Override
    public long renew(String key, String holder, long revision) throws IOException, JetStreamApiException {
        try {
            return kv.update(key, holder.getBytes(StandardCharsets.UTF_8), revision);
        } catch (JetStreamApiException e) {
            if (lostRace(e)) {
                return NOT_HELD;
            }
            throw e;
        }
    }

    @Override
    public void release(String key, long revision) throws IOException, JetStreamApiException {
        try {
            kv.delete(key, revision);
        } catch (JetStreamApiException e) {
            if (!lostRace(e)) {
                throw e;
            }
        }
    }

    @Override
    public boolean isHeld(String key) throws IOException, JetStreamApiException {
        KeyValueEntry entry = kv.get(key);
        return entry != null && entry.getOperation() == KeyValueOperation.PUT;
    }

    /** True when the write lost because the key exists or changed, not because JetStream failed. */
    private static boolean lostRace(JetStreamApiException e) {
        return e.getApiErrorCode() == WRONG_LAST_SEQUENCE;
    }
}
