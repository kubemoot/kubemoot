package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import io.micrometer.core.instrument.simple.SimpleMeterRegistry;

import java.util.List;
import java.util.Optional;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/**
 * Tests for the triage subcommittee selection logic in DiscussionOrchestrator.
 * Uses plain JUnit (no @QuarkusTest) — CI-friendly without Ollama.
 */
class DiscussionOrchestratorTriageTest {

    private DiscussionOrchestrator orchestrator;

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

        var metrics = new DiscussionMetrics(new SimpleMeterRegistry());
        var latencyTracker = mock(LatencyTracker.class);

        var resumeSearchClient = mock(ai.kubemoot.agent.rag.ResumeSearchClient.class);
        orchestrator = new DiscussionOrchestrator(natsProvider, properties, chatService, metrics, latencyTracker, resumeSearchClient);
    }

    @Test
    void parseTriageResult_validJson_returnsCorrectInnerCircle() {
        String json = """
                {"agents": [
                    {"name": "k8s-config", "confidence": 0.95, "reason": "Has resources_list"},
                    {"name": "k8s-workloads", "confidence": 0.90, "reason": "Has pod management"}
                ], "overallConfidence": 0.85}
                """;

        var result = orchestrator.parseTriageResult(json);

        assertNotNull(result);
        assertEquals(2, result.agents().size());
        assertEquals("k8s-config", result.agents().get(0).name());
        assertEquals(0.95, result.agents().get(0).confidence(), 0.01);
        assertEquals("k8s-workloads", result.agents().get(1).name());
        assertEquals(0.85, result.overallConfidence(), 0.01);
    }

    @Test
    void parseTriageResult_withMarkdownFences_stripsAndParses() {
        String json = """
                ```json
                {"agents": [{"name": "nvidia-gpu", "confidence": 0.9, "reason": "GPU metrics"}],
                 "overallConfidence": 0.9}
                ```
                """;

        var result = orchestrator.parseTriageResult(json);

        assertNotNull(result);
        assertEquals(1, result.agents().size());
        assertEquals("nvidia-gpu", result.agents().get(0).name());
    }

    @Test
    void parseTriageResult_malformedJson_returnsNull() {
        var result = orchestrator.parseTriageResult("not json at all");
        assertNull(result);
    }

    @Test
    void parseTriageResult_emptyString_returnsNull() {
        assertNull(orchestrator.parseTriageResult(""));
        assertNull(orchestrator.parseTriageResult(null));
    }

    @Test
    void parseTriageResult_emptyAgentsList_returnsEmptyList() {
        String json = """
                {"agents": [], "overallConfidence": 1.0}
                """;

        var result = orchestrator.parseTriageResult(json);

        assertNotNull(result);
        assertTrue(result.agents().isEmpty());
        assertEquals(1.0, result.overallConfidence(), 0.01);
    }

    @Test
    void parseTriageResult_missingConfidence_defaultsToHalf() {
        String json = """
                {"agents": [{"name": "k8s-config"}]}
                """;

        var result = orchestrator.parseTriageResult(json);

        assertNotNull(result);
        assertEquals(1, result.agents().size());
        assertEquals(0.5, result.agents().get(0).confidence(), 0.01);
        assertEquals(0.5, result.overallConfidence(), 0.01);
    }

    @Test
    void parseTriageResult_agentWithEmptyName_skipped() {
        String json = """
                {"agents": [
                    {"name": "", "confidence": 0.9},
                    {"name": "k8s-config", "confidence": 0.8}
                ], "overallConfidence": 0.7}
                """;

        var result = orchestrator.parseTriageResult(json);

        assertNotNull(result);
        assertEquals(1, result.agents().size());
        assertEquals("k8s-config", result.agents().get(0).name());
    }

    @Test
    void lowConfidence_belowThreshold_flagsForOnboarding() {
        String json = """
                {"agents": [], "overallConfidence": 0.15}
                """;

        var result = orchestrator.parseTriageResult(json);

        assertNotNull(result);
        assertTrue(result.overallConfidence() < 0.3,
                "Confidence below 0.3 should signal a capability gap for onboarding");
    }

    @Test
    void highConfidence_emptyAgents_coordinatorDirectAnswer() {
        String json = """
                {"agents": [], "overallConfidence": 1.0}
                """;

        var result = orchestrator.parseTriageResult(json);

        assertNotNull(result);
        assertTrue(result.agents().isEmpty());
        assertTrue(result.overallConfidence() >= 0.8,
                "High confidence with 0 agents means coordinator can answer directly");
    }
}
