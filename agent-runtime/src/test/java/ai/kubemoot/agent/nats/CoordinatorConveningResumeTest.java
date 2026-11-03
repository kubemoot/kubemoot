package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.function.Consumer;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * What the log keeps of the analyst-first delivery so a restarted coordinator carries on: what each
 * analyst read, who answered with data, and the second review that was convened.
 */
class CoordinatorConveningResumeTest {

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

    private static CoordinatorEvent secondReview() {
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
    void whatAnAgreeReadAndWhetherItAnsweredWithDataAreKept() {
        var log = CoordinatorStateReducer.reduce(List.of(opened(), agreed("compute", true, PATTERN),
                agreed("advisor", false, "ns/crew/t/qm/001-list")));

        assertEquals(Map.of("compute", Set.of(PATTERN), "advisor", Set.of("ns/crew/t/qm/001-list")), log.readKeys);
        assertEquals(Set.of("compute"), log.dataAnalysts);
    }

    @Test
    void theSecondReviewsConveningAndAssignmentsAreKept() {
        var log = CoordinatorStateReducer.reduce(List.of(opened(), secondReview()));

        assertEquals(1, log.convenings);
        assertEquals(List.of("compute"), log.convenedAnalysts);
        assertEquals(List.of(new DeliveryPlan.Assignment("compute", Map.of(PATTERN, 9))), log.convenedAssignments);
    }

    @Test
    void aReopenedThreadStartsItsNextRoundWithNoReadsAndNoConvening() {
        var reopened = event("t/reopened/1", "thread_reopened", d -> d.putArray("roster"));

        var log = CoordinatorStateReducer.reduce(List.of(opened(), agreed("compute", true, PATTERN), secondReview(),
                reopened));

        assertTrue(log.readKeys.isEmpty() && log.dataAnalysts.isEmpty());
        assertEquals(0, log.convenings);
        assertTrue(log.convenedAnalysts.isEmpty());
    }

    @Test
    void aReopenedLiveThreadClearsItsDeliveryRound() {
        var state = new DiscussionOrchestrator.ThreadState("t");
        state.readKeys.put("compute", Set.of(PATTERN));
        state.dataAnalysts.add("compute");
        state.convenings = 1;
        state.convenedAnalysts.add("compute");
        state.resultCache.put("k", "v");

        state.clearDeliveryRound();

        assertTrue(state.readKeys.isEmpty() && state.dataAnalysts.isEmpty() && state.convenedAnalysts.isEmpty());
        assertTrue(state.resultCache.isEmpty());
        assertEquals(0, state.convenings);
    }

    @Test
    void aFirstReviewHasNoConvening() {
        var first = event("t/0/review/1", "review_started", d -> d.putArray("roster").add("compute"));

        assertEquals(0, CoordinatorStateReducer.reduce(List.of(opened(), first)).convenings);
    }

    @Test
    void theRebuiltThreadHasTheReadsTheAnalystsAndTheConveningBack() {
        var log = CoordinatorStateReducer.reduce(List.of(opened(), agreed("compute", true, PATTERN), secondReview()));

        var state = ThreadRebuilder.build(log);

        assertEquals(Set.of(PATTERN), state.readKeys.get("compute"));
        assertEquals(Set.of("compute"), state.dataAnalysts);
        assertEquals(1, state.convenings, "the thread is not convened a second time after a restart");
        assertEquals(List.of("compute"), state.convenedAnalysts);
        assertEquals(log.convenedAssignments, state.convenedAssignments);
    }
}
