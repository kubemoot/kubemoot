package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for the external dashboard-control gating predicates extracted from
 * the (formerly CC-46) handleMessage dispatch: which phases accept a Stop, a Pause,
 * and what a Resume restores. These pin the exact phase conditions so the
 * extract-method refactor cannot silently change control behavior.
 */
class DiscussionOrchestratorControlTest {

    // ---- canStop (Stop) ----

    @Test
    void stop_honoredFromEveryPhaseThatHasNoAnswerYet() {
        assertTrue(DiscussionOrchestrator.canStop(Phase.SUBMITTED), "a Stop can arrive before agents respond");
        assertTrue(DiscussionOrchestrator.canStop(Phase.ADVISORY));
        assertTrue(DiscussionOrchestrator.canStop(Phase.EVALUATING));
        assertTrue(DiscussionOrchestrator.canStop(Phase.REVIEW));
        assertTrue(DiscussionOrchestrator.canStop(Phase.PAUSED), "a paused thread can still be stopped");
        assertTrue(DiscussionOrchestrator.canStop(Phase.SYNTHESIZING), "the answer is not out yet");
        assertTrue(DiscussionOrchestrator.canStop(Phase.RATIFY), "the draft is not the answer yet");
    }

    @Test
    void stop_ignoredOnceTheThreadIsClosed() {
        assertFalse(DiscussionOrchestrator.canStop(Phase.CLOSED));
    }

    // ---- canPause ----

    @Test
    void pause_honoredFromLivePhases() {
        assertTrue(DiscussionOrchestrator.canPause(Phase.SUBMITTED));
        assertTrue(DiscussionOrchestrator.canPause(Phase.ADVISORY));
        assertTrue(DiscussionOrchestrator.canPause(Phase.EVALUATING));
        assertTrue(DiscussionOrchestrator.canPause(Phase.REVIEW));
    }

    @Test
    void pause_ignoredWhenAlreadyPausedClosedOrSynthesizing() {
        assertFalse(DiscussionOrchestrator.canPause(Phase.PAUSED), "no double-pause");
        assertFalse(DiscussionOrchestrator.canPause(Phase.CLOSED));
        assertFalse(DiscussionOrchestrator.canPause(Phase.SYNTHESIZING),
                "do not pause once synthesis has started");
        assertFalse(DiscussionOrchestrator.canPause(Phase.RATIFY), "the draft is on the table; no pause");
    }

    // ---- resumeTarget ----

    @Test
    void resume_restoresSavedPhase() {
        assertEquals(Phase.REVIEW, DiscussionOrchestrator.resumeTarget(Phase.PAUSED, Phase.REVIEW),
                "resume restores the pre-pause phase");
    }

    @Test
    void resume_defaultsToEvaluatingWhenNoSavedPhase() {
        assertEquals(Phase.EVALUATING, DiscussionOrchestrator.resumeTarget(Phase.PAUSED, null),
                "missing pre-pause phase defaults to EVALUATING");
    }

    @Test
    void resume_isNoOpWhenNotPaused() {
        assertNull(DiscussionOrchestrator.resumeTarget(Phase.EVALUATING, Phase.REVIEW),
                "resume on a non-paused thread does nothing");
        assertNull(DiscussionOrchestrator.resumeTarget(Phase.CLOSED, null));
        assertNull(DiscussionOrchestrator.resumeTarget(Phase.SYNTHESIZING, Phase.REVIEW));
        assertNull(DiscussionOrchestrator.resumeTarget(Phase.SUBMITTED, null));
    }
}
