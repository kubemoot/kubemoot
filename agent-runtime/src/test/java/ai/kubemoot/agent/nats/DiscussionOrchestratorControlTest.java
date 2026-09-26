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

    // ---- canForceSynthesis (Stop) ----

    @Test
    void stop_honoredFromLivePhases() {
        assertTrue(DiscussionOrchestrator.canForceSynthesis(Phase.SUBMITTED),
                "a Stop can arrive before agents respond");
        assertTrue(DiscussionOrchestrator.canForceSynthesis(Phase.ADVISORY));
        assertTrue(DiscussionOrchestrator.canForceSynthesis(Phase.EVALUATING));
        assertTrue(DiscussionOrchestrator.canForceSynthesis(Phase.REVIEW));
        assertTrue(DiscussionOrchestrator.canForceSynthesis(Phase.PAUSED),
                "a paused thread can still be stopped");
    }

    @Test
    void stop_ignoredOncePastSynthesis() {
        assertFalse(DiscussionOrchestrator.canForceSynthesis(Phase.SYNTHESIZING));
        assertFalse(DiscussionOrchestrator.canForceSynthesis(Phase.CLOSED));
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
