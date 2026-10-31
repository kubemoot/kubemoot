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
import java.time.Instant;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
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
    private final NatsCoordinatorEventLog log = new NatsCoordinatorEventLog(provider, MAPPER);
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
    void theLastMessageOfASubjectIsReadFromTheStream() throws Exception {
        var management = mock(io.nats.client.JetStreamManagement.class);
        var info = mock(io.nats.client.api.MessageInfo.class);
        var conn = mock(Connection.class);
        var json = MAPPER.writeValueAsBytes(event().toJson(MAPPER));
        when(info.getSeq()).thenReturn(9L);
        when(info.getData()).thenReturn(json);
        when(management.getLastMessage(CrewScope.COORDINATOR_STREAM, SUBJECT)).thenReturn(info);
        when(conn.jetStreamManagement()).thenReturn(management);
        when(provider.getConnection()).thenReturn(conn);

        assertEquals(java.util.Optional.of(new CoordinatorEventLog.LastMessage(9, "t1/opened")), log.last(SUBJECT));
    }

    @Test
    void theLastMessageIsEmptyWhenTheStreamCannotBeRead() throws Exception {
        var management = mock(io.nats.client.JetStreamManagement.class);
        var conn = mock(Connection.class);
        when(management.getLastMessage(any(String.class), any(String.class))).thenThrow(new IOException("down"));
        when(conn.jetStreamManagement()).thenReturn(management);
        when(provider.getConnection()).thenReturn(conn);

        assertEquals(java.util.Optional.empty(), log.last(SUBJECT));
        when(provider.getConnection()).thenReturn(null);
        assertEquals(java.util.Optional.empty(), log.last(SUBJECT));
    }

    @Test
    void withoutAConnectionTheAppendFailsAndNothingThrows() {
        when(provider.getConnection()).thenReturn(null);

        assertEquals(Outcome.FAILED, assertDoesNotThrow(() -> log.append(SUBJECT, event(), 0)).outcome());
        assertEquals(1, log.failures());
    }
}
