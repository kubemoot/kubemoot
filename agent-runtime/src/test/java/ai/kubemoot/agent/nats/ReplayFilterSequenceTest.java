package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Stop, pause and resume messages are ordered against the thread's last decision by their
 * sequence in the discussion stream when the log states one, so no clock decides.
 */
class ReplayFilterSequenceTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final Instant LAST_LOGGED = Instant.parse("2026-10-31T12:00:00Z");

    private static JsonNode control(String sentAt) throws Exception {
        return MAPPER.readTree("{\"messageType\":\"thread_pause\",\"timestamp\":\"" + sentAt + "\"}");
    }

    @Test
    void aControlAfterTheLoggedSequenceIsAppliedAndOneAtOrBeforeItIsNot() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of(), 100);

        assertTrue(filter.appliesControl(control("2026-10-31T12:00:01Z"), 101));
        assertFalse(filter.appliesControl(control("2026-10-31T12:00:01Z"), 100));
        assertFalse(filter.appliesControl(control("2026-10-31T12:00:01Z"), 40));
    }

    @Test
    void aSendersClockBehindTheCoordinatorsDoesNotDropAFreshControl() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of(), 100);

        assertTrue(filter.appliesControl(control("2026-10-31T09:00:00Z"), 150),
                "stamped hours before the last event, but it came after it in the stream");
    }

    @Test
    void aSendersClockAheadOfTheCoordinatorsDoesNotApplyAStaleControl() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of(), 100);

        assertFalse(filter.appliesControl(control("2026-10-31T18:00:00Z"), 90),
                "stamped hours after the last event, but the decisions already account for it");
    }

    @Test
    void withNoLoggedSequenceTheTimestampOrdersAsBefore() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of(), 0);

        assertTrue(filter.appliesControl(control("2026-10-31T12:00:01Z"), 5));
        assertFalse(filter.appliesControl(control("2026-10-31T11:59:59Z"), 5));
    }

    @Test
    void aMessageWhoseSequenceIsUnknownFallsBackToItsTimestamp() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of(), 100);

        assertTrue(filter.appliesControl(control("2026-10-31T12:00:01Z"), 0));
        assertFalse(filter.appliesControl(control("2026-10-31T11:59:59Z"), 0));
    }

    @Test
    void theLiveFilterAppliesEveryControl() throws Exception {
        assertTrue(ReplayFilter.LIVE.appliesControl(control("2020-01-01T00:00:00Z"), 1));
    }
}
