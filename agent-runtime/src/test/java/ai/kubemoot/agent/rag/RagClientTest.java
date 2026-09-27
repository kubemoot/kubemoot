package ai.kubemoot.agent.rag;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Tests for RagClient context building and query formatting.
 * Uses plain JUnit 5 (no @QuarkusTest) to avoid Ollama dependency in CI.
 *
 * Note: RagClient uses java.net.http.HttpClient internally, so we test the
 * formatting/context-building logic and error handling rather than mocking HTTP.
 * The querySource method catches all exceptions and returns empty lists, making
 * it safe to test with unreachable endpoints.
 */
class RagClientTest {

    private final ObjectMapper mapper = new ObjectMapper();

    @Test
    void queryForContext_noSources_returnsEmptyString() {
        var client = new RagClient(stubProperties(List.of()), mapper);
        var context = client.queryForContext("what is kubernetes");
        assertEquals("", context);
    }

    @Test
    void query_noSources_returnsEmptyList() {
        var client = new RagClient(stubProperties(List.of()), mapper);
        var results = client.query("anything");
        assertTrue(results.isEmpty());
    }

    @Test
    void query_sourceWithZeroTopK_skipped() {
        // A source with topK=0 should be skipped entirely
        var source = stubRagSource("disabled-source", "http://localhost:9999", 0);
        var client = new RagClient(stubProperties(List.of(source)), mapper);
        var results = client.query("test query");
        assertTrue(results.isEmpty());
    }

    @Test
    void query_sourceWithNegativeTopK_skipped() {
        var source = stubRagSource("neg-source", "http://localhost:9999", -1);
        var client = new RagClient(stubProperties(List.of(source)), mapper);
        var results = client.query("test query");
        assertTrue(results.isEmpty());
    }

    @Test
    void query_unreachableEndpoint_returnsEmptyGracefully() {
        // RagClient catches exceptions in querySource and returns empty list
        var source = stubRagSource("unreachable", "http://192.0.2.1:1", 5);
        var client = new RagClient(stubProperties(List.of(source)), mapper);
        var results = client.query("test query");
        assertTrue(results.isEmpty());
    }

    @Test
    void queryForContext_unreachableEndpoint_returnsEmptyString() {
        var source = stubRagSource("unreachable", "http://192.0.2.1:1", 5);
        var client = new RagClient(stubProperties(List.of(source)), mapper);
        var context = client.queryForContext("test query");
        assertEquals("", context);
    }

    @Test
    void healthCheck_noSources_returnsEmptyMap() {
        var client = new RagClient(stubProperties(List.of()), mapper);
        var health = client.healthCheck();
        assertTrue(health.isEmpty());
    }

    @Test
    void healthCheck_unreachableSource_returnsFalse() {
        var source = stubRagSource("dead-source", "http://192.0.2.1:1", 5);
        var client = new RagClient(stubProperties(List.of(source)), mapper);
        var health = client.healthCheck();
        assertEquals(1, health.size());
        assertFalse(health.get("dead-source"));
    }

    @Test
    void ragResult_record_fieldsAccessible() {
        var result = new RagClient.RagResult("source-1", "some content", 0.95, Map.of("key", "val"));
        assertEquals("source-1", result.source());
        assertEquals("some content", result.content());
        assertEquals(0.95, result.score(), 0.001);
        assertEquals("val", result.metadata().get("key"));
    }

    @Test
    void multipleSources_allQueriedIndependently() {
        // Both sources are unreachable, but the client should attempt both
        var source1 = stubRagSource("source-a", "http://192.0.2.1:1", 3);
        var source2 = stubRagSource("source-b", "http://192.0.2.2:1", 5);
        var client = new RagClient(stubProperties(List.of(source1, source2)), mapper);
        var results = client.query("multi-source test");
        // Both should fail gracefully and return empty
        assertTrue(results.isEmpty());
    }

    @Test
    void healthCheck_multipleSources_allChecked() {
        var source1 = stubRagSource("src-1", "http://192.0.2.1:1", 3);
        var source2 = stubRagSource("src-2", "http://192.0.2.2:1", 5);
        var client = new RagClient(stubProperties(List.of(source1, source2)), mapper);
        var health = client.healthCheck();
        assertEquals(2, health.size());
        assertFalse(health.get("src-1"));
        assertFalse(health.get("src-2"));
    }

    // --- Stub helpers ---

    private static AgentProperties.RagSource stubRagSource(String name, String endpoint, int topK) {
        return new AgentProperties.RagSource() {
            @Override public String name() { return name; }
            @Override public String endpoint() { return endpoint; }
            @Override public int topK() { return topK; }
        };
    }

    private static AgentProperties stubProperties(List<AgentProperties.RagSource> sources) {
        return new AgentProperties() {
            @Override public Memory memory() { return ai.kubemoot.agent.TestStubs.memory(); }
            @Override public String agentName() { return "test-agent"; }
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
                @Override public boolean enabled() { return true; }
                @Override public int intervalSeconds() { return 60; }
                @Override public String kvBucket() { return "kubemoot_agent_state"; }
            }; }
            @Override public Optional<List<RagSource>> ragSources() {
                return sources.isEmpty() ? Optional.empty() : Optional.of(sources);
            }
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

    /**
     * A query service that behaves like the Python one: a request carrying an
     * HTTP/2 upgrade gets 422 without its body, a plain HTTP/1.1 request gets results.
     */
    private static HttpServer queryService(int plainStatus) throws IOException {
        var server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/query", exchange -> {
            boolean upgrade = exchange.getRequestHeaders().containsKey("Upgrade");
            int status = upgrade ? 422 : plainStatus;
            String body = status == 200
                    ? "{\"results\":[{\"content\":\"Degraded means the coordinator cannot be scheduled.\",\"score\":0.6,\"metadata\":{}}]}"
                    : "{\"detail\":\"unprocessable\"}";
            byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(status, bytes.length);
            try (OutputStream out = exchange.getResponseBody()) {
                out.write(bytes);
            }
        });
        server.start();
        return server;
    }

    @Test
    void queryGetsResultsFromAServiceThatRejectsHttp2Upgrades() throws IOException {
        var server = queryService(200);
        try {
            var source = stubRagSource("docs", "http://127.0.0.1:" + server.getAddress().getPort(), 3);
            var results = new RagClient(stubProperties(List.of(source)), mapper).query("What does Degraded mean?");
            assertEquals(1, results.size());
            assertTrue(results.get(0).content().contains("Degraded means"));
        } finally {
            server.stop(0);
        }
    }

    @Test
    void anErrorStatusYieldsNoResults() throws IOException {
        var server = queryService(500);
        try {
            var source = stubRagSource("docs", "http://127.0.0.1:" + server.getAddress().getPort(), 3);
            assertTrue(new RagClient(stubProperties(List.of(source)), mapper).query("q").isEmpty());
        } finally {
            server.stop(0);
        }
    }

}
