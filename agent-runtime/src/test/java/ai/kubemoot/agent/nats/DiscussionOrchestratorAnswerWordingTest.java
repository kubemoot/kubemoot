package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.Optional;
import java.util.regex.Pattern;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * A published answer never carries operator instructions: what an operator should check goes to
 * the log and the gap-detected signal, and the answer says only what the reader needs.
 */
class DiscussionOrchestratorAnswerWordingTest {

    private static final Pattern OPERATOR_WORDING = Pattern.compile("operator|logs|mcp|timeline|dashboard",
            Pattern.CASE_INSENSITIVE);
    private static final String ANSWER = "Here is what I found.";

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

    private String publish(DiscussionOrchestrator.ThreadState state, String response) {
        return orchestrator.detectAndAppendGapHint(state, response, 0, "general");
    }

    @Test
    void anInfrastructureFailure_addsOnlyPlainWording() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("library-search", "Tool 'search_oreilly_content' failed 2 times");
        state.failedSources.add("search_oreilly_content");

        String answer = publish(state, ANSWER);

        assertTrue(answer.startsWith(ANSWER));
        assertTrue(answer.contains("Some sources could not be reached: search_oreilly_content"), answer);
        assertFalse(OPERATOR_WORDING.matcher(answer).find(), answer);
        assertFalse(answer.contains("library-search"), answer);
    }

    @Test
    void anInfrastructureFailureWithNoNamedSource_stillSaysPlainly() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("library-search", "timed out");

        String answer = publish(state, ANSWER);

        assertTrue(answer.contains("Some sources could not be reached."), answer);
        assertFalse(OPERATOR_WORDING.matcher(answer).find(), answer);
    }

    @Test
    void theOperatorGuidance_livesInTheLogMessage() {
        String guidance = DiscussionOrchestrator.infrastructureGapGuidance("t1", java.util.Set.of("library-search"));

        assertTrue(guidance.contains("Check the failing MCP servers"), guidance);
        assertTrue(guidance.contains("library-search"), guidance);
        assertTrue(guidance.contains("dashboard"), guidance);
    }

    @Test
    void theGapTypeStaysInfrastructure_soTheSignalAndMetricsKeepTheirMeaning() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("library-search", "timed out");

        assertEquals(DiscussionOrchestrator.GapType.INFRASTRUCTURE_GAP,
                DiscussionOrchestrator.classifyNoAgreeGap(state));
    }

    @Test
    void aToolGap_keepsTheOnboardingHintWithoutOperatorWording() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.concernSignals.put("tooler", "needs a calendar tool");

        String answer = publish(state, ANSWER);

        assertTrue(answer.contains("reply: `onboard <tool-domain>`"), answer);
        assertFalse(OPERATOR_WORDING.matcher(answer).find(), answer);
    }

    @Test
    void aToolerGap_keepsTheOnboardingHintWithoutOperatorWording() {
        var state = new DiscussionOrchestrator.ThreadState("t1");

        String answer = publish(state, "No tooler covers this; you could onboard one.");

        assertTrue(answer.contains("reply: `onboard <domain>`"), answer);
        assertFalse(OPERATOR_WORDING.matcher(answer.substring(ANSWER.length())).find(), answer);
    }

    @Test
    void noGap_changesNothing() {
        var state = new DiscussionOrchestrator.ThreadState("t1");

        assertEquals(ANSWER, publish(state, ANSWER));
    }
}
