package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import java.util.OptionalLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The room a thread's results may take in the discussion follows from the readers' context window. */
class DiscussionRoomTest {

    @Test
    void a32kWindowGivesAQuarterOfItInCharactersAndTheSharesOfThat() {
        var window = OptionalLong.of(32_768);

        assertEquals(24_576, DiscussionRoom.chars(window), "a quarter of the window at three characters a token");
        assertEquals(3_072, DiscussionRoom.perResultChars(window), "an eighth of the room");
        assertEquals(6_144, DiscussionRoom.perContributionChars(window), "a quarter of the room");
    }

    @Test
    void a128kWindowGivesFourTimesTheRoomUpToTheBusLimit() {
        var window = OptionalLong.of(131_072);

        assertEquals(98_304, DiscussionRoom.chars(window));
        assertEquals(12_288, DiscussionRoom.perResultChars(window));
        assertEquals(20_000, DiscussionRoom.perContributionChars(window),
                "a quarter of the room is 24,576; the bus limit holds it to " + CallManifest.MAX_INLINE_CHARS);
    }

    @Test
    void anUnknownWindowGivesTheFixedRoom() {
        var none = OptionalLong.empty();

        assertEquals(DiscussionRoom.UNKNOWN_WINDOW_CHARS, DiscussionRoom.chars(none));
        assertEquals(1_000, DiscussionRoom.perResultChars(none));
        assertEquals(2_000, DiscussionRoom.perContributionChars(none));
    }

    @Test
    void theRoomGrowsWithTheWindowAndNeverGoesNegativeOrOverflows() {
        assertEquals(0, DiscussionRoom.perResultChars(OptionalLong.of(0)));
        assertEquals(0, DiscussionRoom.chars(OptionalLong.of(3)));
        assertTrue(DiscussionRoom.chars(OptionalLong.of(Long.MAX_VALUE)) > 0, "clamped, not wrapped");
        assertTrue(DiscussionRoom.perResultChars(OptionalLong.of(65_536))
                > DiscussionRoom.perResultChars(OptionalLong.of(32_768)));
    }
}
