package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link DiscussionOrchestrator#shouldSkipReview(long,int,int,boolean)}.
 *
 * The single-agree fast path skips REVIEW only when there is no dissent AND the
 * crew has no analysts. If analysts exist the REVIEW phase must run so they can
 * self-select, even on a single caller agree.
 */
class DiscussionOrchestratorReviewGateTest {

    @Test
    void singleAgreeNoDissentNoAnalysts_skipsReview() {
        assertTrue(DiscussionOrchestrator.shouldSkipReview(1, 0, 0, false));
    }

    @Test
    void singleAgreeButAnalystsPresent_runsReview() {
        assertFalse(DiscussionOrchestrator.shouldSkipReview(1, 0, 0, true),
                "analysts must get the REVIEW phase even on a single agree");
    }

    @Test
    void multipleAgrees_runsReview() {
        assertFalse(DiscussionOrchestrator.shouldSkipReview(2, 0, 0, false));
    }

    @Test
    void concernsOrBlocks_runReviewRegardless() {
        assertFalse(DiscussionOrchestrator.shouldSkipReview(1, 1, 0, false), "a concern forces review");
        assertFalse(DiscussionOrchestrator.shouldSkipReview(1, 0, 1, false), "a block forces review");
    }

    @Test
    void zeroAgrees_doesNotSkip() {
        assertFalse(DiscussionOrchestrator.shouldSkipReview(0, 0, 0, false));
    }
}
