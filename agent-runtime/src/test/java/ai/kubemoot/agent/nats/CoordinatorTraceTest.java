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
}
