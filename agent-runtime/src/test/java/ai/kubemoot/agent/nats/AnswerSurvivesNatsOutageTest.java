package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import com.fasterxml.jackson.databind.JsonNode;
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
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * A NATS restart while the synthesis and the thread close are being published must not
 * lose them: each publish goes through JetStream, is tried again until the stream
 * acknowledges it, and a repeat carries the same message id.
 */
class AnswerSurvivesNatsOutageTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String THREAD = "t-1";

    private NatsConnectionProvider nats;
    private Connection conn;
    private JetStream js;
    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() throws Exception {
        nats = mock(NatsConnectionProvider.class);
        conn = mock(Connection.class);
        js = mock(JetStream.class);
        when(nats.isConfigured()).thenReturn(true);
        when(nats.getConnection()).thenReturn(conn);
        when(nats.scope()).thenReturn(new CrewScope("crew-ns", "demo"));
        when(conn.getStatus()).thenReturn(Connection.Status.CONNECTED);
        when(conn.jetStream(any(JetStreamOptions.class))).thenReturn(js);
        orchestrator = orchestratorWaiting(Duration.ofSeconds(10));
    }

    private DiscussionOrchestrator orchestratorWaiting(Duration bound) {
        var publisher = new OutageAwarePublisher(nats, new OutageBackoff(bound, Duration.ofMillis(1)));
        return new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("kubernetes"),
                mock(ChatService.class), mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class), publisher);
    }

    /** Every JetStream publish attempted so far: its subject and decoded body. */
    private record Attempt(String subject, JsonNode body, String streamId) {
    }

    private List<Attempt> attempts() throws Exception {
        var subject = ArgumentCaptor.forClass(String.class);
        var body = ArgumentCaptor.forClass(byte[].class);
        var options = ArgumentCaptor.forClass(PublishOptions.class);
        verify(js, org.mockito.Mockito.atLeast(0)).publish(subject.capture(), body.capture(), options.capture());
        var all = new java.util.ArrayList<Attempt>();
        for (int i = 0; i < subject.getAllValues().size(); i++) {
            all.add(new Attempt(subject.getAllValues().get(i), MAPPER.readTree(body.getAllValues().get(i)),
                    options.getAllValues().get(i).getMessageId()));
        }
        return all;
    }

    private PublishAck stored() {
        return mock(PublishAck.class);
    }

    @Test
    void theSynthesisIsPublishedThroughJetStreamAndRetriedUntilAcknowledged() throws Exception {
        var ack = stored();
        when(js.publish(anyString(), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(new IOException("Error Publishing: 503 No Responders Available For Request"))
                .thenThrow(new IOException("Error Publishing: 503 No Responders Available For Request"))
                .thenReturn(ack);

        orchestrator.publishToThread(THREAD, "general", "the answer", DiscussionOrchestrator.MSG_SYNTHESIS);

        // two failed tries of the broadcast subject, then both subjects acknowledged
        var all = attempts();
        assertEquals(4, all.size(), "two failed tries, then the broadcast and the channel subjects");
        assertEquals("the answer", all.get(3).body().path("content").asText());
        assertEquals(1, all.stream().map(a -> a.body().path("messageId").asText()).distinct().count(),
                "every try is the same message, so the stream and the consumers drop a repeat");
        verify(conn, never()).publish(anyString(), any(byte[].class));
    }

    @Test
    void aJetStreamUnavailableAnswerIsWaitedOutToo() throws Exception {
        var unavailable = mock(JetStreamApiException.class);
        when(unavailable.getApiErrorCode()).thenReturn(OutageAwarePublisher.JETSTREAM_UNAVAILABLE);
        var ack = stored();
        when(js.publish(anyString(), any(byte[].class), any(PublishOptions.class)))
                .thenThrow(unavailable).thenReturn(ack);

        orchestrator.publishThreadClose(THREAD, "general");

        assertEquals("thread_close", attempts().get(1).body().path("messageType").asText());
    }

    @Test
    void theSynthesisWaitsForTheConnectionAndIsNotDroppedWhenThereIsNone() throws Exception {
        when(nats.getConnection()).thenReturn(null, null, conn);

        orchestrator.publishToThread(THREAD, "general", "the answer", DiscussionOrchestrator.MSG_SYNTHESIS);

        assertEquals("the answer", attempts().get(0).body().path("content").asText());
    }

    @Test
    void aRefusalByTheStreamIsNotRetried() throws Exception {
        var refusal = mock(JetStreamApiException.class);
        when(refusal.getApiErrorCode()).thenReturn(10059);
        when(js.publish(anyString(), any(byte[].class), any(PublishOptions.class))).thenThrow(refusal);

        orchestrator.publishToThread(THREAD, "general", "the answer", DiscussionOrchestrator.MSG_SYNTHESIS);

        verify(js, times(1)).publish(anyString(), any(byte[].class), any(PublishOptions.class));
    }

    @Test
    void anOutageThatOutlastsTheBoundPublishesNothingAndThrowsNothing() throws Exception {
        var impatient = orchestratorWaiting(Duration.ofMillis(50));
        when(js.publish(anyString(), any(byte[].class), any(PublishOptions.class))).thenThrow(new IOException("503"));

        assertDoesNotThrow(() ->
                impatient.publishToThread(THREAD, "general", "the answer", DiscussionOrchestrator.MSG_SYNTHESIS));

        assertTrue(attempts().size() > 1, "it tried again before giving up");
    }

    @Test
    void aWholeCloseWaitsOutOneBoundNotOnePerPublish() throws Exception {
        var impatient = orchestratorWaiting(Duration.ofMillis(100));
        when(js.publish(anyString(), any(byte[].class), any(PublishOptions.class))).thenThrow(new IOException("503"));
        impatient.handleMessage("{\"messageId\":\"m1\",\"threadId\":\"" + THREAD
                + "\",\"agentName\":\"human\",\"messageType\":\"thread_start\",\"content\":\"\","
                + "\"channel\":\"kubernetes\"}");
        var state = impatient.threadForTest(THREAD);
        long started = System.nanoTime();

        impatient.closeThread(state, "general", "the answer", "synthesized");

        assertTrue(Duration.ofNanos(System.nanoTime() - started).toMillis() < 1000,
                "the synthesis and the close shared one outage deadline");
    }
}
