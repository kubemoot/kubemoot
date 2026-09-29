package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * Tests for DiscussionOrchestrator helper methods extracted during CC refactoring.
 * Plain JUnit 5 (not @QuarkusTest) to avoid Ollama/NATS dependency in CI.
 *
 * Covers: resolveChannel, hasZeroSignals, classifyGap, buildFallbackResponse,
 * looksLikeGapReport, suggestsOnboarding.
 */
class DiscussionOrchestratorHelpersTest {

    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() {
        orchestrator = createOrchestrator("kubernetes,helm,proxmox");
    }

    // --- deadlineWindowMs (heartbeat-floored evaluation deadline) ---

    @Test
    void publishedContent_synthesisDeliveredWholeAndMarkerStripped() {
        // The synthesis is the terminal user answer: it must NOT be cut at the 5000
        // bus cap, and any internal artifact-spill marker must be stripped so the
        // user is never handed an /artifacts/ path they cannot open.
        String body = "A".repeat(20_000);
        String synth = body + "\n\n[ARTIFACT key=crew/t/agent/agree/abc bytes=99 - full data materialized at /artifacts/crew/t/agent/agree/abc]";
        String out = DiscussionOrchestrator.publishedContent("synthesis", synth);
        assertFalse(out.contains("ARTIFACT key="), "artifact marker must be stripped from synthesis");
        assertFalse(out.contains("/artifacts/"), "no internal artifact path may reach the user");
        assertFalse(out.endsWith("..."), "a 20k synthesis must not be truncated");
        assertTrue(out.startsWith(body), "the full answer body must be preserved");
    }

    @Test
    void publishedContent_nonSynthesisKeepsBusCap() {
        // A finding (not the terminal answer) keeps the 5000-char bus-hygiene cap.
        String big = "B".repeat(9000);
        String out = DiscussionOrchestrator.publishedContent("finding", big);
        assertTrue(out.endsWith("..."), "an over-cap finding is still truncated");
        assertTrue(out.length() <= 5000 + 3, "finding is capped at the bus limit");
    }

    @Test
    void deadlineWindow_floorsAtHeartbeatInterval_whenP90Small() {
        // A fast-P90 agent (5s) that is slow this round must still survive to its
        // next heartbeat (30s cadence). Without the floor it would be 5s+grace=15s
        // and expire in the gap between beats (the k8s-workloads timeout bug).
        long window = DiscussionOrchestrator.deadlineWindowMs(5, 10_000L);
        assertEquals(40_000L, window); // max(5s, 30s) + 10s grace
        assertTrue(window > 30_000L, "window must outlive the 30s heartbeat interval");
    }

    @Test
    void deadlineWindow_usesP90_whenLargerThanHeartbeat() {
        // A genuinely slow agent (P90 60s) keeps its larger window.
        assertEquals(70_000L, DiscussionOrchestrator.deadlineWindowMs(60, 10_000L)); // 60s + 10s grace
    }

    // --- resolveChannel ---

    @Test
    void resolveChannel_returnsStoredChannel() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.primaryChannel = "kubernetes";
        assertEquals("kubernetes", DiscussionOrchestrator.resolveChannel(state));
    }

    @Test
    void resolveChannel_defaultsToGeneral_whenNull() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.primaryChannel = null;
        assertEquals("general", DiscussionOrchestrator.resolveChannel(state));
    }

    // --- hasZeroSignals ---

    @Test
    void hasZeroSignals_trueWhenEmpty() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        assertTrue(DiscussionOrchestrator.hasZeroSignals(state));
    }

    @Test
    void hasZeroSignals_falseWhenAgreePresent() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("agent-a", "content");
        assertFalse(DiscussionOrchestrator.hasZeroSignals(state));
    }

    @Test
    void hasZeroSignals_falseWhenStandAsidePresent() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.standAsideSignals.add("agent-b");
        assertFalse(DiscussionOrchestrator.hasZeroSignals(state));
    }

    @Test
    void hasZeroSignals_falseWhenConcernPresent() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.concernSignals.put("agent-c", "concern text");
        assertFalse(DiscussionOrchestrator.hasZeroSignals(state));
    }

    @Test
    void hasZeroSignals_falseWhenBlockPresent() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.blockSignals.put("agent-d", "block text");
        assertFalse(DiscussionOrchestrator.hasZeroSignals(state));
    }

    @Test
    void hasZeroSignals_falseWhenFailurePresent() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("agent-e", "mcp_tool_timeout: get_targets");
        assertFalse(DiscussionOrchestrator.hasZeroSignals(state));
    }

    // --- classifyGap ---

    @Test
    void classifyGap_toolGap_whenZeroAgreesWithConcerns() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.concernSignals.put("agent-a", "TOOL_GAP: missing rabbitmq tools");
        var gap = orchestrator.classifyGap(state, 0, "unable to find information");
        assertEquals(DiscussionOrchestrator.GapType.TOOL_GAP, gap);
    }

    @Test
    void classifyGap_toolerGap_whenZeroAgreesWithStandAsidesAndGapReport() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.standAsideSignals.add("agent-a");
        state.standAsideSignals.add("agent-b");
        var gap = orchestrator.classifyGap(state, 0, "could not find any information");
        assertEquals(DiscussionOrchestrator.GapType.TOOLER_GAP, gap);
    }

    @Test
    void classifyGap_none_whenToolerAgreesPresent() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("agent-a", "Here is what I found...");
        var gap = orchestrator.classifyGap(state, 1, "Based on agent responses...");
        assertEquals(DiscussionOrchestrator.GapType.NONE, gap);
    }

    @Test
    void classifyGap_none_whenNoStandAsidesAndNoConcerns() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        var gap = orchestrator.classifyGap(state, 0, "I can answer this directly.");
        assertEquals(DiscussionOrchestrator.GapType.NONE, gap);
    }

    @Test
    void classifyGap_suppressesGap_whenLlmAnsweredDirectly() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.standAsideSignals.add("agent-a");
        // Response does NOT look like a gap report (direct answer)
        var gap = orchestrator.classifyGap(state, 0, "Kubernetes uses etcd for state storage.");
        assertEquals(DiscussionOrchestrator.GapType.NONE, gap);
    }

    @Test
    void classifyGap_toolerGap_whenLowTriageConfidence() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.triageConfidence = 0.1;
        // No stand-asides but low triage confidence flags tooler gap
        var gap = orchestrator.classifyGap(state, 0, "unable to find relevant toolers");
        assertEquals(DiscussionOrchestrator.GapType.TOOLER_GAP, gap);
    }

    @Test
    void classifyGap_toolerGap_whenResponseSuggestsOnboarding() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.standAsideSignals.add("agent-a");
        var gap = orchestrator.classifyGap(state, 0, "You may want to onboard a new tooler");
        assertEquals(DiscussionOrchestrator.GapType.TOOLER_GAP, gap);
    }

    @Test
    void classifyGap_infrastructureGap_whenZeroAgreesWithFailures() {
        // Toolers tried to evaluate but their tools failed. Distinct from
        // TOOLER_GAP (no relevant expertise) — the fix is to stabilise
        // infrastructure, not to onboard a new tooler. See [[Agent
        // Failure as First-Class Consensus Signal]] and [[feedback_embrace_failure]].
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.failureSignals.put("nvidia-gpu",
                "Tool 'get_targets' failed 2 times in this evaluation");
        var gap = orchestrator.classifyGap(state, 0, "could not retrieve GPU metrics");
        assertEquals(DiscussionOrchestrator.GapType.INFRASTRUCTURE_GAP, gap);
    }

    @Test
    void classifyGap_infrastructureGap_takesPrecedenceOverToolerGap() {
        // When both stand-asides and failures are present with zero agrees,
        // failures win — the infrastructure problem is the stronger signal
        // for the operator to investigate first.
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.standAsideSignals.add("k8s-config");
        state.failureSignals.put("nvidia-gpu", "mcp_tool_timeout");
        var gap = orchestrator.classifyGap(state, 0, "could not determine");
        assertEquals(DiscussionOrchestrator.GapType.INFRASTRUCTURE_GAP, gap);
    }

    @Test
    void classifyGap_toolGap_takesPrecedenceOverFailure() {
        // Concern (TOOL_GAP) carries explicit agent intent about missing
        // tools; failure is a passive infrastructure observation. Concern
        // should still win when both exist with zero agrees — backwards
        // compatibility with the existing "tooler explicitly named the
        // missing tool" path.
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.concernSignals.put("agent-a", "TOOL_GAP: missing rabbitmq tools");
        state.failureSignals.put("agent-b", "mcp_tool_timeout");
        var gap = orchestrator.classifyGap(state, 0, "could not find information");
        assertEquals(DiscussionOrchestrator.GapType.TOOL_GAP, gap);
    }

    // --- buildFallbackResponse ---

    @Test
    void buildFallbackResponse_usesAgreeSignals() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        state.agreeSignals.put("k8s-agent", "Found 3 namespaces");
        state.agreeSignals.put("helm-agent", "Found 2 releases");

        String fallback = orchestrator.buildFallbackResponse(state);
        assertTrue(fallback.contains("k8s-agent"));
        assertTrue(fallback.contains("Found 3 namespaces"));
        assertTrue(fallback.contains("helm-agent"));
        assertTrue(fallback.contains("Found 2 releases"));
    }

    @Test
    void buildFallbackResponse_returnsGenericMessage_whenNoAgrees() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        String fallback = orchestrator.buildFallbackResponse(state);
        // Accurately describes the no-contribution / empty-synthesis case, and must
        // not blame a "resource constraint" (the old wording was misleading).
        assertTrue(fallback.contains("No agent contributed"));
        assertFalse(fallback.contains("resource constraint"));
    }

    // --- looksLikeGapReport ---

    @Test
    void looksLikeGapReport_trueForUnableToFind() {
        assertTrue(orchestrator.looksLikeGapReport("I was unable to find any data on RabbitMQ"));
    }

    @Test
    void looksLikeGapReport_trueForCouldNotFind() {
        assertTrue(orchestrator.looksLikeGapReport("Could not find relevant information"));
    }

    @Test
    void looksLikeGapReport_trueForNoTooler() {
        assertTrue(orchestrator.looksLikeGapReport("There is no tooler for this domain"));
    }

    @Test
    void looksLikeGapReport_falseForDirectAnswer() {
        assertFalse(orchestrator.looksLikeGapReport("Kubernetes uses etcd as its state store."));
    }

    @Test
    void looksLikeGapReport_falseForNull() {
        assertFalse(orchestrator.looksLikeGapReport(null));
    }

    // --- suggestsOnboarding ---

    @Test
    void suggestsOnboarding_trueForOnboardKeyword() {
        assertTrue(orchestrator.suggestsOnboarding("You can onboard a RabbitMQ tooler"));
    }

    @Test
    void suggestsOnboarding_trueForNewTooler() {
        assertTrue(orchestrator.suggestsOnboarding("Consider adding a new tooler for this domain"));
    }

    @Test
    void suggestsOnboarding_trueForNewMcp() {
        assertTrue(orchestrator.suggestsOnboarding("A new MCP server could help"));
    }

    @Test
    void suggestsOnboarding_falseForNormalResponse() {
        assertFalse(orchestrator.suggestsOnboarding("Here are the Kubernetes namespaces"));
    }

    @Test
    void suggestsOnboarding_falseForNull() {
        assertFalse(orchestrator.suggestsOnboarding(null));
    }

    // --- hasSufficientConsensus (Coordinator Sufficient-Agrees Settle) ---
    // settleSeconds = 5 in the stub; lastSignalReceived 6s ago = "substantively quiet".

    private static void agreeFrom(DiscussionOrchestrator.ThreadState state, String... agents) {
        for (String a : agents) state.agreeSignals.put(a, "answer");
    }

    @Test
    void sufficientConsensus_twoToolerAgrees_andQuiet_despitePendingStraggler() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        agreeFrom(state, "obs-metrics", "k8s-metrics");
        state.lastSignalReceived = java.time.Instant.now().minusSeconds(6); // quiet > settleSeconds
        // A straggler is still pending (heartbeating) — must NOT block the settle.
        state.pendingEvaluations.put("nvidia-gpu", System.currentTimeMillis() + 120_000);
        assertTrue(orchestrator.hasSufficientConsensus(state, java.time.Instant.now()),
                "2 tooler agrees + quiet should settle despite a pending straggler");
    }

    @Test
    void sufficientConsensus_blockedByLoneAgree() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        agreeFrom(state, "obs-metrics");
        state.lastSignalReceived = java.time.Instant.now().minusSeconds(10);
        assertFalse(orchestrator.hasSufficientConsensus(state, java.time.Instant.now()),
                "a single agree must keep waiting for peers");
    }

    @Test
    void sufficientConsensus_blockedByOpenConcern() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        agreeFrom(state, "obs-metrics", "k8s-metrics");
        state.concernSignals.put("scheduler-advisor", "but check capacity");
        state.lastSignalReceived = java.time.Instant.now().minusSeconds(10);
        assertFalse(orchestrator.hasSufficientConsensus(state, java.time.Instant.now()),
                "an open concern must never be abandoned by the fast-settle");
    }

    @Test
    void sufficientConsensus_blockedWhenNotQuiet() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        agreeFrom(state, "obs-metrics", "k8s-metrics");
        state.lastSignalReceived = java.time.Instant.now().minusSeconds(2); // < settleSeconds=5
        assertFalse(orchestrator.hasSufficientConsensus(state, java.time.Instant.now()),
                "a recent terminal signal means peers may still be landing — keep waiting");
    }

    @Test
    void sufficientConsensus_researcherAgreesDoNotCount() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        agreeFrom(state, "internet-search", "obs-metrics");
        state.researcherAgents.add("internet-search"); // only 1 tooler agree
        state.lastSignalReceived = java.time.Instant.now().minusSeconds(10);
        assertEquals(1, orchestrator.toolerAgreeCount(state));
        assertFalse(orchestrator.hasSufficientConsensus(state, java.time.Instant.now()),
                "researcher agrees don't reach the 2-tooler threshold");
    }

    // --- synthesis completeness retry ---

    private static final String NAMESPACES = "[resources_list]\n"
            + "APIVERSION   KIND        NAME          STATUS   AGE\n"
            + "v1           Namespace   alpha         Active   1d\n"
            + "v1           Namespace   bravo         Active   1d\n"
            + "v1           Namespace   charlie       Active   1d\n"
            + "v1           Namespace   delta         Active   1d\n"
            + "v1           Namespace   echo          Active   1d\n"
            + "v1           Namespace   foxtrot       Active   1d\n";

    @Test
    void completenessRetryTooLargeForEveryContext_keepsTheFirstSynthesis() {
        var chat = mock(ChatService.class);
        var first = new ChatService.SimpleLlmResult("alpha, bravo, charlie, delta, echo", 10, 5);
        when(chat.simpleLlmCallWithTokens(anyString(), anyString()))
                .thenReturn(first)
                .thenThrow(ai.kubemoot.agent.provider.NoFitException.promptTooLarge("m", "retry too large"));

        var result = createOrchestrator("", chat, 2).synthesizeWithCompleteness("sys", NAMESPACES, "t-c");

        assertSame(first, result);
        verify(chat, times(2)).simpleLlmCallWithTokens(anyString(), anyString());
    }

    @Test
    void completenessRetryFailingForAnotherReason_stillFails() {
        var chat = mock(ChatService.class);
        when(chat.simpleLlmCallWithTokens(anyString(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult("alpha, bravo, charlie, delta, echo", 10, 5))
                .thenThrow(ai.kubemoot.agent.provider.NoFitException.gpuBusy("m", "busy"));

        var orchestrator = createOrchestrator("", chat, 2);
        assertThrows(ai.kubemoot.agent.provider.NoFitException.class,
                () -> orchestrator.synthesizeWithCompleteness("sys", NAMESPACES, "t-c"));
    }

    // --- Helper ---

    private DiscussionOrchestrator createOrchestrator(String channels) {
        return createOrchestrator(channels, mock(ChatService.class), 0);
    }

    private DiscussionOrchestrator createOrchestrator(String channels, ChatService chatService, int completenessRetries) {
        var natsProvider = mock(NatsConnectionProvider.class);
        var metrics = mock(DiscussionMetrics.class);
        var latencyTracker = mock(LatencyTracker.class);
        var resumeSearchClient = mock(ResumeSearchClient.class);
        var properties = stubProperties(channels, completenessRetries);
        return new DiscussionOrchestrator(natsProvider, properties, chatService, metrics, latencyTracker, resumeSearchClient);
    }

    private static AgentProperties stubProperties(String channels, int completenessRetries) {
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
                @Override public int synthesisCompletenessRetries() { return completenessRetries; }
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
            @Override public Optional<String> namespace() { return Optional.of("ns-test"); }
            @Override public Optional<String> crew() { return Optional.empty(); }
            @Override public Optional<String> crewVersion() { return Optional.empty(); }
            @Override public ResumeSearch resumeSearch() { return new ResumeSearch() {
                @Override public Optional<String> endpoint() { return Optional.empty(); }
            }; }
        };
    }

    // --- GPU capacity: waiting and stand-aside reasons reach the answer ---

    private static com.fasterxml.jackson.databind.JsonNode signal(String reason, String model) {
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        var msg = mapper.createObjectNode();
        var meta = msg.putObject("metadata");
        if (reason != null) meta.put("reason", reason);
        if (model != null) meta.put("model", model);
        return msg;
    }

    @Test
    void gpuBusyStandAside_withNoContribution_answersWithTheCapacityMessage() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(state, "rules-keeper", "stand_aside", "", signal("gpu-busy", "qwen3:14b"));

        assertEquals(DiscussionOrchestrator.GPU_BUSY_MESSAGE, DiscussionOrchestrator.capacityMessage(state));
        String fallback = orchestrator.buildFallbackResponse(state);
        assertTrue(fallback.startsWith("The crew's agents could not get a GPU"));
        assertTrue(fallback.contains("not the crew's design"));
        assertFalse(fallback.contains("No agent contributed"));
    }

    @Test
    void modelTooLargeStandAside_namesTheModel() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(state, "big-agent", "stand_aside", "", signal("model-too-large", "qwen3:235b"));

        assertEquals("No GPU in this cluster can hold the model qwen3:235b the agents need; "
                + "add a smaller Model or a larger GPU.", DiscussionOrchestrator.capacityMessage(state));
    }

    @Test
    void bothReasons_reportBoth_tooLargeFirst() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(state, "a", "stand_aside", "", signal("model-too-large", "qwen3:235b"));
        orchestrator.handleAgentSignal(state, "b", "stand_aside", "", signal("gpu-busy", "qwen3:14b"));

        String msg = DiscussionOrchestrator.capacityMessage(state);
        assertTrue(msg.startsWith("No GPU in this cluster can hold the model qwen3:235b"));
        assertTrue(msg.endsWith(DiscussionOrchestrator.GPU_BUSY_MESSAGE));
    }

    @Test
    void agentStillWaitingAtSettle_countsAsGpuBusy() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(state, "rules-keeper", "waiting", "", signal("gpu-busy", "qwen3:14b"));

        assertEquals("qwen3:14b", state.waitingAgents.get("rules-keeper"));
        assertEquals(DiscussionOrchestrator.GPU_BUSY_MESSAGE, DiscussionOrchestrator.capacityMessage(state));
    }

    @Test
    void waitingAgentThatThenAnswers_isNoLongerWaiting_andTheAnswerWins() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(state, "rules-keeper", "waiting", "", signal("gpu-busy", "qwen3:14b"));
        orchestrator.handleAgentSignal(state, "rules-keeper", "agree", "Rule 7 applies", signal(null, null));

        assertTrue(state.waitingAgents.isEmpty());
        assertNull(DiscussionOrchestrator.capacityMessage(state));
        assertTrue(orchestrator.buildFallbackResponse(state).contains("Rule 7 applies"));
    }

    @Test
    void ordinaryStandAside_keepsTheExistingNoContributionText() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(state, "k8s-agent", "stand_aside", "", signal(null, null));
        orchestrator.handleAgentSignal(state, "helm-agent", "stand_aside", "", signal("no-fit", null));

        assertNull(DiscussionOrchestrator.capacityMessage(state));
        assertTrue(state.capacityStandAsides.isEmpty());
        assertTrue(orchestrator.buildFallbackResponse(state).startsWith("No agent contributed an answer"));
    }

    @Test
    void capacityReasonWithAContribution_doesNotReplaceTheAnswer() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        orchestrator.handleAgentSignal(state, "a", "stand_aside", "", signal("gpu-busy", "qwen3:14b"));
        orchestrator.handleAgentSignal(state, "b", "concern", "TOOL_GAP: need a GPU metrics tool", signal(null, null));

        assertNull(DiscussionOrchestrator.capacityMessage(state), "a concern is a contribution");
    }

    @Test
    void recordCapacityReason_toleratesMissingMetadata() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        DiscussionOrchestrator.recordCapacityReason(state, "a", null);
        DiscussionOrchestrator.recordCapacityReason(state, "b", new com.fasterxml.jackson.databind.ObjectMapper().createObjectNode());
        DiscussionOrchestrator.recordCapacityReason(state, "c", signal("model-too-large", null));

        assertEquals(java.util.Map.of("c", "model-too-large"), java.util.Map.copyOf(state.capacityStandAsides));
        assertTrue(state.tooLargeModels.isEmpty());
        assertEquals("No GPU in this cluster can hold the model the agents need; add a smaller Model or a larger GPU.",
                DiscussionOrchestrator.capacityMessage(state));
    }
}
