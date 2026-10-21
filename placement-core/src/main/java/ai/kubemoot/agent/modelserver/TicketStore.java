package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.JsonNode;

import java.util.List;
import java.util.function.Supplier;

/**
 * Where {@link TicketManager} keeps tickets and residency entries: documents under string keys,
 * each with the revision the store gave it when it was created, so concurrent claims can be
 * ordered.
 *
 * <p>Two stores exist. {@link KvTicketStore} keeps them in a NATS KV bucket that every agent
 * process reads and writes (the in-process placement). The placement service keeps them in memory
 * as the single writer and mirrors them to the bucket for the readers that only watch it.</p>
 */
public interface TicketStore {

    /** One stored document and the revision its create was given. */
    record Entry(String key, JsonNode doc, long revision) {}

    /** True when the store can be read and written now; false means callers fall back, never block. */
    boolean available();

    /**
     * Writes a new entry and returns its revision.
     *
     * @throws RuntimeException when the key exists or the write fails
     */
    long create(String key, JsonNode doc);

    /** Writes the entry, replacing any under the key. Best-effort: a failed write is logged and dropped. */
    void put(String key, JsonNode doc);

    /** Removes the entry. Idempotent and best-effort: a failed removal is logged and dropped. */
    void delete(String key);

    /**
     * The readable entries whose key starts with {@code prefix}. An entry that cannot be read is
     * left out (better to under-count than to fail a headroom calculation); a store that cannot be
     * read at all answers empty after logging why.
     */
    List<Entry> scan(String prefix);

    /**
     * Runs {@code action} so that no other claim on this store runs at the same moment, when the
     * store can promise that. A store shared with other processes cannot, and runs it at once; the
     * claim protocol verifies after the write instead.
     */
    default <T> T exclusive(Supplier<T> action) {
        return action.get();
    }
}
