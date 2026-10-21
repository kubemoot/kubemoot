package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.concurrent.CompletableFuture;

/**
 * {@link CapacitySignal} over the placement service's change counter. A waiter whose call found no
 * room asks for the version, retries its placement, and blocks in {@link #awaitChange}; the wait is a
 * request the service holds open and completes at the next change to its tickets or to the server
 * state, so a freed accelerator wakes the waiter at once and not at the next poll.
 *
 * <p>{@link #nudge} (the discussion ended, wake up) ends an open wait by cancelling its request. When
 * the service cannot be reached the wait returns "changed" after a short pause, so the waiter's retry
 * reaches the placement call, which falls back; the waiter never sleeps against an outage.</p>
 */
public class RemoteCapacitySignal implements CapacitySignal {

    private static final Logger log = LoggerFactory.getLogger(RemoteCapacitySignal.class);
    private static final String API = "/placement/v1/capacity";
    /** The longest a single request is held open; a longer wait is several requests. */
    private static final Duration SLICE = Duration.ofSeconds(20);
    private static final long OUTAGE_PAUSE_MS = 500;

    private final PlacementClient client;
    private final ObjectMapper mapper;
    private final Object monitor = new Object();
    private long serviceVersion;
    private long nudges;
    private CompletableFuture<?> open;

    public RemoteCapacitySignal(PlacementClient client, ObjectMapper mapper) {
        this.client = client;
        this.mapper = mapper;
    }

    @Override
    public boolean ensureWatching() {
        try {
            var answer = client.get(API);
            if (answer.status() == 200 && answer.body() != null) {
                remember(answer.body().path("version").asLong());
                return true;
            }
        } catch (PlacementUnavailableException e) {
            log.debug("Capacity version unavailable: {}", e.getMessage());
        }
        return false;
    }

    @Override
    public long version() {
        synchronized (monitor) {
            return serviceVersion + nudges;
        }
    }

    @Override
    public boolean awaitChange(long seen, Duration max) throws InterruptedException {
        long deadline = System.nanoTime() + max.toNanos();
        while (version() == seen) {
            long remaining = deadline - System.nanoTime();
            if (remaining <= 0) {
                return false;
            }
            if (!holdOpen(Duration.ofNanos(Math.min(remaining, SLICE.toNanos())))) {
                return true;
            }
        }
        return true;
    }

    /** One request held open; false when the service could not be reached. */
    private boolean holdOpen(Duration slice) throws InterruptedException {
        CompletableFuture<PlacementClient.Answer> request;
        synchronized (monitor) {
            var body = mapper.createObjectNode().put("seen", serviceVersion).put("maxWaitMs", slice.toMillis());
            request = client.postAsync(API + "/await", body, slice.plusSeconds(5));
            open = request;
        }
        try {
            var answer = request.get();
            if (answer.status() == 200 && answer.body() != null) {
                remember(answer.body().path("version").asLong());
            }
            return true;
        } catch (java.util.concurrent.CancellationException e) {
            return true; // nudged: the caller re-checks
        } catch (java.util.concurrent.ExecutionException e) {
            log.debug("Capacity wait failed: {}", e.getCause() == null ? e.getMessage() : e.getCause().getMessage());
            Thread.sleep(OUTAGE_PAUSE_MS);
            return false;
        } finally {
            synchronized (monitor) {
                open = null;
            }
        }
    }

    @Override
    public void nudge() {
        CompletableFuture<?> toCancel;
        synchronized (monitor) {
            nudges++;
            toCancel = open;
        }
        if (toCancel != null) {
            toCancel.cancel(true);
        }
    }

    private void remember(long version) {
        synchronized (monitor) {
            serviceVersion = Math.max(serviceVersion, version);
        }
    }
}
