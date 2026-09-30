package ai.kubemoot.agent.util;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

class ReplySentinelsTest {

    @Test
    void after_returnsTheTrimmedTextAfterAnOpeningSentinel() {
        assertEquals("the list stops at 22", ReplySentinels.after("  CONCERN:  the list stops at 22 ", ReplySentinels.CONCERN));
        assertEquals("", ReplySentinels.after("CONCUR:", ReplySentinels.CONCUR));
    }

    @Test
    void after_isNullWhenTheReplyDoesNotOpenWithTheSentinel() {
        assertNull(ReplySentinels.after("I CONCUR: fine", ReplySentinels.CONCUR));
        assertNull(ReplySentinels.after("concur: fine", ReplySentinels.CONCUR), "case-sensitive");
        assertNull(ReplySentinels.after(null, ReplySentinels.TOOL_GAP));
        assertNull(ReplySentinels.after("", ReplySentinels.CONCERN));
    }
}
