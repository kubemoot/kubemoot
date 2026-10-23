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

    // --- admitForEvaluation guard order ---

    private static String msg(String type, String thread, String agent, String id, String metadata) {
        return "{\"messageType\":\"" + type + "\",\"threadId\":\"" + thread
                + "\",\"agentName\":\"" + agent + "\",\"messageId\":\"" + id
                + "\",\"content\":\"c\"" + (metadata == null ? "" : ",\"metadata\":" + metadata) + "}";
    }

    @Test
    void admit_selectedTriggerTakesOnePermitAndReturnsTheEvaluation() throws Exception {
        var sub = createSubscriber("k8s-agent");
        var evaluation = sub.admitForEvaluation(
                msg("advisory_ready", "t1", "coord", "m1", "{\"innerCircle\":[\"k8s-agent\"]}"));
        assertNotNull(evaluation);
        assertEquals("t1", evaluation.threadId());
        assertTrue(evaluation.selected());
        assertTrue(evaluation.conversation().contains("Evaluation Phase Started"));
        assertEquals(1, sub.rateLimitPermits("t1"));
    }

    @Test
    void admit_ownMessageTakesNoPermit() throws Exception {
        var sub = createSubscriber("k8s-agent");
        assertNull(sub.admitForEvaluation(msg("advisory_ready", "t1", "k8s-agent", "m1", null)));
        assertEquals(0, sub.rateLimitPermits("t1"));
    }

    @Test
    void admit_duplicateMessageTakesNoSecondPermit() throws Exception {
        var sub = createSubscriber("k8s-agent");
        String data = msg("advisory_ready", "t1", "coord", "m1", null);
        assertNotNull(sub.admitForEvaluation(data));
        assertNull(sub.admitForEvaluation(data));
        assertEquals(1, sub.rateLimitPermits("t1"));
    }

    @Test
    void admit_closedThreadTakesNoPermit() throws Exception {
        var sub = createSubscriber("k8s-agent");
        assertNull(sub.admitForEvaluation(msg("synthesis", "t1", "coord", "m1", null)));
        assertNull(sub.admitForEvaluation(msg("advisory_ready", "t1", "coord", "m2", null)));
        assertEquals(0, sub.rateLimitPermits("t1"));
    }

    @Test
    void admit_nonTriggerTypeTakesNoPermit() throws Exception {
        var sub = createSubscriber("k8s-agent");
        assertNull(sub.admitForEvaluation(msg("agree", "t1", "helm-agent", "m1", null)));
        assertEquals(0, sub.rateLimitPermits("t1"));
    }

    @Test
    void admit_innerCircleIsCheckedLastAfterThePermit() throws Exception {
        var sub = createSubscriber("proxmox-agent");
        assertNull(sub.admitForEvaluation(
                msg("advisory_ready", "t1", "coord", "m1", "{\"innerCircle\":[\"k8s-agent\"]}")));
        assertEquals(1, sub.rateLimitPermits("t1"), "the permit is taken before the inner-circle check");
    }

    @Test
    void admit_exhaustedRateLimitStopsBeforeTheInnerCircle() throws Exception {
        var sub = createSubscriber("k8s-agent");
        for (int i = 0; i < 3; i++) {
            assertNotNull(sub.admitForEvaluation(msg("advisory_ready", "t1", "coord", "m" + i, null)));
        }
        assertNull(sub.admitForEvaluation(msg("advisory_ready", "t1", "coord", "m9", null)));
        assertEquals(3, sub.rateLimitPermits("t1"));
    }

    @Test
    void jetStream_ackedWhenTheMessageIsNotAdmitted() {
        var sub = createSubscriber("k8s-agent");
        var natsMsg = mock(io.nats.client.Message.class);
        when(natsMsg.getSubject()).thenReturn("kubemoot.discuss.ns-a.nocrew.general.t1");
        when(natsMsg.getData()).thenReturn(
                msg("agree", "t1", "helm-agent", "m1", null).getBytes(java.nio.charset.StandardCharsets.UTF_8));
        sub.handleJetStreamMessage(natsMsg);
        verify(natsMsg).ack();
        assertEquals(0, sub.rateLimitPermits("t1"));
    }

    @Test
    void jetStream_ackedWhenTheMessageIsMalformed() {
        var sub = createSubscriber("k8s-agent");
        var natsMsg = mock(io.nats.client.Message.class);
        when(natsMsg.getSubject()).thenReturn("kubemoot.discuss.ns-a.nocrew.general.t1");
        when(natsMsg.getData()).thenReturn("not json".getBytes(java.nio.charset.StandardCharsets.UTF_8));
        sub.handleJetStreamMessage(natsMsg);
        verify(natsMsg).ack();
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

    static AgentProperties stubProperties(String agentName, String channels,
                                                    String jsConsumer, boolean hasTriageEndpoint) {
        return new AgentProperties() {
            @Override public ai.kubemoot.agent.config.AgentProperties.Scheduler scheduler() { return ai.kubemoot.agent.TestStubs.scheduler(); }
            @Override public Memory memory() { return ai.kubemoot.agent.TestStubs.memory(); }
            @Override public String agentName() { return agentName; }
            @Override public String agentDescription() { return ""; }
            @Override public String agentType() { return "chat"; }
            @Override public Optional<String> systemPrompt() { return Optional.empty(); }
            @Override public Optional<String> systemPromptFile() { return Optional.empty(); }
            @Override public Model model() { return new Model() {
                @Override public String name() { return "default"; }
                @Override public String server() { return "ollama"; }
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
        var nfe = ai.kubemoot.agent.modelserver.NoFitException.gpuBusy("qwen3:14b", "wait limit");
        assertEquals("Could not get a GPU: every GPU that can hold qwen3:14b was busy",
                DiscussionSubscriber.noFitContent(nfe));
        var meta = DiscussionSubscriber.noFitMetadata(nfe);
        assertEquals("gpu-busy", meta.get("reason"));
        assertEquals("qwen3:14b", meta.get("model"));
        assertEquals("wait limit", meta.get("predictorReason"));
    }

    @Test
    void noFit_promptTooLarge_contentAndMetadataNameTheReasonAndModel() {
        var nfe = ai.kubemoot.agent.modelserver.NoFitException.promptTooLarge("m:14b", "prompt ~20000 tokens");
        assertEquals("The prompt is larger than the context window any GPU gives m:14b",
                DiscussionSubscriber.noFitContent(nfe));
        assertEquals("prompt-too-large", DiscussionSubscriber.noFitMetadata(nfe).get("reason"));
        assertEquals("prompt ~20000 tokens", DiscussionSubscriber.noFitMetadata(nfe).get("predictorReason"));
    }

    @Test
    void noFit_modelTooLarge_contentAndMetadataNameTheReasonAndModel() {
        var nfe = ai.kubemoot.agent.modelserver.NoFitException.modelTooLarge("qwen3:235b", "too big");
        assertEquals("No GPU in this cluster can hold the model qwen3:235b", DiscussionSubscriber.noFitContent(nfe));
        assertEquals("model-too-large", DiscussionSubscriber.noFitMetadata(nfe).get("reason"));
    }

    @Test
    void serverAttribution_carriesTheModelTheCallRanOn() {
        var result = new ChatService.ChatResult("c", "answer", "qwen3:14b", null, 1, 1, "ollama-a", "warm");
        var meta = DiscussionSubscriber.serverAttribution(result);
        assertEquals("ollama-a", meta.get("server"));
        assertEquals("qwen3:14b", meta.get("model"));
        assertEquals("warm", meta.get("pickReason"));
        var evicting = new ChatService.ChatResult("c", "answer", "qwen3:32b", null, 1, 1, "ollama-a", "cold",
                List.of("qwen3:8b"));
        assertEquals(List.of("qwen3:8b"), DiscussionSubscriber.serverAttribution(evicting).get("evicted"));
        assertNull(DiscussionSubscriber.serverAttribution(
                new ChatService.ChatResult("c", "answer", "qwen3:14b", null, 1, 1, "", "")),
                "the static fallback has no JIT attribution");
    }

    private static final String SUBJECT_T1 = "kubemoot.discuss.ns-a.nocrew.general.t1";

    /** Every message the connection published, parsed, in publish order. */
    private static List<com.fasterxml.jackson.databind.JsonNode> published(Connection conn, int count)
            throws Exception {
        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn, times(count)).publish(anyString(), payload.capture());
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        var out = new java.util.ArrayList<com.fasterxml.jackson.databind.JsonNode>();
        for (byte[] p : payload.getAllValues()) {
            out.add(mapper.readTree(p));
        }
        return out;
    }

    private static List<String> modelsUsed(com.fasterxml.jackson.databind.JsonNode msg) {
        var out = new java.util.ArrayList<String>();
        msg.path("metadata").path("modelsUsed").forEach(n -> out.add(n.asText()));
        return out;
    }

    @Test
    void finding_replannedToAnAlternative_reportsTheAlternativeNotTheConfiguredModel() throws Exception {
        var conn = mock(Connection.class);
        var sub = createSubscriberWithConnection(conn);
        // The stub agent is configured with qwen2.5:32b; the call was re-planned elsewhere.
        var result = new ChatService.ChatResult("c", "the finding", "alt-model:14b", "t1", 100, 20,
                "server-b", "re-planned");

        sub.classifyAndPublishResult(SUBJECT_T1, "t1", result,
                new DiscussionSubscriber.SignalTiming(500, 1, 100, 20), "static-label");

        var msg = published(conn, 1).get(0);
        assertEquals("agree", msg.path("messageType").asText());
        var meta = msg.path("metadata");
        assertEquals("alt-model:14b", meta.path("modelName").asText());
        assertEquals("server-b", meta.path("gpuLabel").asText());
        assertEquals(List.of("alt-model:14b@server-b"), modelsUsed(msg));
        assertEquals(100, meta.path("inputTokens").asLong(), "token counts are unchanged");
        assertEquals(20, meta.path("outputTokens").asLong());
    }

    @Test
    void finding_whoseCallsRanOnTwoModels_reportsTheLastAndListsBoth() throws Exception {
        var conn = mock(Connection.class);
        var sub = createSubscriberWithConnection(conn);
        var result = new ChatService.ChatResult("c", "the finding", "second:8b", "t1", 1, 1,
                "server-b", "", List.of(), List.of(
                        new ai.kubemoot.agent.chat.ModelUse("first:32b", "server-a"),
                        new ai.kubemoot.agent.chat.ModelUse("second:8b", "server-b")));

        sub.classifyAndPublishResult(SUBJECT_T1, "t1", result, DiscussionSubscriber.SignalTiming.NONE, "static");

        var msg = published(conn, 1).get(0);
        assertEquals("second:8b", msg.path("metadata").path("modelName").asText());
        assertEquals("server-b", msg.path("metadata").path("gpuLabel").asText());
        assertEquals(List.of("first:32b@server-a", "second:8b@server-b"), modelsUsed(msg));
        assertEquals("second:8b", msg.path("metadata").path("model").asText());
        assertEquals("server-b", msg.path("metadata").path("server").asText());
    }

    @Test
    void serverAttribution_readsTheLastCall_evenWhenTheResultFieldsDisagree() {
        var result = new ChatService.ChatResult("c", "r", "stale:32b", "t1", 1, 1, "stale-server", "",
                List.of(), List.of(new ai.kubemoot.agent.chat.ModelUse("last:8b", "server-b")));

        var meta = DiscussionSubscriber.findingMetadata(result);

        assertEquals("last:8b", meta.get("modelName"));
        assertEquals("last:8b", meta.get("model"));
        assertEquals("server-b", meta.get("server"));
    }

    @Test
    void triage_whenTheThreadClosedDuringIt_standsAsideNamingWhereTriageRan() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.getTriageModelId()).thenReturn("");
        var sub = createSubscriberWithConnection(conn, chat);
        when(chat.triageChatWithTokens(anyString(), anyString())).thenAnswer(inv -> {
            sub.handleMessageForTest(SUBJECT_T1, """
                    {"messageId": "m-c", "threadId": "t1", "agentName": "coordinator",
                     "messageType": "thread_close", "content": ""}""");
            return new ChatService.SimpleLlmResult("CONTRIBUTE", 5, 1,
                    new ai.kubemoot.agent.chat.ModelUse("triage-model:4b", "server-c"));
        });

        sub.evaluateSelected(SUBJECT_T1, "t1", "conversation", true);

        var msgs = published(conn, 2);
        assertEquals("qwen2.5:32b", msgs.get(0).path("metadata").path("modelName").asText(),
                "an empty triage model id is not reported; the configured model stays");
        var standAside = msgs.get(1);
        assertEquals("stand_aside", standAside.path("messageType").asText());
        assertEquals("triage-model:4b", standAside.path("metadata").path("modelName").asText());
        assertEquals("server-c", standAside.path("metadata").path("gpuLabel").asText());
        verify(chat, never()).directChat(any(), anyBoolean(), any());
    }

    @Test
    void standAsideAfterMulling_reportsTheModelItsCallsRanOn() throws Exception {
        var conn = mock(Connection.class);
        var sub = createSubscriberWithConnection(conn);
        var result = new ChatService.ChatResult("c", "NOTHING_TO_ADD", "alt-model:14b", "t1", 1, 1,
                "server-b", "");

        sub.classifyAndPublishResult(SUBJECT_T1, "t1", result, DiscussionSubscriber.SignalTiming.NONE, "static");

        var msg = published(conn, 1).get(0);
        assertEquals("stand_aside", msg.path("messageType").asText());
        assertEquals("alt-model:14b", msg.path("metadata").path("modelName").asText());
        assertTrue(msg.path("metadata").path("model").isMissingNode(),
                "a plain stand-aside carries no capacity model: " + msg);
    }

    @Test
    void noResult_standsAsideWithTheConfiguredModelAndFallbackLabel() throws Exception {
        var conn = mock(Connection.class);
        var sub = createSubscriberWithConnection(conn);

        sub.classifyAndPublishResult(SUBJECT_T1, "t1", null, DiscussionSubscriber.SignalTiming.NONE, "static");

        var meta = published(conn, 1).get(0).path("metadata");
        assertEquals("qwen2.5:32b", meta.path("modelName").asText());
        assertEquals("static", meta.path("gpuLabel").asText());
        assertTrue(meta.path("modelsUsed").isMissingNode());
    }

    @Test
    void triage_reportsTheTriageModelAndTheServerItRanOn() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.getTriageModelId()).thenReturn("triage-model:4b");
        when(chat.getTriageEndpoint()).thenReturn("http://ollama.static-ns:11434");
        when(chat.triageChatWithTokens(anyString(), anyString())).thenReturn(new ChatService.SimpleLlmResult(
                "NOTHING_TO_ADD", 50, 2, new ai.kubemoot.agent.chat.ModelUse("triage-model:4b", "server-c")));
        var sub = createSubscriberWithConnection(conn, chat);

        sub.evaluateSelected(SUBJECT_T1, "t1", "conversation", false);

        var msgs = published(conn, 2);
        assertEquals("triaging", msgs.get(0).path("messageType").asText());
        assertEquals("triage-model:4b", msgs.get(0).path("metadata").path("modelName").asText(),
                "before triage runs, the signal names the triage model, not the mulling model");
        assertTrue(msgs.get(0).path("metadata").path("gpuLabel").isMissingNode(),
                "before triage is placed, the signal names no accelerator: " + msgs.get(0));
        var standAside = msgs.get(1);
        assertEquals("stand_aside", standAside.path("messageType").asText());
        assertEquals("triage-model:4b", standAside.path("metadata").path("modelName").asText());
        assertEquals("server-c", standAside.path("metadata").path("gpuLabel").asText());
        assertEquals(List.of("triage-model:4b@server-c"), modelsUsed(standAside));
    }

    @Test
    void evaluating_namesNoAcceleratorUntilTheCallRan() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.getTriageEndpoint()).thenReturn("http://ollama.static-ns:11434");
        when(chat.triageChatWithTokens(anyString(), anyString())).thenReturn(new ChatService.SimpleLlmResult(
                "CONTRIBUTE", 5, 1, new ai.kubemoot.agent.chat.ModelUse("triage-model:4b", "server-c")));
        when(chat.directChat(any(), anyBoolean(), any())).thenReturn(new ChatService.ChatResult("c", "an answer",
                "alt-model:14b", "t1", 1, 1, "server-b", ""));
        var sub = createSubscriberWithConnection(conn, chat);

        sub.evaluateSelected(SUBJECT_T1, "t1", "conversation", false);

        var msgs = published(conn, 3);
        assertEquals(List.of("triaging", "evaluating", "agree"),
                msgs.stream().map(m -> m.path("messageType").asText()).toList());
        assertTrue(msgs.get(0).path("metadata").path("gpuLabel").isMissingNode(), msgs.get(0).toString());
        assertTrue(msgs.get(1).path("metadata").path("gpuLabel").isMissingNode(),
                "the evaluating signal precedes the call, so it names no accelerator: " + msgs.get(1));
        assertEquals("server-b", msgs.get(2).path("metadata").path("gpuLabel").asText(),
                "the finding names the server the call ran on");
    }

    @Test
    void triage_onTheStaticEndpoint_keepsTheEndpointLabel() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.getTriageEndpoint()).thenReturn("http://ollama.static-ns:11434");
        when(chat.triageChatWithTokens(anyString(), anyString())).thenReturn(new ChatService.SimpleLlmResult(
                "NOTHING_TO_ADD", 50, 2, new ai.kubemoot.agent.chat.ModelUse("triage-model:4b", "")));
        var sub = createSubscriberWithConnection(conn, chat);

        sub.evaluateSelected(SUBJECT_T1, "t1", "conversation", false);

        var msgs = published(conn, 2);
        assertEquals("qwen2.5:32b", msgs.get(0).path("metadata").path("modelName").asText(),
                "with no triage model known, the configured model stays");
        assertEquals("static-ns", msgs.get(1).path("metadata").path("gpuLabel").asText());
        assertEquals("triage-model:4b", msgs.get(1).path("metadata").path("modelName").asText());
    }

    @Test
    void triageThatCannotBePlaced_standsAsideWithTheReason() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.triageChatWithTokens(anyString(), anyString())).thenThrow(
                ai.kubemoot.agent.modelserver.NoFitException.promptTooLarge("m:8b", "triage prompt ~9000 tokens"));
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
        when(chat.triageChatWithTokens(anyString(), anyString())).thenThrow(new IllegalStateException("connection refused"));
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
        var wait = sub.new ThreadCapacityWait("kubemoot.discuss.ns-a.nocrew.general.t1", "t1");

        wait.onWaiting("qwen3:14b");

        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(anyString(), payload.capture());
        var msg = new com.fasterxml.jackson.databind.ObjectMapper().readTree(payload.getValue());
        assertEquals("waiting", msg.path("messageType").asText());
        assertEquals("qwen3:14b", msg.path("metadata").path("model").asText());
        assertTrue(msg.path("metadata").path("gpuLabel").isMissingNode(), "no accelerator while waiting: " + msg);
        assertEquals("gpu-busy", msg.path("metadata").path("reason").asText());
    }

    @Test
    void capacityWait_publishesEvaluatingWhenCapacityArrives() throws Exception {
        var conn = mock(Connection.class);
        var sub = createSubscriberWithConnection(conn);
        var wait = sub.new ThreadCapacityWait("kubemoot.discuss.ns-a.nocrew.general.t1", "t1");

        wait.onCapacity("qwen3:14b");

        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn).publish(anyString(), payload.capture());
        var msg = new com.fasterxml.jackson.databind.ObjectMapper().readTree(payload.getValue());
        assertEquals("evaluating", msg.path("messageType").asText());
        assertEquals("qwen3:14b", msg.path("metadata").path("model").asText());
        assertTrue(msg.path("metadata").path("gpuLabel").isMissingNode(),
                "the call is not placed yet, so no accelerator: " + msg);
    }

    @Test
    void capacityWait_endsAndWakesWhenTheThreadSynthesizes() {
        var sub = createSubscriber("test-agent");
        var wait = sub.new ThreadCapacityWait("kubemoot.discuss.ns-a.nocrew.general.t1", "t1");
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
        var wait = sub.new ThreadCapacityWait("kubemoot.discuss.ns-a.nocrew.general.t2", "t2");
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

    @Test
    void signalTiming_noneIsAllZero_andOfCarriesNoTokens() {
        var none = DiscussionSubscriber.SignalTiming.NONE;
        assertEquals(0L, none.inferenceMs() + none.inferenceStartMs() + none.inputTokens() + none.outputTokens());
        var timed = DiscussionSubscriber.SignalTiming.of(120L, 5L);
        assertEquals(120L, timed.inferenceMs());
        assertEquals(5L, timed.inferenceStartMs());
        assertEquals(0L, timed.inputTokens());
        assertEquals(0L, timed.outputTokens());
    }

    private static com.fasterxml.jackson.databind.JsonNode lastPublished(Connection conn) throws Exception {
        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn, atLeastOnce()).publish(anyString(), payload.capture());
        return new com.fasterxml.jackson.databind.ObjectMapper().readTree(payload.getValue());
    }

    private static ai.kubemoot.agent.chat.ModelUse placedUse(String model, String server, String policy) {
        var call = new ai.kubemoot.agent.modelserver.PlacedCall(server, model,
                new ai.kubemoot.agent.modelserver.PlacementDecision(policy, "because " + policy, null, null));
        return new ai.kubemoot.agent.chat.ModelUse(model, server, call);
    }

    @Test
    void aTriageStandAside_reportsThePlacementOfItsTriageCall() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.triageChatWithTokens(anyString(), anyString())).thenReturn(new ChatService.SimpleLlmResult(
                "NOTHING_TO_ADD", 50, 2, placedUse("triage-model:4b", "server-c", "learned")));
        var sub = createSubscriberWithConnection(conn, chat);

        sub.evaluateSelected(SUBJECT_T1, "t1", "conversation", false);

        var calls = published(conn, 2).get(1).path("metadata").path("placementCalls");
        assertEquals(1, calls.size());
        assertEquals("learned", calls.get(0).path("policy").asText());
        assertEquals("triage-model:4b", calls.get(0).path("model").asText());
    }

    @Test
    void anAgentThatGoesOnAfterTriage_reportsItsTriageCallBeforeItsEvaluation() throws Exception {
        var conn = mock(Connection.class);
        var chat = mock(ChatService.class);
        when(chat.getToolNames()).thenReturn(List.of());
        when(chat.triageChatWithTokens(anyString(), anyString())).thenReturn(new ChatService.SimpleLlmResult(
                "CONTRIBUTE", 50, 2, placedUse("triage-model:4b", "server-c", "algorithm")));
        var evaluation = new ChatService.ChatResult("c", "the finding", "qwen:14b", "t1", 10, 5, "server-b", "",
                List.of(), List.of(placedUse("qwen:14b", "server-b", "learned")));
        when(chat.directChat(any(), anyBoolean(), any())).thenReturn(evaluation);
        var sub = createSubscriberWithConnection(conn, chat);

        sub.evaluateSelected(SUBJECT_T1, "t1", "conversation", true);

        var agree = lastPublished(conn);
        assertEquals("agree", agree.path("messageType").asText());
        var calls = agree.path("metadata").path("placementCalls");
        assertEquals(2, calls.size());
        assertEquals("triage-model:4b", calls.get(0).path("model").asText(), "the triage call comes first");
        assertEquals("learned", calls.get(1).path("policy").asText());
        assertEquals(List.of("qwen:14b@server-b"), modelsUsed(agree), "the evaluation's own places are unchanged");
    }

    @Test
    void classifyException_vllmContextRejectionIsContextExceeded() {
        String vllm = "HTTP 400: This model's maximum context length is 32768 tokens. "
                + "However, your prompt contains at least 32769 input tokens";
        assertEquals("context_exceeded", DiscussionSubscriber.classifyException(
                "dev.langchain4j.exception.InvalidRequestException", vllm));
        assertEquals("context_exceeded", DiscussionSubscriber.classifyException(
                "dev.langchain4j.exception.InvalidRequestException", "{\"code\":\"context_length_exceeded\"}"));
    }

    @Test
    void classifyException_otherFailuresKeepTheirBuckets() {
        assertEquals("model_timeout", DiscussionSubscriber.classifyException("x.Y", "timeout period of 120000ms"));
        assertEquals("model_oom", DiscussionSubscriber.classifyException("java.lang.OutOfMemoryError", "heap"));
        assertEquals("server_unreachable", DiscussionSubscriber.classifyException("x.Y", "Connection refused"));
        assertEquals("internal_exception", DiscussionSubscriber.classifyException("x.Y", "HTTP 400: bad schema"));
    }

    @Test
    void classifyException_contextRejectionWinsOverATimeoutMatch() {
        assertEquals("context_exceeded", DiscussionSubscriber.classifyException("x.TimeoutException",
                "Timed out after: This model's maximum context length is 32768 tokens"));
    }
}
