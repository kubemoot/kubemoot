package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.JetStream;
import io.nats.client.PublishOptions;
import io.nats.client.api.PublishAck;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.time.Instant;
import java.util.concurrent.CompletableFuture;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The NATS side of the log: one publish per event, keyed for duplicate detection, never throwing. */
class NatsCoordinatorEventLogTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String SUBJECT = "kubemoot.coordinator.ns.crew.t1";

    private final NatsConnectionProvider provider = mock(NatsConnectionProvider.class);
    private final NatsCoordinatorEventLog log = new NatsCoordinatorEventLog(provider, MAPPER);

    private static CoordinatorEvent event() {
        return new CoordinatorEvent("t1/opened", "thread_opened", "t1", 0, Instant.parse("2026-10-31T12:00:00Z"),
                "coordinator", MAPPER.createObjectNode().put("userQuery", "q"));
    }

    @Test
    void publishesTheEventJsonToTheThreadSubjectWithItsIdAsTheMessageId() throws Exception {
        var conn = mock(Connection.class);
        var js = mock(JetStream.class);
        when(provider.getConnection()).thenReturn(conn);
        when(conn.jetStream()).thenReturn(js);
        when(js.publishAsync(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenReturn(CompletableFuture.completedFuture(mock(PublishAck.class)));

        log.append(SUBJECT, event());

        var body = ArgumentCaptor.forClass(byte[].class);
        var options = ArgumentCaptor.forClass(PublishOptions.class);
        verify(js).publishAsync(eq(SUBJECT), body.capture(), options.capture());
        assertEquals("t1/opened", options.getValue().getMessageId());
        assertEquals(event(), CoordinatorEvent.parse(MAPPER, body.getValue()));
        assertEquals(0, log.failures());
    }

    @Test
    void withoutAConnectionTheEventIsCountedAsLostAndNothingThrows() {
        when(provider.getConnection()).thenReturn(null);

        assertDoesNotThrow(() -> log.append(SUBJECT, event()));

        assertEquals(1, log.failures());
    }

    @Test
    void aRefusedPublishIsCountedWhenTheAcknowledgementFails() throws Exception {
        var conn = mock(Connection.class);
        var js = mock(JetStream.class);
        when(provider.getConnection()).thenReturn(conn);
        when(conn.jetStream()).thenReturn(js);
        when(js.publishAsync(any(String.class), any(byte[].class), any(PublishOptions.class)))
                .thenReturn(CompletableFuture.failedFuture(new IllegalStateException("no stream matches")));

        assertDoesNotThrow(() -> log.append(SUBJECT, event()));

        assertEquals(1, log.failures());
    }

    @Test
    void aThrowingJetStreamIsCountedAsLostAndNothingThrows() throws Exception {
        var conn = mock(Connection.class);
        when(provider.getConnection()).thenReturn(conn);
        when(conn.jetStream()).thenThrow(new java.io.IOException("jetstream not enabled"));

        assertDoesNotThrow(() -> log.append(SUBJECT, event()));

        assertEquals(1, log.failures());
    }
}
