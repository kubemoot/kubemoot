package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class ToolResultBudgetTest {

    /** A ratio at or above the conservative one, so budget math uses 3 characters per token. */
    private static CharsPerToken defaultRatio() {
        return new CharsPerToken();
    }

    @Test
    void aNumericStandIn_usesUpMoreRoomThanItsLength_andAnErrorReplacementUsesExactlyItsLength() {
        String numeric = "{\"cpu\":[0.12,0.34,0.56],\"mem\":[1048576,2097152]}\n".repeat(100_000);
        var ratio = defaultRatio();
        var budget = new ToolResultBudget(32_768, 0, ratio);
        var first = budget.admit("t", numeric, true);
        assertTrue(first.replaced());
        var second = budget.admit("t", "{}", true);
        long used = first.roomTokens() - second.roomTokens();
        assertEquals(ratio.tokens(1 + first.text().length() + ToolResultStandIn.extraChars(first.text())), used);
        assertTrue(used > ratio.tokens(1 + first.text().length()));

        var errors = new ToolResultBudget(32_768, 0, ratio);
        var error = errors.admit("t", numeric, false);
        assertTrue(error.replaced());
        long errorUsed = error.roomTokens() - errors.admit("t", "{}", false).roomTokens();
        assertEquals(ratio.tokens(1 + error.text().length()), errorUsed);
    }

    @Test
    void unknownWindow_zeroAndNegative_admitEverything() {
        for (long window : new long[] {0, -1}) {
            var budget = new ToolResultBudget(window, 0, defaultRatio());
            var admission = budget.admit("t", "x".repeat(1_000_000), false);
            assertFalse(admission.replaced(), "window " + window);
            assertEquals(1_000_000, admission.text().length());
        }
    }

    @Test
    void windowBelowTheReserve_hasNoRoom_andReplacesEvenASmallResult() {
        var budget = new ToolResultBudget(500, 0, defaultRatio());

        var admission = budget.admit("t", "x".repeat(100), false);

        assertTrue(admission.replaced());
        assertEquals(0, admission.roomTokens());
    }

    @Test
    void resultExactlyEqualToTheRoom_isKept_andOneMoreCharacterIsNot() {
        // Window 8192, no prompt: room is 8192 - 1024 = 7168 tokens = 21504 characters at 3 per token,
        // counting the tool name's 4 characters.
        var fits = new ToolResultBudget(8_192, 0, defaultRatio()).admit("tool", "x".repeat(21_500), false);
        var over = new ToolResultBudget(8_192, 0, defaultRatio()).admit("tool", "x".repeat(21_501), false);

        assertFalse(fits.replaced());
        assertEquals(7_168, fits.resultTokens());
        assertTrue(over.replaced());
    }

    @Test
    void nullResult_isTreatedAsEmpty() {
        var admission = new ToolResultBudget(8_192, 0, defaultRatio()).admit("tool", null, false);

        assertFalse(admission.replaced());
        assertNull(admission.text());
    }

    @Test
    void roomGoingNegativeAfterAReplacement_replacesEveryLaterResultWithTheShortError() {
        var budget = new ToolResultBudget(8_192, 8_000, defaultRatio());

        var first = budget.admit("resources_list", "x".repeat(10_000), false);
        var second = budget.admit("resources_list", "y", false);
        var third = budget.admit("resources_list", "y", false);

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

        var admission = budget.admit("resources_list", "x".repeat(300_000), false);

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

        assertEquals(7_168, budget.admit("tool", "x".repeat(21_500), false).resultTokens());
    }

    @Test
    void errorJson_escapesQuotesBackslashesAndControlCharacters() {
        assertEquals("{\"error\": \"a\\\"b\\\\c\\nd\\u0001\"}", ToolResultBudget.errorJson("a\"b\\c\nd\u0001"));
    }

    @Test
    void tooler_resultOverTheRoom_getsAStandInNotAnError() {
        var budget = new ToolResultBudget(32_768, 1_000, defaultRatio());
        String result = "{\"items\": [" + "{\"n\":1},".repeat(20_000) + "{}]}";

        var admission = budget.admit("pods_list", result, true);

        assertTrue(admission.replaced());
        assertTrue(admission.text().startsWith("[pods_list] The full result is kept for the crew"), admission.text());
        assertTrue(admission.text().contains("20001 items"), admission.text());
    }

    @Test
    void tooler_resultThatFits_isAdmittedUnchanged() {
        var admission = new ToolResultBudget(32_768, 1_000, defaultRatio()).admit("pods_list", "small", true);

        assertFalse(admission.replaced());
        assertEquals("small", admission.text());
    }

    @Test
    void tooler_theThresholdIsTheWindowMinusThePromptMinusTheReserve() {
        // Window 32768, prompt 20000, reserve 4096: 8672 tokens of room, 26016 characters at 3 per token
        // counting the 4 characters of the tool name.
        long room = 32_768 - 20_000 - ToolResultBudget.replyReserve(32_768);
        assertEquals(8_672, room);
        var fits = new ToolResultBudget(32_768, 20_000, defaultRatio()).admit("tool", "x".repeat(26_012), true);
        var over = new ToolResultBudget(32_768, 20_000, defaultRatio()).admit("tool", "x".repeat(26_013), true);

        assertFalse(fits.replaced());
        assertTrue(over.replaced());
        assertEquals(8_672, over.roomTokens());
    }

    @Test
    void tooler_standInKeepsTheHeadWhileTwiceTheReserveIsLeft() {
        // Window 32768, reserve 4096: the room after the reserve is 4096 or more.
        var budget = new ToolResultBudget(32_768, 24_576, defaultRatio());

        var admission = budget.admit("pods_list", "row\n".repeat(60_000), true);

        assertTrue(admission.replaced());
        assertTrue(admission.text().contains("First " + ToolResultStandIn.HEAD_LINES + " lines"), admission.text());
    }

    @Test
    void tooler_standInDropsTheHeadOnceLessThanTwiceTheReserveIsLeft() {
        // Room after the reserve is 4095 tokens, one under the reserve.
        var budget = new ToolResultBudget(32_768, 24_577, defaultRatio());

        var admission = budget.admit("pods_list", "row\n".repeat(60_000), true);

        assertTrue(admission.replaced());
        assertFalse(admission.text().contains("First "), admission.text());
        assertTrue(admission.text().contains("Size: 240000 bytes, 60000 lines."), admission.text());
        assertTrue(admission.text().contains("stop: the crew receives all of it"), admission.text());
    }
}
