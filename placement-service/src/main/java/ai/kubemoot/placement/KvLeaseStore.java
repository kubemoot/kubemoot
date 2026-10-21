package ai.kubemoot.placement;

import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.KeyValue;

import java.nio.charset.StandardCharsets;

/**
 * {@link LeaseStore} over the NATS KV bucket {@value #BUCKET} (the chart creates it with a TTL equal to
 * the lease, history 1). Taking the lease is a create, which fails while the key exists; renewing and
 * releasing are compare-and-set on the last revision, which fail when another holder has written since.
 */
final class KvLeaseStore implements LeaseStore {

    static final String BUCKET = "kubemoot_placement_leader";
    private static final String KEY = "leader";
    /** JetStream answers a write whose expected last sequence does not match with this error code. */
    private static final int WRONG_LAST_SEQUENCE = 10071;

    private final KeyValue kv;

    KvLeaseStore(Connection connection) throws Exception {
        this.kv = connection.keyValue(BUCKET);
    }

    @Override
    public long acquire(String holder) throws Exception {
        try {
            return kv.create(KEY, holder.getBytes(StandardCharsets.UTF_8));
        } catch (JetStreamApiException e) {
            if (lostRace(e)) {
                return NOT_HELD;
            }
            throw e;
        }
    }

    @Override
    public long renew(String holder, long revision) throws Exception {
        try {
            return kv.update(KEY, holder.getBytes(StandardCharsets.UTF_8), revision);
        } catch (JetStreamApiException e) {
            if (lostRace(e)) {
                return NOT_HELD;
            }
            throw e;
        }
    }

    @Override
    public void release(long revision) throws Exception {
        try {
            kv.delete(KEY, revision);
        } catch (JetStreamApiException e) {
            if (!lostRace(e)) {
                throw e;
            }
        }
    }

    /** True when the write lost because the key exists or changed, not because JetStream failed. */
    private static boolean lostRace(JetStreamApiException e) {
        return e.getApiErrorCode() == WRONG_LAST_SEQUENCE;
    }
}
