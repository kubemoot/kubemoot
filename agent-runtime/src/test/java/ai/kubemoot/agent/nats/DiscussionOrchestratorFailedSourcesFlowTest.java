package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.Optional;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** A failure signal's tool names reach the thread's failed sources, and a new round forgets them. */
class DiscussionOrchestratorFailedSourcesFlowTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() {
        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        var triageModel = mock(AgentProperties.TriageModel.class);
        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("coordinator");
        when(properties.triageModel()).thenReturn(triageModel);
        when(triageModel.timeoutSeconds()).thenReturn(120);
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("general"));
        orchestrator = new DiscussionOrchestrator(mock(NatsConnectionProvider.class), properties,
                mock(ChatService.class), new DiscussionMetrics(new SimpleMeterRegistry()),
                mock(LatencyTracker.class), mock(ResumeSearchClient.class));
    }

    private JsonNode failureOf(String failedTool) throws Exception {
        return MAPPER.readTree("{\"metadata\":{\"failedTool\":\"" + failedTool + "\"}}");
    }

    @Test
    void aFailureSignal_recordsItsToolNames() throws Exception {
        var state = new DiscussionOrchestrator.ThreadState("t1");

        orchestrator.handleAgentSignal(state, "library-search", "failure", "Tools unavailable ...",
                failureOf("search_oreilly_content"));

        assertEquals(Set.of("search_oreilly_content"), state.failedSources);
        assertTrue(state.failureSignals.containsKey("library-search"));
    }

    @Test
    void aFailureNamingAHostInsteadOfATool_recordsNothing() throws Exception {
        var state = new DiscussionOrchestrator.ThreadState("t1");

        orchestrator.handleAgentSignal(state, "a", "failure", "x", failureOf("ocrew-gateway.crew-ocrew.svc"));

        assertTrue(state.failedSources.isEmpty());
        assertTrue(state.failureSignals.containsKey("a"));
    }

    @Test
    void aNewEvaluationRound_forgetsThem() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("a", "x");
        state.failedSources.add("tool_a");

        orchestrator.resetEvaluationClock(state);

        assertTrue(state.failedSources.isEmpty());
    }

    @Test
    void aReopenedThread_forgetsThem() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.phase = DiscussionOrchestrator.Phase.CLOSED;
        state.failureSignals.put("a", "x");
        state.failedSources.add("tool_a");

        orchestrator.handleThreadReopen(state, "t1", "reply", "human");

        assertTrue(state.failedSources.isEmpty());
        assertTrue(state.failureSignals.isEmpty());
    }
}
