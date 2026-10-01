package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;

import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/**
 * State-driven settling: a phase settles as soon as every participant it waits
 * for has signalled, and the floors are only a safety net while the roster is
 * unknown or a participant is pending. The guard: never settle on state while a
 * selected tooler (or a woken reviewer) has not published agree, concern, block,
 * stand_aside or failure in this phase.
 */
class DiscussionOrchestratorRosterSettleTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() {
        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        var triageModel = mock(AgentProperties.TriageModel.class);
        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("coordinator");
        when(properties.triageModel()).thenReturn(triageModel);
        when(triageModel.timeoutSeconds()).thenReturn(120);
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("general"));
        when(discuss.settleSeconds()).thenReturn(5);
        when(discuss.minEvalSeconds()).thenReturn(20);
        when(discuss.minReviewSeconds()).thenReturn(10);
        when(discuss.evalGraceSeconds()).thenReturn(10);
        orchestrator = new DiscussionOrchestrator(mock(NatsConnectionProvider.class), properties,
                mock(ChatService.class), new DiscussionMetrics(new SimpleMeterRegistry()),
                mock(LatencyTracker.class), mock(ResumeSearchClient.class));
    }

    private static DiscussionOrchestrator.PhaseSettle evalSettle(AtomicInteger advanced) {
        return new DiscussionOrchestrator.PhaseSettle(20, 120, true, true, advanced::incrementAndGet);
    }

    private static DiscussionOrchestrator.PhaseSettle reviewSettle(AtomicInteger advanced) {
        return new DiscussionOrchestrator.PhaseSettle(10, 0, false, false, advanced::incrementAndGet);
    }

    private static JsonNode msg() {
        return MAPPER.createObjectNode();
    }

    /** An EVALUATING thread whose roster is {@code toolers}, none signalled yet. */
    private static DiscussionOrchestrator.ThreadState evaluating(String id, String... toolers) {
        var s = new DiscussionOrchestrator.ThreadState(id);
        s.phase = DiscussionOrchestrator.Phase.EVALUATING;
        s.phaseRoster = PhaseRoster.of(List.of(toolers));
        return s;
    }

    private void signal(DiscussionOrchestrator.ThreadState s, String agent, String type) {
        orchestrator.handleAgentSignal(s, agent, type, "content from " + agent, msg());
    }

    // ---- the evaluation roster ----

    @Test
    void evaluationRoster_isTheSelectedAgentsMinusTheAnalysts() {
        assertEquals(Set.of("k8s-config", "internet-search"), DiscussionOrchestrator.evaluationRoster(
                Set.of("k8s-config", "internet-search", "compute"), Set.of("compute", "k8s-advisor")));
    }

    @Test
    void evaluationRoster_isUnknownWithoutAnInnerCircle() {
        assertEquals(Set.of(), DiscussionOrchestrator.evaluationRoster(null, Set.of("compute")));
        assertEquals(Set.of(), DiscussionOrchestrator.evaluationRoster(Set.of(), Set.of()));
        assertEquals(Set.of("a"), DiscussionOrchestrator.evaluationRoster(Set.of("a"), null));
    }

    @Test
    void resetEvaluationClock_installsAFreshRosterOfTheSelectedToolers() {
        var s = evaluating("w1", "old-tooler");
        signal(s, "old-tooler", "agree");
        s.innerCircle = java.util.concurrent.ConcurrentHashMap.newKeySet();
        s.innerCircle.addAll(Set.of("k8s-nodes", "k8s-metrics"));

        orchestrator.resetEvaluationClock(s);

        assertEquals(Set.of("k8s-nodes", "k8s-metrics"), s.phaseRoster.members());
        assertFalse(DiscussionOrchestrator.rosterSignalled(s), "nothing has signalled on the new roster");
        assertTrue(s.agreeSignals.isEmpty());
    }

    @Test
    void resetEvaluationClock_withoutASelection_leavesTheRosterUnknown() {
        var s = evaluating("w2", "old-tooler");
        s.innerCircle = null;

        orchestrator.resetEvaluationClock(s);

        assertFalse(s.phaseRoster.isKnown());
    }

    // ---- who reviews ----

    @Test
    void reviewers_areTheSelectedAnalysts_whenAnyWereSelected() {
        assertEquals(List.of("k8s-advisor"), DiscussionOrchestrator.reviewersFor(
                List.of("k8s-advisor"), Set.of("k8s-advisor", "compute", "proxmox-advisor")));
    }

    @Test
    void reviewers_areEveryAnalyst_whenNoneWasSelected() {
        var all = Set.of("k8s-advisor", "compute", "proxmox-advisor");
        assertEquals(all, Set.copyOf(DiscussionOrchestrator.reviewersFor(List.of(), all)));
        assertEquals(all, Set.copyOf(DiscussionOrchestrator.reviewersFor(null, all)));
    }

    @Test
    void reviewers_areNone_whenTheCrewHasNoAnalysts() {
        assertTrue(DiscussionOrchestrator.reviewersFor(List.of(), Set.of()).isEmpty());
        assertTrue(DiscussionOrchestrator.reviewersFor(null, null).isEmpty());
    }

    // ---- settlePhase in EVALUATING ----

    @Test
    void evaluation_settlesInsideTheFloor_onceEverySelectedToolerHasSignalled() {
        var s = evaluating("e1", "k8s-config", "k8s-workloads");
        signal(s, "k8s-config", "agree");
        signal(s, "k8s-workloads", "stand_aside");
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 6, evalSettle(advanced));

        assertEquals(1, advanced.get(), "the data is complete at 6 s: the 20 s floor does not apply");
    }

    @Test
    void evaluation_keepsTheFloor_whileASelectedToolerHasNotSignalled() {
        var s = evaluating("e2", "k8s-config", "k8s-workloads");
        signal(s, "k8s-config", "agree");
        s.lastSignalReceived = Instant.now().minusSeconds(30);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 6, evalSettle(advanced));

        assertEquals(0, advanced.get(), "k8s-workloads is silent, so the floor holds as the safety net");
    }

    @Test
    void evaluation_withAnUnknownRoster_keepsTheFloor() {
        var s = new DiscussionOrchestrator.ThreadState("e3");
        signal(s, "k8s-config", "agree");
        s.lastSignalReceived = Instant.now().minusSeconds(30);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 6, evalSettle(advanced));
        assertEquals(0, advanced.get());
        orchestrator.settlePhase(s, Instant.now(), 21, evalSettle(advanced));
        assertEquals(1, advanced.get(), "past the floor the quiet settle applies, as before");
    }

    @Test
    void evaluation_neverAdvancesWhileTheAdvisoryIsPending() {
        var s = evaluating("e4", "k8s-config");
        signal(s, "k8s-config", "agree");
        s.advisoryPending.set(true);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 30, evalSettle(advanced));

        assertEquals(0, advanced.get());
    }

    // ---- the guard ----

    @ParameterizedTest
    @ValueSource(strings = {"agree", "contribution", "concern", "block", "stand_aside", "decline", "failure"})
    void guard_everyTerminalSignalCounts(String terminal) {
        var s = evaluating("g1", "k8s-nodes");
        signal(s, "k8s-nodes", terminal);
        assertTrue(DiscussionOrchestrator.rosterSignalled(s), terminal + " ends the tooler's turn");
    }

    @ParameterizedTest
    @ValueSource(strings = {"triaging", "evaluating", "heartbeat", "waiting"})
    void guard_aToolerThatHasOnlyStartedHoldsThePhase(String progress) {
        var s = evaluating("g2", "k8s-nodes", "k8s-metrics");
        signal(s, "k8s-nodes", "agree");
        signal(s, "k8s-metrics", "triaging");
        signal(s, "k8s-metrics", progress);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 6, evalSettle(advanced));

        assertFalse(DiscussionOrchestrator.rosterSignalled(s), progress + " is not a terminal signal");
        assertEquals(0, advanced.get());
    }

    @Test
    void guard_aSignalFromAnEarlierPhaseDoesNotCount() {
        var s = evaluating("g3", "k8s-advisor");
        signal(s, "k8s-advisor", "agree");
        assertTrue(DiscussionOrchestrator.rosterSignalled(s));

        orchestrator.seedReviewRoster(s, List.of("k8s-advisor"), System.currentTimeMillis());
        s.pendingEvaluations.remove("k8s-advisor");

        assertTrue(s.agreeSignals.containsKey("k8s-advisor"), "the earlier agree is still on the thread");
        assertFalse(DiscussionOrchestrator.rosterSignalled(s), "but it is not a REVIEW signal");
    }

    @Test
    void guard_aToolerWhoseDeadlineExpired_countsAsAFailure() {
        var s = evaluating("g4", "k8s-nodes", "k8s-metrics");
        signal(s, "k8s-nodes", "agree");
        s.pendingEvaluations.put("k8s-metrics", System.currentTimeMillis() - 1);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 6, evalSettle(advanced));

        assertTrue(s.failureSignals.containsKey("k8s-metrics"));
        assertEquals(1, advanced.get(), "an expired tooler is a failure, and the roster is then complete");
    }

    @Test
    void guard_aReopenedThreadForgetsTheRoster() {
        var s = evaluating("g5", "k8s-nodes");
        signal(s, "k8s-nodes", "agree");
        s.phase = DiscussionOrchestrator.Phase.CLOSED;

        orchestrator.handleThreadReopen(s, "g5", "reply", "human");

        assertFalse(s.phaseRoster.isKnown());
        assertFalse(DiscussionOrchestrator.rosterSignalled(s));
    }

    // ---- REVIEW: woken reviewers are pending from the start ----

    /** A thread whose evaluation left one tooler agree, quiet for a minute. */
    private static DiscussionOrchestrator.ThreadState evaluatedThread() {
        var state = new DiscussionOrchestrator.ThreadState("t-review");
        state.phase = DiscussionOrchestrator.Phase.REVIEW;
        state.agreeSignals.put("k8s-config", "namespaces: a, b, c");
        state.lastSignalReceived = Instant.now().minusSeconds(60);
        return state;
    }

    @Test
    void review_holdsPastItsFloor_untilEveryWokenReviewerSignals() {
        var state = evaluatedThread();
        orchestrator.seedReviewRoster(state, List.of("k8s-advisor", "compute"), System.currentTimeMillis());
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(state, Instant.now(), 15, reviewSettle(advanced));
        assertEquals(0, advanced.get(), "carried-over tooler agrees and quiet do not settle REVIEW");

        signal(state, "k8s-advisor", "agree");
        orchestrator.settlePhase(state, Instant.now(), 2, reviewSettle(advanced));
        assertEquals(0, advanced.get(), "compute is still pending");

        signal(state, "compute", "concern");
        orchestrator.settlePhase(state, Instant.now(), 3, reviewSettle(advanced));
        assertEquals(1, advanced.get(), "every reviewer has signalled: REVIEW advances inside its floor");
    }

    @Test
    void review_seedUsesTheTriagingDeadline_andDropsEvalStragglers() {
        var state = evaluatedThread();
        state.pendingEvaluations.put("slow-tooler", 1L);
        long now = 1_000_000L;

        orchestrator.seedReviewRoster(state, List.of("compute", "k8s-advisor"), now);

        assertEquals(2, state.pendingEvaluations.size());
        assertFalse(state.pendingEvaluations.containsKey("slow-tooler"), "eval stragglers are dropped");
        assertEquals(now + 120_000L, state.pendingEvaluations.get("compute"));
        assertEquals(Set.of("compute", "k8s-advisor"), state.phaseRoster.members());
    }

    @Test
    void review_aReviewerThatNeverSignals_expiresAsAFailure_andReviewAdvances() {
        var state = evaluatedThread();
        orchestrator.seedReviewRoster(state, List.of("proxmox-advisor"), System.currentTimeMillis() - 200_000L);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(state, Instant.now(), 15, reviewSettle(advanced));

        assertTrue(state.failureSignals.containsKey("proxmox-advisor"),
                "a woken reviewer that never answered is a failure, not silently dropped");
        assertTrue(state.pendingEvaluations.isEmpty());
        assertEquals(1, advanced.get());
    }

    @Test
    void review_withNoReviewers_leavesTheRosterUnknownAndTheFloorInCharge() {
        var state = evaluatedThread();
        orchestrator.seedReviewRoster(state, List.of(), System.currentTimeMillis());
        var advanced = new AtomicInteger();

        assertTrue(state.pendingEvaluations.isEmpty());
        assertFalse(state.phaseRoster.isKnown());
        orchestrator.settlePhase(state, Instant.now(), 5, reviewSettle(advanced));
        assertEquals(0, advanced.get(), "inside the floor nothing settles on state");
        orchestrator.settlePhase(state, Instant.now(), 11, reviewSettle(advanced));
        assertEquals(1, advanced.get(), "past the floor the quiet settle applies, as before");
    }
}
