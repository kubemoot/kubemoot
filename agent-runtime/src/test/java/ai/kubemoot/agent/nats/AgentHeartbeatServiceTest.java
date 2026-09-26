package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ModelWarmupService;
import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import org.mockito.ArgumentCaptor;

import static org.mockito.Mockito.*;

class AgentHeartbeatServiceTest {

    private NatsConnectionProvider natsProvider;
    private ModelWarmupService warmupService;
    private AgentProperties properties;
    private AgentHeartbeatService service;

    @BeforeEach
    void setUp() {
        natsProvider = mock(NatsConnectionProvider.class);
        warmupService = mock(ModelWarmupService.class);
        properties = stubProperties("test-agent", true, 60, "kubemoot_agent_state");
        service = new AgentHeartbeatService(natsProvider, warmupService, properties);
    }

    @Test
    void publishHeartbeat_putsToKvBucket() throws Exception {
        var conn = mock(Connection.class);
        var kv = mock(KeyValue.class);

        when(natsProvider.getConnection()).thenReturn(conn);
        when(natsProvider.isAvailable()).thenReturn(true);
        when(warmupService.isOllamaReachable()).thenReturn(true);
        when(conn.keyValue("kubemoot_agent_state")).thenReturn(kv);

        service.publishHeartbeat();

        verify(kv).put(eq("test-agent"), any(byte[].class));
    }

    @Test
    void publishHeartbeat_gracefulNoOpWhenNatsUnavailable() {
        when(natsProvider.getConnection()).thenReturn(null);

        // Should not throw
        service.publishHeartbeat();
    }

    @Test
    void publishHeartbeat_gracefulOnException() throws Exception {
        var conn = mock(Connection.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(conn.keyValue("kubemoot_agent_state")).thenThrow(new RuntimeException("NATS down"));

        // Should not throw
        service.publishHeartbeat();
    }

    @Test
    void buildHeartbeatJson_containsAllFields() {
        Instant now = Instant.parse("2026-03-06T10:00:00Z");
        Instant lastInf = Instant.parse("2026-03-06T09:55:00Z");

        String json = service.buildHeartbeatJson("my-agent", now, true, true, "qwen2.5:32b", lastInf);

        assertTrue(json.contains("\"agent\":\"my-agent\""));
        assertTrue(json.contains("\"timestamp\":\"2026-03-06T10:00:00Z\""));
        assertTrue(json.contains("\"nats\":true"));
        assertTrue(json.contains("\"ollama\":true"));
        assertTrue(json.contains("\"model\":\"qwen2.5:32b\""));
        assertTrue(json.contains("\"lastInference\":\"2026-03-06T09:55:00Z\""));
    }

    @Test
    void buildHeartbeatJson_omitsLastInferenceWhenNull() {
        Instant now = Instant.parse("2026-03-06T10:00:00Z");

        String json = service.buildHeartbeatJson("my-agent", now, true, false, "qwen2.5:32b", null);

        assertFalse(json.contains("lastInference"));
        assertTrue(json.contains("\"ollama\":false"));
    }

    @Test
    void recordInference_updatesTimestamp() throws Exception {
        var conn = mock(Connection.class);
        var kv = mock(KeyValue.class);

        when(natsProvider.getConnection()).thenReturn(conn);
        when(natsProvider.isAvailable()).thenReturn(true);
        when(warmupService.isOllamaReachable()).thenReturn(true);
        when(conn.keyValue("kubemoot_agent_state")).thenReturn(kv);

        service.recordInference();
        service.publishHeartbeat();

        var captor = ArgumentCaptor.forClass(byte[].class);
        verify(kv).put(eq("test-agent"), captor.capture());
        String json = new String(captor.getValue());
        assertTrue(json.contains("\"lastInference\":"));
    }

    // --- Stub ---

    private static AgentProperties stubProperties(String agentName, boolean heartbeatEnabled,
                                                    int intervalSeconds, String kvBucket) {
        return new AgentProperties() {
            @Override public Memory memory() { return ai.kubemoot.agent.TestStubs.memory(); }
            @Override public String agentName() { return agentName; }
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
                @Override public int timeoutSeconds() { return 30; }
                @Override public int maxInferencesPerMinute() { return 10; }
                @Override public int maxContributionsPerThread() { return 3; }
                @Override public boolean tooler() { return true; }
                @Override public Optional<String> keywords() { return Optional.empty(); }
                @Override public String relevanceMode() { return "keyword"; }
                @Override public Optional<String> relevancePromptHint() { return Optional.empty(); }
                @Override public String priority() { return "medium"; }
                @Override public int advisoryGraceSeconds() { return 5; }
                @Override public String role() { return "tooler"; }
                @Override public boolean coordinator() { return false; }
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
                @Override public boolean enabled() { return heartbeatEnabled; }
                @Override public int intervalSeconds() { return intervalSeconds; }
                @Override public String kvBucket() { return kvBucket; }
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
