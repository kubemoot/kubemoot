package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.JsonNode;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Supplier;

/** A {@link TicketStore} in a map: revisions count up, and it counts how often a claim ran exclusively. */
public final class FakeTicketStore implements TicketStore {

    private final Map<String, Entry> entries = new TreeMap<>();
    private long sequence;
    private boolean available = true;
    /** How many times {@link #exclusive} ran an action. */
    public final AtomicInteger exclusiveRuns = new AtomicInteger();

    /** Marks the store unavailable (or available again). */
    public synchronized void setAvailable(boolean value) {
        available = value;
    }

    /** The keys held now, in key order. */
    public synchronized List<String> keys() {
        return new ArrayList<>(entries.keySet());
    }

    @Override
    public synchronized boolean available() {
        return available;
    }

    @Override
    public synchronized long create(String key, JsonNode doc) {
        if (!available || entries.containsKey(key)) {
            throw new IllegalStateException("cannot create " + key);
        }
        entries.put(key, new Entry(key, doc, ++sequence));
        return sequence;
    }

    @Override
    public synchronized void put(String key, JsonNode doc) {
        if (available) {
            entries.put(key, new Entry(key, doc, ++sequence));
        }
    }

    @Override
    public synchronized void delete(String key) {
        if (available) {
            entries.remove(key);
        }
    }

    @Override
    public synchronized List<Entry> scan(String prefix) {
        List<Entry> out = new ArrayList<>();
        if (available) {
            entries.forEach((k, e) -> {
                if (k.startsWith(prefix)) {
                    out.add(e);
                }
            });
        }
        return out;
    }

    @Override
    public <T> T exclusive(Supplier<T> action) {
        exclusiveRuns.incrementAndGet();
        return action.get();
    }
}
