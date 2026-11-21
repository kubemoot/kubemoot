package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.tracing.AgentTrace;
import io.nats.client.Connection;
import io.nats.client.JetStream;
import io.nats.client.JetStreamApiException;
import io.nats.client.PublishOptions;
import io.nats.client.impl.Headers;

import java.io.IOException;
import java.util.UUID;

/**
 * Message ids derived from a coordinator event id. A message that carries out a logged
 * decision gets an id made from the decision's event id, so the same decision published
 * twice (a replay after a crash) has the same id: consumers drop a repeated
 * {@code messageId}, and the stream drops a repeated {@code Nats-Msg-Id} inside its
 * duplicate window. Without the event log there is no derived id and a message gets a
 * random one, published as before.
 */
final class DerivedIds {

    /** The header the stream reads to drop a duplicate. */
    static final String MSG_ID_HEADER = "Nats-Msg-Id";

    private DerivedIds() {
    }

    /** {@code derived}, or a random id when there is none. */
    static String idOrRandom(String derived) {
        return derived != null ? derived : UUID.randomUUID().toString();
    }

    /**
     * The stream-level id of {@code derived} on {@code subject}. The subject is part of it
     * because the stream tells duplicates apart across all its subjects, and one message is
     * published to several (the broadcast subject and the channel).
     */
    static String streamId(String derived, String subject) {
        return derived + "@" + subject;
    }

    /** Publishes {@code payload}; with a derived id it carries {@code Nats-Msg-Id}. */
    static void publish(Connection conn, String subject, byte[] payload, String derived) {
        Headers base = derived == null ? null : new Headers().put(MSG_ID_HEADER, streamId(derived, subject));
        AgentTrace.publish(conn, subject, base, payload);
    }

    /**
     * Publishes {@code payload} through JetStream and waits for the stream's acknowledgement;
     * with a derived id the stream drops a repeat of it.
     */
    static void publishAcked(JetStream js, String subject, byte[] payload, String derived)
            throws IOException, JetStreamApiException {
        var options = PublishOptions.builder();
        if (derived != null) {
            options.messageId(streamId(derived, subject));
        }
        AgentTrace.publishAcked(js, subject, null, payload, options.build());
    }
}
