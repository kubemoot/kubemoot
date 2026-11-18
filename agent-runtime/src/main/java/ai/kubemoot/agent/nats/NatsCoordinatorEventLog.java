package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.CoordinatorEventLog.AppendResult.Outcome;
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
 * {@code FAILED} and nothing is thrown. A publish that fails because NATS or JetStream is
 * unreachable (a restart) is tried again, paced by a growing pause, until it is acknowledged;
 * the close of a thread passes its own longer {@link OutageBackoff.Wait}, other events wait briefly;
 * the event id makes a repeat of one that was stored but not acknowledged safe. The wait is
 * bounded as a safety net, and only an outage that outlasts the bound is {@code FAILED}; the
 * recorder then stops appending to that thread, so a possibly stored event is never mistaken
 * for a fence.
 */
final class NatsCoordinatorEventLog implements CoordinatorEventLog {

    /** The JetStream API error code for "wrong last sequence" (an expected-last-subject-sequence mismatch). */
    static final int WRONG_LAST_SEQUENCE = 10071;

    private static final Logger log = LoggerFactory.getLogger(NatsCoordinatorEventLog.class);
    private static final long LOG_EVERY = 100;
    private static final Duration ACK_TIMEOUT = Duration.ofSeconds(2);

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper mapper;
    private final OutageBackoff brief;
    private final AtomicLong failures = new AtomicLong();

    NatsCoordinatorEventLog(NatsConnectionProvider natsProvider, ObjectMapper mapper) {
        this(natsProvider, mapper, OutageBackoff.brief());
    }

    NatsCoordinatorEventLog(NatsConnectionProvider natsProvider, ObjectMapper mapper, OutageBackoff brief) {
        this.natsProvider = natsProvider;
        this.mapper = mapper;
        this.brief = brief;
    }

    @Override
    public AppendResult append(String subject, CoordinatorEvent event, long expectedLastSequence) {
        // Outside a close the caller is a bus dispatcher thread: wait only briefly for NATS.
        return append(subject, event, expectedLastSequence, brief.start());
    }

    @Override
    public AppendResult append(String subject, CoordinatorEvent event, long expectedLastSequence,
                               OutageBackoff.Wait wait) {
        byte[] body;
        try {
            body = mapper.writeValueAsBytes(event.toJson(mapper));
        } catch (JsonProcessingException e) {
            return failed(event, e.getMessage(), e);
        }
        var options = PublishOptions.builder().messageId(event.eventId())
                .expectedLastSubjectSequence(expectedLastSequence).build();
        wait.resetPause();
        while (true) {
            Attempt attempt = tryAppend(subject, body, options);
            if (attempt.result != null) {
                return attempt.result.outcome() == Outcome.FAILED ? failed(event, attempt.reason, attempt.cause)
                        : attempt.result;
            }
            // NATS or JetStream is not there (a restart): the same event id makes a repeat safe.
            if (!wait.next()) {
                return failed(event, attempt.reason, attempt.cause);
            }
        }
    }

    /** One try: a final result, or (result null) the reason NATS could not take it yet. */
    private record Attempt(AppendResult result, String reason, Throwable cause) {
    }

    private Attempt tryAppend(String subject, byte[] body, PublishOptions options) {
        Connection conn = natsProvider.getConnection();
        if (conn == null) {
            return new Attempt(null, "no NATS connection", null);
        }
        try {
            var js = conn.jetStream(JetStreamOptions.builder().requestTimeout(ACK_TIMEOUT).build());
            return new Attempt(AppendResult.appended(js.publish(subject, body, options).getSeqno()), null, null);
        } catch (JetStreamApiException e) {
            if (OutageAwarePublisher.isOutage(e)) {
                return new Attempt(null, e.getMessage(), e);
            }
            // A refusal is the stream's answer, not an outage: wrong last sequence fences, others fail.
            return new Attempt(e.getApiErrorCode() == WRONG_LAST_SEQUENCE ? AppendResult.fenced()
                    : AppendResult.failed(), e.getMessage(), e);
        } catch (IOException e) {
            return new Attempt(null, e.getMessage(), e);
        } catch (RuntimeException e) {
            return new Attempt(AppendResult.failed(), e.getMessage(), e);
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
