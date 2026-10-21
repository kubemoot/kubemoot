package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * The coordinator sets an agent's deadline from the latency history of the server its
 * last timed call ran on. The evaluating and heartbeat signals name no server (the
 * call is not placed yet), so the server comes from the agent's last terminal signal.
 */
class DiscussionOrchestratorDeadlineServerTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private DiscussionOrchestrator orchestrator;
    private LatencyTracker latencyTracker;

    @BeforeEach
    void setUp() {
        // Build minimal orchestrator for testing parseTriageResult()
        var natsProvider = mock(NatsConnectionProvider.class);
        var chatService = mock(ChatService.class);
        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        when(properties.discuss()).thenReturn(discuss);
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("kubernetes"));
        when(discuss.settleSeconds()).thenReturn(5);
        when(discuss.minEvalSeconds()).thenReturn(20);
        when(discuss.minReviewSeconds()).thenReturn(10);
        when(discuss.advisoryTimeoutSeconds()).thenReturn(10);
        when(discuss.evaluationTimeoutSeconds()).thenReturn(15);
        when(discuss.reviewTimeoutSeconds()).thenReturn(15);
        when(discuss.synthesisTimeoutSeconds()).thenReturn(90);
        when(discuss.evalGraceSeconds()).thenReturn(10);
        when(discuss.conversationMaxTurns()).thenReturn(10);
        when(discuss.conversationTtlMinutes()).thenReturn(1440);
        when(discuss.conversationKvBucket()).thenReturn("kubemoot_conversations");
        when(discuss.resumePreFilterTopK()).thenReturn(5);
        when(properties.agentName()).thenReturn("test-coordinator");

        latencyTracker = mock(LatencyTracker.class);
        var metrics = new DiscussionMetrics(new SimpleMeterRegistry());

        var resumeSearchClient = mock(ai.kubemoot.agent.rag.ResumeSearchClient.class);
        orchestrator = new DiscussionOrchestrator(natsProvider, properties, chatService, metrics, latencyTracker, resumeSearchClient);
    }

    private static JsonNode signal(String json) {
        try {
            return MAPPER.readTree(json);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }

    private static final String NO_SERVER = "{\"metadata\": {}}";
    private static final String RAN_ON_SERVER_B =
            "{\"metadata\": {\"gpuLabel\": \"server-b\", \"inferenceMs\": 4000}}";

    @Test
    void evaluatingWithNoServer_beforeAnyCallRan_looksUpTheUnknownServer() {
        var state = new DiscussionOrchestrator.ThreadState("t1");

        orchestrator.handleAgentSignal(state, "k8s-nodes", "evaluating", "", signal(NO_SERVER));

        verify(latencyTracker).getExpectedSeconds("k8s-nodes", "unknown");
    }

    @Test
    void evaluatingWithNoServer_usesTheServerTheAgentsLastCallRanOn() {
        var first = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(first, "k8s-nodes", "evaluating", "", signal(NO_SERVER));
        orchestrator.handleAgentSignal(first, "k8s-nodes", "stand_aside", "", signal(RAN_ON_SERVER_B));
        verify(latencyTracker).recordLatency("k8s-nodes", "server-b", 4000L);

        var second = new DiscussionOrchestrator.ThreadState("t2");
        orchestrator.handleAgentSignal(second, "k8s-nodes", "evaluating", "", signal(NO_SERVER));
        orchestrator.handleAgentSignal(second, "k8s-nodes", "heartbeat", "", signal(NO_SERVER));

        verify(latencyTracker, org.mockito.Mockito.times(2)).getExpectedSeconds("k8s-nodes", "server-b");
    }

    @Test
    void anotherAgentsServer_isNotBorrowed() {
        var first = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(first, "k8s-nodes", "evaluating", "", signal(NO_SERVER));
        orchestrator.handleAgentSignal(first, "k8s-nodes", "stand_aside", "", signal(RAN_ON_SERVER_B));

        assertEquals("unknown", orchestrator.expectedServer("compute", signal(NO_SERVER)));
        assertEquals("server-b", orchestrator.expectedServer("k8s-nodes", signal(NO_SERVER)));
        assertEquals("server-c", orchestrator.expectedServer("k8s-nodes",
                signal("{\"metadata\": {\"gpuLabel\": \"server-c\"}}")),
                "a signal that names its server is looked up as named");
    }
}
