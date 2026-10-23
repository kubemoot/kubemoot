package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class ToolResultBudgetTest {

    /** A ratio at or above the conservative one, so budget math uses 3 characters per token. */
    private static CharsPerToken defaultRatio() {
        return new CharsPerToken();
    }

    @Test
    void unknownWindow_zeroAndNegative_admitEverything() {
        for (long window : new long[] {0, -1}) {
            var budget = new ToolResultBudget(window, 0, defaultRatio());
            var admission = budget.admit("t", "x".repeat(1_000_000));
            assertFalse(admission.replaced(), "window " + window);
            assertEquals(1_000_000, admission.text().length());
        }
    }

    @Test
    void windowBelowTheReserve_hasNoRoom_andReplacesEvenASmallResult() {
        var budget = new ToolResultBudget(500, 0, defaultRatio());

        var admission = budget.admit("t", "x".repeat(100));

        assertTrue(admission.replaced());
        assertEquals(0, admission.roomTokens());
    }

    @Test
    void resultExactlyEqualToTheRoom_isKept_andOneMoreCharacterIsNot() {
        // Window 8192, no prompt: room is 8192 - 1024 = 7168 tokens = 21504 characters at 3 per token,
        // counting the tool name's 4 characters.
        var fits = new ToolResultBudget(8_192, 0, defaultRatio()).admit("tool", "x".repeat(21_500));
        var over = new ToolResultBudget(8_192, 0, defaultRatio()).admit("tool", "x".repeat(21_501));

        assertFalse(fits.replaced());
        assertEquals(7_168, fits.resultTokens());
        assertTrue(over.replaced());
    }

    @Test
    void nullResult_isTreatedAsEmpty() {
        var admission = new ToolResultBudget(8_192, 0, defaultRatio()).admit("tool", null);

        assertFalse(admission.replaced());
        assertNull(admission.text());
    }

    @Test
    void roomGoingNegativeAfterAReplacement_replacesEveryLaterResultWithTheShortError() {
        var budget = new ToolResultBudget(8_192, 8_000, defaultRatio());

        var first = budget.admit("resources_list", "x".repeat(10_000));
        var second = budget.admit("resources_list", "y");
        var third = budget.admit("resources_list", "y");

        assertTrue(first.replaced());
        assertEquals(ToolResultBudget.shortError("resources_list"), first.text());
        assertEquals(ToolResultBudget.shortError("resources_list"), second.text());
        assertEquals(0, third.roomTokens());
        assertTrue(third.replaced());
    }

    @Test
    void replyReserve_isTheLargerOfTheMinimumAndAnEighthOfTheWindow() {
        assertEquals(1_024, ToolResultBudget.replyReserve(8_192));
        assertEquals(4_096, ToolResultBudget.replyReserve(32_768));
        assertEquals(1_024, ToolResultBudget.replyReserve(100));
    }

    @Test
    void fullError_namesTheToolSizeRoomAndTheFilters() {
        var budget = new ToolResultBudget(32_768, 7_000, defaultRatio());

        var admission = budget.admit("resources_list", "x".repeat(300_000));

        assertEquals("{\"error\": \"Tool result larger than the room left in the context window: resources_list"
                + " returned about 100005 tokens and about 21672 tokens are left. The result was not kept."
                + " Call resources_list again with a filter: a namespace, a label selector, a field selection,"
                + " a limit, or a shorter time range.\"}", admission.text());
    }

    @Test
    void conservativeRatio_countsAJsonResultAtThreeCharactersPerTokenEvenWhenTheMeasuredRatioIsHigher() {
        var ratio = new CharsPerToken();
        ratio.observe(7_000, 1_000); // 7 characters per token measured
        var budget = new ToolResultBudget(8_192, 0, ratio);

        assertEquals(7_168, budget.admit("tool", "x".repeat(21_500)).resultTokens());
    }

    @Test
    void errorJson_escapesQuotesBackslashesAndControlCharacters() {
        assertEquals("{\"error\": \"a\\\"b\\\\c\\nd\\u0001\"}", ToolResultBudget.errorJson("a\"b\\c\nd\u0001"));
    }
}
