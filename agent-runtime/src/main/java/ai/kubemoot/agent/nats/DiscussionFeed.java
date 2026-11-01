package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.JetStreamApiException;
import io.nats.client.Message;
import io.nats.client.PushSubscribeOptions;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.DeliverPolicy;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.nio.charset.StandardCharsets;

/**
 * The coordinator's live feed of the discussion subjects when leases are on: an ordered
 * JetStream consumer that starts at the next new message. Each message arrives with its sequence
 * in the discussion stream, which the coordinator stamps on the events it writes so that a
 * replica that takes a thread over can tell which stop, pause and resume messages the thread's
 * decisions already account for. An ordered consumer also recreates itself after a gap, so a
 * brief disconnect loses nothing.
 *
 * <p>Every replica reads every message; a replica acts only on the threads it owns. A queue group
 * would hand a message to one replica, not necessarily the owner, so it is not used.
 */
final class DiscussionFeed {

    private static final Logger log = LoggerFactory.getLogger(DiscussionFeed.class);

    /** What the feed hands over: the message body and its stream sequence. */
    @FunctionalInterface
    interface Handler {
        void accept(String data, long streamSequence);
    }

    private DiscussionFeed() {
    }

    /**
     * Subscribes {@code handler} to {@code subject} through an ordered consumer. When the stream
     * cannot be read that way, the feed falls back to a plain subscription (no sequences; controls
     * are then ordered by time) and says so.
     */
    static void subscribe(Connection conn, Dispatcher dispatcher, String subject, Handler handler) {
        try {
            var consumer = ConsumerConfiguration.builder().deliverPolicy(DeliverPolicy.New).build();
            var options = PushSubscribeOptions.builder().stream(CoordinatorResume.DISCUSS_STREAM).ordered(true)
                    .configuration(consumer).build();
            conn.jetStream().subscribe(subject, dispatcher, msg -> deliver(msg, handler), false, options);
            log.info("Following {} through an ordered consumer of {}", subject, CoordinatorResume.DISCUSS_STREAM);
        } catch (IOException | JetStreamApiException | RuntimeException e) {
            log.warn("The discussion stream could not be followed with a consumer ({}); using a plain subscription, "
                    + "so stop, pause and resume messages are ordered by time", e.getMessage());
            dispatcher.subscribe(subject, msg -> deliver(msg, handler));
        }
    }

    private static void deliver(Message msg, Handler handler) {
        try {
            handler.accept(new String(msg.getData(), StandardCharsets.UTF_8), sequenceOf(msg));
        } catch (RuntimeException e) {
            log.debug("Error handling orchestrator message: {}", e.getMessage());
        }
    }

    private static long sequenceOf(Message msg) {
        try {
            return msg.metaData().streamSequence();
        } catch (RuntimeException e) {
            return 0L;
        }
    }
}
