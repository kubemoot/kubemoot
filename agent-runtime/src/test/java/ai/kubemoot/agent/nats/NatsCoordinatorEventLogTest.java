package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.CoordinatorEventLog.AppendResult;
import ai.kubemoot.agent.nats.CoordinatorEventLog.AppendResult.Outcome;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.JetStream;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamOptions;
import io.nats.client.PublishOptions;
import io.nats.client.api.PublishAck;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.io.IOException;
import java.time.Duration;
import java.time.Instant;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.mockingDetails;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * The NATS side of the log: one acknowledged publish per event, keyed for duplicate
 * detection and fenced by the last sequence the writer saw, never throwing.
 */
class NatsCoordinatorEventLogTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String SUBJECT = "kubemoot.coordinator.ns.crew.t1";

    private final NatsConnectionProvider provider = mock(NatsConnectionProvider.class);
    /** A log that does not wait for NATS: one try per append. */
    private final NatsCoordinatorEventLog log =
            new NatsCoordinatorEventLog(provider, MAPPER, new OutageBackoff(Duration.ZERO, Duration.ofMillis(1)));
    /** A log that waits out an outage, with short pauses. */
    private final NatsCoordinatorEventLog patient =
            new NatsCoordinatorEventLog(provider, MAPPER, new OutageBackoff(Duration.ofSeconds(10), Duration.ofMillis(1)));
    private final JetStream js = mock(JetStream.class);

    private static CoordinatorEvent event() {
        return new CoordinatorEvent("t1/opened", "thread_opened", "t1", 0, Instant.parse("2026-10-31T12:00:00Z"),
                "coordinator", MAPPER.createObjectNode().put("userQuery", "q"));
    }

    private static PublishAck ack(long sequence) {
        var ack = mock(PublishAck.class);
        when(ack.getSeqno()).thenReturn(sequence);
        return ack;
    }

    @BeforeEach
    void connected() throws Exception {
        var conn = mock(Connection.class);
        when(provider.getConnection()).thenReturn(conn);
        when(conn.jetStream(any(JetStreamOptions.class))).thenReturn(js);
    }

    @Test
    void publishesTheEventJsonToTheThreadSubjectWithItsIdAndTheExpectedSequence() throws Exception {
        var stored = ack(7);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class))).thenReturn(stored);

        AppendResult result = log.append(SUBJECT, event(), 6);

        var body = ArgumentCaptor.forClass(byte[].class);
        var options = ArgumentCaptor.forClass(PublishOptions.class);
        verify(js).publish(eq(SUBJECT), body.capture(), options.capture());
        assertEquals("t1/opened", options.getValue().getMessageId());
        assertEquals(6, options.getValue().getExpectedLastSubjectSequence());
        assertEquals(event(), CoordinatorEvent.parse(MAPPER, body.getValue()));
        assertEquals(AppendResult.appended(7), result, "the sequence the stream stored it at");
        assertEquals(0, log.failures());
    }

    @Test
    void theFirstEventOfAThreadExpectsAnEmptySubject() throws Exception {
        var stored = ack(1);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class))).thenReturn(stored);

        log.append(SUBJECT, event(), 0);

        var options = ArgumentCaptor.forClass(PublishOptions.class);
        verify(js).publish(eq(SUBJECT), any(byte[].class), options.capture());
        assertEquals(0, options.getValue().getExpectedLastSubjectSequence());
    }

    @Test
    void aRefusalForAWrongLastSequenceFencesTheWriterAndIsNotRetried() throws Exception {
        var refusal = mock(JetStreamApiException.class);
        when(refusal.getApiErrorCode()).thenReturn(NatsCoordinatorEventLog.WRONG_LAST_SEQUENCE);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class))).thenThrow(refusal);

        AppendResult result = log.append(SUBJECT, event(), 3);

        assertEquals(Outcome.FENCED, result.outcome());
        verify(js, times(1)).publish(any(String.class), any(byte[].class), any(PublishOptions.class));
    }

    @Test
    void anyOtherRefusalFailsTheAppendWithoutARetry() throws Exception {
        var refusal = mock(JetStreamApiException.class);
        when(refusal.getApiErrorCode()).thenReturn(10059);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class))).thenThrow(refusal);

        assertEquals(Outcome.FAILED, log.append(SUBJECT, event(), 0).outcome());
        verify(js, times(1)).publish(any(String.class), any(byte[].class), any(PublishOptions.class));
        assertEquals(1, log.failures());
    }

    @Test
    void aTimedOutPublishFailsTheAppendWithoutARetryAndNothingThrows() throws Exception {
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(new IOException("timeout"));

        assertEquals(Outcome.FAILED, assertDoesNotThrow(() -> log.append(SUBJECT, event(), 0)).outcome());
        verify(js, times(1)).publish(any(String.class), any(byte[].class), any(PublishOptions.class));
    }

    @Test
    void withoutAConnectionTheAppendFailsAndNothingThrows() {
        when(provider.getConnection()).thenReturn(null);

        assertEquals(Outcome.FAILED, assertDoesNotThrow(() -> log.append(SUBJECT, event(), 0)).outcome());
        assertEquals(1, log.failures());
    }

    @Test
    void anAppendDuringANatsRestartIsRetriedUntilTheStreamAcknowledgesIt() throws Exception {
        var stored = ack(9);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(new IOException("Error Publishing: 503 No Responders Available For Request"))
                .thenThrow(new IOException("Error Publishing: 503 No Responders Available For Request"))
                .thenReturn(stored);

        AppendResult result = patient.append(SUBJECT, event(), 8);

        assertEquals(AppendResult.appended(9), result, "the thread stays logged");
        verify(js, times(3)).publish(eq(SUBJECT), any(byte[].class), any(PublishOptions.class));
        assertEquals(0, patient.failures());
    }

    @Test
    void everyRetryCarriesTheSameEventIdSoAStoredRepeatIsDroppedByTheStream() throws Exception {
        var second = ack(2);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(new IOException("timeout")).thenReturn(second);

        patient.append(SUBJECT, event(), 1);

        var options = ArgumentCaptor.forClass(PublishOptions.class);
        verify(js, times(2)).publish(eq(SUBJECT), any(byte[].class), options.capture());
        assertEquals("t1/opened", options.getAllValues().get(0).getMessageId());
        assertEquals("t1/opened", options.getAllValues().get(1).getMessageId());
    }

    @Test
    void anAppendWaitsForTheConnectionToComeBack() throws Exception {
        var conn = mock(Connection.class);
        when(conn.jetStream(any(JetStreamOptions.class))).thenReturn(js);
        when(provider.getConnection()).thenReturn(null, null, conn);
        var stored = ack(4);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class))).thenReturn(stored);

        assertEquals(AppendResult.appended(4), patient.append(SUBJECT, event(), 3));
    }

    @Test
    void anOutageThatOutlastsTheBoundFailsTheAppendAndNothingThrows() throws Exception {
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(new IOException("503"));
        var impatient = new NatsCoordinatorEventLog(provider, MAPPER, new OutageBackoff(Duration.ofMillis(60), Duration.ofMillis(5)));

        assertEquals(Outcome.FAILED, assertDoesNotThrow(() -> impatient.append(SUBJECT, event(), 0)).outcome());
        assertEquals(1, impatient.failures());
        assertTrue(mockingDetails(js).getInvocations().size() > 1, "it tried again before giving up");
    }

    @Test
    void aFenceAfterARetryIsStillAFence() throws Exception {
        var refusal = mock(JetStreamApiException.class);
        when(refusal.getApiErrorCode()).thenReturn(NatsCoordinatorEventLog.WRONG_LAST_SEQUENCE);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(new IOException("503")).thenThrow(refusal);

        assertEquals(Outcome.FENCED, patient.append(SUBJECT, event(), 3).outcome());
    }

    @Test
    void aJetStreamUnavailableAnswerIsAnOutageNotARefusal() throws Exception {
        var unavailable = mock(JetStreamApiException.class);
        when(unavailable.getApiErrorCode()).thenReturn(OutageAwarePublisher.JETSTREAM_UNAVAILABLE);
        var stored = ack(5);
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(unavailable).thenReturn(stored);

        assertEquals(AppendResult.appended(5), patient.append(SUBJECT, event(), 4));
    }

    @Test
    void theEventsOfOneDecisionShareOneWait() throws Exception {
        when(js.publish(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(new IOException("503"));
        var wait = new OutageBackoff(Duration.ofMillis(80), Duration.ofMillis(5)).start();
        long started = System.nanoTime();

        log.append(SUBJECT, event(), 0, wait);
        patient.append(SUBJECT, event(), 0, wait);
        patient.append(SUBJECT, event(), 0, wait);

        assertTrue(Duration.ofNanos(System.nanoTime() - started).toMillis() < 1000,
                "three appends waited out one bound, not three");
    }
}
