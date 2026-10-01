package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.ObjectStoreManagement;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

/**
 * Tests for DiscussionSubscriber helper methods.
 * Plain JUnit 5 (not @QuarkusTest) to avoid Ollama/NATS dependency in CI.
 *
 * Covers: isExcludedByInnerCircle. (The agent-side keyword relevance gate was
 * removed — the coordinator's resume model owns subcommittee selection — so the
 * computeRelevanceScore / shouldStandAsideForIrrelevance tests went with it.)
 */
class DiscussionSubscriberHelpersTest {

    // --- isExcludedByInnerCircle ---

    @Test
    void isExcludedByInnerCircle_falseWhenNoMetadata() {
        var subscriber = createSubscriber("test-agent");
        String data = """
                {"threadId": "t1", "messageType": "advisory_ready", "content": "test"}""";
        assertFalse(subscriber.isExcludedByInnerCircle(data, "t1"));
    }

    @Test
    void isExcludedByInnerCircle_falseWhenNoInnerCircleField() {
        var subscriber = createSubscriber("test-agent");
        String data = """
                {"threadId": "t1", "metadata": {"userQuery": "test"}}""";
        assertFalse(subscriber.isExcludedByInnerCircle(data, "t1"));
    }

    @Test
    void isExcludedByInnerCircle_falseWhenEmptyInnerCircle() {
        var subscriber = createSubscriber("test-agent");
        String data = """
                {"threadId": "t1", "metadata": {"innerCircle": []}}""";
        assertFalse(subscriber.isExcludedByInnerCircle(data, "t1"));
    }

    @Test
    void isExcludedByInnerCircle_falseWhenAgentInCircle() {
        var subscriber = createSubscriber("k8s-agent");
        String data = """
                {"threadId": "t1", "metadata": {"innerCircle": ["k8s-agent", "helm-agent"]}}""";
        assertFalse(subscriber.isExcludedByInnerCircle(data, "t1"));
    }

    @Test
    void isExcludedByInnerCircle_trueWhenAgentNotInCircle() {
        var subscriber = createSubscriber("proxmox-agent");
        String data = """
                {"threadId": "t1", "metadata": {"innerCircle": ["k8s-agent", "helm-agent"]}}""";
        assertTrue(subscriber.isExcludedByInnerCircle(data, "t1"));
    }

    @Test
    void isExcludedByInnerCircle_falseOnMalformedJson() {
        var subscriber = createSubscriber("test-agent");
        assertFalse(subscriber.isExcludedByInnerCircle("not json", "t1"));
    }

    @Test
    void isExcludedByInnerCircle_falseWhenInnerCircleNotArray() {
        var subscriber = createSubscriber("test-agent");
        String data = """
                {"threadId": "t1", "metadata": {"innerCircle": "not-an-array"}}""";
        assertFalse(subscriber.isExcludedByInnerCircle(data, "t1"));
    }

    // --- isExplicitlySelected (skip the triage veto for a coordinator-selected agent) ---

    @Test
    void isExplicitlySelected_trueWhenAgentInNonEmptyCircle() {
        var subscriber = createSubscriber("k8s-workloads");
        String data = """
                {"metadata": {"innerCircle": ["k8s-config", "k8s-workloads"]}}""";
        assertTrue(subscriber.isExplicitlySelected(data),
                "an agent named in a non-empty innerCircle was explicitly selected");
    }

    @Test
    void isExplicitlySelected_falseWhenNoCircleEmptyOrNotSelected() {
        assertFalse(createSubscriber("k8s-workloads").isExplicitlySelected(
                """
                {"metadata": {}}"""), "no innerCircle -> not explicitly selected (open broadcast)");
        assertFalse(createSubscriber("k8s-workloads").isExplicitlySelected(
                """
                {"metadata": {"innerCircle": []}}"""), "empty innerCircle -> not explicitly selected");
        assertFalse(createSubscriber("k8s-workloads").isExplicitlySelected(
                """
                {"metadata": {"innerCircle": ["k8s-config", "obs-metrics"]}}"""),
                "agent not in the circle -> not explicitly selected");
        assertFalse(createSubscriber("k8s-workloads").isExplicitlySelected("not json"),
                "malformed data -> not explicitly selected");
    }

    // --- resolveContentField (artifact spill decision) ---

    @Test
    void resolveContentField_nullContent_returnsEmpty() {
        var sub = createSubscriber("test-agent");
        var meta = new HashMap<String, Object>();
        assertEquals("", sub.resolveContentField(null, "t1", "agree", null, meta));
        assertFalse(meta.containsKey("artifact"));
    }

    @Test
    void resolveContentField_smallContent_inlinedNoSpill() {
        var sub = createSubscriber("test-agent");
        var meta = new HashMap<String, Object>();
        String out = sub.resolveContentField(null, "t1", "agree", "small body", meta);
        assertEquals("small body", out);
        assertFalse(meta.containsKey("artifact"), "small content must not spill");
    }

    @Test
    void resolveContentField_largeContent_spillFails_fallsBackToInline() throws Exception {
        var sub = createSubscriber("test-agent");
        var conn = mock(Connection.class);
        when(conn.objectStoreManagement()).thenThrow(new RuntimeException("nats down"));
        var meta = new HashMap<String, Object>();
        String big = "x".repeat(5000); // over the 4096 spill threshold
        String out = sub.resolveContentField(conn, "t1", "agree", big, meta);
        assertFalse(out.contains("[ARTIFACT"), "spill failure must fall back to inline, no marker");
        assertFalse(meta.containsKey("artifact"), "no artifact metadata when spill fails");
    }

    @Test
    void resolveContentField_largeContent_spillSucceeds_marksAndSetsMetadata() throws Exception {
        var sub = createSubscriber("compute-agent");
        var conn = mock(Connection.class);
        var osm = mock(ObjectStoreManagement.class);
        var os = mock(ObjectStore.class);
        when(conn.objectStoreManagement()).thenReturn(osm);
        when(conn.objectStore(anyString())).thenReturn(os);

        var meta = new HashMap<String, Object>();
        String big = "y".repeat(5000);
        String out = sub.resolveContentField(conn, "t1", "agree", big, meta);

        assertTrue(out.contains("[ARTIFACT key="), "large spilled content carries a marker");
        // Overflow posts ONLY the reference - the truncated inline preview is gone, so
        // no consumer can tally incomplete data; they must read the artifact file.
        assertFalse(out.contains("y".repeat(100)), "spilled content posts no inline preview, only the marker");
        assertTrue(out.contains("/artifacts/"), "the marker names the file path to read");
        assertTrue(meta.containsKey("artifact"), "spill sets structured metadata.artifact");
        @SuppressWarnings("unchecked")
        var ref = (Map<String, Object>) meta.get("artifact");
        assertEquals("kubemoot_discussion_artifacts", ref.get("bucket"));
        assertEquals(5000, ((Number) ref.get("bytes")).intValue(), "bytes is the full byte[] length");
        assertTrue(((String) ref.get("key")).startsWith("ns-a/nocrew/t1/compute-agent/agree-"),
                "key is namespace-, crew-, and thread-scoped");
        verify(os).put(anyString(), any(byte[].class));
        verify(conn).publish(eq("kubemoot.artifacts.ns-a.nocrew.t1"), any(byte[].class));
    }

    // --- Helper methods ---

    private DiscussionSubscriber createSubscriber(String agentName) {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", null));
        var chatService = mock(ChatService.class);
        var props = stubProperties(agentName, "kubernetes", null, false);
        var metrics = new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, chatService, metrics, props, "http://localhost:11434");
    }

    private static AgentProperties stubProperties(String agentName, String channels,
                                                    String jsConsumer, boolean hasTriageEndpoint) {
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
                @Override public Optional<String> channels() {
                    return channels == null || channels.isEmpty() ? Optional.empty() : Optional.of(channels);
                }
                @Override public int timeoutSeconds() { return 30; }
                @Override public int maxInferencesPerMinute() { return 10; }
                @Override public int maxContributionsPerThread() { return 3; }
                @Override public boolean tooler() { return true; }
                @Override public Optional<String> keywords() { return Optional.of("kubernetes"); }
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
                @Override public Optional<String> jetstreamConsumer() {
                    return jsConsumer == null || jsConsumer.isEmpty() ? Optional.empty() : Optional.of(jsConsumer);
                }
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
                @Override public Optional<String> endpoint() {
                    return hasTriageEndpoint ? Optional.of("http://triage:11434") : Optional.empty();
                }
                @Override public double temperature() { return 0.3; }
                @Override public int maxTokens() { return 2048; }
                @Override public int timeoutSeconds() { return 120; }
            }; }
            @Override public Optional<String> namespace() { return Optional.of("ns-test"); }
            @Override public Optional<String> crew() { return Optional.empty(); }
            @Override public Optional<String> crewVersion() { return Optional.empty(); }
            @Override public ResumeSearch resumeSearch() { return new ResumeSearch() {
                @Override public Optional<String> endpoint() { return Optional.empty(); }
            }; }
        };
    }

    // --- GPU capacity: stand-aside reasons, waiting signal, thread-end wake ---

    @Test
    void noFit_gpuBusy_contentAndMetadataNameTheReasonAndModel() {
        var nfe = ai.kubemoot.agent.provider.NoFitException.gpuBusy("qwen3:14b", "wait limit");
        assertEquals("Could not get a GPU: every GPU that can hold qwen3:14b was busy",
                DiscussionSubscriber.noFitContent(nfe));
        var meta = DiscussionSubscriber.noFitMetadata(nfe);
        assertEquals("gpu-busy", meta.get("reason"));
        assertEquals("qwen3:14b", meta.get("model"));
        assertEquals("wait limit", meta.get("predictorReason"));
    }

    @Test
    void noFit_promptTooLarge_contentAndMetadataNameTheReasonAndModel() {
        var nfe = ai.kubemoot.agent.provider.NoFitException.promptTooLarge("m:14b", "prompt ~20000 tokens");
        assertEquals("The prompt is larger than the context window any GPU gives m:14b",
                DiscussionSubscriber.noFitContent(nfe));
        assertEquals("prompt-too-large", DiscussionSubscriber.noFitMetadata(nfe).get("reason"));
        assertEquals("prompt ~20000 tokens", DiscussionSubscriber.noFitMetadata(nfe).get("predictorReason"));
    }

    @Test
    void noFit_modelTooLarge_contentAndMetadataNameTheReasonAndModel() {
        var nfe = ai.kubemoot.agent.provider.NoFitException.modelTooLarge("qwen3:235b", "too big");
        assertEquals("No GPU in this cluster can hold the model qwen3:235b", DiscussionSubscriber.noFitContent(nfe));
        assertEquals("model-too-large", DiscussionSubscriber.noFitMetadata(nfe).get("reason"));
    }

    @Test
    void providerAttribution_carriesTheModelTheCallRanOn() {
        var result = new ChatService.ChatResult("c", "answer", "qwen3:14b", null, 1, 1, "ollama-a", "warm");
        var meta = DiscussionSubscriber.providerAttribution(result);
        assertEquals("ollama-a", meta.get("provider"));
        assertEquals("qwen3:14b", meta.get("model"));
        assertEquals("warm", meta.get("pickReason"));
        var evicting = new ChatService.ChatResult("c", "answer", "qwen3:32b", null, 1, 1, "ollama-a", "cold",
                List.of("qwen3:8b"));
        assertEquals(List.of("qwen3:8b"), DiscussionSubscriber.providerAttribution(evicting).get("evicted"));
        assertNull(DiscussionSubscriber.providerAttribution(
                new ChatService.ChatResult("c", "answer", "qwen3:14b", null, 1, 1, "", "")),
                "the static fallback has no JIT attribution");
    }

    @Test
    void triageThatCannotBePlaced_standsAsideWithTheReason() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.triageChat(anyString(), anyString())).thenThrow(
                ai.kubemoot.agent.provider.NoFitException.promptTooLarge("m:8b", "triage prompt ~9000 tokens"));
        var sub = createSubscriberWithConnection(conn, chat);

        assertNull(sub.runTriage("kubemoot.discuss.ns-a.nocrew.general.t1", "t1", "conversation", "4090",
                System.currentTimeMillis()));

        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(anyString(), payload.capture());
        var msg = new com.fasterxml.jackson.databind.ObjectMapper().readTree(payload.getValue());
        assertEquals("stand_aside", msg.path("messageType").asText());
        assertEquals("prompt-too-large", msg.path("metadata").path("reason").asText());
        assertEquals("m:8b", msg.path("metadata").path("model").asText());
    }

    @Test
    void triageThatFailsOtherwise_standsAsideWithoutAReason() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.triageChat(anyString(), anyString())).thenThrow(new IllegalStateException("connection refused"));
        var sub = createSubscriberWithConnection(conn, chat);

        assertNull(sub.runTriage("kubemoot.discuss.ns-a.nocrew.general.t1", "t1", "conversation", "4090",
                System.currentTimeMillis()));

        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(anyString(), payload.capture());
        var msg = new com.fasterxml.jackson.databind.ObjectMapper().readTree(payload.getValue());
        assertEquals("stand_aside", msg.path("messageType").asText());
        assertTrue(msg.path("metadata").path("reason").isMissingNode(), msg.toString());
    }

    @Test
    void selection_plansTheFirstCallWithTheThreadsConversation() {
        var chat = mock(ChatService.class);
        var sub = createSubscriberWithConnection(mock(Connection.class), chat);

        sub.commitToThread("t1", "the thread's conversation");

        verify(chat, org.mockito.Mockito.timeout(2_000))
                .commitToThread(org.mockito.ArgumentMatchers.eq("t1"), org.mockito.ArgumentMatchers.anyLong(),
                        org.mockito.ArgumentMatchers.eq("the thread's conversation"));
    }

    @Test
    void capacityWait_publishesWaitingWithModelAndReason() throws Exception {
        var conn = mock(Connection.class);
        var sub = createSubscriberWithConnection(conn);
        var wait = sub.new ThreadCapacityWait("kubemoot.discuss.ns-a.nocrew.general.t1", "t1", "5090");

        wait.onWaiting("qwen3:14b");

        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(anyString(), payload.capture());
        var msg = new com.fasterxml.jackson.databind.ObjectMapper().readTree(payload.getValue());
        assertEquals("waiting", msg.path("messageType").asText());
        assertEquals("qwen3:14b", msg.path("metadata").path("model").asText());
        assertEquals("gpu-busy", msg.path("metadata").path("reason").asText());
    }

    @Test
    void capacityWait_publishesEvaluatingWhenCapacityArrives() throws Exception {
        var conn = mock(Connection.class);
        var sub = createSubscriberWithConnection(conn);
        var wait = sub.new ThreadCapacityWait("kubemoot.discuss.ns-a.nocrew.general.t1", "t1", "5090");

        wait.onCapacity("qwen3:14b");

        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(anyString(), payload.capture());
        var msg = new com.fasterxml.jackson.databind.ObjectMapper().readTree(payload.getValue());
        assertEquals("evaluating", msg.path("messageType").asText());
        assertEquals("qwen3:14b", msg.path("metadata").path("model").asText());
    }

    @Test
    void capacityWait_endsAndWakesWhenTheThreadSynthesizes() {
        var sub = createSubscriber("test-agent");
        var wait = sub.new ThreadCapacityWait("kubemoot.discuss.ns-a.nocrew.general.t1", "t1", "5090");
        var woken = new java.util.concurrent.atomic.AtomicInteger();
        wait.wakeOnEnd(woken::incrementAndGet);
        assertFalse(wait.threadEnded());

        sub.handleMessageForTest("kubemoot.discuss.ns-a.nocrew.general.t1", """
                {"messageId": "m-1", "threadId": "t1", "agentName": "coordinator",
                 "messageType": "synthesis", "content": "done"}""");

        assertTrue(wait.threadEnded());
        assertEquals(1, woken.get(), "the waiter is woken once when the thread ends");
    }

    @Test
    void capacityWait_registeredAfterTheThreadEnded_wakesAtOnce() {
        var sub = createSubscriber("test-agent");
        sub.handleMessageForTest("kubemoot.discuss.ns-a.nocrew.general.t2", """
                {"messageId": "m-2", "threadId": "t2", "agentName": "coordinator",
                 "messageType": "thread_close", "content": ""}""");
        var wait = sub.new ThreadCapacityWait("kubemoot.discuss.ns-a.nocrew.general.t2", "t2", "5090");
        var woken = new java.util.concurrent.atomic.AtomicInteger();

        wait.wakeOnEnd(woken::incrementAndGet);

        assertEquals(1, woken.get());
    }

    private DiscussionSubscriber createSubscriberWithConnection(Connection conn) {
        return createSubscriberWithConnection(conn, mock(ChatService.class));
    }

    private DiscussionSubscriber createSubscriberWithConnection(Connection conn, ChatService chat) {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", null));
        when(natsProvider.getConnection()).thenReturn(conn);
        var metrics = new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, chat, metrics,
                stubProperties("test-agent", "kubernetes", null, false), "http://localhost:11434");
    }
}
