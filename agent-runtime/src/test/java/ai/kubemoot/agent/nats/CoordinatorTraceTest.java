package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The counters that give a coordinator event its natural id. */
class CoordinatorTraceTest {

    @Test
    void aNewTraceHasNotOpenedTheThreadAndIsInRoundZero() {
        var trace = new CoordinatorTrace();

        assertFalse(trace.opened());
        assertEquals(0, trace.round());
    }

    @Test
    void markingOpenedSticks() {
        var trace = new CoordinatorTrace();

        trace.markOpened();

        assertTrue(trace.opened());
    }

    @Test
    void entriesIntoAPhaseAreCountedFromOne() {
        var trace = new CoordinatorTrace();

        assertEquals(1, trace.enter("REVIEW"));
        assertEquals(2, trace.enter("REVIEW"));
        assertEquals(1, trace.enter("EVALUATING"), "each phase counts on its own");
    }

    @Test
    void aNewRoundStartsTheCountsAgain() {
        var trace = new CoordinatorTrace();
        trace.enter("EVALUATING");

        assertEquals(1, trace.nextRound());

        assertEquals(1, trace.round());
        assertEquals(1, trace.enter("EVALUATING"), "the same phase in a later round is a new first entry");
    }

    @Test
    void theLockIsOneObjectPerTrace() {
        var a = new CoordinatorTrace();
        var b = new CoordinatorTrace();

        assertNotNull(a.lock());
        assertEquals(a.lock(), a.lock());
        assertFalse(a.lock() == b.lock(), "two threads do not wait on each other");
    }

    private static CoordinatorEvent event(String id, String type) {
        return new CoordinatorEvent(id, type, "t1", 0, java.time.Instant.EPOCH, "c",
                new com.fasterxml.jackson.databind.ObjectMapper().createObjectNode());
    }

    @Test
    void anAppendedEventSetsTheSequenceTheNextAppendExpectsAndIsKeptForTheCheck() {
        var trace = new CoordinatorTrace();
        assertEquals(0, trace.lastSequence());

        trace.appended(event("t1/opened", "thread_opened"), 5);

        assertEquals(5, trace.lastSequence());
        assertEquals("t1/opened", trace.lastEventId("thread_opened"));
        assertEquals(1, trace.kept().size());
        assertEquals(null, trace.lastEventId("settled"));
    }

    @Test
    void fencingSticks() {
        var trace = new CoordinatorTrace();
        assertFalse(trace.fenced());

        trace.fence();

        assertTrue(trace.fenced());
    }

    @Test
    void aThreadWithMoreEventsThanItKeepsIsNotChecked() {
        var trace = new CoordinatorTrace();
        for (int i = 0; i <= CoordinatorTrace.MAX_KEPT_EVENTS; i++) {
            trace.appended(event("t1/e" + i, "settled"), i + 1L);
        }

        assertEquals(null, trace.kept());
        assertEquals(CoordinatorTrace.MAX_KEPT_EVENTS + 1L, trace.lastSequence(), "the fence still tracks every append");
    }
}
