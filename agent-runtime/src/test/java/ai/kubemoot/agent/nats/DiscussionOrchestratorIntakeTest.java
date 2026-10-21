package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;
import io.nats.client.Connection;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/**
 * A question another publisher puts on the bus as a thread_start (the dashboard's
 * New Discussion form) starts the coordinator's phase machine, the same as a
 * question the coordinator starts itself.
 */
class DiscussionOrchestratorIntakeTest {

    private static final String THREAD = "t-intake";

    private DiscussionOrchestrator orchestrator;
    private DiscussionMetrics metrics;

    @BeforeEach
    void setUp() {
        var nats = mock(NatsConnectionProvider.class);
        when(nats.getConnection()).thenReturn(mock(Connection.class));
        when(nats.scope()).thenReturn(new CrewScope("crew-ns", "demo"));
        metrics = mock(DiscussionMetrics.class);
        orchestrator = new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("general"),
                mock(ChatService.class), metrics, mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
    }

    private void send(String agent, String type, String metadataJson) {
        orchestrator.handleMessage("{\"messageId\":\"m-" + agent + "-" + type + "\",\"threadId\":\"" + THREAD
                + "\",\"agentName\":\"" + agent + "\",\"messageType\":\"" + type
                + "\",\"content\":\"What books has Sam Newman written?\",\"channel\":\"general\""
                + (metadataJson == null ? "" : ",\"metadata\":" + metadataJson) + "}");
    }

    @Test
    void aHumanThreadStart_movesTheThreadToAdvisory() {
        send("human", "thread_start", "{\"userQuery\":\"What books has Sam Newman written?\"}");

        var state = orchestrator.threadForTest(THREAD);
        assertEquals(Phase.ADVISORY, state.phase, "the question must not wait in SUBMITTED");
        assertEquals("What books has Sam Newman written?", state.userQuery);
        assertEquals("general", state.primaryChannel);
        verify(metrics).threadStarted();
    }

    @Test
    void theThreadStartsCrewAndConversation_areKept() {
        send("human", "thread_start", "{\"crew\":\"oguide\",\"conversationId\":\"c-1\"}");

        var state = orchestrator.threadForTest(THREAD);
        assertEquals("oguide", state.crew);
        assertEquals("c-1", state.conversationId);
    }

    @Test
    void aThreadStartWithoutMetadata_stillStartsTheThread() {
        send("human", "thread_start", null);

        var state = orchestrator.threadForTest(THREAD);
        assertEquals(Phase.ADVISORY, state.phase);
        assertNull(state.conversationId);
    }

    @Test
    void theCoordinatorsOwnThreadStart_doesNotStartTheThreadAgain() {
        String self = DiscussionOrchestratorTest.stubProperties("general").agentName();
        send(self, "thread_start", null);

        assertEquals(Phase.SUBMITTED, orchestrator.threadForTest(THREAD).phase,
                "the coordinator moves its own threads in orchestrate()");
        verify(metrics, never()).threadStarted();
    }

    @Test
    void aSecondThreadStart_doesNotRestartAThreadAlreadyUnderWay() {
        send("human", "thread_start", null);
        var state = orchestrator.threadForTest(THREAD);
        state.phase = Phase.EVALUATING;

        orchestrator.handleMessage("{\"messageId\":\"m-again\",\"threadId\":\"" + THREAD
                + "\",\"agentName\":\"human\",\"messageType\":\"thread_start\",\"content\":\"again\"}");

        assertEquals(Phase.EVALUATING, state.phase);
        verify(metrics, times(1)).threadStarted();
    }

    @Test
    void anotherMessageType_leavesANewThreadSubmitted() {
        send("k8s", "agree", null);

        assertEquals(Phase.SUBMITTED, orchestrator.threadForTest(THREAD).phase);
        verify(metrics, never()).threadStarted();
    }
}
