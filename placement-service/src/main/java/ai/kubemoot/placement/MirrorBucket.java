package ai.kubemoot.placement;

/**
 * The tickets bucket as the mirror uses it: write an entry, remove one, sweep the markers removals
 * leave, and follow every entry from the start. {@link NatsMirrorBucket} is the NATS KV bucket; tests
 * use a fake.
 */
interface MirrorBucket {

    /** One entry the bucket holds or just changed. {@code value} is null for a removal. */
    record Item(String key, byte[] value, long createdAtMs) {}

    /** Receives the bucket's entries: first everything it holds now, then each change. */
    interface Watcher {
        void item(Item item);

        /** All entries held when the watch started have been delivered. */
        void caughtUp();
    }

    /** Writes the entry, replacing any under the key. */
    void put(String key, byte[] value) throws Exception;

    /** Removes the entry. */
    void remove(String key) throws Exception;

    /** Drops the removal markers, so keys that are gone stop being listed. */
    void sweepMarkers() throws Exception;

    /** Follows the bucket until the returned handle is closed. */
    AutoCloseable watch(Watcher watcher) throws Exception;
}
