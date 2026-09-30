package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/**
 * The review decision point after EVALUATING: the crew's policy chooses concur,
 * full, or none; the runtime's guards escalate; a review always names its
 * analysts and never wakes every analyst in the crew.
 */
class DiscussionOrchestratorReviewDecisionTest {

    private static final String CATALOG = """
            [{"name":"k8s-config","role":"tooler","description":"config"},
             {"name":"k8s-advisor","role":"analyst","description":"kubernetes analyst"},
             {"name":"obs-advisor","role":"analyst","description":"observability analyst"},
             {"name":"compute","role":"analyst","description":"counts and sorts"},
             {"name":"proxmox-advisor","role":"analyst","description":"proxmox analyst"}]
            """;
    private static final List<String> RANKING =
            List.of("k8s-config", "k8s-advisor", "obs-advisor", "compute", "proxmox-advisor");
    private static final ObjectMapper JSON = new ObjectMapper();

    private final ChatService chatService = mock(ChatService.class);
    private final ResumeSearchClient resumeSearch = mock(ResumeSearchClient.class);
    private final Connection conn = mock(Connection.class);

    private DiscussionOrchestrator orchestrator(boolean reviewDecision, String tier) {
        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        var triageModel = mock(AgentProperties.TriageModel.class);
        var model = mock(AgentProperties.Model.class);
        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("coordinator");
        when(properties.triageModel()).thenReturn(triageModel);
        when(properties.model()).thenReturn(model);
        when(model.endpoint()).thenReturn("http://ollama:11434");
        when(model.model()).thenReturn("qwen3:32b");
        when(properties.systemPrompt()).thenReturn(Optional.of("coordinator policy"));
        when(properties.systemPromptFile()).thenReturn(Optional.empty());
        when(triageModel.timeoutSeconds()).thenReturn(120);
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("general"));
        when(discuss.settleSeconds()).thenReturn(5);
        when(discuss.minEvalSeconds()).thenReturn(20);
        when(discuss.minReviewSeconds()).thenReturn(10);
        when(discuss.evalGraceSeconds()).thenReturn(10);
        when(discuss.resumePreFilterTopK()).thenReturn(10);
        when(discuss.reviewDecision()).thenReturn(reviewDecision);
        when(discuss.reviewDecisionTier()).thenReturn(tier);
        var nats = mock(NatsConnectionProvider.class);
        when(nats.getConnection()).thenReturn(conn);
        when(nats.scope()).thenReturn(CrewScope.of("crew-ns", "pilot"));
        when(resumeSearch.searchResumes(anyString(), anyInt())).thenReturn(RANKING);
        var o = new DiscussionOrchestrator(nats, properties, chatService,
                new DiscussionMetrics(new SimpleMeterRegistry()), mock(LatencyTracker.class), resumeSearch);
        o.loadCrewResumesForTest(CATALOG);
        return o;
    }

    private static DiscussionOrchestrator.ThreadState decidingThread(String... selected) {
        var state = new DiscussionOrchestrator.ThreadState("t-decide");
        state.userQuery = "List the namespaces in the Kubernetes cluster.";
        state.crew = "pilot";
        state.innerCircle = ConcurrentHashMap.newKeySet();
        state.innerCircle.add("k8s-config");
        state.innerCircle.addAll(List.of(selected));
        state.agreeSignals.put("k8s-config", "default\nkube-system\nmonitoring");
        state.phase = DiscussionOrchestrator.Phase.DECIDING;
        return state;
    }

    private void decisionReturns(String json) {
        when(chatService.hasDistinctTriageModel()).thenReturn(true);
        when(chatService.triageChatWithTokens(anyString(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(json, 100, 10));
        when(chatService.simpleLlmCallWithTokens(anyString(), anyString()))
                .thenReturn(new ChatService.SimpleLlmResult(json, 100, 10));
    }

    /** Every message the coordinator published, in order. */
    private List<JsonNode> published() throws Exception {
        var captor = ArgumentCaptor.forClass(byte[].class);
        verify(conn, atLeast(0)).publish(anyString(), captor.capture());
        var out = new ArrayList<JsonNode>();
        for (byte[] b : captor.getAllValues()) {
            out.add(JSON.readTree(b));
        }
        return out;
    }

    private JsonNode reviewReady() throws Exception {
        return published().stream()
                .filter(m -> "review_ready".equals(m.path("messageType").asText()))
                .reduce((a, b) -> b).orElseThrow();
    }

    // ---- crews without the review decision ----

    @Test
    void withoutTheDecision_theFullReviewWakesTheSelectedAnalysts_withNoModelCall() throws Exception {
        var o = orchestrator(false, "fast");
        var state = decidingThread("k8s-advisor", "compute");

        o.decideReview(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state.phase);
        assertEquals(Set.of("compute", "k8s-advisor"), state.phaseRoster);
        assertEquals(Set.of("compute", "k8s-advisor"), state.pendingEvaluations.keySet(), "roster seeded");
        verifyNoInteractions(chatService);
        var ready = reviewReady();
        assertEquals("full", ready.path("metadata").path("reviewMode").asText());
        assertTrue(published().stream().noneMatch(m -> "review_decision".equals(m.path("messageType").asText())),
                "a crew without the decision publishes no decision");
    }

    @Test
    void withNoAnalystSelected_theReviewWakesOnlyTheBestMatch_neverEveryAnalyst() throws Exception {
        var o = orchestrator(false, "fast");
        var state = decidingThread();

        o.decideReview(state, 1);

        assertEquals(Set.of("k8s-advisor"), state.phaseRoster);
        var circle = reviewReady().path("metadata").path("innerCircle");
        assertEquals(1, circle.size(), "review_ready always names its analysts");
        assertEquals("k8s-advisor", circle.get(0).asText());
    }

    @Test
    void withNoAnalystSelectedAndNoRanking_theThreadSynthesizes() throws Exception {
        var o = orchestrator(false, "fast");
        when(resumeSearch.searchResumes(anyString(), anyInt())).thenReturn(null);
        var state = decidingThread();

        o.decideReview(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
        assertTrue(published().stream().noneMatch(m -> "review_ready".equals(m.path("messageType").asText())));
    }

    // ---- crews with the review decision ----

    @Test
    void concur_asksTheBestMatchingSelectedAnalyst_withTheConcurrenceRequest() throws Exception {
        var o = orchestrator(true, "fast");
        decisionReturns("{\"review\":\"concur\",\"reason\":\"the listing answers it\"}");
        var state = decidingThread("compute", "obs-advisor");

        o.decideReview(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.CONCURRING, state.phase);
        assertEquals("obs-advisor", state.concurrer, "obs-advisor ranks above compute for this question");
        assertEquals(Set.of("obs-advisor"), state.phaseRoster);
        var ready = reviewReady();
        assertEquals("concur", ready.path("metadata").path("reviewMode").asText());
        assertTrue(ready.path("content").asText().startsWith(ReviewDecision.CONCURRENCE_REQUEST));
        assertTrue(ready.path("content").asText().contains("[k8s-config] default"));
        var decision = published().stream()
                .filter(m -> "review_decision".equals(m.path("messageType").asText())).findFirst().orElseThrow();
        assertEquals("concur", decision.path("metadata").path("decision").asText());
        assertEquals("the listing answers it", decision.path("metadata").path("reason").asText());
        assertEquals("fast", decision.path("metadata").path("tier").asText());
    }

    @Test
    void concur_withNoAnalystSelected_asksTheBestMatchAmongAllAnalysts() {
        var o = orchestrator(true, "fast");
        decisionReturns("{\"review\":\"concur\"}");
        var state = decidingThread();

        o.decideReview(state, 1);

        assertEquals("k8s-advisor", state.concurrer);
    }

    @Test
    void full_runsTheReviewWithTheSelectedAnalysts() {
        var o = orchestrator(true, "fast");
        decisionReturns("{\"review\":\"full\",\"reason\":\"needs a count\"}");
        var state = decidingThread("compute", "k8s-advisor");

        o.decideReview(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state.phase);
        assertEquals(Set.of("compute", "k8s-advisor"), state.phaseRoster);
    }

    @Test
    void none_goesStraightToSynthesis() {
        var o = orchestrator(true, "fast");
        decisionReturns("{\"review\":\"none\"}");
        var state = decidingThread("k8s-advisor");

        o.decideReview(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
    }

    @Test
    void aFailedTooler_escalatesWithoutAskingTheModel() {
        var o = orchestrator(true, "fast");
        var state = decidingThread("k8s-advisor");
        state.failureSignals.put("k8s-workloads", "tool failed");

        var decision = o.reviewDecisionFor(state, 1);

        assertEquals(ReviewDecision.Shape.FULL, decision.shape());
        assertTrue(decision.forced());
        verify(chatService, never()).triageChatWithTokens(anyString(), anyString());
        verify(chatService, never()).simpleLlmCallWithTokens(anyString(), anyString());
    }

    @Test
    void aFailedDecisionCall_isAFullReview() {
        var o = orchestrator(true, "fast");
        when(chatService.hasDistinctTriageModel()).thenReturn(true);
        when(chatService.triageChatWithTokens(anyString(), anyString())).thenThrow(new IllegalStateException("gpu"));
        var state = decidingThread("k8s-advisor");

        assertEquals(ReviewDecision.Shape.FULL, o.reviewDecisionFor(state, 1).shape());
    }

    @Test
    void fastTier_usesTheTriageModel_reasoningTier_theMainModel() {
        decisionReturns("{\"review\":\"concur\"}");
        orchestrator(true, "fast").reviewDecisionFor(decidingThread("k8s-advisor"), 1);
        verify(chatService).triageChatWithTokens(anyString(), anyString());
        verify(chatService, never()).simpleLlmCallWithTokens(anyString(), anyString());

        orchestrator(true, "reasoning").reviewDecisionFor(decidingThread("k8s-advisor"), 1);
        verify(chatService).simpleLlmCallWithTokens(anyString(), anyString());
    }

    @Test
    void fastTier_withNoDistinctTriageModel_usesTheMainModel() {
        decisionReturns("{\"review\":\"concur\"}");
        when(chatService.hasDistinctTriageModel()).thenReturn(false);
        orchestrator(true, null).reviewDecisionFor(decidingThread("k8s-advisor"), 1);
        verify(chatService).simpleLlmCallWithTokens(anyString(), anyString());
    }

    @Test
    void aThreadStoppedWhileDeciding_isLeftAlone() {
        var o = orchestrator(true, "fast");
        decisionReturns("{\"review\":\"concur\"}");
        var state = decidingThread("k8s-advisor");
        state.phase = DiscussionOrchestrator.Phase.SYNTHESIZING;

        o.decideReview(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
        verify(conn, never()).publish(anyString(), any(byte[].class));
    }

    @Test
    void aDecisionCannotBePaused() {
        assertFalse(DiscussionOrchestrator.canPause(DiscussionOrchestrator.Phase.DECIDING));
        assertTrue(DiscussionOrchestrator.canPause(DiscussionOrchestrator.Phase.CONCURRING));
    }

    // ---- after the concurrence check ----

    private DiscussionOrchestrator.ThreadState concurringThread(DiscussionOrchestrator o, String... selected) {
        decisionReturns("{\"review\":\"concur\"}");
        var state = decidingThread(selected);
        o.decideReview(state, 1);
        assertEquals(DiscussionOrchestrator.Phase.CONCURRING, state.phase);
        return state;
    }

    @Test
    void agreement_goesToSynthesis() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor");
        state.pendingEvaluations.remove("k8s-advisor");
        state.agreeSignals.put("k8s-advisor", "concur, the list is complete");

        o.afterConcurrence(state);

        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
    }

    @Test
    void aConcern_escalatesToTheFullReview_withoutWakingTheConcurrerAgain() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor", "compute");
        assertEquals("k8s-advisor", state.concurrer);
        state.pendingEvaluations.remove("k8s-advisor");
        state.concernSignals.put("k8s-advisor", "the listing is truncated");

        o.afterConcurrence(state);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state.phase);
        assertEquals(Set.of("compute"), state.phaseRoster);
    }

    @Test
    void aConcernFromTheOnlySelectedAnalyst_escalatesToTheNextBestMatch() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor");
        state.concernSignals.put("k8s-advisor", "missing namespaces");

        o.afterConcurrence(state);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state.phase);
        assertEquals(Set.of("obs-advisor"), state.phaseRoster);
    }

    @Test
    void aFailedConcurrer_escalatesToo() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor", "compute");
        state.failureSignals.put("k8s-advisor", "context full");

        o.afterConcurrence(state);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state.phase);
    }

    @Test
    void anObjectionFromTheConcurrer_escalatesToo() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor", "compute");
        state.blockSignals.put("k8s-advisor", "this answer is wrong");

        o.afterConcurrence(state);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state.phase);
    }

    @Test
    void aStandAsideFromTheConcurrer_goesToSynthesis() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor");
        state.standAsideSignals.add("k8s-advisor");

        assertFalse(DiscussionOrchestrator.concurrerDissented(state));
        o.afterConcurrence(state);
        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
    }

    @Test
    void aRankingThatFails_isNoRanking_notASkippedReview() {
        var o = orchestrator(false, "fast");
        when(resumeSearch.searchResumes(anyString(), anyInt())).thenThrow(new IllegalStateException("down"));
        var state = decidingThread("compute", "k8s-advisor");

        o.decideReviewOrSynthesize(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state.phase, "the selected analysts still review");
        assertEquals(Set.of("compute", "k8s-advisor"), state.phaseRoster);
    }

    @Test
    void aFailureWhileShapingTheReview_synthesizes_ratherThanHangingInDeciding() {
        var o = orchestrator(true, "fast");
        decisionReturns("{\"review\":\"concur\"}");
        var state = decidingThread("k8s-advisor");
        state.innerCircle = null;
        when(resumeSearch.searchResumes(anyString(), anyInt())).thenReturn(RANKING);
        var broken = spy(o);
        doThrow(new IllegalStateException("boom")).when(broken).decideReview(any(), anyLong());

        broken.decideReviewOrSynthesize(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
    }

    @Test
    void aStopWhileDeciding_synthesizes_andTheLateDecisionIsIgnored() throws Exception {
        var o = orchestrator(true, "fast");
        decisionReturns("{\"review\":\"concur\"}");
        var state = decidingThread("k8s-advisor");

        o.handleStopRequest(state, state.threadId, "dashboard");
        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
        o.decideReview(state, 1);

        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
        assertTrue(published().stream().noneMatch(m -> "review_ready".equals(m.path("messageType").asText())));
    }

    @Test
    void aStopWhileConcurring_isNotOverwrittenByTheConcurrenceSettle() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor", "compute");
        state.concernSignals.put("k8s-advisor", "missing namespaces");

        o.handleStopRequest(state, state.threadId, "dashboard");
        o.afterConcurrence(state);

        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase,
                "the stop wins; the escalation does not move the thread back to REVIEW");
    }

    @Test
    void theCoordinatorIgnoresTheEchoOfItsOwnReviewDecision() {
        var o = orchestrator(true, "fast");
        assertTrue(o.isOwnControlEcho("coordinator", "review_decision"));
        assertTrue(o.isOwnControlEcho("coordinator", "review_ready"));
        assertFalse(o.isOwnControlEcho("k8s-advisor", "review_decision"), "only its own");
        assertFalse(o.isOwnControlEcho("coordinator", "synthesis"));
    }

    @Test
    void aReopenedThread_forgetsTheReviewOfTheLastQuestion() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor");
        state.phase = DiscussionOrchestrator.Phase.CLOSED;

        o.handleThreadReopen(state, state.threadId, "reply", "human");

        assertEquals(DiscussionOrchestrator.Phase.EVALUATING, state.phase);
        assertNull(state.concurrer);
        assertNull(state.resumeRanking);
        assertTrue(state.phaseRoster.isEmpty());
    }

    @Test
    void concurring_settlesAsSoonAsTheAnalystAnswers_beforeTheFloor() {
        var o = orchestrator(true, "fast");
        var state = concurringThread(o, "k8s-advisor");
        var advanced = new AtomicInteger();
        var settle = new DiscussionOrchestrator.PhaseSettle(10, 0, false, false, advanced::incrementAndGet);

        o.settlePhase(state, Instant.now(), 2, settle);
        assertEquals(0, advanced.get(), "the analyst has not answered");

        state.pendingEvaluations.remove("k8s-advisor");
        state.agreeSignals.put("k8s-advisor", "concur");
        state.lastSignalReceived = Instant.now();
        o.settlePhase(state, Instant.now(), 3, settle);
        assertEquals(1, advanced.get(), "state, not the 10 s floor, ends the phase");
    }
}
