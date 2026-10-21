package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.TicketManager;
import ai.kubemoot.placement.MirrorBucket.Item;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.inject.Inject;
import jakarta.inject.Singleton;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import java.util.function.BooleanSupplier;
import java.util.function.LongSupplier;

/**
 * Keeps the NATS KV tickets bucket a mirror of {@link TicketTable}, and brings the table up from the
 * bucket when this replica becomes the leader.
 *
 * <ul>
 *   <li><b>Write-behind.</b> Each change to the table is queued and written to the bucket by one
 *       thread, so a claim never waits for NATS. The dashboard, kmctl and the scheduler read the
 *       bucket, and keep working, a few milliseconds behind.</li>
 *   <li><b>Recovery.</b> At start the mirror follows the bucket from its beginning and takes in every
 *       entry the previous leader left, so a handover loses no ticket.</li>
 *   <li><b>Foreign writers.</b> The same watch carries on afterwards. An entry written by someone else
 *       (an agent still placing in-process, accelerator-mcp's calibration probes) is taken into the
 *       table, so both kinds of caller see each other's claims while agents move over.</li>
 *   <li><b>Fencing.</b> The writer writes only while the lease is held ({@link #fenceWith}). A replica
 *       that lost the lease drops its queue, because the next leader may already have recovered from
 *       the bucket and must not find an old leader's writes behind it.</li>
 *   <li><b>Repair.</b> The mirror knows which keys the bucket holds (from the watch). After a write was
 *       dropped or failed it rewrites what the table holds and removes what the bucket holds and the
 *       table does not, so neither a lost claim nor a lost release outlives the repair.</li>
 *   <li><b>Markers.</b> A removal leaves a delete marker in the bucket. The mirror sweeps them every
 *       {@link #MARKER_SWEEP}, so the bucket stays as small as the live entries and the readers that
 *       list it do not pay for released tickets.</li>
 * </ul>
 */
@Singleton
public class TicketMirror implements TicketTable.Listener {

    private static final Logger log = LoggerFactory.getLogger(TicketMirror.class);

    static final Duration MARKER_SWEEP = Duration.ofSeconds(30);
    static final Duration EXPIRY_SWEEP = Duration.ofSeconds(5);
    private static final int QUEUE_CAPACITY = 20_000;
    private static final int WRITE_ATTEMPTS = 3;

    /** Opens the bucket, or fails when NATS is not reachable. */
    interface BucketSource {
        MirrorBucket open() throws Exception;
    }

    private record Op(String key, byte[] value) {}

    private final TicketTable table;
    private final BucketSource source;
    private final ObjectMapper mapper;
    private final long mintedMemoryMs;
    private final LongSupplier clock;
    private final BlockingQueue<Op> queue = new ArrayBlockingQueue<>(QUEUE_CAPACITY);
    /** Ticket keys this replica created, for a while after: their echo in the watch is not a foreign write. */
    private final Map<String, Long> minted = new ConcurrentHashMap<>();
    /** The keys the bucket holds, as the watch last reported them. */
    private final Set<String> inBucket = ConcurrentHashMap.newKeySet();
    private final AtomicBoolean dirty = new AtomicBoolean();
    private final AtomicLong written = new AtomicLong();
    private final AtomicLong failed = new AtomicLong();
    private final AtomicLong dropped = new AtomicLong();
    private volatile BooleanSupplier lease = () -> true;
    private volatile boolean running;
    private MirrorBucket bucket;
    private AutoCloseable watch;
    private Thread writer;
    private ScheduledExecutorService housekeeping;

    @Inject
    public TicketMirror(TicketTable table, NatsConnection nats, ObjectMapper mapper, ServiceSettings settings) {
        this(table, () -> new NatsMirrorBucket(nats.require()), mapper, settings.ticketTtl().toMillis() * 2,
                System::currentTimeMillis);
        table.addListener(this);
    }

    TicketMirror(TicketTable table, BucketSource source, ObjectMapper mapper, long mintedMemoryMs,
                 LongSupplier clock) {
        this.table = table;
        this.source = source;
        this.mapper = mapper;
        this.mintedMemoryMs = mintedMemoryMs;
        this.clock = clock;
    }

    /** The mirror writes only while {@code held} says this replica still holds the lease. */
    void fenceWith(BooleanSupplier held) {
        this.lease = held;
    }

    // ---- lifecycle ----

    /**
     * Follows the bucket, takes in what it holds, and starts mirroring. Returns when the entries held
     * at the start are in the table.
     *
     * @throws TimeoutException when the bucket does not deliver its entries within {@code catchUp}
     */
    synchronized void start(Duration catchUp) throws Exception {
        if (running) {
            return;
        }
        bucket = source.open();
        CountDownLatch caughtUp = new CountDownLatch(1);
        watch = bucket.watch(new MirrorBucket.Watcher() {
            @Override
            public void item(Item item) {
                take(item);
            }

            @Override
            public void caughtUp() {
                caughtUp.countDown();
            }
        });
        if (!caughtUp.await(catchUp.toMillis(), TimeUnit.MILLISECONDS)) {
            closeQuietly(watch);
            throw new TimeoutException("the tickets bucket did not deliver its entries within " + catchUp);
        }
        running = true;
        startWriter();
        startHousekeeping();
        log.info("Mirror running: {} entries recovered from the tickets bucket", table.size());
    }

    /**
     * Stops mirroring. What is queued is written first, for at most {@code drain}; pass a zero drain
     * when the lease is lost, so nothing is written after it.
     */
    synchronized void stop(Duration drain) {
        if (!running) {
            return;
        }
        long until = clock.getAsLong() + drain.toMillis();
        while (!drain.isZero() && !queue.isEmpty() && clock.getAsLong() < until) {
            sleepBriefly();
        }
        running = false;
        closeQuietly(watch);
        if (housekeeping != null) {
            housekeeping.shutdownNow();
        }
        if (writer != null) {
            writer.interrupt();
        }
        queue.clear();
        minted.clear();
        inBucket.clear();
    }

    boolean running() {
        return running;
    }

    // ---- table changes to the bucket ----

    @Override
    public void changed(TicketTable.Change change) {
        if (!running || change.source() != TicketTable.Source.LOCAL) {
            return;
        }
        byte[] value = null;
        if (!change.removal()) {
            if (!change.key().startsWith(TicketManager.RESIDENCY_PREFIX)) {
                minted.put(change.key(), clock.getAsLong() + mintedMemoryMs);
            }
            value = encode(change.doc());
            if (value == null) {
                return;
            }
        }
        if (!queue.offer(new Op(change.key(), value))) {
            dropped.incrementAndGet();
            dirty.set(true);
        }
    }

    private byte[] encode(JsonNode doc) {
        try {
            return mapper.writeValueAsBytes(doc);
        } catch (Exception e) {
            log.warn("Cannot encode a ticket for the mirror: {}", e.getMessage());
            return null;
        }
    }

    // ---- bucket entries to the table ----

    /** Takes in one bucket entry, unless this replica wrote it or already holds the key. */
    private void take(Item item) {
        if (item.value() == null) {
            inBucket.remove(item.key());
        } else {
            inBucket.add(item.key());
        }
        if (minted.containsKey(item.key())) {
            return;
        }
        if (item.value() == null) {
            if (!item.key().startsWith(TicketManager.RESIDENCY_PREFIX)) {
                table.removeForeign(item.key());
            }
            return;
        }
        try {
            table.ingest(item.key(), mapper.readTree(item.value()), item.createdAtMs());
        } catch (Exception e) {
            log.debug("Skipping unreadable entry {} in the tickets bucket: {}", item.key(), e.getMessage());
        }
    }

    // ---- the writer ----

    private void startWriter() {
        writer = new Thread(this::writeLoop, "ticket-mirror-writer");
        writer.setDaemon(true);
        writer.start();
    }

    private void writeLoop() {
        while (running) {
            try {
                Op op = queue.poll(500, TimeUnit.MILLISECONDS);
                if (op != null && lease.getAsBoolean()) {
                    apply(op);
                }
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }
        }
    }

    private void apply(Op op) {
        for (int attempt = 1; attempt <= WRITE_ATTEMPTS && lease.getAsBoolean(); attempt++) {
            try {
                if (op.value() == null) {
                    bucket.remove(op.key());
                } else {
                    bucket.put(op.key(), op.value());
                }
                written.incrementAndGet();
                return;
            } catch (Exception e) {
                if (attempt == WRITE_ATTEMPTS) {
                    failed.incrementAndGet();
                    dirty.set(true);
                    log.warn("Mirroring {} to the tickets bucket failed: {}", op.key(), e.getMessage());
                } else {
                    sleepBriefly();
                }
            }
        }
    }

    // ---- housekeeping ----

    private void startHousekeeping() {
        housekeeping = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "ticket-mirror-housekeeping");
            t.setDaemon(true);
            return t;
        });
        housekeeping.scheduleWithFixedDelay(this::expire, EXPIRY_SWEEP.toMillis(), EXPIRY_SWEEP.toMillis(),
                TimeUnit.MILLISECONDS);
        housekeeping.scheduleWithFixedDelay(this::tidyBucket, MARKER_SWEEP.toMillis(), MARKER_SWEEP.toMillis(),
                TimeUnit.MILLISECONDS);
    }

    /** Drops the entries nobody released; the table publishes each removal and the writer mirrors it. */
    void expire() {
        try {
            int expired = table.sweep().size();
            if (expired > 0) {
                log.info("Dropped {} tickets that were never released", expired);
            }
        } catch (RuntimeException e) {
            log.warn("Sweeping expired tickets failed: {}", e.getMessage());
        }
    }

    /** Sweeps removal markers, forgets old minted keys, and repairs the bucket when a write was lost. */
    void tidyBucket() {
        if (!lease.getAsBoolean()) {
            return;
        }
        try {
            bucket.sweepMarkers();
        } catch (Exception e) {
            log.warn("Sweeping removal markers failed: {}", e.getMessage());
        }
        long now = clock.getAsLong();
        minted.values().removeIf(until -> until <= now);
        if (dirty.compareAndSet(true, false)) {
            resync();
        }
    }

    /**
     * Makes the bucket match the table after a write was dropped or failed: every entry the table
     * holds is written again, and every key the bucket holds that the table does not is removed.
     */
    void resync() {
        log.info("Repairing the tickets bucket from the table after a lost write");
        var held = table.snapshot();
        Set<String> heldKeys = ConcurrentHashMap.newKeySet();
        for (var entry : held) {
            heldKeys.add(entry.key());
            changed(new TicketTable.Change(entry.key(), entry.doc(), TicketTable.Source.LOCAL));
        }
        for (String key : Set.copyOf(inBucket)) {
            if (!heldKeys.contains(key)) {
                changed(removal(key));
            }
        }
    }

    private static TicketTable.Change removal(String key) {
        return new TicketTable.Change(key, null, TicketTable.Source.LOCAL);
    }

    // ---- observation ----

    int queued() {
        return queue.size();
    }

    long written() {
        return written.get();
    }

    long failed() {
        return failed.get();
    }

    long dropped() {
        return dropped.get();
    }

    /** The keys the bucket holds, as far as the watch has reported. */
    Set<String> bucketKeys() {
        return Set.copyOf(inBucket);
    }

    private static void closeQuietly(AutoCloseable closeable) {
        if (closeable == null) {
            return;
        }
        try {
            closeable.close();
        } catch (Exception e) {
            log.debug("Closing the bucket watch failed: {}", e.getMessage());
        }
    }

    private static void sleepBriefly() {
        try {
            Thread.sleep(100);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
