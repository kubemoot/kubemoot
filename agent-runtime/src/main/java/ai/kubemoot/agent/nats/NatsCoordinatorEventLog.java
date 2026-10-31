package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamOptions;
import io.nats.client.PublishOptions;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.time.Duration;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Writes coordinator events to the {@code KUBEMOOT_COORDINATOR} stream with a JetStream
 * publish that waits for the acknowledgement. The event id is the {@code Nats-Msg-Id}, so
 * the stream drops a repeat inside its duplicate window and answers it with the original
 * sequence; the expected last subject sequence makes the stream refuse the append when
 * another writer got there first.
 *
 * <p>A refusal for a wrong last sequence is {@code FENCED}; anything else that fails is
 * {@code FAILED} and nothing is thrown. A {@code FAILED} publish may have been stored (a
 * timeout); the recorder stops appending to that thread, so it is never mistaken for a fence.
 */
final class NatsCoordinatorEventLog implements CoordinatorEventLog {

    /** The JetStream API error code for "wrong last sequence" (an expected-last-subject-sequence mismatch). */
    static final int WRONG_LAST_SEQUENCE = 10071;

    private static final Logger log = LoggerFactory.getLogger(NatsCoordinatorEventLog.class);
    private static final long LOG_EVERY = 100;
    private static final Duration ACK_TIMEOUT = Duration.ofSeconds(2);

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper mapper;
    private final AtomicLong failures = new AtomicLong();

    NatsCoordinatorEventLog(NatsConnectionProvider natsProvider, ObjectMapper mapper) {
        this.natsProvider = natsProvider;
        this.mapper = mapper;
    }

    @Override
    public AppendResult append(String subject, CoordinatorEvent event, long expectedLastSequence) {
        byte[] body;
        try {
            body = mapper.writeValueAsBytes(event.toJson(mapper));
        } catch (JsonProcessingException e) {
            return failed(event, e.getMessage(), e);
        }
        var options = PublishOptions.builder().messageId(event.eventId())
                .expectedLastSubjectSequence(expectedLastSequence).build();
        Connection conn = natsProvider.getConnection();
        if (conn == null) {
            return failed(event, "no NATS connection", null);
        }
        try {
            var js = conn.jetStream(JetStreamOptions.builder().requestTimeout(ACK_TIMEOUT).build());
            return AppendResult.appended(js.publish(subject, body, options).getSeqno());
        } catch (JetStreamApiException e) {
            return e.getApiErrorCode() == WRONG_LAST_SEQUENCE ? AppendResult.fenced()
                    : failed(event, e.getMessage(), e);
        } catch (IOException | RuntimeException e) {
            return failed(event, e.getMessage(), e);
        }
    }

    /** Events lost since start; for tests. */
    long failures() {
        return failures.get();
    }

    private AppendResult failed(CoordinatorEvent event, String reason, Throwable cause) {
        long count = failures.incrementAndGet();
        if (count == 1 || count % LOG_EVERY == 0) {
            log.warn("Coordinator event {} was not written ({} failures so far): {}", event.eventId(), count, reason);
        } else {
            log.debug("Coordinator event {} was not written: {}", event.eventId(), reason, cause);
        }
        return AppendResult.failed();
    }
}
