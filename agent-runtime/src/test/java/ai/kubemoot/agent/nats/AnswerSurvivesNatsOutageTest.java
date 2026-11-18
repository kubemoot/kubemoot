package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.JetStreamManagement;
import io.nats.client.impl.Headers;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.time.Duration;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.mockingDetails;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * A NATS restart while the synthesis and the thread close are being published must not
 * lose them: the publishes wait for NATS and JetStream, and a repeat carries the same message id.
 */
class AnswerSurvivesNatsOutageTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String THREAD = "t-1";

    private NatsConnectionProvider nats;
    private Connection conn;
    private JetStreamManagement management;
    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() throws Exception {
        nats = mock(NatsConnectionProvider.class);
        conn = mock(Connection.class);
        management = mock(JetStreamManagement.class);
        when(nats.isConfigured()).thenReturn(true);
        when(nats.getConnection()).thenReturn(conn);
        when(nats.scope()).thenReturn(new CrewScope("crew-ns", "demo"));
        when(conn.getStatus()).thenReturn(Connection.Status.CONNECTED);
        when(conn.jetStreamManagement()).thenReturn(management);
        orchestrator = new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("kubernetes"),
                mock(ChatService.class), mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
        patience(Duration.ofSeconds(10));
    }

    private void patience(Duration bound) {
        orchestrator.outagePublisherForTest(new OutageAwarePublisher(nats, bound, Duration.ofMillis(1)));
    }

    /** The bodies published so far, with or without a stream id header; the count must be {@code expected}. */
    private List<JsonNode> publishedBodies(int expected) {
        var bodies = new java.util.ArrayList<JsonNode>();
        for (var call : mockingDetails(conn).getInvocations()) {
            if (call.getMethod().getName().equals("publish")) {
                Object[] args = call.getArguments();
                try {
                    bodies.add(MAPPER.readTree((byte[]) args[args.length - 1]));
                } catch (IOException e) {
                    throw new IllegalStateException(e);
                }
            }
        }
        assertEquals(expected, bodies.size());
        return bodies;
    }

    @Test
    void theSynthesisWaitsForJetStreamAfterARestartAndIsThenPublishedOnce() throws Exception {
        when(management.getAccountStatistics())
                .thenThrow(new IOException("503 No Responders Available For Request"))
                .thenThrow(new IOException("503 No Responders Available For Request"))
                .thenReturn(null);

        orchestrator.publishToThread(THREAD, "general", "the answer", DiscussionOrchestrator.MSG_SYNTHESIS);

        // broadcast and the channel: two subjects, each published once, none while JetStream was down
        var bodies = publishedBodies(2);
        assertEquals("the answer", bodies.get(0).path("content").asText());
        assertEquals(bodies.get(0).path("messageId"), bodies.get(1).path("messageId"));
        verify(management, times(3)).getAccountStatistics();
    }

    @Test
    void theSynthesisWaitsForTheConnectionAndIsNotDroppedWhenThereIsNone() throws Exception {
        when(nats.getConnection()).thenReturn(null, null, conn);

        orchestrator.publishToThread(THREAD, "general", "the answer", DiscussionOrchestrator.MSG_SYNTHESIS);

        assertEquals("the answer", publishedBodies(2).get(0).path("content").asText());
    }

    @Test
    void aPublishTheServerDidNotConfirmIsRepeatedWithTheSameMessageId() throws Exception {
        doThrow(new java.util.concurrent.TimeoutException("flush timed out")).doNothing().when(conn).flush(any(Duration.class));

        orchestrator.publishToThread(THREAD, "general", "the answer", DiscussionOrchestrator.MSG_SYNTHESIS);

        var bodies = publishedBodies(4);
        assertEquals(1, bodies.stream().map(b -> b.path("messageId").asText()).distinct().count(),
                "the repeat is the same message, which the stream and the consumers drop");
    }

    @Test
    void theThreadCloseWaitsOutARestartToo() throws Exception {
        when(management.getAccountStatistics()).thenThrow(new IOException("503")).thenReturn(null);

        orchestrator.publishThreadClose(THREAD, "general");

        var bodies = publishedBodies(2);
        assertEquals("thread_close", bodies.get(0).path("messageType").asText());
    }

    @Test
    void anOutageThatOutlastsTheBoundPublishesNothingAndThrowsNothing() throws Exception {
        patience(Duration.ofMillis(50));
        when(management.getAccountStatistics()).thenThrow(new IOException("503"));

        assertDoesNotThrow(() ->
                orchestrator.publishToThread(THREAD, "general", "the answer", DiscussionOrchestrator.MSG_SYNTHESIS));

        verify(conn, never()).publish(anyString(), any(Headers.class), any(byte[].class));
        verify(conn, never()).publish(anyString(), any(byte[].class));
    }

    @Test
    void theRunnerReportsWhetherThePublishesWereTaken() throws Exception {
        var publisher = new OutageAwarePublisher(nats, Duration.ofMillis(50), Duration.ofMillis(1));
        doNothing().when(conn).flush(any(Duration.class));
        assertTrue(publisher.run("x", c -> c.publish("s", new byte[0])));
        verify(conn).publish(eq("s"), any(byte[].class));

        when(management.getAccountStatistics()).thenThrow(new IOException("503"));
        assertFalse(publisher.run("x", c -> c.publish("s2", new byte[0])));
    }
}
