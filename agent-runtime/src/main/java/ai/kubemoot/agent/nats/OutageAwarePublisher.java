package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.time.Duration;
import java.time.Instant;
import java.util.concurrent.TimeoutException;

/**
 * Carries out the publishes that end a discussion (the synthesis and the thread close) so a
 * NATS restart in the middle of them does not lose the answer. A core publish to a subject
 * the discussion stream captures is dropped without an error while NATS is down or while
 * JetStream has not recovered after a restart, so the publish waits for both: the
 * connection is up and JetStream answers. The publishes are then flushed, which confirms
 * the server took them. When either step fails the whole action is tried again, paced by a
 * short pause that grows to a cap. The messages are built once by the caller, so a repeat
 * carries the same message id and a consumer or the stream drops the duplicate.
 *
 * <p>The wait is driven by the state of NATS; the bound is a safety net for an outage that
 * never ends. Without a NATS URL the action runs once on whatever connection the provider
 * has, as before.
 */
final class OutageAwarePublisher {

    private static final Logger log = LoggerFactory.getLogger(OutageAwarePublisher.class);

    static final Duration DEFAULT_BOUND = Duration.ofMinutes(2);
    private static final Duration FIRST_PAUSE = Duration.ofMillis(250);
    private static final Duration MAX_PAUSE = Duration.ofSeconds(3);
    private static final Duration FLUSH_TIMEOUT = Duration.ofSeconds(2);

    /** The publishes of one decision, on a connection that is ready for them. */
    @FunctionalInterface
    interface Publishes {
        void publishOn(Connection conn) throws IOException;
    }

    private final NatsConnectionProvider provider;
    private final Duration bound;
    private final Duration firstPause;

    OutageAwarePublisher(NatsConnectionProvider provider) {
        this(provider, DEFAULT_BOUND, FIRST_PAUSE);
    }

    OutageAwarePublisher(NatsConnectionProvider provider, Duration bound, Duration firstPause) {
        this.provider = provider;
        this.bound = bound;
        this.firstPause = firstPause;
    }

    /**
     * Runs {@code publishes} once NATS can take them, retrying through an outage. Returns true
     * when they were published; false when there was no connection to run them on, the bound
     * passed, or the thread was interrupted.
     */
    boolean run(String what, Publishes publishes) {
        if (!provider.isConfigured()) {
            return runOnce(publishes);
        }
        Instant deadline = Instant.now().plus(bound);
        Duration pause = firstPause;
        while (true) {
            if (attempt(publishes)) {
                return true;
            }
            if (!Instant.now().plus(pause).isBefore(deadline) || !sleep(pause)) {
                log.warn("Gave up on {}: NATS did not take it within {}", what, bound);
                return false;
            }
            pause = pause.multipliedBy(2).compareTo(MAX_PAUSE) > 0 ? MAX_PAUSE : pause.multipliedBy(2);
        }
    }

    private boolean runOnce(Publishes publishes) {
        Connection conn = provider.getConnection();
        if (conn == null) {
            return false;
        }
        try {
            publishes.publishOn(conn);
            return true;
        } catch (IOException e) {
            log.debug("Publish failed: {}", e.getMessage());
            return false;
        }
    }

    private boolean attempt(Publishes publishes) {
        Connection conn = provider.getConnection();
        if (conn == null || conn.getStatus() != Connection.Status.CONNECTED) {
            return false;
        }
        try {
            conn.jetStreamManagement().getAccountStatistics();
            publishes.publishOn(conn);
            conn.flush(FLUSH_TIMEOUT);
            return true;
        } catch (IOException | JetStreamApiException | TimeoutException e) {
            log.debug("NATS is not ready for the publish yet: {}", e.getMessage());
            return false;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        }
    }

    private static boolean sleep(Duration pause) {
        try {
            Thread.sleep(pause.toMillis());
            return true;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        }
    }
}
