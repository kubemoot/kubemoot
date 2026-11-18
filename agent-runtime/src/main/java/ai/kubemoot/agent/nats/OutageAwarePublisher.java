package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.JetStream;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamOptions;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.time.Duration;

/**
 * Carries out the publishes that end a discussion (the synthesis and the thread close) so a
 * NATS restart in the middle of them does not lose the answer. Each message is published
 * through JetStream and counts as delivered only when the stream acknowledges it. While NATS or
 * JetStream is unreachable the publish is tried again, paced by an {@link OutageBackoff.Wait},
 * until the acknowledgement arrives. A message with a derived id carries it as
 * {@code Nats-Msg-Id}, so a repeat of a publish that was stored but not acknowledged is dropped
 * by the stream (inside its duplicate window, which is longer than the retry bound).
 *
 * <p>Without a NATS URL the publishes run once on whatever connection the provider has.
 */
final class OutageAwarePublisher {

    private static final Logger log = LoggerFactory.getLogger(OutageAwarePublisher.class);
    private static final Duration ACK_TIMEOUT = Duration.ofSeconds(2);
    /** The JetStream API error code for "JetStream system temporarily unavailable". */
    static final int JETSTREAM_UNAVAILABLE = 10008;
    private static final int SERVICE_UNAVAILABLE = 503;

    /** The publishes of one decision, on a JetStream context. */
    @FunctionalInterface
    interface Publishes {
        void publishOn(JetStream js) throws IOException, JetStreamApiException;
    }

    private final NatsConnectionProvider provider;
    private final OutageBackoff backoff;

    OutageAwarePublisher(NatsConnectionProvider provider) {
        this(provider, OutageBackoff.standard());
    }

    OutageAwarePublisher(NatsConnectionProvider provider, OutageBackoff backoff) {
        this.provider = provider;
        this.backoff = backoff;
    }

    /** Begins the wait one decision shares across its publishes. */
    OutageBackoff.Wait startWait() {
        return backoff.start();
    }

    /** True when {@code e} says JetStream is not serving (a restart), not that it refused the message. */
    static boolean isOutage(JetStreamApiException e) {
        return e.getApiErrorCode() == JETSTREAM_UNAVAILABLE || e.getErrorCode() == SERVICE_UNAVAILABLE;
    }

    /**
     * Runs {@code publishes} once NATS can take them, retrying through an outage within {@code wait}.
     * Returns true when every publish was acknowledged; false when there was no connection to run
     * them on, the stream refused a message, the wait passed its deadline, or the thread was interrupted.
     */
    boolean run(String what, Publishes publishes, OutageBackoff.Wait wait) {
        wait.resetPause();
        if (!provider.isConfigured()) {
            return attempt(publishes, false) == Attempt.DELIVERED;
        }
        Attempt result = attempt(publishes, true);
        while (result == Attempt.UNREACHABLE) {
            if (!wait.next()) {
                log.warn("Gave up on {}: NATS did not take it within {}", what, backoff.bound());
                return false;
            }
            result = attempt(publishes, true);
        }
        if (result == Attempt.REFUSED) {
            log.warn("The stream refused {}", what);
        }
        return result == Attempt.DELIVERED;
    }

    private enum Attempt { DELIVERED, UNREACHABLE, REFUSED }

    private Attempt attempt(Publishes publishes, boolean needsConnected) {
        Connection conn = provider.getConnection();
        if (conn == null || (needsConnected && conn.getStatus() != Connection.Status.CONNECTED)) {
            return Attempt.UNREACHABLE;
        }
        try {
            publishes.publishOn(conn.jetStream(JetStreamOptions.builder().requestTimeout(ACK_TIMEOUT).build()));
            return Attempt.DELIVERED;
        } catch (JetStreamApiException e) {
            log.debug("JetStream answered {}: {}", e.getApiErrorCode(), e.getMessage());
            return isOutage(e) ? Attempt.UNREACHABLE : Attempt.REFUSED;
        } catch (IOException e) {
            log.debug("NATS is not accepting the publish yet: {}", e.getMessage());
            return Attempt.UNREACHABLE;
        }
    }
}
