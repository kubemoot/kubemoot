package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class ToolCallRepeatGuardTest {

    @Test
    void anIdenticalCallIsAllowedTwice_andRefusedFromTheThirdTime() {
        var guard = new ToolCallRepeatGuard();

        assertFalse(guard.refuses("pods_list", "{\"a\":1}"));
        assertFalse(guard.refuses("pods_list", "{\"a\":1}"));
        assertTrue(guard.refuses("pods_list", "{\"a\":1}"));
        assertTrue(guard.refuses("pods_list", "{\"a\":1}"));
    }

    @Test
    void differentArgumentsOrADifferentToolIsADifferentCall() {
        var guard = new ToolCallRepeatGuard();
        for (int i = 0; i < 5; i++) {
            assertFalse(guard.refuses("pods_list", "{\"page\":" + i + "}"));
        }
        assertFalse(guard.refuses("pods_list", "{\"a\":1}"));
        assertFalse(guard.refuses("nodes_list", "{\"a\":1}"));
    }

    @Test
    void keyOrderAndWhitespaceDoNotMakeACallDifferent() {
        var guard = new ToolCallRepeatGuard();

        assertFalse(guard.refuses("t", "{\"a\":1,\"b\":{\"y\":2,\"x\":[1,2]}}"));
        assertFalse(guard.refuses("t", "{ \"b\": {\"x\": [1, 2], \"y\": 2}, \"a\": 1 }"));
        assertTrue(guard.refuses("t", "{\"b\":{\"x\":[1,2],\"y\":2},\"a\":1}"));
    }

    @Test
    void arrayOrderStillMakesACallDifferent() {
        assertNotEquals(ToolCallRepeatGuard.canonical("[1,2]"), ToolCallRepeatGuard.canonical("[2,1]"));
    }

    @Test
    void emptyNullAndNonJsonArguments_areComparedAsText() {
        assertEquals("", ToolCallRepeatGuard.canonical(null));
        assertEquals("", ToolCallRepeatGuard.canonical("  "));
        assertEquals("not json", ToolCallRepeatGuard.canonical("  not json "));
        var guard = new ToolCallRepeatGuard();
        assertFalse(guard.refuses("t", null));
        assertFalse(guard.refuses("t", ""));
        assertTrue(guard.refuses("t", null));
    }

    @Test
    void refusal_isAToolErrorThatNamesTheToolAndWhatToChange() {
        String refusal = ToolCallRepeatGuard.refusal("pods_list");

        assertTrue(refusal.startsWith("{\"error\":"), refusal);
        assertTrue(refusal.contains("pods_list") && refusal.contains("Change the arguments"), refusal);
    }
}
