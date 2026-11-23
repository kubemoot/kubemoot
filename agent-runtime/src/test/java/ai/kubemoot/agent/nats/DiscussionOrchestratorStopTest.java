package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.Map;
import java.util.Optional;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.spy;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * A person's stop abandons the turn: the thread closes at once with a text that says the person
 * stopped it, from whatever phase it is in, and nothing still running can close it again with an
 * answer. A stop after the answer changes nothing.
 */
class DiscussionOrchestratorStopTest {

    private static final String THREAD = "thread-stop";
    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() {
        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        var triageModel = mock(AgentProperties.TriageModel.class);
        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("test-coordinator");
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("kubernetes"));
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
        when(properties.triageModel()).thenReturn(triageModel);
        when(triageModel.timeoutSeconds()).thenReturn(120);
        orchestrator = spy(new DiscussionOrchestrator(mock(NatsConnectionProvider.class), properties,
                mock(ChatService.class), new DiscussionMetrics(new SimpleMeterRegistry()),
                mock(LatencyTracker.class), mock(ResumeSearchClient.class)));
    }

    private DiscussionOrchestrator.ThreadState threadIn(Phase phase) {
        var state = new DiscussionOrchestrator.ThreadState(THREAD);
        state.userQuery = "anything";
        state.phase = phase;
        return state;
    }

    private void verifyStoppedAnswerPublished() {
        verify(orchestrator).publishToThread(eq(THREAD), anyString(), eq(DiscussionOrchestrator.STOPPED_ANSWER),
                eq(DiscussionOrchestrator.MSG_SYNTHESIS), eq(Map.of("outcome", "stopped")));
        verify(orchestrator).publishThreadClose(eq(THREAD), anyString(), eq(true));
    }

    @Test
    void stopDuringARunningTurnClosesItAtOnceWithTheStoppedText() {
        for (Phase phase : new Phase[] {Phase.SUBMITTED, Phase.ADVISORY, Phase.EVALUATING, Phase.REVIEW,
                Phase.PAUSED, Phase.SYNTHESIZING, Phase.RATIFY}) {
            org.mockito.Mockito.clearInvocations(orchestrator);
            var state = threadIn(phase);
            var future = orchestrator.registerPendingForTest(THREAD);
            state.agreeSignals.put("k8s-tooler", "3 pods");

            orchestrator.handleStopRequest(state, THREAD, "discussion-gateway");

            assertEquals(Phase.CLOSED, state.phase, "stopped from " + phase);
            assertTrue(state.stopped);
            assertEquals(DiscussionOrchestrator.STOPPED_ANSWER, future.getNow(null),
                    "the turn's answer is the stopped text, not a synthesis of " + phase);
            verifyStoppedAnswerPublished();
            assertEquals(Map.of("k8s-tooler", "3 pods"), state.agreeSignals, "the partial work stays");
        }
    }

    @Test
    void theStoppedTextSaysAPersonStoppedItAndThatItIsNotAnAnswer() {
        assertTrue(DiscussionOrchestrator.STOPPED_ANSWER.startsWith("Stopped by the person who asked"));
        assertTrue(DiscussionOrchestrator.STOPPED_ANSWER.contains("not an answer"));
    }

    @Test
    void aStopAfterTheAnswerChangesNothing() {
        var state = threadIn(Phase.CLOSED);
        state.answer = "the real answer";

        orchestrator.handleStopRequest(state, THREAD, "discussion-gateway");

        assertFalse(state.stopped);
        assertEquals("the real answer", state.answer);
        verify(orchestrator, never()).publishToThread(anyString(), anyString(), anyString(), anyString(), any());
    }

    @Test
    void aStopThatArrivesWhileTheRealCloseIsUnderWayDoesNotMislabelTheAnswer() {
        var state = threadIn(Phase.SYNTHESIZING);
        state.closeWait = new OutageBackoff(java.time.Duration.ofSeconds(1), java.time.Duration.ofMillis(1)).start();

        orchestrator.handleStopRequest(state, THREAD, "discussion-gateway");

        assertFalse(state.stopped);
        verify(orchestrator, never()).publishToThread(anyString(), anyString(), anyString(), anyString(), any());
    }

    @Test
    void aSecondStopIsIgnored() {
        var state = threadIn(Phase.EVALUATING);
        orchestrator.handleStopRequest(state, THREAD, "discussion-gateway");
        org.mockito.Mockito.clearInvocations(orchestrator);

        orchestrator.handleStopRequest(state, THREAD, "discussion-gateway");

        verify(orchestrator, never()).publishToThread(anyString(), anyString(), anyString(), anyString(), any());
    }

    @Test
    void aSynthesisOrFallbackThatFinishesAfterTheStopDoesNotCloseTheThreadAgain() {
        var state = threadIn(Phase.SYNTHESIZING);
        orchestrator.handleStopRequest(state, THREAD, "discussion-gateway");
        org.mockito.Mockito.clearInvocations(orchestrator);

        orchestrator.closeThread(state, "general", "a late synthesis", "synthesized");
        orchestrator.closeThread(state, "general", "a late fallback", "fallback");

        verify(orchestrator, never()).publishToThread(anyString(), anyString(), anyString(), anyString(), any());
        assertEquals(DiscussionOrchestrator.STOPPED_ANSWER, state.answer, "the stopped text stays the turn's answer");
    }

    @Test
    void aDraftIsNotPutToTheTableForAStoppedThread() {
        var state = threadIn(Phase.SYNTHESIZING);
        state.stopped = true;

        assertFalse(orchestrator.beginRatification(state, "a draft", java.time.Instant.now()));
        assertEquals(Phase.SYNTHESIZING, state.phase, "no RATIFY phase begins");
    }

    @Test
    void aHumanReplyReopensAStoppedThreadForAFreshRound() {
        var state = threadIn(Phase.EVALUATING);
        orchestrator.handleStopRequest(state, THREAD, "discussion-gateway");

        orchestrator.handleThreadReopen(state, THREAD, "reply", "human");

        assertFalse(state.stopped, "the new round is not a stopped one");
    }
}
