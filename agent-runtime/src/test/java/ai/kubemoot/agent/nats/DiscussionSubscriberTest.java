package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.*;
import io.nats.client.api.ConsumerInfo;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/**
 * Tests for DiscussionSubscriber JetStream vs Dispatcher subscription modes.
 * Uses plain JUnit (not @QuarkusTest) to avoid Ollama dependency in CI.
 */
class DiscussionSubscriberTest {

    @Test
    void isUsingJetStream_whenConsumerConfigured() {
        var subscriber = createSubscriber("discuss-test-agent", "kubernetes");
        assertTrue(subscriber.isUsingJetStream());
    }

    @Test
    void isUsingJetStream_whenConsumerNotConfigured() {
        var subscriber = createSubscriber(null, "kubernetes");
        assertFalse(subscriber.isUsingJetStream());
    }

    @Test
    void isUsingJetStream_whenConsumerEmpty() {
        var subscriber = createSubscriber("", "kubernetes");
        assertFalse(subscriber.isUsingJetStream());
    }

    @Test
    void trySubscribe_dispatcherPath_whenNoJetStreamConsumer() throws Exception {
        var natsProvider = mock(NatsConnectionProvider.class);
        var conn = mock(Connection.class);
        var dispatcher = mock(Dispatcher.class);

        when(natsProvider.getConnection()).thenReturn(conn);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", "crew-x"));
        when(conn.createDispatcher()).thenReturn(dispatcher);
        var subscription = mock(Subscription.class);
        when(dispatcher.subscribe(anyString(), any(MessageHandler.class))).thenReturn(subscription);

        var subscriber = createSubscriber(null, "kubernetes", natsProvider);
        boolean result = subscriber.trySubscribe();

        assertTrue(result);
        // Dispatcher mode: subscribes to broadcast + each channel, namespace-scoped
        verify(dispatcher).subscribe(eq("kubemoot.discuss.ns-a.crew-x.broadcast.>"), any(MessageHandler.class));
        verify(dispatcher).subscribe(eq("kubemoot.discuss.ns-a.crew-x.kubernetes.>"), any(MessageHandler.class));
    }

    @Test
    void trySubscribe_jetStreamPath_whenConsumerConfigured() throws Exception {
        var natsProvider = mock(NatsConnectionProvider.class);
        var conn = mock(Connection.class);
        var dispatcher = mock(Dispatcher.class);
        var jetStream = mock(JetStream.class);
        var subscription = mock(JetStreamSubscription.class);

        when(natsProvider.getConnection()).thenReturn(conn);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", "crew-x"));
        when(conn.createDispatcher()).thenReturn(dispatcher);
        when(conn.jetStream()).thenReturn(jetStream);
        when(jetStream.subscribe(anyString(), any(Dispatcher.class),
                any(MessageHandler.class), eq(false), any(PushSubscribeOptions.class))).thenReturn(subscription);

        var subscriber = createSubscriber("discuss-test-agent", "kubernetes", natsProvider);
        boolean result = subscriber.trySubscribe();

        assertTrue(result);
        // JetStream mode: single subscribe call with durable consumer
        verify(jetStream).subscribe(eq("kubemoot.discuss.ns-a.crew-x.>"), any(Dispatcher.class),
                any(MessageHandler.class), eq(false), any(PushSubscribeOptions.class));
    }

    @Test
    void trySubscribe_returnsNull_whenNoConnection() {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.getConnection()).thenReturn(null);

        var subscriber = createSubscriber("discuss-test-agent", "kubernetes", natsProvider);
        boolean result = subscriber.trySubscribe();

        assertFalse(result);
    }

    // --- Helper methods ---

    private DiscussionSubscriber createSubscriber(String jetstreamConsumer, String channels) {
        return createSubscriber(jetstreamConsumer, channels, mock(NatsConnectionProvider.class));
    }

    private DiscussionSubscriber createSubscriber(String jetstreamConsumer, String channels,
                                                   NatsConnectionProvider natsProvider) {
        var chatService = mock(ChatService.class);
        var props = stubProperties("test-agent", channels, jetstreamConsumer);
        var metrics = new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, chatService,
                metrics, props, "http://localhost:11434");
    }

    private static AgentProperties stubProperties(String agentName, String channels, String jsConsumer) {
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
                @Override public Optional<String> endpoint() { return Optional.empty(); }
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
}
