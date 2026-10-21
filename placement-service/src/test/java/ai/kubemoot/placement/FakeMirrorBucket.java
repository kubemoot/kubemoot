package ai.kubemoot.placement;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * A {@link MirrorBucket} in a map. {@link #watch} replays what the bucket holds (and the removal
 * markers) and then reports every later write, as the NATS watch does. Writes can be made to fail.
 */
final class FakeMirrorBucket implements MirrorBucket {

    final Map<String, byte[]> entries = new LinkedHashMap<>();
    private final Map<String, Long> createdAt = new LinkedHashMap<>();
    final List<String> removedKeys = new ArrayList<>();
    final AtomicInteger markerSweeps = new AtomicInteger();
    private final List<Watcher> watchers = new ArrayList<>();
    private int failingWrites;
    private boolean neverCatchUp;

    /** An entry already in the bucket, as the previous leader's mirror left it. */
    synchronized void seed(String key, byte[] value) {
        seed(key, value, System.currentTimeMillis());
    }

    /** An entry written at {@code createdAtMs}. */
    synchronized void seed(String key, byte[] value, long createdAtMs) {
        entries.put(key, value);
        createdAt.put(key, createdAtMs);
    }

    /** The next {@code n} writes throw. */
    synchronized void failNextWrites(int n) {
        failingWrites = n;
    }

    /** The watch never reports that it has caught up. */
    synchronized void neverCatchUp() {
        neverCatchUp = true;
    }

    /** Another writer's change: a put, or a removal when {@code value} is null. */
    synchronized void foreignWrite(String key, byte[] value, long createdAtMs) {
        if (value == null) {
            entries.remove(key);
        } else {
            entries.put(key, value);
        }
        watchers.forEach(w -> w.item(new Item(key, value, createdAtMs)));
    }

    @Override
    public synchronized void put(String key, byte[] value) throws Exception {
        failIfAsked();
        entries.put(key, value);
        long now = System.currentTimeMillis();
        watchers.forEach(w -> w.item(new Item(key, value, now)));
    }

    @Override
    public synchronized void remove(String key) throws Exception {
        failIfAsked();
        entries.remove(key);
        removedKeys.add(key);
        long now = System.currentTimeMillis();
        watchers.forEach(w -> w.item(new Item(key, null, now)));
    }

    @Override
    public void sweepMarkers() {
        markerSweeps.incrementAndGet();
    }

    @Override
    public synchronized AutoCloseable watch(Watcher watcher) {
        long now = System.currentTimeMillis();
        entries.forEach((k, v) -> watcher.item(new Item(k, v, createdAt.getOrDefault(k, now))));
        if (!neverCatchUp) {
            watcher.caughtUp();
        }
        watchers.add(watcher);
        return () -> {
            synchronized (this) {
                watchers.remove(watcher);
            }
        };
    }

    private void failIfAsked() throws Exception {
        if (failingWrites > 0) {
            failingWrites--;
            throw new java.io.IOException("write failed");
        }
    }

    synchronized int watching() {
        return watchers.size();
    }
}
