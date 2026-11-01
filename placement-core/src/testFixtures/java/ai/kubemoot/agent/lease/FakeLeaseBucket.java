package ai.kubemoot.agent.lease;

import java.io.IOException;
import java.util.HashMap;
import java.util.Map;
import java.util.function.LongSupplier;

/**
 * An in-memory lease bucket with the semantics of the NATS KV one: create fails while the key
 * exists, update and delete are compare-and-set on the revision, and an entry that was not written
 * within the TTL is gone. {@link #storeFor} gives each replica its own handle on the shared bucket.
 */
public final class FakeLeaseBucket {

    private record Entry(String holder, long revision, long writtenAt) {
    }

    private final LongSupplier nanos;
    private final long ttlNanos;
    private final Map<String, Entry> entries = new HashMap<>();
    private long sequence;
    private volatile boolean unreachable;

    public FakeLeaseBucket(LongSupplier nanos, long ttlNanos) {
        this.nanos = nanos;
        this.ttlNanos = ttlNanos;
    }

    /** While true every call fails as it does when NATS cannot be reached. */
    public void setUnreachable(boolean value) {
        unreachable = value;
    }

    /** Who holds {@code key} now, or null when nobody does. */
    public synchronized String holder(String key) {
        Entry entry = live(key);
        return entry == null ? null : entry.holder();
    }

    /** A handle on the bucket; every handle sees the same entries. */
    public LeaseStore storeFor() {
        return new LeaseStore() {
            @Override
            public long acquire(String key, String holder) throws IOException {
                synchronized (FakeLeaseBucket.this) {
                    check();
                    return live(key) != null ? NOT_HELD : write(key, holder);
                }
            }

            @Override
            public long renew(String key, String holder, long revision) throws IOException {
                synchronized (FakeLeaseBucket.this) {
                    check();
                    Entry entry = live(key);
                    return entry == null || entry.revision() != revision ? NOT_HELD : write(key, holder);
                }
            }

            @Override
            public void release(String key, long revision) throws IOException {
                synchronized (FakeLeaseBucket.this) {
                    check();
                    Entry entry = live(key);
                    if (entry != null && entry.revision() == revision) {
                        entries.remove(key);
                    }
                }
            }
        };
    }

    private Entry live(String key) {
        Entry entry = entries.get(key);
        if (entry != null && nanos.getAsLong() - entry.writtenAt() >= ttlNanos) {
            entries.remove(key);
            return null;
        }
        return entry;
    }

    private long write(String key, String holder) {
        long revision = ++sequence;
        entries.put(key, new Entry(holder, revision, nanos.getAsLong()));
        return revision;
    }

    private void check() throws IOException {
        if (unreachable) {
            throw new IOException("NATS unreachable");
        }
    }
}
