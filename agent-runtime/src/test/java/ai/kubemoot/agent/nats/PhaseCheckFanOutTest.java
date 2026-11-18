package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import io.nats.client.Connection;
import io.nats.client.JetStream;
import io.nats.client.JetStreamOptions;
import io.nats.client.PublishOptions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.time.Duration;
import java.time.Instant;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.timeout;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * The periodic phase check runs each thread's check on the llmExecutor, so a close that
 * waits out a NATS outage holds neither the scheduler nor another thread's check.
 */
class PhaseCheckFanOutTest {

    private DiscussionOrchestrator orchestrator;
    private DiscussionMetrics metrics;
    private JetStream js;

    @BeforeEach
    void setUp() throws Exception {
        var nats = mock(NatsConnectionProvider.class);
        var conn = mock(Connection.class);
        js = mock(JetStream.class);
        metrics = mock(DiscussionMetrics.class);
        when(nats.isConfigured()).thenReturn(true);
        when(nats.getConnection()).thenReturn(conn);
        when(nats.scope()).thenReturn(new CrewScope("crew-ns", "demo"));
        when(conn.getStatus()).thenReturn(Connection.Status.CONNECTED);
        when(conn.jetStream(any(JetStreamOptions.class))).thenReturn(js);
        // thread tA's subjects are down; everything else is acknowledged
        when(js.publish(anyString(), any(byte[].class), any(PublishOptions.class))).thenAnswer(call -> {
            if (((String) call.getArgument(0)).endsWith(".tA")) {
                throw new IOException("503");
            }
            return null;
        });
        var publisher = new OutageAwarePublisher(nats, new OutageBackoff(Duration.ofSeconds(4), Duration.ofMillis(5)));
        orchestrator = new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("kubernetes"),
                mock(ChatService.class), metrics, mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class), publisher);
    }

    /** A thread past the hard ceiling, so its next check closes it. */
    private DiscussionOrchestrator.ThreadState overdueThread(String id) {
        orchestrator.handleMessage("{\"messageId\":\"m-" + id + "\",\"threadId\":\"" + id
                + "\",\"agentName\":\"human\",\"messageType\":\"thread_start\",\"content\":\"\","
                + "\"channel\":\"kubernetes\"}");
        var state = orchestrator.threadForTest(id);
        state.threadCreated = Instant.now().minus(Duration.ofDays(1));
        return state;
    }

    @Test
    void aCloseWaitingOnOneThreadDoesNotBlockAnotherThreadsCheck() throws Exception {
        var a = overdueThread("tA");
        var b = overdueThread("tB");

        orchestrator.checkPhaseTransitions();

        long started = System.nanoTime();
        while (b.phase != DiscussionOrchestrator.Phase.CLOSED && Duration.ofNanos(System.nanoTime() - started).toSeconds() < 3) {
            Thread.sleep(10);
        }
        assertEquals(DiscussionOrchestrator.Phase.CLOSED, b.phase, "tB closed while tA's close was still waiting");
        assertTrue(a.phase != DiscussionOrchestrator.Phase.CLOSED || Duration.ofNanos(System.nanoTime() - started).toSeconds() >= 3,
                "tA is still waiting for NATS");
    }

    @Test
    void overlappingTicksCloseAThreadOnce() throws Exception {
        overdueThread("tA");

        for (int i = 0; i < 5; i++) {
            orchestrator.checkPhaseTransitions();
            Thread.sleep(20);
        }

        verify(metrics, timeout(2000).times(1)).threadTimedOut();
        Thread.sleep(300);
        verify(metrics, times(1)).threadTimedOut();
    }

    @Test
    void aRefusedSubmitAfterShutdownReturnsCleanlyAndLeavesNoThreadMarkedQueued() {
        var b = overdueThread("tB");
        orchestrator.stopLlmExecutor();

        assertDoesNotThrow(() -> orchestrator.checkPhaseTransitions());

        assertTrue(!b.checkQueued.get(), "a refused submit must not leave the thread marked as queued");
    }
}
