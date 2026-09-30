package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.Message;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

class RequestQueueConsumerTest {

    @Test
    void parseRequestExtractsAllFields() throws IOException {
        String json = """
                {
                    "message": "How many nodes are in the cluster?",
                    "conversationId": "abc-123",
                    "crew": "homelab-pilot",
                    "timestamp": "2026-03-19T10:00:00Z",
                    "use_rag": true,
                    "use_tools": false
                }
                """;

        var request = RequestQueueConsumer.parseRequest(json.getBytes(StandardCharsets.UTF_8));

        assertEquals("How many nodes are in the cluster?", request.message());
        assertEquals("abc-123", request.conversationId());
        assertEquals("homelab-pilot", request.crew());
        assertEquals("2026-03-19T10:00:00Z", request.timestamp());
        assertTrue(request.useRag());
        assertFalse(request.useTools());
    }

    @Test
    void parseRequestDefaultsRagAndToolsToTrue() throws IOException {
        String json = """
                {
                    "message": "test",
                    "conversationId": "xyz-789",
                    "crew": "hello"
                }
                """;

        var request = RequestQueueConsumer.parseRequest(json.getBytes(StandardCharsets.UTF_8));

        assertTrue(request.useRag());
        assertTrue(request.useTools());
    }

    @Test
    void consumerNameIncludesNamespaceAndCrew() {
        assertEquals("request-ns-a-homelab-pilot", CrewScope.of("ns-a", "homelab-pilot").requestConsumer());
        assertEquals("request-ns-b-hello", CrewScope.of("ns-b", "hello").requestConsumer());
    }

    @Test
    void filterSubjectIncludesNamespaceAndCrew() {
        assertEquals("kubemoot.request.ns-a.homelab-pilot", CrewScope.of("ns-a", "homelab-pilot").requestSubject());
        assertEquals("kubemoot.request.ns-b.hello", CrewScope.of("ns-b", "hello").requestSubject());
    }

    @Test
    void parseRequestHandlesMissingOptionalFields() throws IOException {
        String json = """
                {
                    "message": "minimal request"
                }
                """;

        var request = RequestQueueConsumer.parseRequest(json.getBytes(StandardCharsets.UTF_8));

        assertEquals("minimal request", request.message());
        assertEquals("", request.conversationId());
        assertEquals("", request.crew());
        assertEquals("", request.timestamp());
        assertTrue(request.useRag());
        assertTrue(request.useTools());
    }

    // resetConsumerIfPresent — guards the queue against the recurring wedge where
    // a previous coordinator pod died with a message in ack-pending state. With
    // maxAckPending=1 that blocks redeliveries until ackWait (now bounded to 10m,
    // was 30m); the reset clears the consumer position so the new pod starts fresh.

    @Test
    void resetConsumerIfPresentDeletesExistingConsumer() throws Exception {
        JetStreamManagement jsm = mock(JetStreamManagement.class);
        when(jsm.deleteConsumer("KUBEMOOT_REQUEST", "request-homelab-pilot")).thenReturn(true);

        RequestQueueConsumer.resetConsumerIfPresent(jsm, "KUBEMOOT_REQUEST", "request-homelab-pilot");

        verify(jsm, times(1)).deleteConsumer("KUBEMOOT_REQUEST", "request-homelab-pilot");
    }

    @Test
    void resetConsumerIfPresentSilentOnNotFound() throws Exception {
        JetStreamManagement jsm = mock(JetStreamManagement.class);
        JetStreamApiException notFound = mock(JetStreamApiException.class);
        when(notFound.getApiErrorCode()).thenReturn(10014);
        when(jsm.deleteConsumer(anyString(), anyString())).thenThrow(notFound);

        // Should not propagate — first-time startup is the expected case
        assertDoesNotThrow(() ->
                RequestQueueConsumer.resetConsumerIfPresent(jsm, "KUBEMOOT_REQUEST", "request-new-crew"));
    }

    @Test
    void resetConsumerIfPresentSwallowsUnexpectedApiException() throws Exception {
        JetStreamManagement jsm = mock(JetStreamManagement.class);
        JetStreamApiException other = mock(JetStreamApiException.class);
        when(other.getApiErrorCode()).thenReturn(10003); // arbitrary non-10014 code
        when(other.getMessage()).thenReturn("forbidden");
        when(jsm.deleteConsumer(anyString(), anyString())).thenThrow(other);

        // Failure path: do not propagate. The subsequent subscribe will surface
        // any real connectivity issue with a clearer error.
        assertDoesNotThrow(() ->
                RequestQueueConsumer.resetConsumerIfPresent(jsm, "KUBEMOOT_REQUEST", "request-x"));
    }

    @Test
    void resetConsumerIfPresentSwallowsIOException() throws Exception {
        JetStreamManagement jsm = mock(JetStreamManagement.class);
        when(jsm.deleteConsumer(anyString(), anyString())).thenThrow(new IOException("connection reset"));

        assertDoesNotThrow(() ->
                RequestQueueConsumer.resetConsumerIfPresent(jsm, "KUBEMOOT_REQUEST", "request-x"));
    }

    // --- Ack reliability + poison handling (Discussion Request Pump Can Wedge a Crew) ---

    @Test
    void ackConfirmedUsesSyncAck() throws Exception {
        Message msg = mock(Message.class);
        RequestQueueConsumer.ackConfirmed(msg);
        // Confirmed ack so a dropped ack can't leave the maxAckPending=1 slot occupied.
        verify(msg, times(1)).ackSync(any(Duration.class));
        verify(msg, never()).ack();
    }

    @Test
    void ackConfirmedFallsBackToAsyncAckWhenSyncFails() throws Exception {
        Message msg = mock(Message.class);
        doThrow(new RuntimeException("ack confirm timed out")).when(msg).ackSync(any(Duration.class));

        RequestQueueConsumer.ackConfirmed(msg); // must not propagate
        verify(msg, times(1)).ack();
    }

    @Test
    void deliveredCountReturnsZeroWhenMetadataUnavailable() {
        Message msg = mock(Message.class);
        when(msg.metaData()).thenThrow(new IllegalStateException("not a JetStream message"));
        // Robust default: never throw out of the failure path that decides dead-lettering.
        assertEquals(0L, RequestQueueConsumer.deliveredCount(msg));
    }

    // The core guarantee: a request whose orchestrate() throws must NOT propagate out
    // of processMessage (the consumer loop keeps pulling), and a poison request must be
    // dead-lettered instead of wedging the FIFO queue. This is the exact live-wedge
    // scenario the fix targets. See [[Discussion Request Pump Can Wedge a Crew]].
    @Test
    void processMessageDoesNotPropagateAndDeadLettersWhenOrchestrateThrows() throws Exception {
        var consumer = coordinatorConsumer(orchestratorThatThrows());
        Message msg = validRequestMessage();
        // metadata unavailable -> deliveredCount 0 -> dead-letter (cannot count retries)
        when(msg.metaData()).thenThrow(new IllegalStateException("no meta"));

        assertDoesNotThrow(() -> consumer.processMessage(msg));
        verify(msg, never()).ack();
        verify(msg, never()).ackSync(any(Duration.class));
        verify(msg, times(1)).term();
    }

    @Test
    void processMessageAcksOnSuccess() throws Exception {
        var orch = mock(DiscussionOrchestrator.class);
        var consumer = coordinatorConsumer(orch);
        Message msg = validRequestMessage();

        consumer.processMessage(msg);

        verify(orch, times(1)).orchestrate("hi", "c1", "demo");
        verify(msg, times(1)).ackSync(any(Duration.class));
        verify(msg, never()).term();
    }

    // A pod shutdown interrupts orchestrate(), which returns without an answer. The
    // request goes back to the queue for the replacement coordinator instead of being
    // acked, which dropped the question and left its thread open forever.
    @Test
    void processMessageReturnsTheRequestWhenShutdownInterruptsIt() throws Exception {
        var orch = mock(DiscussionOrchestrator.class);
        when(orch.orchestrate(anyString(), anyString(), anyString())).thenAnswer(inv -> {
            Thread.currentThread().interrupt();
            return null;
        });
        var consumer = coordinatorConsumer(orch);
        Message msg = validRequestMessage();

        try {
            consumer.processMessage(msg);
        } finally {
            Thread.interrupted();
        }

        verify(msg, times(1)).nak();
        verify(msg, never()).ackSync(any(Duration.class));
        verify(msg, never()).ack();
        verify(msg, never()).term();
    }

    @Test
    void processMessageReturnsTheRequestAfterShutdownStarted() throws Exception {
        var orch = mock(DiscussionOrchestrator.class);
        var consumer = coordinatorConsumer(orch);
        consumer.onShutdown(null);
        Message msg = validRequestMessage();
        doThrow(new IllegalStateException("connection closed")).when(msg).nak();

        assertDoesNotThrow(() -> consumer.processMessage(msg));

        verify(msg, times(1)).nak();
        verify(msg, never()).ackSync(any(Duration.class));
    }

    @Test
    void processMessageAcksADiscussionThatFinishedAsShutdownBegan() throws Exception {
        var orch = mock(DiscussionOrchestrator.class);
        when(orch.orchestrate(anyString(), anyString(), anyString()))
                .thenReturn(new DiscussionOrchestrator.OrchestrateResult("t1", "answer"));
        var consumer = coordinatorConsumer(orch);
        consumer.onShutdown(null);
        Message msg = validRequestMessage();

        consumer.processMessage(msg);

        verify(msg, times(1)).ackSync(any(Duration.class));
        verify(msg, never()).nak();
    }

    private static DiscussionOrchestrator orchestratorThatThrows() {
        var orch = mock(DiscussionOrchestrator.class);
        when(orch.orchestrate(anyString(), anyString(), anyString()))
                .thenThrow(new RuntimeException("orchestrate boom"));
        return orch;
    }

    private static RequestQueueConsumer coordinatorConsumer(DiscussionOrchestrator orch) {
        var nats = mock(NatsConnectionProvider.class);
        var props = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        when(props.discuss()).thenReturn(discuss);
        when(discuss.coordinator()).thenReturn(true);
        when(props.crew()).thenReturn(Optional.of("demo"));
        return new RequestQueueConsumer(nats, orch, props);
    }

    private static Message validRequestMessage() {
        Message msg = mock(Message.class);
        when(msg.getData()).thenReturn(
                "{\"message\":\"hi\",\"conversationId\":\"c1\",\"crew\":\"demo\"}"
                        .getBytes(StandardCharsets.UTF_8));
        return msg;
    }
}
