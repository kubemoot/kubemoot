package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.time.Instant;
import java.util.List;
import java.util.Set;
import java.util.function.Consumer;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * A log written by an earlier coordinator carries fields that are no longer read (what each agent read,
 * whether it answered with data, the rounds and the delivery of a synthesis). Such a log replays
 * without a failure, and the fields change nothing in the rebuilt thread.
 */
class CoordinatorDeliveryResumeTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final Instant T0 = Instant.parse("2026-11-02T10:00:00Z");
    private static final String PATTERN = "ns/crew/t/qm/*-get_vm_rrd_data";

    private static CoordinatorEvent event(String id, String type, Consumer<ObjectNode> data) {
        ObjectNode fields = MAPPER.createObjectNode();
        data.accept(fields);
        return new CoordinatorEvent(id, type, "t", 0, T0, "c", fields);
    }

    private static CoordinatorEvent opened() {
        return event("t/opened", "thread_opened", d -> {
            d.put("openedAt", T0.toString());
            d.put("phaseStartedAt", T0.toString());
        });
    }

    private static CoordinatorEvent agreed(String agent, boolean withData, String... keys) {
        return event("t/0/signal/" + agent + "/m", "signal_applied", d -> {
            d.put("agent", agent);
            d.put("signal", "agree");
            d.put("messageId", "m-" + agent);
            d.put("withData", withData);
            var array = d.putArray("readKeys");
            for (String k : keys) {
                array.add(k);
            }
        });
    }

    /** A second review event as an earlier coordinator wrote it: the roster and fields no longer read. */
    private static CoordinatorEvent olderSecondReview() {
        return event("t/0/review/2", "review_started", d -> {
            d.putArray("roster").add("compute");
            var convening = d.putObject("convening");
            convening.put("count", 1);
            convening.putArray("analysts").add("compute");
            var one = convening.putArray("assignments").addObject();
            one.put("analyst", "compute");
            one.putObject("patterns").put(PATTERN, 9);
        });
    }

    @Test
    void anAgreeEventWithTheOldReadFieldsStillCountsAsTheAgentsAgree() {
        var log = CoordinatorStateReducer.reduce(List.of(opened(), agreed("compute", true, PATTERN),
                agreed("advisor", false, "ns/crew/t/qm/001-list")));

        assertEquals(Set.of("compute", "advisor"), log.agree.keySet());
    }

    @Test
    void aLogWithUnknownFieldsInItsReviewEventReplaysAndTheRestOfTheReviewIsKept() {
        var log = CoordinatorStateReducer.reduce(List.of(opened(), agreed("compute", true, PATTERN),
                olderSecondReview()));

        assertEquals(List.of("compute"), List.copyOf(log.rosterMembers));
        assertTrue(log.reviewRosterWritten);
        assertEquals(Set.of("compute"), log.agree.keySet());
    }

    @Test
    void aThreadRebuiltFromALogWithTheOldFieldsHasTheSignalsAndNoReads() {
        var log = CoordinatorStateReducer.reduce(List.of(opened(), agreed("compute", true, PATTERN),
                olderSecondReview()));

        var state = ThreadRebuilder.build(log);

        assertEquals(Set.of("compute"), state.agreeSignals.keySet());
    }

    @Test
    void aSynthesisEventWithTheOldRoundsAndDeliveryFieldsIsTolerated() {
        var done = event("t/0/synthesis/done/1", "synthesis_done", d -> {
            d.put("outcome", "synthesized");
            d.put("rounds", 3);
            d.put("filledBytes", 120_000L);
            var delivery = d.putObject("delivery");
            delivery.putArray("covered").add("a");
            delivery.putArray("uncovered");
            delivery.putArray("convened").add("compute");
            delivery.putObject("stepMillis").put("convene", 4000L).put("catalog", 3L);
        });

        var log = CoordinatorStateReducer.reduce(List.of(opened(), olderSecondReview(), done));

        assertNotNull(log);
    }

    @Test
    void aReopenedThreadReplaysFromALogThatHadReadsInIt() {
        var reopened = event("t/reopened/1", "thread_reopened", d -> d.putArray("roster"));

        var log = CoordinatorStateReducer.reduce(List.of(opened(), agreed("compute", true, PATTERN),
                olderSecondReview(), reopened));

        assertTrue(log.agree.isEmpty(), "the new round starts with no signals");
    }

    @Test
    void anEventWithNothingButUnknownFieldsIsTolerated() {
        var odd = event("t/0/signal/x/m", "signal_applied", d -> {
            d.put("agent", "x");
            d.put("signal", "agree");
            d.put("readKeys", "not-an-array");
            d.put("withData", "maybe");
        });

        assertDoesNotThrow(() -> CoordinatorStateReducer.reduce(List.of(opened(), odd)));
    }
}
