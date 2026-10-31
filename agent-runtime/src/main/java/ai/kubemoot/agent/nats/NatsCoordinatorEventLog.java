package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.PublishOptions;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.concurrent.atomic.AtomicLong;

/**
 * Writes coordinator events to the {@code KUBEMOOT_COORDINATOR} stream with a JetStream
 * publish that does not wait for the acknowledgement, so a slow or missing stream never
 * delays a phase change. The event id is the {@code Nats-Msg-Id}, so the stream drops a
 * duplicate inside its duplicate window. A failed write is counted and logged (the first
 * one, then every hundredth) and the event is lost; nothing else changes.
 */
final class NatsCoordinatorEventLog implements CoordinatorEventLog {

    private static final Logger log = LoggerFactory.getLogger(NatsCoordinatorEventLog.class);
    private static final long LOG_EVERY = 100;

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper mapper;
    private final AtomicLong failures = new AtomicLong();

    NatsCoordinatorEventLog(NatsConnectionProvider natsProvider, ObjectMapper mapper) {
        this.natsProvider = natsProvider;
        this.mapper = mapper;
    }

    @Override
    public void append(String subject, CoordinatorEvent event) {
        try {
            Connection conn = natsProvider.getConnection();
            if (conn == null) {
                failed(event, "no NATS connection", null);
                return;
            }
            byte[] body = mapper.writeValueAsBytes(event.toJson(mapper));
            var options = PublishOptions.builder().messageId(event.eventId()).build();
            conn.jetStream().publishAsync(subject, body, options)
                    .whenComplete((ack, error) -> {
                        if (error != null) {
                            failed(event, error.getMessage(), error);
                        }
                    });
        } catch (Exception e) {
            failed(event, e.getMessage(), e);
        }
    }

    /** Events lost since start; for tests and metrics. */
    long failures() {
        return failures.get();
    }

    private void failed(CoordinatorEvent event, String reason, Throwable cause) {
        long count = failures.incrementAndGet();
        if (count == 1 || count % LOG_EVERY == 0) {
            log.warn("Coordinator event {} was not written ({} lost so far): {}", event.eventId(), count, reason);
        } else {
            log.debug("Coordinator event {} was not written: {}", event.eventId(), reason, cause);
        }
    }
}
