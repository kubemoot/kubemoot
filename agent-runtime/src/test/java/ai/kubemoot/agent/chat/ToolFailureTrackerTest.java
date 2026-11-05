package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.chat.ToolFailureTracker.Verdict;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class ToolFailureTrackerTest {

    private static ToolFailureTracker tracker() {
        return new ToolFailureTracker(ChatService.MAX_SAME_TOOL_FAILURES, ChatService.MAX_CORRECTING_TOOL_FAILURES);
    }

    @Test
    void firstFailureContinues() {
        assertEquals(Verdict.CONTINUE, tracker().record("{\"q\":1}", "boom", false));
    }

    @Test
    void sameCallRepeatedStops() {
        var t = tracker();
        t.record("{\"q\":1}", "error one", false);
        assertEquals(Verdict.STUCK, t.record("{\"q\":1}", "error two", false));
        assertEquals(2, t.failures());
    }

    @Test
    void sameCallWithDifferentWhitespaceAndKeyOrderStops() {
        var t = tracker();
        t.record("{\"a\": 1, \"b\": 2}", "error one", false);
        assertEquals(Verdict.STUCK, t.record("{\"b\":2,\"a\":1}", "error two", false));
    }

    @Test
    void sameErrorWithDifferentArgumentsStops() {
        var t = tracker();
        t.record("{\"q\":1}", "request timed out", false);
        assertEquals(Verdict.STUCK, t.record("{\"q\":2}", "request  timed\nout", false));
    }

    @Test
    void refusedRepeatCountsAsRepeat() {
        var t = tracker();
        t.record("{\"q\":1}", "error one", false);
        assertEquals(Verdict.STUCK, t.record("{\"q\":2}", "refused", true));
    }

    @Test
    void firstFailureThatIsARefusalContinues() {
        assertEquals(Verdict.CONTINUE, tracker().record("{\"q\":1}", "refused", true));
    }

    @Test
    void correctionsContinueUpToTheBudgetThenStop() {
        var t = tracker();
        for (int i = 1; i < ChatService.MAX_CORRECTING_TOOL_FAILURES; i++) {
            assertEquals(Verdict.CONTINUE, t.record("{\"q\":" + i + "}", "no such column c" + i, false));
        }
        assertEquals(Verdict.CORRECTIONS_EXHAUSTED,
                t.record("{\"q\":99}", "no such column c99", false));
        assertEquals(ChatService.MAX_CORRECTING_TOOL_FAILURES, t.failures());
    }

    @Test
    void nullArgumentsAndEmptyErrorTextAreHandled() {
        var t = tracker();
        assertEquals(Verdict.CONTINUE, t.record(null, "", false));
        assertEquals(Verdict.STUCK, t.record(null, null, false));
    }

    @Test
    void emptyErrorTextTwiceWithNewArgumentsStops() {
        var t = tracker();
        t.record("{\"q\":1}", "", false);
        assertEquals(Verdict.STUCK, t.record("{\"q\":2}", "   ", false));
    }
}
