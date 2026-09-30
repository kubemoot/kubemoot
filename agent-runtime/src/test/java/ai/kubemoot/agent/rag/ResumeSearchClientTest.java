package ai.kubemoot.agent.rag;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Tests for ResumeSearchClient.
 * Uses plain JUnit 5 (no @QuarkusTest) to avoid Ollama dependency in CI.
 *
 * Since ResumeSearchClient uses java.net.http.HttpClient internally, we test
 * error handling and graceful degradation by pointing at unreachable endpoints.
 */
class ResumeSearchClientTest {

    private final ObjectMapper mapper = new ObjectMapper();

    @Test
    void testSearchResumes_returnsNullWhenEndpointNotConfigured() {
        var client = new ResumeSearchClient(stubProperties(null), mapper);
        var results = client.searchResumes("what pods are running", 5);
        assertNull(results, "Should return null when endpoint is not configured");
    }

    @Test
    void testSearchResumes_returnsNullWhenEndpointEmpty() {
        var client = new ResumeSearchClient(stubProperties(""), mapper);
        var results = client.searchResumes("what pods are running", 5);
        assertNull(results, "Should return null when endpoint is empty");
    }

    @Test
    void testSearchResumes_returnsNullOnError() {
        // Point at an unreachable endpoint — should return null gracefully
        var client = new ResumeSearchClient(stubProperties("http://192.0.2.1:1"), mapper);
        var results = client.searchResumes("test query", 5);
        assertNull(results, "Should return null on connection error (graceful degradation)");
    }

    @Test
    void testIsAvailable_falseWhenNotConfigured() {
        var client = new ResumeSearchClient(stubProperties(null), mapper);
        assertFalse(client.isAvailable());
    }

    @Test
    void testIsAvailable_falseWhenUnreachable() {
        var client = new ResumeSearchClient(stubProperties("http://192.0.2.1:1"), mapper);
        assertFalse(client.isAvailable());
    }

    // --- Stub helpers ---

    private static AgentProperties stubProperties(String resumeEndpoint) {
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
                @Override public Optional<String> channels() { return Optional.empty(); }
                @Override public int maxInferencesPerMinute() { return 10; }
                @Override public int maxContributionsPerThread() { return 3; }
                @Override public boolean tooler() { return true; }
                @Override public String priority() { return "medium"; }
                @Override public String role() { return "tooler"; }
                @Override public boolean coordinator() { return false; }
                @Override public boolean computeContract() { return false; }
                @Override public boolean metricsDrillContract() { return false; }
                @Override public Optional<String> alwaysCandidateAgents() { return Optional.empty(); }
                @Override public int synthesisCompletenessRetries() { return 0; }
                @Override public boolean answerDirectly() { return true; }
                @Override public boolean hasAnalysts() { return false; }
                @Override public boolean reviewDecision() { return false; }
                @Override public String reviewDecisionTier() { return "fast"; }
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
            @Override public Optional<String> namespace() { return Optional.of("ns-test"); }
            @Override public Optional<String> crew() { return Optional.empty(); }
            @Override public Optional<String> crewVersion() { return Optional.empty(); }
            @Override public ResumeSearch resumeSearch() { return new ResumeSearch() {
                @Override public Optional<String> endpoint() {
                    return resumeEndpoint == null ? Optional.empty() : Optional.of(resumeEndpoint);
                }
            }; }
        };
    }
}
