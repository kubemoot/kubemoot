package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** A coordinator that takes up a thread another run wrote carries on numbering its events from where the log stopped. */
class CoordinatorTraceRestoreTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static CoordinatorEvent event(String id, String type, int round, String phase) {
        ObjectNode data = MAPPER.createObjectNode();
        if (phase != null) {
            data.put("phase", phase);
        }
        return new CoordinatorEvent(id, type, "t", round, Instant.EPOCH, "c", data);
    }

    @Test
    void aRestoredTraceIsOpenedAtTheLogsRoundAndSequence() {
        var trace = new CoordinatorTrace();

        trace.restore(List.of(
                event("t/opened", "thread_opened", 0, null),
                event("t/0/closed", "thread_closed", 0, null),
                event("t/reopened/1", "thread_reopened", 1, null)), 17);

        assertTrue(trace.opened());
        assertEquals(1, trace.round());
        assertEquals(17, trace.lastSequence(), "the next append states the last event read");
        assertEquals("t/reopened/1", trace.lastEventId("thread_reopened"));
        assertFalse(trace.unlogged());
        assertFalse(trace.fenced());
    }

    @Test
    void newEventsOfAKindTheLogAlreadyHasGetTheNextNumber() {
        var trace = new CoordinatorTrace();

        trace.restore(List.of(
                event("t/opened", "thread_opened", 0, null),
                event("t/0/phase/EVALUATING/1", "phase_entered", 0, "EVALUATING"),
                event("t/0/phase/REVIEW/1", "phase_entered", 0, "REVIEW"),
                event("t/0/phase/EVALUATING/2", "phase_entered", 0, "EVALUATING"),
                event("t/0/subcommittee/1", "subcommittee_convened", 0, null),
                event("t/0/evaluation/1", "evaluation_started", 0, null),
                event("t/0/settled/EVALUATING/1", "settled", 0, "EVALUATING"),
                event("t/0/synthesis/started/1", "synthesis_started", 0, null),
                event("t/0/resumed/1", "coordinator_resumed", 0, null)), 9);

        assertEquals(3, trace.enter("EVALUATING"), "round 0 has entered EVALUATING twice; the third is next");
        assertEquals(2, trace.enter("REVIEW"));
        assertEquals(2, trace.enter(CoordinatorEvent.SUBCOMMITTEE_CONVENED));
        assertEquals(2, trace.enter(CoordinatorEvent.EVALUATION_STARTED));
        assertEquals(2, trace.enter(CoordinatorEvent.SETTLED + "EVALUATING"));
        assertEquals(2, trace.enter(CoordinatorEvent.SYNTHESIS_STARTED));
        assertEquals(2, trace.enter(CoordinatorEvent.COORDINATOR_RESUMED));
        assertEquals(1, trace.enter(CoordinatorEvent.SYNTHESIS_DONE), "no answer was logged, so the first is next");
    }

    @Test
    void theEventsReadAreKeptForTheCheckAtClose() {
        var trace = new CoordinatorTrace();
        var events = List.of(event("t/opened", "thread_opened", 0, null));

        trace.restore(events, 1);

        assertEquals(events, trace.kept());
    }

    @Test
    void aTraceThatWasNeverRestoredHasNoLastEvent() {
        assertNull(new CoordinatorTrace().lastEventId("thread_opened"));
    }
}
