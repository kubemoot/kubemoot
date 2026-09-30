package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/**
 * REVIEW's roster is pending from the moment REVIEW begins, so the phase cannot
 * settle at its floor before a woken reviewer has published its first signal.
 */
class DiscussionOrchestratorReviewRosterTest {

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

    private static DiscussionOrchestrator.PhaseSettle reviewSettle(AtomicInteger advanced) {
        return new DiscussionOrchestrator.PhaseSettle(10, 0, false, false, advanced::incrementAndGet);
    }

    /** A thread whose evaluation left one tooler agree, quiet for a minute. */
    private static DiscussionOrchestrator.ThreadState evaluatedThread() {
        var state = new DiscussionOrchestrator.ThreadState("t-review");
        state.agreeSignals.put("k8s-config", "namespaces: a, b, c");
        state.lastSignalReceived = Instant.now().minusSeconds(60);
        return state;
    }

    @Test
    void seededRoster_holdsReviewPastItsFloor_untilTheReviewerSignals() {
        var state = evaluatedThread();
        long now = System.currentTimeMillis();
        orchestrator.seedReviewRoster(state, List.of("k8s-advisor"), now);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(state, Instant.now(), 15, reviewSettle(advanced));
        assertEquals(0, advanced.get(), "a woken reviewer that has not signalled yet holds REVIEW");

        state.pendingEvaluations.remove("k8s-advisor");
        state.agreeSignals.put("k8s-advisor", "concur");
        orchestrator.settlePhase(state, Instant.now(), 16, reviewSettle(advanced));
        assertEquals(1, advanced.get(), "REVIEW advances once its roster has signalled");
    }

    @Test
    void seededRoster_usesTheTriagingDeadline_andDropsEvalStragglers() {
        var state = evaluatedThread();
        state.pendingEvaluations.put("slow-tooler", 1L);
        long now = 1_000_000L;

        orchestrator.seedReviewRoster(state, List.of("compute", "k8s-advisor"), now);

        assertEquals(2, state.pendingEvaluations.size());
        assertFalse(state.pendingEvaluations.containsKey("slow-tooler"), "eval stragglers are dropped");
        assertEquals(now + 120_000L, state.pendingEvaluations.get("compute"));
    }

    @Test
    void reviewerThatNeverSignals_expiresAtItsDeadline_asAFailure_andReviewAdvances() {
        var state = evaluatedThread();
        orchestrator.seedReviewRoster(state, List.of("proxmox-advisor"), System.currentTimeMillis() - 200_000L);
        var advanced = new AtomicInteger();

        orchestrator.settlePhase(state, Instant.now(), 15, reviewSettle(advanced));

        assertTrue(state.failureSignals.containsKey("proxmox-advisor"),
                "a woken reviewer that never answered is a failure, not silently dropped");
        assertTrue(state.pendingEvaluations.isEmpty());
        assertEquals(1, advanced.get(), "REVIEW advances once the expired reviewer is recorded");
    }

    @Test
    void emptyRoster_leavesNothingPending() {
        var state = evaluatedThread();
        orchestrator.seedReviewRoster(state, List.of(), System.currentTimeMillis());
        assertTrue(state.pendingEvaluations.isEmpty());
    }
}
