package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.TicketManager;
import ai.kubemoot.agent.modelserver.TicketStore;
import com.fasterxml.jackson.databind.JsonNode;
import jakarta.inject.Inject;
import jakarta.inject.Singleton;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.locks.ReentrantLock;
import java.util.function.BooleanSupplier;
import java.util.function.LongSupplier;
import java.util.function.Supplier;

/**
 * The tickets and residency entries, in memory. The leader of the placement service is their single
 * writer: a claim reads the table, checks the budget and writes its ticket as one step under one lock,
 * so two claims can never both fit what fits only one, and nothing needs the post-write verification
 * that claims racing across processes need. The NATS KV bucket is a mirror ({@link TicketMirror}) kept
 * for the readers that only look at it.
 *
 * <p>The table answers only while its replica holds the lease: {@link #available()} is false the
 * moment the lease fence passes, whether or not the replica has noticed yet, so a deposed leader
 * cannot hand out a ticket another leader's table does not know.</p>
 *
 * <p>Every change is published to the listeners <em>inside</em> the lock, so listeners see the changes
 * to a key in the order they happened (a release can never overtake the claim it releases). Listeners
 * must therefore not block.</p>
 *
 * <p>An entry expires when its holder never gives it back: a ticket {@code ticketTtl} after it was
 * created, a residency entry that long after it was last refreshed. This is the same safety net the
 * bucket's TTL is for in-process placement; the normal exit is the release.</p>
 */
@Singleton
public class TicketTable implements TicketStore {

    /** Where a change came from: a caller of this service, or a reader of the mirror taking in a foreign write. */
    public enum Source { LOCAL, FOREIGN }

    /** A change to the table, for the mirror and the capacity signal. {@code doc} is null for a removal. */
    public record Change(String key, JsonNode doc, Source source) {
        public boolean removal() {
            return doc == null;
        }
    }

    /** Receives every change after it is made, under the table's lock; it must not block. */
    public interface Listener {
        void changed(Change change);
    }

    private record Slot(Entry entry, long expiresAtMs) {}

    private final ReentrantLock lock = new ReentrantLock();
    private final TreeMap<String, Slot> slots = new TreeMap<>();
    private final Map<String, String> keyByTicketId = new java.util.HashMap<>();
    private final List<Listener> listeners = new CopyOnWriteArrayList<>();
    private final LongSupplier clock;
    private final long ticketTtlMs;
    private long sequence;
    private volatile boolean active;
    private volatile BooleanSupplier lease = () -> true;

    @Inject
    public TicketTable(ServiceSettings settings) {
        this(settings.ticketTtl().toMillis(), System::currentTimeMillis);
    }

    TicketTable(long ticketTtlMs, LongSupplier clock) {
        this.ticketTtlMs = ticketTtlMs;
        this.clock = clock;
    }

    /** Registers a listener for every change from now on. */
    public void addListener(Listener listener) {
        listeners.add(listener);
    }

    /** The table answers only while {@code held} says this replica still holds the lease. */
    void fenceWith(BooleanSupplier held) {
        this.lease = held;
    }

    /** Opens the table to claims: the leader has recovered what the previous leader left. */
    public void activate() {
        active = true;
    }

    /** Closes the table and forgets everything: this replica is not the leader any more. */
    public void deactivate() {
        active = false;
        lock.lock();
        try {
            slots.clear();
            keyByTicketId.clear();
        } finally {
            lock.unlock();
        }
    }

    @Override
    public boolean available() {
        return active && lease.getAsBoolean();
    }

    @Override
    public long create(String key, JsonNode doc) {
        lock.lock();
        try {
            if (!available() || slots.containsKey(key)) {
                throw new IllegalStateException("cannot create " + key);
            }
            long revision = store(key, doc, clock.getAsLong() + ticketTtlMs);
            publish(new Change(key, doc, Source.LOCAL));
            return revision;
        } finally {
            lock.unlock();
        }
    }

    @Override
    public void put(String key, JsonNode doc) {
        lock.lock();
        try {
            if (!available()) {
                return;
            }
            store(key, doc, clock.getAsLong() + ticketTtlMs);
            publish(new Change(key, doc, Source.LOCAL));
        } finally {
            lock.unlock();
        }
    }

    @Override
    public void delete(String key) {
        lock.lock();
        try {
            remove(key, Source.LOCAL);
        } finally {
            lock.unlock();
        }
    }

    /** The ticket under this id, released: true when it was held. */
    public boolean deleteTicket(String ticketId) {
        lock.lock();
        try {
            String key = keyByTicketId.get(ticketId);
            return key != null && remove(key, Source.LOCAL);
        } finally {
            lock.unlock();
        }
    }

    @Override
    public List<Entry> scan(String prefix) {
        long now = clock.getAsLong();
        List<Entry> out = new ArrayList<>();
        lock.lock();
        try {
            for (Slot s : slots.subMap(prefix, true, prefix + Character.MAX_VALUE, true).values()) {
                if (s.expiresAtMs() > now) {
                    out.add(s.entry());
                }
            }
        } finally {
            lock.unlock();
        }
        return out;
    }

    @Override
    public <T> T exclusive(Supplier<T> action) {
        lock.lock();
        try {
            return action.get();
        } finally {
            lock.unlock();
        }
    }

    // ---- entries from the mirror ----

    /**
     * Takes in an entry another writer put in the bucket (the previous leader's mirror at recovery,
     * or a writer that places in-process or probes accelerators). It is kept only when this table
     * holds nothing under the key: the table is the authority for what it knows.
     *
     * @param createdAtMs when the bucket entry was written; the entry expires that long after
     */
    public boolean ingest(String key, JsonNode doc, long createdAtMs) {
        long expiresAt = createdAtMs + ticketTtlMs;
        if (expiresAt <= clock.getAsLong()) {
            return false;
        }
        lock.lock();
        try {
            if (slots.containsKey(key)) {
                return false;
            }
            store(key, doc, expiresAt);
            publish(new Change(key, doc, Source.FOREIGN));
            return true;
        } finally {
            lock.unlock();
        }
    }

    /** Drops an entry another writer removed from the bucket. */
    public boolean removeForeign(String key) {
        lock.lock();
        try {
            return remove(key, Source.FOREIGN);
        } finally {
            lock.unlock();
        }
    }

    /** True when the table holds an unexpired entry under the key. */
    public boolean holds(String key) {
        lock.lock();
        try {
            Slot s = slots.get(key);
            return s != null && s.expiresAtMs() > clock.getAsLong();
        } finally {
            lock.unlock();
        }
    }

    /** The entries held now, for resynchronizing the mirror. */
    public List<Entry> snapshot() {
        return scan("");
    }

    /** Removes the entries that expired; the keys removed. */
    public List<String> sweep() {
        long now = clock.getAsLong();
        List<String> expired = new ArrayList<>();
        lock.lock();
        try {
            slots.entrySet().removeIf(e -> {
                boolean gone = e.getValue().expiresAtMs() <= now;
                if (gone) {
                    expired.add(e.getKey());
                    forgetTicket(e.getValue().entry());
                }
                return gone;
            });
            expired.forEach(k -> publish(new Change(k, null, Source.LOCAL)));
        } finally {
            lock.unlock();
        }
        return expired;
    }

    /** How many entries the table holds, expired ones not yet swept included. */
    public int size() {
        lock.lock();
        try {
            return slots.size();
        } finally {
            lock.unlock();
        }
    }

    // ---- internals (the lock is held) ----

    private long store(String key, JsonNode doc, long expiresAtMs) {
        long revision = ++sequence;
        Slot previous = slots.put(key, new Slot(new Entry(key, doc, revision), expiresAtMs));
        if (previous != null) {
            forgetTicket(previous.entry());
        }
        String ticketId = doc.path("ticketId").asText(null);
        if (ticketId != null && !key.startsWith(TicketManager.RESIDENCY_PREFIX)) {
            keyByTicketId.put(ticketId, key);
        }
        return revision;
    }

    private boolean remove(String key, Source source) {
        Slot s = slots.remove(key);
        if (s == null) {
            return false;
        }
        forgetTicket(s.entry());
        publish(new Change(key, null, source));
        return true;
    }

    private void forgetTicket(Entry entry) {
        String ticketId = entry.doc().path("ticketId").asText(null);
        if (ticketId != null) {
            keyByTicketId.remove(ticketId, entry.key());
        }
    }

    private void publish(Change change) {
        for (Listener l : listeners) {
            l.changed(change);
        }
    }
}
