package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

/**
 * Verifies that a woken specialist loads the coordinator-selected skill bodies
 * and injects them into its discussion turn (Chunk 4 of the Skills feature).
 *
 * Plain JUnit 5 (not @QuarkusTest) to avoid Ollama/NATS dependency in CI.
 *
 * Covered scenarios:
 * - Advisory_ready with selectedSkills: bodies prepended to the mulling message.
 * - Missing skill file: skipped with no failure; remaining skills still load.
 * - Empty/absent selectedSkills: NOTHING injected, message byte-identical to baseline.
 */
class DiscussionSubscriberSkillsTest {

    // -------------------------------------------------------------------------
    // Skill body injection into the mulling turn
    // -------------------------------------------------------------------------

    /**
     * When advisory_ready carries selectedSkills and the skill files exist,
     * handleMessage must store the names and the next mulling call must receive
     * the skill bodies prepended to the conversation.
     */
    @Test
    void mullingTurnReceivesSkillBodies(@TempDir Path skillsDir) throws IOException {
        // Arrange: write two skill files.
        Files.writeString(skillsDir.resolve("k8s-networking.txt"),
                "ASSERT pods use ClusterIP for intra-cluster traffic");
        Files.writeString(skillsDir.resolve("observability.txt"),
                "WHEN querying metrics THEN use Prometheus range queries");

        var capturedMessage = new String[1];
        var chatService = mock(ChatService.class);
        when(chatService.directChat(any(ChatService.ChatRequest.class), anyBoolean()))
                .thenAnswer(inv -> {
                    capturedMessage[0] = ((ChatService.ChatRequest) inv.getArgument(0)).message();
                    return new ChatService.ChatResult("conv", "tool output", "model", null);
                });

        var subscriber = createSubscriber("k8s-tooler", chatService, skillsDir.toString());

        // Feed advisory_ready with selectedSkills.
        String threadId = "thread-skills-01";
        String advisoryReady = advisoryReadyJson(threadId, "k8s-networking", "observability");
        subscriber.handleMessageForTest(advisoryReadySubject(threadId), advisoryReady);

        // Simulate mulling inference by invoking the package-private helper directly.
        subscriber.runMullingInferenceForTest("Thread conversation here.", threadId);

        assertNotNull(capturedMessage[0], "directChat must be called");
        assertTrue(capturedMessage[0].contains("k8s-networking"),
                "skill name header must appear in message");
        assertTrue(capturedMessage[0].contains("ASSERT pods use ClusterIP"),
                "k8s-networking body must be injected");
        assertTrue(capturedMessage[0].contains("WHEN querying metrics"),
                "observability body must be injected");
        assertTrue(capturedMessage[0].contains("Thread conversation here."),
                "original conversation must still be present");
        // Skill context must come BEFORE the conversation.
        int skillPos = capturedMessage[0].indexOf("ASSERT pods use ClusterIP");
        int convPos = capturedMessage[0].indexOf("Thread conversation here.");
        assertTrue(skillPos < convPos, "skill bodies must precede the conversation text");
    }

    /**
     * When a skill file listed in selectedSkills does not exist on disk, that
     * skill is silently skipped. Remaining skills are still injected and the
     * turn is not failed.
     */
    @Test
    void missingSkillFileIsSkipped(@TempDir Path skillsDir) throws IOException {
        // Only write one of the two selected skill files.
        Files.writeString(skillsDir.resolve("present-skill.txt"), "ASSERT present skill body");
        // "missing-skill.txt" is intentionally not created.

        var capturedMessage = new String[1];
        var chatService = mock(ChatService.class);
        when(chatService.directChat(any(ChatService.ChatRequest.class), anyBoolean()))
                .thenAnswer(inv -> {
                    capturedMessage[0] = ((ChatService.ChatRequest) inv.getArgument(0)).message();
                    return new ChatService.ChatResult("conv", "ok", "model", null);
                });

        var subscriber = createSubscriber("k8s-tooler", chatService, skillsDir.toString());

        String threadId = "thread-missing-01";
        String advisoryReady = advisoryReadyJson(threadId, "present-skill", "missing-skill");
        subscriber.handleMessageForTest(advisoryReadySubject(threadId), advisoryReady);

        subscriber.runMullingInferenceForTest("The question.", threadId);

        assertNotNull(capturedMessage[0]);
        assertTrue(capturedMessage[0].contains("ASSERT present skill body"),
                "present skill body must be injected");
        assertFalse(capturedMessage[0].contains("missing-skill"),
                "missing skill must not cause an error marker or placeholder");
        assertTrue(capturedMessage[0].contains("The question."),
                "original conversation must still be present");
    }

    /**
     * ZERO guarantee: when no selectedSkills are present in advisory_ready,
     * the message passed to directChat is byte-identical to the raw conversation
     * string: no prefix, no header, no whitespace added.
     */
    @Test
    void noSkillsSelectedLeavesMessageUnchanged(@TempDir Path skillsDir) throws IOException {
        Files.writeString(skillsDir.resolve("some-skill.txt"), "should never appear");

        var capturedMessage = new String[1];
        var chatService = mock(ChatService.class);
        when(chatService.directChat(any(ChatService.ChatRequest.class), anyBoolean()))
                .thenAnswer(inv -> {
                    capturedMessage[0] = ((ChatService.ChatRequest) inv.getArgument(0)).message();
                    return new ChatService.ChatResult("conv", "ok", "model", null);
                });

        var subscriber = createSubscriber("k8s-tooler", chatService, skillsDir.toString());

        // advisory_ready with NO selectedSkills field.
        String threadId = "thread-noskills-01";
        String advisoryReady = advisoryReadyNoSkillsJson(threadId);
        subscriber.handleMessageForTest(advisoryReadySubject(threadId), advisoryReady);

        String baseConversation = "Baseline conversation content.";
        subscriber.runMullingInferenceForTest(baseConversation, threadId);

        assertNotNull(capturedMessage[0]);
        assertEquals(baseConversation, capturedMessage[0],
                "Message must be byte-identical to baseline when no skills are selected");
    }

    /**
     * When selectedSkills is absent entirely (no key in metadata), the same
     * ZERO guarantee applies.
     */
    @Test
    void absentSelectedSkillsKeyLeavesMessageUnchanged(@TempDir Path skillsDir) {
        var capturedMessage = new String[1];
        var chatService = mock(ChatService.class);
        when(chatService.directChat(any(ChatService.ChatRequest.class), anyBoolean()))
                .thenAnswer(inv -> {
                    capturedMessage[0] = ((ChatService.ChatRequest) inv.getArgument(0)).message();
                    return new ChatService.ChatResult("conv", "ok", "model", null);
                });

        var subscriber = createSubscriber("k8s-tooler", chatService, skillsDir.toString());

        // No advisory_ready at all for this thread.
        String threadId = "thread-nokey-01";
        String baseConversation = "Clean baseline.";
        subscriber.runMullingInferenceForTest(baseConversation, threadId);

        assertNotNull(capturedMessage[0]);
        assertEquals(baseConversation, capturedMessage[0],
                "No advisory_ready means no skills in context; message must be unchanged");
    }

    // -------------------------------------------------------------------------
    // Helpers
    // -------------------------------------------------------------------------

    private DiscussionSubscriber createSubscriber(String agentName, ChatService chatService,
                                                   String skillsDir) {
        var props = stubProperties(agentName);
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.getConnection()).thenReturn(mock(Connection.class));
        var metrics = new DiscussionMetrics(
                new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        var loader = new SkillBodyLoader(skillsDir);
        var subscriber = new DiscussionSubscriber(natsProvider, chatService, metrics, props,
                "http://localhost:11434");
        subscriber.setSkillBodyLoaderForTest(loader);
        return subscriber;
    }

    private static String advisoryReadySubject(String threadId) {
        return "kubemoot.discuss.crew-x.broadcast." + threadId;
    }

    private static String advisoryReadyJson(String threadId, String... skillNames) {
        var skills = new StringBuilder("[");
        for (int i = 0; i < skillNames.length; i++) {
            if (i > 0) skills.append(",");
            skills.append("\"").append(skillNames[i]).append("\"");
        }
        skills.append("]");
        return """
                {
                  "messageId": "msg-001",
                  "threadId": "%s",
                  "agentName": "coordinator",
                  "messageType": "advisory_ready",
                  "content": "advisory summary",
                  "channel": "broadcast",
                  "timestamp": "2026-01-01T00:00:00Z",
                  "metadata": {
                    "userQuery": "What is the cluster state?",
                    "selectedSkills": %s
                  }
                }""".formatted(threadId, skills);
    }

    private static String advisoryReadyNoSkillsJson(String threadId) {
        return """
                {
                  "messageId": "msg-002",
                  "threadId": "%s",
                  "agentName": "coordinator",
                  "messageType": "advisory_ready",
                  "content": "advisory summary",
                  "channel": "broadcast",
                  "timestamp": "2026-01-01T00:00:00Z",
                  "metadata": {
                    "userQuery": "What is the cluster state?",
                    "selectedSkills": []
                  }
                }""".formatted(threadId);
    }

    private static AgentProperties stubProperties(String agentName) {
        return new AgentProperties() {
            @Override public ai.kubemoot.agent.config.AgentProperties.Memory memory() {
                return ai.kubemoot.agent.TestStubs.memory();
            }
            @Override public String agentName() { return agentName; }
            @Override public String agentDescription() { return "test agent"; }
            @Override public String agentType() { return "chat"; }
            @Override public Optional<String> systemPrompt() { return Optional.empty(); }
            @Override public Optional<String> systemPromptFile() { return Optional.empty(); }
            @Override public Model model() { return new Model() {
                @Override public String name() { return "default"; }
                @Override public String provider() { return "ollama"; }
                @Override public String model() { return "qwen3:8b"; }
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
                @Override public boolean requiredForReadiness() { return false; }
            }; }
            @Override public Nats nats() { return new Nats() {
                @Override public Optional<String> url() { return Optional.empty(); }
            }; }
            @Override public Discuss discuss() { return new Discuss() {
                @Override public Optional<String> channels() { return Optional.of("kubernetes"); }
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
                @Override public boolean enabled() { return false; }
                @Override public int intervalSeconds() { return 60; }
                @Override public String kvBucket() { return "kubemoot_agent_state"; }
            }; }
            @Override public Optional<java.util.List<RagSource>> ragSources() { return Optional.empty(); }
            @Override public Optional<java.util.List<McpServer>> mcpServers() { return Optional.empty(); }
            @Override public Optional<java.util.List<String>> enabledTools() { return Optional.empty(); }
            @Override public Optional<java.util.List<String>> disabledTools() { return Optional.empty(); }
            @Override public TriageModel triageModel() { return new TriageModel() {
                @Override public Optional<String> modelId() { return Optional.empty(); }
                @Override public Optional<String> endpoint() { return Optional.empty(); }
                @Override public double temperature() { return 0.3; }
                @Override public int maxTokens() { return 2048; }
                @Override public int timeoutSeconds() { return 120; }
            }; }
            @Override public Optional<String> crew() { return Optional.of("crew-x"); }
            @Override public Optional<String> crewVersion() { return Optional.empty(); }
            @Override public ResumeSearch resumeSearch() { return new ResumeSearch() {
                @Override public Optional<String> endpoint() { return Optional.empty(); }
            }; }
        };
    }
}
