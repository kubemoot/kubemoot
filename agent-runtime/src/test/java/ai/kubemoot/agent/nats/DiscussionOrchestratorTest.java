package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.mock;

/**
 * Tests for DiscussionOrchestrator's testable utility methods.
 * Uses plain JUnit 5 (no @QuarkusTest) to avoid NATS/Ollama dependency in CI.
 *
 * Note: Full phase transition testing is not practical without @QuarkusTest
 * because DiscussionOrchestrator requires NATS connections, scheduled executors,
 * and a circular dependency with ChatService. These tests cover the package-private
 * utility methods: parseTriageResult, classifyChannelFromAdvisory,
 * subscriptionSubject, and formatConversationContext.
 */
class DiscussionOrchestratorTest {

    // --- parseTriageResult tests ---

    @Test
    void parseTriageResult_validJson_parsesAgents() {
        var orchestrator = createOrchestrator("kubernetes,helm");
        var result = orchestrator.parseTriageResult("""
                {"agents": [{"name": "k8s-agent", "confidence": 0.9, "reason": "Has kubectl tools"}],
                 "overallConfidence": 0.85}""");

        assertNotNull(result);
        assertEquals(1, result.agents().size());
        assertEquals("k8s-agent", result.agents().getFirst().name());
        assertEquals(0.9, result.agents().getFirst().confidence(), 0.001);
        assertEquals("Has kubectl tools", result.agents().getFirst().reason());
        assertEquals(0.85, result.overallConfidence(), 0.001);
    }

    @Test
    void parseTriageResult_multipleAgents() {
        var orchestrator = createOrchestrator("kubernetes");
        var result = orchestrator.parseTriageResult("""
                {"agents": [
                    {"name": "agent-a", "confidence": 0.95, "reason": "reason A"},
                    {"name": "agent-b", "confidence": 0.7, "reason": "reason B"}
                ], "overallConfidence": 0.8}""");

        assertNotNull(result);
        assertEquals(2, result.agents().size());
        assertEquals("agent-a", result.agents().get(0).name());
        assertEquals("agent-b", result.agents().get(1).name());
    }

    @Test
    void parseTriageResult_withMarkdownFences_stripsAndParses() {
        var orchestrator = createOrchestrator("kubernetes");
        var result = orchestrator.parseTriageResult("""
                ```json
                {"agents": [{"name": "test-agent", "confidence": 0.8, "reason": "test"}],
                 "overallConfidence": 0.75}
                ```""");

        assertNotNull(result);
        assertEquals(1, result.agents().size());
        assertEquals("test-agent", result.agents().getFirst().name());
    }

    @Test
    void parseTriageResult_nullResponse_returnsNull() {
        var orchestrator = createOrchestrator("kubernetes");
        assertNull(orchestrator.parseTriageResult(null));
    }

    @Test
    void parseTriageResult_emptyResponse_returnsNull() {
        var orchestrator = createOrchestrator("kubernetes");
        assertNull(orchestrator.parseTriageResult(""));
    }

    @Test
    void parseTriageResult_invalidJson_returnsNull() {
        var orchestrator = createOrchestrator("kubernetes");
        assertNull(orchestrator.parseTriageResult("not json at all"));
    }

    @Test
    void parseTriageResult_missingOverallConfidence_defaultsTo05() {
        var orchestrator = createOrchestrator("kubernetes");
        var result = orchestrator.parseTriageResult("""
                {"agents": [{"name": "agent", "confidence": 0.9, "reason": "test"}]}""");

        assertNotNull(result);
        assertEquals(0.5, result.overallConfidence(), 0.001);
    }

    @Test
    void parseTriageResult_emptyAgentName_skipped() {
        var orchestrator = createOrchestrator("kubernetes");
        var result = orchestrator.parseTriageResult("""
                {"agents": [{"name": "", "confidence": 0.9, "reason": "empty name"}],
                 "overallConfidence": 0.5}""");

        assertNotNull(result);
        assertTrue(result.agents().isEmpty());
    }

    @Test
    void parseTriageResult_missingAgentFields_usesDefaults() {
        var orchestrator = createOrchestrator("kubernetes");
        var result = orchestrator.parseTriageResult("""
                {"agents": [{"name": "minimal-agent"}], "overallConfidence": 0.6}""");

        assertNotNull(result);
        assertEquals(1, result.agents().size());
        assertEquals("minimal-agent", result.agents().getFirst().name());
        assertEquals(0.5, result.agents().getFirst().confidence(), 0.001);
        assertEquals("", result.agents().getFirst().reason());
    }

    // --- classifyChannelFromAdvisory tests ---

    @Test
    void classifyChannel_matchesTechnology() {
        var orchestrator = createOrchestrator("kubernetes,helm,proxmox");
        assertEquals("kubernetes", orchestrator.classifyChannelFromAdvisory(List.of("Kubernetes")));
    }

    @Test
    void classifyChannel_partialMatch() {
        var orchestrator = createOrchestrator("kubernetes,helm,proxmox");
        assertEquals("helm", orchestrator.classifyChannelFromAdvisory(List.of("helm-charts")));
    }

    @Test
    void classifyChannel_noMatch_returnsGeneral() {
        var orchestrator = createOrchestrator("kubernetes,helm");
        assertEquals("general", orchestrator.classifyChannelFromAdvisory(List.of("rabbitmq")));
    }

    @Test
    void classifyChannel_nullTechnologies_returnsGeneral() {
        var orchestrator = createOrchestrator("kubernetes");
        assertEquals("general", orchestrator.classifyChannelFromAdvisory(null));
    }

    @Test
    void classifyChannel_emptyTechnologies_returnsGeneral() {
        var orchestrator = createOrchestrator("kubernetes");
        assertEquals("general", orchestrator.classifyChannelFromAdvisory(List.of()));
    }

    @Test
    void classifyChannel_firstMatchWins() {
        var orchestrator = createOrchestrator("kubernetes,helm");
        // "kubernetes" should match first
        assertEquals("kubernetes", orchestrator.classifyChannelFromAdvisory(
                List.of("Kubernetes", "Helm")));
    }

    // --- subscriptionSubject tests ---

    @Test
    void subscriptionSubject_withCrew_includesCrewId() {
        assertEquals("kubemoot.discuss.my-crew.>",
                DiscussionOrchestrator.subscriptionSubject("my-crew"));
    }

    @Test
    void subscriptionSubject_nullCrew_globalWildcard() {
        assertEquals("kubemoot.discuss.>",
                DiscussionOrchestrator.subscriptionSubject(null));
    }

    @Test
    void subscriptionSubject_emptyCrew_globalWildcard() {
        assertEquals("kubemoot.discuss.>",
                DiscussionOrchestrator.subscriptionSubject(""));
    }

    // --- formatConversationContext tests ---

    @Test
    void formatConversationContext_emptyList_returnsEmptyString() {
        assertEquals("", DiscussionOrchestrator.formatConversationContext(List.of()));
    }

    @Test
    void formatConversationContext_nullList_returnsEmptyString() {
        assertEquals("", DiscussionOrchestrator.formatConversationContext(null));
    }

    @Test
    void formatConversationContext_withTurns_includesQAndA() {
        var context = List.of(
                Map.of("query", "What is Flux?", "response", "Flux is a GitOps tool"),
                Map.of("query", "How does it deploy?", "response", "Via HelmRelease CRDs")
        );
        var formatted = DiscussionOrchestrator.formatConversationContext(context);

        assertTrue(formatted.contains("Previous conversation"));
        assertTrue(formatted.contains("Q: What is Flux?"));
        assertTrue(formatted.contains("A: Flux is a GitOps tool"));
        assertTrue(formatted.contains("Q: How does it deploy?"));
        assertTrue(formatted.contains("A: Via HelmRelease CRDs"));
        assertTrue(formatted.contains("Current question"));
    }

    // --- Helper to construct DiscussionOrchestrator with mock dependencies ---

    private DiscussionOrchestrator createOrchestrator(String channels) {
        var natsProvider = mock(NatsConnectionProvider.class);
        var chatService = mock(ChatService.class);
        var metrics = mock(DiscussionMetrics.class);
        var latencyTracker = mock(LatencyTracker.class);
        var properties = stubProperties(channels);

        var resumeSearchClient = mock(ai.kubemoot.agent.rag.ResumeSearchClient.class);
        return new DiscussionOrchestrator(natsProvider, properties, chatService, metrics, latencyTracker, resumeSearchClient);
    }

    private static AgentProperties stubProperties(String channels) {
        return new AgentProperties() {
            @Override public Memory memory() { return ai.kubemoot.agent.TestStubs.memory(); }
            @Override public String agentName() { return "test-coordinator"; }
            @Override public String agentDescription() { return ""; }
            @Override public String agentType() { return "chat"; }
            @Override public Optional<String> systemPrompt() { return Optional.empty(); }
            @Override public Optional<String> systemPromptFile() { return Optional.empty(); }
            @Override public Model model() { return new Model() {
                @Override public String name() { return "default"; }
                @Override public String provider() { return "ollama"; }
                @Override public String model() { return "qwen2.5:32b"; }
                @Override public String endpoint() { return "http://localhost:11434"; }
                @Override public double temperature() { return 0.3; }
                @Override public int maxTokens() { return 4096; }
                @Override public int maxToolIterations() { return 10; }
                @Override public Optional<Boolean> think() { return Optional.empty(); }
            }; }
            @Override public Gateway gateway() { return new Gateway() {
                @Override public boolean enabled() { return false; }
                @Override public Optional<String> endpoint() { return Optional.empty(); }
                @Override public String healthCheckInterval() { return "30s"; }
                @Override public String healthCheckTimeout() { return "5s"; }
                @Override public boolean requiredForReadiness() { return true; }
            }; }
            @Override public Nats nats() { return new Nats() {
                @Override public Optional<String> url() { return Optional.empty(); }
            }; }
            @Override public Discuss discuss() { return new Discuss() {
                @Override public Optional<String> channels() {
                    return channels == null || channels.isEmpty() ? Optional.empty() : Optional.of(channels);
                }
                @Override public int timeoutSeconds() { return 30; }
                @Override public int maxInferencesPerMinute() { return 10; }
                @Override public int maxContributionsPerThread() { return 3; }
                @Override public boolean tooler() { return false; }
                @Override public Optional<String> keywords() { return Optional.empty(); }
                @Override public String relevanceMode() { return "keyword"; }
                @Override public Optional<String> relevancePromptHint() { return Optional.empty(); }
                @Override public String priority() { return "high"; }
                @Override public int advisoryGraceSeconds() { return 5; }
                @Override public String role() { return "coordinator"; }
                @Override public boolean coordinator() { return true; }
                @Override public boolean computeContract() { return false; }
                @Override public boolean metricsDrillContract() { return false; }
                @Override public Optional<String> alwaysCandidateAgents() { return Optional.empty(); }
                @Override public int synthesisCompletenessRetries() { return 0; }
                @Override public boolean answerDirectly() { return true; }
                @Override public boolean hasAnalysts() { return false; }
                @Override public int advisoryTimeoutSeconds() { return 10; }
                @Override public int evaluationTimeoutSeconds() { return 300; }
                @Override public int reviewTimeoutSeconds() { return 15; }
                @Override public int minEvalSeconds() { return 45; }
                @Override public int settleSeconds() { return 5; }
                @Override public int minReviewSeconds() { return 10; }
                @Override public int synthesisTimeoutSeconds() { return 90; }
                @Override public Optional<String> triagePrompt() { return Optional.empty(); }
                @Override public Optional<String> jetstreamConsumer() { return Optional.empty(); }
                @Override public int evalGraceSeconds() { return 10; }
                @Override public int conversationMaxTurns() { return 10; }
                @Override public int conversationTtlMinutes() { return 1440; }
                @Override public String conversationKvBucket() { return "kubemoot_conversations"; }
                @Override public int resumePreFilterTopK() { return 5; }
            }; }
            @Override public Onboarding onboarding() { return new Onboarding() {
                @Override public boolean mode() { return false; }
                @Override public int gapDetectionTimeoutSeconds() { return 10; }
            }; }
            @Override public Rtfm rtfm() { return new Rtfm() {
                @Override public boolean mode() { return false; }
            }; }
            @Override public Heartbeat heartbeat() { return new Heartbeat() {
                @Override public boolean enabled() { return true; }
                @Override public int intervalSeconds() { return 60; }
                @Override public String kvBucket() { return "kubemoot_agent_state"; }
            }; }
            @Override public Optional<List<RagSource>> ragSources() { return Optional.empty(); }
            @Override public Optional<List<McpServer>> mcpServers() { return Optional.empty(); }
            @Override public Optional<List<String>> enabledTools() { return Optional.empty(); }
            @Override public Optional<List<String>> disabledTools() { return Optional.empty(); }
            @Override public TriageModel triageModel() { return new TriageModel() {
                @Override public Optional<String> modelId() { return Optional.empty(); }
                @Override public Optional<String> endpoint() { return Optional.empty(); }
                @Override public double temperature() { return 0.3; }
                @Override public int maxTokens() { return 2048; }
                @Override public int timeoutSeconds() { return 120; }
            }; }
            @Override public Optional<String> crew() { return Optional.empty(); }
            @Override public Optional<String> crewVersion() { return Optional.empty(); }
            @Override public ResumeSearch resumeSearch() { return new ResumeSearch() {
                @Override public Optional<String> endpoint() { return Optional.empty(); }
            }; }
        };
    }
}
