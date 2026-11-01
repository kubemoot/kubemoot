package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Which messages read back from the stream a restarted coordinator still applies. */
class ReplayFilterTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final Instant LAST_LOGGED = Instant.parse("2026-10-31T12:00:00Z");

    private static JsonNode message(String messageId, String timestamp) throws Exception {
        return MAPPER.readTree("{\"messageId\":\"" + messageId + "\",\"timestamp\":\"" + timestamp + "\"}");
    }

    @Test
    void theLiveFilterAppliesEverything() throws Exception {
        JsonNode msg = message("m1", "2020-01-01T00:00:00Z");

        assertTrue(ReplayFilter.LIVE.appliesSignal("agree", msg));
        assertTrue(ReplayFilter.LIVE.appliesSignal("heartbeat", msg));
        assertTrue(ReplayFilter.LIVE.appliesControl(msg));
    }

    @Test
    void aSignalTheLogRecordedIsNotAppliedAgain() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of("m1"));

        assertFalse(filter.appliesSignal("agree", message("m1", "2026-10-31T11:00:00Z")));
        assertFalse(filter.appliesSignal("evaluating", message("m1", "2026-10-31T11:00:00Z")));
        assertFalse(filter.appliesSignal("failure", message("m1", "2026-10-31T11:00:00Z")));
    }

    @Test
    void aSignalTheLogDoesNotHoldIsApplied() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of("m1"));

        assertTrue(filter.appliesSignal("agree", message("m2", "2026-10-31T12:00:05Z")));
        assertTrue(filter.appliesSignal("stand_aside", message("m3", "2026-10-31T12:00:06Z")));
    }

    @Test
    void aMessageWithNoIdCannotBeMatchedSoItIsApplied() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of(""));

        assertTrue(filter.appliesSignal("agree", MAPPER.readTree("{\"timestamp\":\"2026-10-31T12:00:05Z\"}")));
    }

    @Test
    void aHeartbeatIsNeverReplayed() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of());

        assertFalse(filter.appliesSignal("heartbeat", message("m9", "2026-10-31T12:00:05Z")));
    }

    @Test
    void aMessageThatIsNotASignalIsNotFilteredOut() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of("m1"));

        assertTrue(filter.appliesSignal("advisory", message("m1", "2026-10-31T11:00:00Z")));
    }

    @Test
    void aControlSentAfterTheLastLoggedDecisionIsAppliedAndAnEarlierOneIsNot() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of());

        assertTrue(filter.appliesControl(message("c1", "2026-10-31T12:00:01Z")));
        assertFalse(filter.appliesControl(message("c2", "2026-10-31T11:59:59Z")));
    }

    @Test
    void aControlWithNoReadableTimeIsNotApplied() throws Exception {
        var filter = new ReplayFilter(LAST_LOGGED, Set.of());

        assertFalse(filter.appliesControl(MAPPER.readTree("{\"timestamp\":\"yesterday\"}")));
        assertFalse(filter.appliesControl(MAPPER.readTree("{}")));
    }
}
