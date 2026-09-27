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
 * Tests for Skills chunk 3: coordinator skill catalog, selection, and propagation.
 *
 * Covers:
 * - compactCatalog: skills appear in a distinct "Skills available:" section.
 * - compactCatalog: zero skills leaves the catalog unchanged from baseline.
 * - runReasoningSelectBrief: reads the optional "skills" JSON field.
 * - runReasoningSelectBrief: validates skill names against knownSkillNames.
 * - runReasoningSelectBrief: unknown skill names are dropped (hallucination guard).
 * - advisory_ready metadata: carries selectedSkills when present; omits when absent.
 * - ResumeSearchClient vector path: skill docs (no agent_name) are skipped.
 *
 * Plain JUnit 5, no @QuarkusTest, no Ollama.
 */
class DiscussionOrchestratorSkillsTest {

    private DiscussionOrchestrator orchestrator;
    private ChatService chatService;
    private NatsConnectionProvider natsProvider;

    // Combined crew catalog: two agents + two skills
    private static final String MIXED_CATALOG_JSON = """
            [{"name":"k8s-metrics","description":"Kubernetes metrics tooler","role":"tooler",
              "tools":["pods_top","resources_list"]},
             {"name":"proxmox-agent","description":"Proxmox hypervisor tooler","role":"tooler",
              "tools":["proxmox-list-nodes"]},
             {"name":"k8s-runbook","description":"Kubernetes runbook for safe restarts",
              "kind":"skill","order":10},
             {"name":"gpu-sop","description":"GPU scheduling standard operating procedure",
              "kind":"skill","order":20}]
            """;

    // Agent-only catalog (no skills) - must be byte-identical before/after skill support
    private static final String AGENTS_ONLY_JSON = """
            [{"name":"k8s-metrics","description":"Kubernetes metrics tooler","role":"tooler",
              "tools":["pods_top","resources_list"]},
             {"name":"proxmox-agent","description":"Proxmox hypervisor tooler","role":"tooler",
              "tools":["proxmox-list-nodes"]}]
            """;

    @BeforeEach
    void setUp() {
        natsProvider = mock(NatsConnectionProvider.class);
        chatService = mock(ChatService.class);
        var resumeSearchClient = mock(ResumeSearchClient.class);
        var properties = mock(AgentProperties.class);
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

    // =========================================================================
    // 1. Catalog: skills section appears when skill entries are present
    // =========================================================================

    @Test
    void compactCatalog_mixedCatalog_hasDistinctSkillsSection() {
        String compact = orchestrator.compactCatalog(MIXED_CATALOG_JSON);

        assertNotNull(compact);
        assertTrue(compact.contains("k8s-metrics"), "agent name present");
        assertTrue(compact.contains("proxmox-agent"), "agent name present");
        assertTrue(compact.contains("Skills available:"), "skills section header present");
        assertTrue(compact.contains("k8s-runbook"), "skill name present");
        assertTrue(compact.contains("gpu-sop"), "skill name present");
        assertTrue(compact.contains("Kubernetes runbook"), "skill description present");
        int agentPos = compact.indexOf("k8s-metrics");
        int headerPos = compact.indexOf("Skills available:");
        assertTrue(agentPos < headerPos, "agent lines come before skills section");
    }

    @Test
    void compactCatalog_preFiltered_keepsOnlyCandidateAgentsButAllSkills() {
        // The RAG pre-filter narrows the catalog: only agents in the candidate set are
        // rendered, so the coordinator reasons over the relevant few (not all). Skills
        // are NOT agents the pre-filter ranks, so they are always kept.
        String compact = orchestrator.compactCatalog(MIXED_CATALOG_JSON, java.util.Set.of("k8s-metrics"));

        assertNotNull(compact);
        assertTrue(compact.contains("k8s-metrics"), "the candidate agent is kept");
        assertFalse(compact.contains("proxmox-agent"),
                "a non-candidate agent is dropped from the narrowed catalog");
        assertTrue(compact.contains("k8s-runbook") && compact.contains("gpu-sop"),
                "skills are always kept, regardless of the agent pre-filter");
    }

    @Test
    void compactCatalog_nullKeepAgents_keepsAllAgents() {
        // A null candidate set means no pre-filter (resume search unavailable): the
        // full catalog is the fallback, byte-identical to the single-arg overload.
        assertEquals(orchestrator.compactCatalog(MIXED_CATALOG_JSON),
                orchestrator.compactCatalog(MIXED_CATALOG_JSON, null),
                "null keepAgents == full catalog (the single-arg overload)");
    }

    @Test
    void buildAdvisoryCatalog_withCandidates_narrowsToThePreFilteredSet() {
        // The decision the coordinator's selection rests on: a non-null candidate list
        // (the resume pre-filter ranked some) narrows the catalog to exactly those agents.
        String catalog = orchestrator.buildAdvisoryCatalog(MIXED_CATALOG_JSON,
                java.util.List.of("k8s-metrics"));

        assertNotNull(catalog);
        assertTrue(catalog.contains("k8s-metrics"), "the candidate agent reaches the coordinator");
        assertFalse(catalog.contains("proxmox-agent"),
                "a non-candidate agent is NOT in the catalog the coordinator reasons over");
    }

    @Test
    void buildAdvisoryCatalog_nullCandidates_widensToFullCatalog() {
        // The resume-search-unavailable path: null candidates -> the full crew catalog,
        // byte-identical to compacting without a filter.
        assertEquals(orchestrator.compactCatalog(MIXED_CATALOG_JSON),
                orchestrator.buildAdvisoryCatalog(MIXED_CATALOG_JSON, null),
                "null candidates widens to the full catalog");
    }

    @Test
    void inlineArtifactContent_noMarker_returnsUnchangedWithoutReadingNats() {
        String text = "k8s-config agree: found 28 namespaces with their labels";
        assertEquals(text, orchestrator.inlineArtifactContent(text));
        verifyNoInteractions(natsProvider);
    }

    @Test
    void inlineArtifactContent_replacesMarkerWithArtifactContentFromObjectStore() throws Exception {
        // The fix: a marker-only contribution is filled in with the artifact's real
        // content read from the object store, so the tool-free synthesizer works over
        // the full data instead of an empty reference.
        var conn = mock(io.nats.client.Connection.class);
        var os = mock(io.nats.client.ObjectStore.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(conn.objectStore("kubemoot_discussion_artifacts")).thenReturn(os);
        String fullData = "namespaces: arc-runners, cert-manager, harbor, sonarqube, ... (28 total)";
        doAnswer(inv -> {
            ((java.io.OutputStream) inv.getArgument(1))
                    .write(fullData.getBytes(java.nio.charset.StandardCharsets.UTF_8));
            return null;
        }).when(os).get(anyString(), any(java.io.OutputStream.class));

        String key = "ns-a/homelab-pilot/t1/k8s-config/agree-abc";
        String input = "k8s-config agree: [ARTIFACT key=" + key
                + " bytes=64 - the FULL data is in the file /artifacts/" + key + "]";
        String out = orchestrator.inlineArtifactContent(input);

        assertTrue(out.contains(fullData), "the marker is replaced with the artifact content: " + out);
        assertFalse(out.contains("[ARTIFACT key="), "the marker is gone once the content is read in");
    }

    @Test
    void compactCatalog_skillsNotRenderedAsAgents() {
        String compact = orchestrator.compactCatalog(MIXED_CATALOG_JSON);

        assertNotNull(compact);
        int headerPos = compact.indexOf("Skills available:");
        String agentSection = compact.substring(0, headerPos);
        assertFalse(agentSection.contains("k8s-runbook"),
                "skill name must not appear in agent section");
        assertFalse(agentSection.contains("gpu-sop"),
                "skill name must not appear in agent section");
    }

    @Test
    void compactCatalog_zeroSkills_catalogByteIdenticalToBaseline() {
        String withSkills = orchestrator.compactCatalog(MIXED_CATALOG_JSON);
        String withoutSkills = orchestrator.compactCatalog(AGENTS_ONLY_JSON);

        assertNotNull(withoutSkills);
        assertNotNull(withSkills);
        assertFalse(withoutSkills.contains("Skills available:"),
                "catalog with no skill entries must not contain skills section header");
        assertTrue(withSkills.startsWith(withoutSkills.trim()),
                "agent section of mixed catalog must match the agents-only catalog");
    }

    @Test
    void compactCatalog_skillsOnly_hasSkillsSectionNoAgentLines() {
        String skillsOnly = """
                [{"name":"k8s-runbook","description":"Kubernetes runbook","kind":"skill","order":10}]
                """;
        String compact = orchestrator.compactCatalog(skillsOnly);

        assertNotNull(compact);
        assertTrue(compact.contains("Skills available:"), "skills header present even when no agents");
        assertTrue(compact.contains("k8s-runbook"), "skill name present");
    }

    // =========================================================================
    // 2. Selection parsing: coordinator JSON may include optional "skills" array
    // =========================================================================

    @Test
    void runReasoningSelectBrief_withSkillsField_parsesAndStoresOnState() {
        String llmResponse = """
                {"selected": ["k8s-metrics"],
                 "brief": "Check metrics then consult the runbook.",
                 "technologies": ["kubernetes"],
                 "skills": ["k8s-runbook"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 100, 40));

        var state = new DiscussionOrchestrator.ThreadState("thread-s1");
        state.userQuery = "Is it safe to restart the deployment?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "proxmox-agent"));
        orchestrator.loadKnownSkillNamesForTest(List.of("k8s-runbook", "gpu-sop"));

        var result = orchestrator.runReasoningSelectBrief(state, MIXED_CATALOG_JSON);

        assertNotNull(result);
        assertEquals(List.of("k8s-metrics"), result.selected());
        assertNotNull(state.selectedSkills, "selectedSkills must be stored on state");
        assertTrue(state.selectedSkills.contains("k8s-runbook"), "selected skill stored");
        assertEquals(1, state.selectedSkills.size());
    }

    @Test
    void runReasoningSelectBrief_absentSkillsField_stateHasNoSelectedSkills() {
        String llmResponse = """
                {"selected": ["k8s-metrics"],
                 "brief": "Check metrics.",
                 "technologies": ["kubernetes"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 80, 30));

        var state = new DiscussionOrchestrator.ThreadState("thread-s2");
        state.userQuery = "What pods are running?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics"));
        orchestrator.loadKnownSkillNamesForTest(List.of("k8s-runbook"));

        orchestrator.runReasoningSelectBrief(state, MIXED_CATALOG_JSON);

        assertNull(state.selectedSkills, "absent skills field leaves selectedSkills null (baseline unchanged)");
    }

    @Test
    void runReasoningSelectBrief_emptySkillsArray_stateHasNoSelectedSkills() {
        String llmResponse = """
                {"selected": ["k8s-metrics"],
                 "brief": "Check metrics.",
                 "technologies": ["kubernetes"],
                 "skills": []}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 80, 30));

        var state = new DiscussionOrchestrator.ThreadState("thread-s3");
        state.userQuery = "What pods are running?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics"));
        orchestrator.loadKnownSkillNamesForTest(List.of("k8s-runbook"));

        orchestrator.runReasoningSelectBrief(state, MIXED_CATALOG_JSON);

        assertNull(state.selectedSkills, "empty skills array must not set selectedSkills (baseline unchanged)");
    }

    @Test
    void runReasoningSelectBrief_multipleSkills_allStoredOnState() {
        String llmResponse = """
                {"selected": ["k8s-metrics", "proxmox-agent"],
                 "brief": "Check metrics and GPU load.",
                 "technologies": ["kubernetes", "proxmox"],
                 "skills": ["k8s-runbook", "gpu-sop"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 120, 50));

        var state = new DiscussionOrchestrator.ThreadState("thread-s4");
        state.userQuery = "Restart and GPU check?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics", "proxmox-agent"));
        orchestrator.loadKnownSkillNamesForTest(List.of("k8s-runbook", "gpu-sop"));

        orchestrator.runReasoningSelectBrief(state, MIXED_CATALOG_JSON);

        assertNotNull(state.selectedSkills);
        assertEquals(2, state.selectedSkills.size());
        assertTrue(state.selectedSkills.contains("k8s-runbook"));
        assertTrue(state.selectedSkills.contains("gpu-sop"));
    }

    // =========================================================================
    // 3. Skill name validation: hallucinated skill names are dropped
    // =========================================================================

    @Test
    void runReasoningSelectBrief_hallucinatedSkillName_isDropped() {
        String llmResponse = """
                {"selected": ["k8s-metrics"],
                 "brief": "Check metrics.",
                 "technologies": ["kubernetes"],
                 "skills": ["k8s-runbook", "invented-skill-that-does-not-exist"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 90, 35));

        var state = new DiscussionOrchestrator.ThreadState("thread-s5");
        state.userQuery = "Safe restart?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics"));
        orchestrator.loadKnownSkillNamesForTest(List.of("k8s-runbook", "gpu-sop"));

        orchestrator.runReasoningSelectBrief(state, MIXED_CATALOG_JSON);

        assertNotNull(state.selectedSkills);
        assertEquals(1, state.selectedSkills.size(), "hallucinated skill name must be dropped");
        assertTrue(state.selectedSkills.contains("k8s-runbook"), "valid skill name kept");
        assertFalse(state.selectedSkills.contains("invented-skill-that-does-not-exist"));
    }

    @Test
    void runReasoningSelectBrief_allSkillsHallucinated_stateHasNoSelectedSkills() {
        String llmResponse = """
                {"selected": ["k8s-metrics"],
                 "brief": "Check metrics.",
                 "technologies": ["kubernetes"],
                 "skills": ["ghost-skill", "phantom-runbook"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 80, 30));

        var state = new DiscussionOrchestrator.ThreadState("thread-s6");
        state.userQuery = "Any runbooks?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics"));
        orchestrator.loadKnownSkillNamesForTest(List.of("k8s-runbook", "gpu-sop"));

        orchestrator.runReasoningSelectBrief(state, MIXED_CATALOG_JSON);

        assertNull(state.selectedSkills,
                "all-hallucinated skills result in no selectedSkills on state (empty list not stored)");
    }

    @Test
    void runReasoningSelectBrief_noKnownSkillNames_skillValidationSkipped() {
        String llmResponse = """
                {"selected": ["k8s-metrics"],
                 "brief": "Check metrics.",
                 "technologies": ["kubernetes"],
                 "skills": ["any-skill-name"]}
                """;
        when(chatService.simpleLlmCallWithTokens(any(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(llmResponse, 70, 25));

        var state = new DiscussionOrchestrator.ThreadState("thread-s7");
        state.userQuery = "Check?";
        orchestrator.loadKnownAgentNamesForTest(List.of("k8s-metrics"));
        // Intentionally skip loadKnownSkillNamesForTest -> set stays empty -> validation bypassed

        orchestrator.runReasoningSelectBrief(state, MIXED_CATALOG_JSON);

        assertNotNull(state.selectedSkills, "without known names, skill is kept (no validation)");
        assertTrue(state.selectedSkills.contains("any-skill-name"));
    }

    // =========================================================================
    // 4. Propagation: isSkillNode and catalog helpers work correctly
    // =========================================================================

    @Test
    void isSkillNode_kindSkill_returnsTrue() throws Exception {
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        var node = mapper.readTree("{\"name\":\"k8s-runbook\",\"kind\":\"skill\",\"order\":10}");
        assertTrue(DiscussionOrchestrator.isSkillNode(node));
    }

    @Test
    void isSkillNode_noKindField_returnsFalse() throws Exception {
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        var node = mapper.readTree("{\"name\":\"k8s-metrics\",\"role\":\"tooler\"}");
        assertFalse(DiscussionOrchestrator.isSkillNode(node));
    }

    @Test
    void isSkillNode_differentKind_returnsFalse() throws Exception {
        var mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        var node = mapper.readTree("{\"name\":\"something\",\"kind\":\"template\"}");
        assertFalse(DiscussionOrchestrator.isSkillNode(node));
    }

    @Test
    void buildCatalogString_noSkills_returnsAgentSectionOnly() {
        var agentSb = new StringBuilder("- k8s-metrics: Kubernetes metrics tooler Tools: pods_top.\n");
        var skillSb = new StringBuilder();
        String result = DiscussionOrchestrator.buildCatalogString(agentSb, skillSb);
        assertEquals(agentSb.toString(), result, "no skills means output equals agent section unchanged");
        assertFalse(result.contains("Skills available:"), "no skills header when no skills");
    }

    @Test
    void buildCatalogString_withSkills_appendsSkillsSection() {
        var agentSb = new StringBuilder("- k8s-metrics: Kubernetes metrics tooler.\n");
        var skillSb = new StringBuilder("- k8s-runbook: Kubernetes runbook.\n");
        String result = DiscussionOrchestrator.buildCatalogString(agentSb, skillSb);
        assertTrue(result.contains("Skills available:"), "skills header present");
        assertTrue(result.contains("k8s-metrics"), "agent line present");
        assertTrue(result.contains("k8s-runbook"), "skill line present");
        assertTrue(result.indexOf("k8s-metrics") < result.indexOf("Skills available:"));
    }

    // =========================================================================
    // 5. Zero-skills guarantee: agents-only catalog unchanged
    // =========================================================================

    @Test
    void zeroSkills_compactCatalog_identicalToPreSkillBaseline() {
        String compact = orchestrator.compactCatalog(AGENTS_ONLY_JSON);

        assertNotNull(compact);
        assertFalse(compact.contains("Skills available:"),
                "zero-skills guarantee: no skills header in agents-only catalog");
        assertFalse(compact.contains("kind"), "no kind field leaked into output");
        assertTrue(compact.contains("k8s-metrics"));
        assertTrue(compact.contains("proxmox-agent"));
    }
}
