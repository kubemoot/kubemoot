package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/**
 * Tests for the coordinator reasoning select+brief path in DiscussionOrchestrator.
 *
 * Covers:
 * - Well-formed {"selected",[...],"brief":"...","technologies":[...]} parsed correctly.
 * - Missing/empty "selected" field triggers the similarity fallback contract.
 * - Hallucinated agent names are dropped; valid names are preserved.
 * - Channel classification still works when technologies list is present.
 * - Malformed JSON response triggers the fallback (returns null from runReasoningSelectBrief).
 * - readTree-only parsing (no records/readValue - GraalVM native rule).
 *
 * Plain JUnit 5, no @QuarkusTest, no Ollama.
 */
class DiscussionOrchestratorReasoningSelectTest {

    private DiscussionOrchestrator orchestrator;
    private ChatService chatService;
    private ResumeSearchClient resumeSearchClient;
    private AgentProperties properties;

    private static final String SAMPLE_RESUMES = """
            [{"name":"k8s-metrics","description":"Kubernetes metrics tooler",
              "tools":["pods_top","resources_list"]},
             {"name":"k8s-workloads","description":"Kubernetes workloads tooler",
              "tools":["resources_list","pod_logs"]},
             {"name":"proxmox-agent","description":"Proxmox hypervisor tooler",
              "tools":["proxmox-list-nodes","proxmox-vm-list"]}]
            """;

    @BeforeEach
    void setUp() {
        var natsProvider = mock(NatsConnectionProvider.class);
        chatService = mock(ChatService.class);
        resumeSearchClient = mock(ResumeSearchClient.class);

        properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        var triageModel = mock(AgentProperties.TriageModel.class);
        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("test-coordinator");
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("kubernetes,proxmox"));
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
        when(properties.triageModel()).thenReturn(triageModel);
        when(triageModel.timeoutSeconds()).thenReturn(120);

        var metrics = new DiscussionMetrics(new SimpleMeterRegistry());
        var latencyTracker = mock(LatencyTracker.class);

        orchestrator = new DiscussionOrchestrator(
                natsProvider, properties, chatService, metrics, latencyTracker, resumeSearchClient);
    }

    // --- Well-formed response: primary path succeeds ---

    @Test
    void runReasoningSelectBrief_wellFormedResponse_parsesSelectedAndBrief() {
        String llmResponse = """
                {"selected": ["k8s-metrics", "k8s-workloads"],
                 "brief": "To judge restart safety, check current load via pods_top, then review each deployment's replica count.",
                 "technologies": ["kubernetes"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 120, 40));

        var state = new DiscussionOrchestrator.ThreadState("thread-1");
        state.userQuery = "Is it safe to restart the busiest namespace's deployments?";

        // Seed the known agent names from the sample resumes so name validation does not block.
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "k8s-workloads", "proxmox-agent"));

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result, "Primary path must return a result for well-formed JSON");
        assertEquals(List.of("k8s-metrics", "k8s-workloads"), result.selected());
        assertTrue(result.brief().contains("pods_top"), "Brief must contain the suggested method");
        assertEquals(List.of("kubernetes"), result.technologies());
        assertTrue(result.inferenceMs() >= 0);
        assertEquals(120, result.inputTokens());
        assertEquals(40, result.outputTokens());
    }

    @Test
    void runReasoningSelectBrief_withMarkdownFences_stripsAndParses() {
        String llmResponse = """
                ```json
                {"selected": ["proxmox-agent"],
                 "brief": "Check node resource usage via proxmox-list-nodes.",
                 "technologies": ["proxmox"]}
                ```
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 80, 30));

        var state = new DiscussionOrchestrator.ThreadState("thread-2");
        state.userQuery = "How many Proxmox nodes are there?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "k8s-workloads", "proxmox-agent"));

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result);
        assertEquals(List.of("proxmox-agent"), result.selected());
        assertEquals(List.of("proxmox"), result.technologies());
    }

    // --- Missing/empty "selected" triggers fallback ---

    @Test
    void runReasoningSelectBrief_emptySelectedArray_returnsResultWithEmptyList() {
        // Empty selected is valid JSON and returns a result; the CALLER checks
        // result.selected().isEmpty() to decide whether to trigger the fallback.
        String llmResponse = """
                {"selected": [],
                 "brief": "This is answerable from general knowledge.",
                 "technologies": []}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 50, 20));

        var state = new DiscussionOrchestrator.ThreadState("thread-3");
        state.userQuery = "What is the OSI model?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "k8s-workloads", "proxmox-agent"));

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result);
        assertTrue(result.selected().isEmpty(), "Empty selected list must be preserved for caller to handle");
    }

    @Test
    void runReasoningSelectBrief_missingSelectedField_returnsResultWithEmptyList() {
        // LLM returned JSON without "selected" - caller sees empty list and falls back.
        String llmResponse = """
                {"brief": "Some brief without a selected field.",
                 "technologies": ["kubernetes"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 60, 25));

        var state = new DiscussionOrchestrator.ThreadState("thread-4");
        state.userQuery = "List all pods";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "k8s-workloads", "proxmox-agent"));

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result);
        assertTrue(result.selected().isEmpty(),
                "Missing selected field must yield empty list so caller falls back to similarity");
    }

    // --- Malformed JSON triggers fallback (null return) ---

    @Test
    void runReasoningSelectBrief_malformedJson_returnsNull() {
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult("not valid json at all", 40, 10));

        var state = new DiscussionOrchestrator.ThreadState("thread-5");
        state.userQuery = "List namespaces";

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNull(result, "Malformed JSON must return null so caller falls back to similarity");
    }

    @Test
    void runReasoningSelectBrief_emptyResponse_returnsNull() {
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult("", 0, 0));

        var state = new DiscussionOrchestrator.ThreadState("thread-6");
        state.userQuery = "List namespaces";

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNull(result, "Empty LLM response must return null so caller falls back");
    }

    @Test
    void runReasoningSelectBrief_nullResponse_returnsNull() {
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(null, 0, 0));

        var state = new DiscussionOrchestrator.ThreadState("thread-7");
        state.userQuery = "List namespaces";

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNull(result, "Null LLM response must return null so caller falls back");
    }

    // --- Hallucinated agent names are dropped ---

    @Test
    void runReasoningSelectBrief_hallucinatedAgentNames_areDropped() {
        String llmResponse = """
                {"selected": ["k8s-metrics", "hallucinated-agent", "k8s-workloads"],
                 "brief": "Check metrics and workloads.",
                 "technologies": ["kubernetes"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 100, 35));

        var state = new DiscussionOrchestrator.ThreadState("thread-8");
        state.userQuery = "What pods are running?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "k8s-workloads", "proxmox-agent"));

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result);
        assertEquals(List.of("k8s-metrics", "k8s-workloads"), result.selected(),
                "Hallucinated agent name must be dropped from selected list");
    }

    @Test
    void runReasoningSelectBrief_allNamesHallucinated_returnsEmptySelected() {
        // All names hallucinated -> selected is empty -> caller falls back to similarity.
        String llmResponse = """
                {"selected": ["ghost-agent", "phantom-tooler"],
                 "brief": "Some brief.",
                 "technologies": ["kubernetes"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 70, 25));

        var state = new DiscussionOrchestrator.ThreadState("thread-9");
        state.userQuery = "What pods are running?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "k8s-workloads", "proxmox-agent"));

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result);
        assertTrue(result.selected().isEmpty(),
                "All-hallucinated selection must yield empty list so fallback triggers");
    }

    @Test
    void runReasoningSelectBrief_noKnownAgentNames_skipsValidation() {
        // When knownAgentNames is empty (resumes not loaded yet), validation is skipped.
        String llmResponse = """
                {"selected": ["k8s-metrics"],
                 "brief": "Check metrics.",
                 "technologies": ["kubernetes"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 60, 20));

        var state = new DiscussionOrchestrator.ThreadState("thread-10");
        state.userQuery = "What is CPU usage?";
        // Intentionally skip seeding the known agent names so the set stays empty and validation is bypassed.

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result);
        assertEquals(List.of("k8s-metrics"), result.selected(),
                "Without known names for validation, all selected names are kept");
    }

    // --- Channel classification still works ---

    @Test
    void classifyChannel_fromReasoningTechnologies_matchesConfiguredChannel() {
        // Verifies that the technologies list from the reasoning result flows through
        // to classifyChannelFromAdvisory correctly.
        // Channel "kubernetes" is configured in setUp; technology "kubernetes" should match.
        String channel = orchestrator.classifyChannelFromAdvisory(List.of("kubernetes"));
        assertEquals("kubernetes", channel);
    }

    @Test
    void classifyChannel_fromProxmoxTechnology_matchesProxmoxChannel() {
        String channel = orchestrator.classifyChannelFromAdvisory(List.of("proxmox"));
        assertEquals("proxmox", channel);
    }

    @Test
    void classifyChannel_emptyTechnologies_defaultsToGeneral() {
        String channel = orchestrator.classifyChannelFromAdvisory(List.of());
        assertEquals("general", channel);
    }

    @Test
    void classifyChannel_unknownTechnology_defaultsToGeneral() {
        String channel = orchestrator.classifyChannelFromAdvisory(List.of("rabbitmq"));
        assertEquals("general", channel);
    }

    // --- Brief content contract: additive (not prohibitive) ---

    @Test
    void runReasoningSelectBrief_briefFieldPresent_isIncludedInResult() {
        // The brief must be surfaced as-is (the LLM is instructed to be additive;
        // this test verifies the parse path, not the LLM's prose).
        String expectedBrief =
                "Check per-namespace usage via pods_top, then review replica headroom in each deployment.";
        String llmResponse = String.format(
                "{\"selected\":[\"k8s-metrics\"],\"brief\":\"%s\",\"technologies\":[\"kubernetes\"]}",
                expectedBrief);
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 90, 30));

        var state = new DiscussionOrchestrator.ThreadState("thread-11");
        state.userQuery = "Is it safe to restart?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "k8s-workloads", "proxmox-agent"));

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result);
        assertEquals(expectedBrief, result.brief());
    }

    @Test
    void runReasoningSelectBrief_missingBriefField_defaultsToEmptyString() {
        String llmResponse = """
                {"selected": ["k8s-metrics"], "technologies": ["kubernetes"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 60, 20));

        var state = new DiscussionOrchestrator.ThreadState("thread-12");
        state.userQuery = "List pods";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "k8s-workloads", "proxmox-agent"));

        var result = orchestrator.runReasoningSelectBrief(state, SAMPLE_RESUMES);

        assertNotNull(result);
        assertEquals("", result.brief(), "Missing brief field must default to empty string, not null");
    }

    // --- compactCatalog: lean catalog for reasoning, bulky prompt dropped ---

    @Test
    void compactCatalog_keepsNameDescriptionTools_dropsPrompt() {
        String fullCatalog = """
                [{"name":"k8s-metrics","description":"Kubernetes metrics tooler",
                  "keywords":["top","cpu"],"tools":["pods_top","resources_list"],
                  "role":"tooler","prompt":"a very long per-agent prompt that must not appear"},
                 {"name":"internet-search","description":"Web search","tools":["search"],
                  "role":"researcher","prompt":"another long prompt"}]
                """;
        String compact = orchestrator.compactCatalog(fullCatalog);

        assertNotNull(compact);
        assertTrue(compact.contains("k8s-metrics"), "name kept");
        assertTrue(compact.contains("Kubernetes metrics tooler"), "description kept");
        assertTrue(compact.contains("pods_top"), "tools kept");
        assertTrue(compact.contains("[researcher]"), "non-tooler role surfaced");
        assertFalse(compact.contains("long per-agent prompt"), "bulky prompt must be dropped");
        assertFalse(compact.contains("keywords"), "keywords must be dropped");
    }

    @Test
    void compactCatalog_nullOrNonArray_returnsNull() {
        assertNull(orchestrator.compactCatalog(null));
        assertNull(orchestrator.compactCatalog("   "));
        assertNull(orchestrator.compactCatalog("{\"not\":\"an array\"}"));
        assertNull(orchestrator.compactCatalog("not json at all"));
    }

    // --- Answer-directly: empty selection is honored, not treated as failure ---
    // (Coordinator Empty Selection Treated As Failure)

    @Test
    void shouldAnswerDirectly_emptySelectionWithBrief_isTrue() {
        // The coordinator deliberately selected no toolers and answered in its brief.
        var r = new DiscussionOrchestrator.ReasoningResult(
                List.of(), "The capital of France is Paris.", List.of(), 10, 1, 1);
        assertTrue(DiscussionOrchestrator.shouldAnswerDirectly(r),
                "empty selection + a real brief = answer directly, no toolers woken");
    }

    @Test
    void shouldAnswerDirectly_falseForSelectionFailureOrUnusable() {
        // Toolers were selected -> NOT answer-directly (they run).
        assertFalse(DiscussionOrchestrator.shouldAnswerDirectly(
                new DiscussionOrchestrator.ReasoningResult(List.of("k8s-metrics"), "brief", List.of(), 10, 1, 1)));
        // Empty selection but no brief -> unusable, must fall back (not answer empty).
        assertFalse(DiscussionOrchestrator.shouldAnswerDirectly(
                new DiscussionOrchestrator.ReasoningResult(List.of(), "  ", List.of(), 10, 1, 1)));
        // Reasoning failed entirely -> fall back, never answer directly.
        assertFalse(DiscussionOrchestrator.shouldAnswerDirectly(null));
    }

    @Test
    void shouldAnswerDirectly_gated_offForSingleSpecialistCrews() {
        // Would answer directly on its own (empty selection + brief)...
        var r = new DiscussionOrchestrator.ReasoningResult(
                List.of(), "The capital of France is Paris.", List.of(), 10, 1, 1);
        assertTrue(DiscussionOrchestrator.shouldAnswerDirectly(r, true),
                "enabled (pilot crews): honor answer-directly");
        // ...but with the flag OFF (fitness judge) it NEVER answers directly - it must
        // fall through to selection and delegate to its specialist.
        assertFalse(DiscussionOrchestrator.shouldAnswerDirectly(r, false),
                "disabled (single-specialist judge crew): never answer directly");
        // Disabled also stays false for the cases that were already false.
        assertFalse(DiscussionOrchestrator.shouldAnswerDirectly(null, false));
        assertFalse(DiscussionOrchestrator.shouldAnswerDirectly(null, true));
    }

    @Test
    void answerDirectly_closesThreadWithBriefAsSynthesis_wakingNoToolers() {
        // The predicate gate is true; this exercises the ACTION it guards: the brief
        // becomes the synthesis answer, the channel is classified from technologies, the
        // thread closes, and the user's pending result is completed with the brief - all
        // without selecting/waking any tooler or making a second LLM call.
        // (natsProvider is a mock -> getConnection() returns null -> publishes no-op safely.)
        var state = new DiscussionOrchestrator.ThreadState("thread-direct");
        state.userQuery = "What is the capital of France?";
        var future = orchestrator.registerPendingForTest(state.threadId);

        var reasoning = new DiscussionOrchestrator.ReasoningResult(
                List.of(), "The capital of France is Paris.", List.of("kubernetes"), 42, 5, 3);

        orchestrator.answerDirectly(state, reasoning);

        assertEquals(DiscussionOrchestrator.Phase.CLOSED, state.phase,
                "answer-directly must close the thread, not leave it evaluating");
        assertEquals("The capital of France is Paris.", state.advisoryContent,
                "the coordinator's brief is preserved as the advisory content");
        assertEquals("kubernetes", state.primaryChannel,
                "channel is classified from the reasoning technologies");
        assertNull(state.innerCircle, "no subcommittee is formed - no toolers are woken");
        assertTrue(future.isDone(), "the user's pending result must be completed");
        assertEquals("The capital of France is Paris.", future.getNow(null),
                "the brief is returned to the user verbatim as the synthesis");
    }

    // --- Analyst subcommittee scoping (Coordinator Domain-Scoped Analyst Subcommittee) ---

    @Test
    void analystNamesFromResumes_returnsOnlyAnalystRoleAgents() {
        String resumes = """
                [{"name":"k8s-metrics","role":"tooler","description":"metrics"},
                 {"name":"scheduler-advisor","role":"analyst","description":"reviews scheduling"},
                 {"name":"obs-advisor","role":"analyst","description":"reviews observability"},
                 {"name":"internet-search","role":"researcher","description":"web"},
                 {"name":"k8s-workloads","description":"no role field = tooler default"}]
                """;
        var analysts = orchestrator.analystNamesFromResumes(resumes);

        assertEquals(2, analysts.size(), "only analyst-role agents counted");
        assertTrue(analysts.contains("scheduler-advisor"));
        assertTrue(analysts.contains("obs-advisor"));
        assertFalse(analysts.contains("k8s-metrics"), "tooler excluded");
        assertFalse(analysts.contains("internet-search"), "researcher excluded");
        assertFalse(analysts.contains("k8s-workloads"), "role-less (tooler) excluded");
    }

    @Test
    void analystNamesFromResumes_emptyOrMalformed_returnsEmptySet() {
        assertTrue(orchestrator.analystNamesFromResumes(null).isEmpty());
        assertTrue(orchestrator.analystNamesFromResumes("   ").isEmpty());
        assertTrue(orchestrator.analystNamesFromResumes("not json").isEmpty());
        assertTrue(orchestrator.analystNamesFromResumes("[]").isEmpty());
    }

    @Test
    void analystCircleFrom_keepsOnlyAnalystsFromTheSelectedSubcommittee() {
        var innerCircle = new java.util.LinkedHashSet<>(List.of("k8s-metrics", "scheduler-advisor", "k8s-workloads"));
        var analystNames = java.util.Set.of("scheduler-advisor", "obs-advisor");

        var circle = DiscussionOrchestrator.analystCircleFrom(innerCircle, analystNames);

        assertEquals(List.of("scheduler-advisor"), circle,
                "only the selected agents that are analysts are kept");
    }

    @Test
    void analystCircleFrom_toolersOnlyOrNoSelection_returnsEmpty() {
        var analystNames = java.util.Set.of("scheduler-advisor");
        // innerCircle has only toolers -> no analysts to scope -> empty (review broadcasts)
        assertTrue(DiscussionOrchestrator.analystCircleFrom(
                java.util.Set.of("k8s-metrics", "k8s-workloads"), analystNames).isEmpty());
        // null/empty innerCircle (selection failed / broadcast) -> empty
        assertTrue(DiscussionOrchestrator.analystCircleFrom(null, analystNames).isEmpty());
        assertTrue(DiscussionOrchestrator.analystCircleFrom(java.util.Set.of(), analystNames).isEmpty());
        // no analysts in crew -> empty
        assertTrue(DiscussionOrchestrator.analystCircleFrom(java.util.Set.of("scheduler-advisor"), java.util.Set.of()).isEmpty());
    }

    // --- Phase transition table (Discussion Phase Lifecycle as Explicit State Machine) ---

    // The phase machine is a declarative table: exactly ADVISORY, EVALUATING, and
    // REVIEW auto-advance; SUBMITTED/PAUSED/SYNTHESIZING/CLOSED have no automatic
    // edge (event-driven or terminal). Locking the table guards against a future
    // edit silently dropping a phase's transition (the analyst-drop class of bug).
    @Test
    void autoTransitionTable_coversOnlyTheAutoAdvancingPhases() {
        var phases = orchestrator.autoTransitionPhases();
        assertEquals(
                java.util.Set.of(DiscussionOrchestrator.Phase.ADVISORY,
                        DiscussionOrchestrator.Phase.EVALUATING,
                        DiscussionOrchestrator.Phase.CONCURRING,
                        DiscussionOrchestrator.Phase.REVIEW),
                java.util.Set.copyOf(phases),
                "exactly ADVISORY, EVALUATING, CONCURRING, REVIEW auto-advance; DECIDING advances on its "
                        + "decision, and terminal/paused phases have no auto edge");
    }

    // --- Hard-ceiling watchdog: a discussion must never hang ---
    // (Discussion Request Pump Can Wedge a Crew)

    @Test
    void hardCeiling_exceedsTheSumOfPhaseBudgets_soItNeverTripsAHealthyRun() {
        // 120 cold-grace + 10 advisory + 15 eval + 15 review + 90 synth + 180 margin.
        long ceiling = orchestrator.hardCeilingSeconds();
        assertTrue(ceiling >= 120 + 10 + 15 + 15 + 90,
                "ceiling must sit above the phase budgets + cold-start grace");
        assertEquals(120 + 10 + 15 + 15 + 90 + 180, ceiling,
                "ceiling = coldGrace + advisory + eval + review + synth + margin");
    }

    @Test
    void forceClose_overCeilingStuckThread_closesAndCompletesTheFuture() {
        // A thread stuck in SYNTHESIZING (a phase with NO auto edge) past the ceiling
        // must be force-closed so the caller's future completes and the pump unblocks.
        var state = new DiscussionOrchestrator.ThreadState("thread-stuck");
        state.userQuery = "anything";
        state.phase = DiscussionOrchestrator.Phase.SYNTHESIZING;
        var future = orchestrator.registerPendingForTest(state.threadId);

        var now = state.threadCreated.plusSeconds(orchestrator.hardCeilingSeconds() + 1);
        boolean handled = orchestrator.forceCloseIfOverCeiling(state, now);

        assertTrue(handled, "an over-ceiling thread is handled (force-closed) this tick");
        assertEquals(DiscussionOrchestrator.Phase.CLOSED, state.phase, "thread must be CLOSED");
        assertTrue(future.isDone(), "the caller's pending future must be completed, never left hanging");
        assertNotNull(future.getNow(null), "a terminal response is delivered, not null");
    }

    @Test
    void forceClose_underCeiling_leavesTheThreadRunning() {
        var state = new DiscussionOrchestrator.ThreadState("thread-young");
        state.phase = DiscussionOrchestrator.Phase.EVALUATING;
        var future = orchestrator.registerPendingForTest(state.threadId);

        var now = state.threadCreated.plusSeconds(5); // well under the ceiling
        boolean handled = orchestrator.forceCloseIfOverCeiling(state, now);

        assertFalse(handled, "a young thread is left for the normal per-phase settles");
        assertEquals(DiscussionOrchestrator.Phase.EVALUATING, state.phase, "phase untouched");
        assertFalse(future.isDone(), "future must not be completed early");
    }

    @Test
    void forceClose_closedOrPaused_isNoOp() {
        var closed = new DiscussionOrchestrator.ThreadState("thread-closed");
        closed.phase = DiscussionOrchestrator.Phase.CLOSED;
        // Already terminal: reported handled, nothing to do.
        assertTrue(orchestrator.forceCloseIfOverCeiling(closed,
                closed.threadCreated.plusSeconds(orchestrator.hardCeilingSeconds() + 100)));

        var paused = new DiscussionOrchestrator.ThreadState("thread-paused");
        paused.phase = DiscussionOrchestrator.Phase.PAUSED;
        // A deliberate pause is never force-closed by the watchdog, even past the ceiling.
        assertFalse(orchestrator.forceCloseIfOverCeiling(paused,
                paused.threadCreated.plusSeconds(orchestrator.hardCeilingSeconds() + 100)));
        assertEquals(DiscussionOrchestrator.Phase.PAUSED, paused.phase, "paused stays paused");
    }

    @Test
    void forceCloseZombie_whenAnotherSweepOwnsIt_returnsFalseAndEvictsNothing() {
        // CAS fails (transitioning already held by a concurrent sweep): the zombie is
        // left for a later pass and no eviction is recorded.
        var state = new DiscussionOrchestrator.ThreadState("zombie-owned");
        state.phase = DiscussionOrchestrator.Phase.EVALUATING;
        state.transitioning.set(true); // a sweep owns it
        var evicted = new java.util.ArrayList<String>();

        boolean removed = orchestrator.forceCloseZombie(state, 9_999L, evicted);

        assertFalse(removed, "CAS-lost zombie is left in place this pass");
        assertTrue(evicted.isEmpty(), "no eviction recorded when a sweep owns the thread");
        assertTrue(state.transitioning.get(), "the owning sweep's flag is left untouched");
    }

    @Test
    void forceCloseZombie_alreadyClosedUnderCas_evictsExactlyOnce() {
        // CAS succeeds but the phase is already CLOSED (another sweep closed it): record
        // the single eviction and remove — guards the "not a double-add" invariant.
        var state = new DiscussionOrchestrator.ThreadState("zombie-closed");
        state.phase = DiscussionOrchestrator.Phase.CLOSED;
        var evicted = new java.util.ArrayList<String>();

        boolean removed = orchestrator.forceCloseZombie(state, 9_999L, evicted);

        assertTrue(removed, "an already-closed zombie is removed");
        assertEquals(java.util.List.of("zombie-closed"), evicted,
                "exactly one eviction record, never a double-add");
        assertFalse(state.transitioning.get(), "transitioning released on exit");
    }

    @Test
    void forceCloseZombie_liveZombie_closesCompletesFutureAndEvictsOnce() {
        // CAS succeeds, phase still live: close the thread, complete its pending future,
        // record the single eviction, release the CAS.
        var state = new DiscussionOrchestrator.ThreadState("zombie-live");
        state.userQuery = "anything";
        state.phase = DiscussionOrchestrator.Phase.SYNTHESIZING;
        var future = orchestrator.registerPendingForTest(state.threadId);
        var evicted = new java.util.ArrayList<String>();

        boolean removed = orchestrator.forceCloseZombie(state, 9_999L, evicted);

        assertTrue(removed, "a live zombie is force-closed and removed");
        assertEquals(DiscussionOrchestrator.Phase.CLOSED, state.phase, "phase set to CLOSED");
        assertEquals(java.util.List.of("zombie-live"), evicted, "exactly one eviction record");
        assertFalse(state.transitioning.get(), "transitioning released on exit");
        assertTrue(future.isDone(), "the caller's pending future is completed, not left hanging");
    }

    // --- Provenance: crew name + crew chart version in thread_start metadata ---
    // (Record Crew Name and Version in Discussion Thread)

    @Test
    void threadStartMetadata_includesCrewNameAndVersion_whenVersionPresent() {
        when(properties.crewVersion()).thenReturn(Optional.of("1.4.2"));

        var md = orchestrator.buildThreadStartMetadata(
                "q", "general", "conv-1", List.of(), "homelab-pilot", "thread-x");

        assertEquals("homelab-pilot", md.get("crew"), "crew name recorded for provenance");
        assertEquals("1.4.2", md.get("crewVersion"),
                "crew chart version recorded so a thread is attributable to a crew version");
    }

    @Test
    void threadStartMetadata_omitsCrewVersion_whenAbsent() {
        // Hand-applied crew with no chart version (Mockito returns Optional.empty()).
        when(properties.crewVersion()).thenReturn(Optional.empty());

        var md = orchestrator.buildThreadStartMetadata(
                "q", "general", "conv-1", List.of(), "homelab-pilot", "thread-y");

        assertEquals("homelab-pilot", md.get("crew"));
        assertFalse(md.containsKey("crewVersion"), "no version key when the crew carries no chart version");
    }
}
