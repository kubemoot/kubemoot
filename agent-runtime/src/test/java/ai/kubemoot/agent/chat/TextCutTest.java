package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class TextCutTest {

    private static final String EMOJI = "\uD83D\uDE00";

    @Test
    void aCutNeverSplitsASurrogatePair() {
        assertEquals("ab", TextCut.prefix("ab" + EMOJI + "cd", 3));
        assertEquals("ab" + EMOJI, TextCut.prefix("ab" + EMOJI + "cd", 4));
        assertEquals("ab" + TextCut.MARKER, TextCut.truncate("ab" + EMOJI + "cd", 3));
    }

    @Test
    void shortTextNullAndNonPositiveRoomAreHandled() {
        assertEquals("abc", TextCut.truncate("abc", 3));
        assertEquals("", TextCut.truncate(null, 3));
        assertEquals("", TextCut.prefix("abc", 0));
        assertEquals("", TextCut.prefix(null, 3));
    }
}
