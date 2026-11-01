package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamSubscription;
import io.nats.client.Message;
import io.nats.client.PushSubscribeOptions;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.DeliverPolicy;

import java.io.IOException;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.List;

/**
 * Reads a stream with an ordered consumer: an ephemeral push consumer that starts at the
 * first message, delivers in order, and recreates itself if a message is missed. Nothing is
 * acknowledged and nothing is left behind. The read ends when the consumer reports no
 * message pending after the one just read, so it sees every message the stream held when
 * the read began and any that arrived while it ran.
 */
final class NatsBusReader implements BusReader {

    /** How long to wait for the first message; a subject with no message yields nothing after this. */
    private static final Duration FIRST_WAIT = Duration.ofMillis(1500);
    /** How long to wait for the next message once the first came. */
    private static final Duration NEXT_WAIT = Duration.ofSeconds(5);

    private final NatsConnectionProvider natsProvider;

    NatsBusReader(NatsConnectionProvider natsProvider) {
        this.natsProvider = natsProvider;
    }

    @Override
    public List<BusMessage> readAll(String stream, String filterSubject) throws IOException {
        return read(stream, filterSubject, PushSubscribeOptions.builder().stream(stream).ordered(true).build());
    }

    @Override
    public List<BusMessage> readSince(String stream, String filterSubject, Instant since) throws IOException {
        var config = ConsumerConfiguration.builder().deliverPolicy(DeliverPolicy.ByStartTime)
                .startTime(since.atZone(ZoneOffset.UTC)).build();
        return read(stream, filterSubject,
                PushSubscribeOptions.builder().stream(stream).ordered(true).configuration(config).build());
    }

    private List<BusMessage> read(String stream, String filterSubject, PushSubscribeOptions options)
            throws IOException {
        Connection conn = natsProvider.getConnection();
        if (conn == null) {
            throw new IOException("no NATS connection");
        }
        var out = new ArrayList<BusMessage>();
        try {
            JetStreamSubscription sub = conn.jetStream().subscribe(filterSubject, options);
            try {
                drain(sub, out);
            } finally {
                sub.unsubscribe();
            }
        } catch (JetStreamApiException e) {
            throw new IOException("could not read " + stream + " " + filterSubject + ": " + e.getMessage(), e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IOException("interrupted while reading " + stream, e);
        }
        return out;
    }

    private static void drain(JetStreamSubscription sub, List<BusMessage> out) throws InterruptedException {
        Duration wait = FIRST_WAIT;
        Message message = sub.nextMessage(wait);
        while (message != null) {
            var meta = message.metaData();
            out.add(new BusMessage(meta.streamSequence(), message.getSubject(), message.getData()));
            if (meta.pendingCount() == 0) {
                return;
            }
            wait = NEXT_WAIT;
            message = sub.nextMessage(wait);
        }
    }
}
