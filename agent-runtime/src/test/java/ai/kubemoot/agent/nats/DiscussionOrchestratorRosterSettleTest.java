package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/**
 * A phase settles when every participant it waits for has signalled (state);
 * the evaluation floor is only a safety net while the roster is unknown or has
 * signals pending.
 */
class DiscussionOrchestratorRosterSettleTest {

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
    }

    // ---- rosterSignalled ----

    @Test
    void rosterSignalled_whenEveryMemberHasATerminalSignal() {
        var s = new DiscussionOrchestrator.ThreadState("r1");
        s.phaseRoster = Set.of("a", "b", "c", "d", "e");
        s.agreeSignals.put("a", "data");
        s.concernSignals.put("b", "missing");
        s.blockSignals.put("c", "no");
        s.failureSignals.put("d", "tool failed");
        s.standAsideSignals.add("e");
        assertTrue(DiscussionOrchestrator.rosterSignalled(s));
    }

    @Test
    void rosterNotSignalled_whileAMemberIsSilentOrPending() {
        var s = new DiscussionOrchestrator.ThreadState("r2");
        s.phaseRoster = Set.of("a", "b");
        s.agreeSignals.put("a", "data");
        assertFalse(DiscussionOrchestrator.rosterSignalled(s), "b has not signalled");
        s.agreeSignals.put("b", "data");
        s.pendingEvaluations.put("b", Long.MAX_VALUE);
        assertFalse(DiscussionOrchestrator.rosterSignalled(s), "b is still pending");
    }

    @Test
    void anUnknownRoster_isNeverSignalled() {
        var s = new DiscussionOrchestrator.ThreadState("r3");
        s.agreeSignals.put("a", "data");
        assertFalse(DiscussionOrchestrator.rosterSignalled(s));
        s.phaseRoster = null;
        assertFalse(DiscussionOrchestrator.rosterSignalled(s));
    }

    // ---- settlePhase in EVALUATING ----

    @Test
    void evaluation_settlesInsideTheFloor_onceEverySelectedToolerHasSignalled() {
        var s = new DiscussionOrchestrator.ThreadState("e1");
        s.phaseRoster = Set.of("k8s-config");
        s.agreeSignals.put("k8s-config", "default, kube-system");
        s.lastSignalReceived = Instant.now();
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 6, evalSettle(advanced));

        assertEquals(1, advanced.get(), "the data is complete at 6 s: the 20 s floor does not apply");
    }

    @Test
    void evaluation_keepsTheFloor_whileASelectedToolerHasNotSignalled() {
        var s = new DiscussionOrchestrator.ThreadState("e2");
        s.phaseRoster = Set.of("k8s-config", "k8s-workloads");
        s.agreeSignals.put("k8s-config", "default, kube-system");
        s.lastSignalReceived = Instant.now().minusSeconds(30);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 6, evalSettle(advanced));

        assertEquals(0, advanced.get(), "k8s-workloads is silent, so the floor holds as the safety net");
    }

    @Test
    void evaluation_withAnUnknownRoster_keepsTheFloor() {
        var s = new DiscussionOrchestrator.ThreadState("e3");
        s.agreeSignals.put("k8s-config", "default");
        s.lastSignalReceived = Instant.now().minusSeconds(30);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 6, evalSettle(advanced));
        assertEquals(0, advanced.get());
        orchestrator.settlePhase(s, Instant.now(), 21, evalSettle(advanced));
        assertEquals(1, advanced.get(), "past the floor the quiet settle applies, as before");
    }

    @Test
    void evaluation_neverAdvancesWhileTheAdvisoryIsPending() {
        var s = new DiscussionOrchestrator.ThreadState("e4");
        s.phaseRoster = Set.of("k8s-config");
        s.agreeSignals.put("k8s-config", "stale");
        s.advisoryPending.set(true);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(s, Instant.now(), 30, evalSettle(advanced));

        assertEquals(0, advanced.get());
    }
}
