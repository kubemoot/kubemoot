package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for the unified phase-settle decision helpers
 * ({@link DiscussionOrchestrator#hasSubstantiveSignal} and
 * {@link DiscussionOrchestrator#isColdStartWaiting}) that drive the single
 * settlePhase() used by BOTH the EVALUATING and REVIEW phases.
 *
 * These replaced the two divergent functions (eval roster-gated, review a blunt
 * timer) whose drift dropped the analyst's REVIEW contribution. The PhaseSettle
 * config is what differs per phase: eval gets a cold-start grace + advisory-pending
 * check; review gets neither (its agents are already warm).
 */
class DiscussionOrchestratorSettleTest {

    private static DiscussionOrchestrator.PhaseSettle evalCfg() {
        // floor, coldStartGrace=120 (eval), fastPath, checkAdvisoryPending=true
        return new DiscussionOrchestrator.PhaseSettle(45, 120, true, true, () -> { });
    }

    private static DiscussionOrchestrator.PhaseSettle reviewCfg() {
        // floor, coldStartGrace=0 (review: agents warm), fastPath, checkAdvisoryPending=false
        return new DiscussionOrchestrator.PhaseSettle(10, 0, true, false, () -> { });
    }

    // ---- hasSubstantiveSignal ----

    @Test
    void substantive_whenAToolerAgreed() {
        var s = new DiscussionOrchestrator.ThreadState("t1");
        assertTrue(DiscussionOrchestrator.hasSubstantiveSignal(s, 1));
    }

    @Test
    void substantive_onConcernOrBlock_evenWithZeroAgrees() {
        var s = new DiscussionOrchestrator.ThreadState("t2");
        s.concernSignals.put("k8s-tooler", "metric server missing");
        assertTrue(DiscussionOrchestrator.hasSubstantiveSignal(s, 0), "a concern is substantive");
        var s2 = new DiscussionOrchestrator.ThreadState("t3");
        s2.blockSignals.put("proxmox-tooler", "API down");
        assertTrue(DiscussionOrchestrator.hasSubstantiveSignal(s2, 0), "a block is substantive");
    }

    @Test
    void notSubstantive_whenNothingButStandAsides() {
        var s = new DiscussionOrchestrator.ThreadState("t4");
        s.standAsideSignals.add("obs-advisor");
        assertFalse(DiscussionOrchestrator.hasSubstantiveSignal(s, 0),
                "stand-asides alone are not substantive (no agree, concern, or block)");
    }

    // ---- isColdStartWaiting ----

    @Test
    void coldStart_evalWaitsWhileZeroSignalsInsideGrace() {
        var s = new DiscussionOrchestrator.ThreadState("t5"); // no signals
        assertTrue(DiscussionOrchestrator.isColdStartWaiting(s, 50, evalCfg()),
                "eval holds for cold-start agents scaling from zero");
        assertFalse(DiscussionOrchestrator.isColdStartWaiting(s, 130, evalCfg()),
                "past the grace window it stops waiting");
    }

    @Test
    void coldStart_neverWaitsOnceASignalArrived() {
        var s = new DiscussionOrchestrator.ThreadState("t6");
        s.agreeSignals.put("k8s-tooler", "3 pods");
        assertFalse(DiscussionOrchestrator.isColdStartWaiting(s, 5, evalCfg()),
                "a signal means agents are awake - no cold-start hold");
    }

    @Test
    void coldStart_reviewNeverWaits_graceDisabled() {
        var s = new DiscussionOrchestrator.ThreadState("t7"); // zero signals
        assertFalse(DiscussionOrchestrator.isColdStartWaiting(s, 1, reviewCfg()),
                "REVIEW has no cold-start grace (its agents are warm from eval)");
    }
}
