package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class ToolResultStandInTest {

    @Test
    void standIn_namesTheToolTheSizeAndTheFirstLines() {
        String result = "line one\nline two\nline three\n";

        String standIn = ToolResultStandIn.of("pods_list", result);

        assertTrue(standIn.startsWith("[pods_list] The full result is kept for the crew"), standIn);
        assertTrue(standIn.contains("Size: 29 bytes, 3 lines."), standIn);
        assertTrue(standIn.contains("line one\nline two\nline three"), standIn);
        assertTrue(standIn.contains("If this is the data you asked for, stop"), standIn);
    }

    @Test
    void standIn_countsItemsOfAJsonListAndOfAnItemsObject() {
        assertEquals(3, ToolResultStandIn.itemCount("[1,2,3]"));
        assertEquals(2, ToolResultStandIn.itemCount("  {\"kind\":\"PodList\",\"items\":[{},{}]}"));
        assertTrue(ToolResultStandIn.of("t", "[1,2,3]").contains(", 3 items."));
    }

    @Test
    void itemCount_isAbsentForOtherShapes() {
        assertEquals(-1, ToolResultStandIn.itemCount("NAME READY\npod-1 1/1"));
        assertEquals(-1, ToolResultStandIn.itemCount("{\"items\": \"not a list\"}"));
        assertEquals(-1, ToolResultStandIn.itemCount("{\"no\": \"items\"}"));
        assertEquals(-1, ToolResultStandIn.itemCount("{broken json"));
        assertEquals(-1, ToolResultStandIn.itemCount(""));
        assertFalse(ToolResultStandIn.of("t", "plain text").contains("items"));
    }

    @Test
    void head_stopsAtTheLineLimit() {
        String text = "l\n".repeat(50);

        assertEquals(ToolResultStandIn.HEAD_LINES, ToolResultStandIn.head(text).split("\n").length);
    }

    @Test
    void head_stopsAtTheCharacterLimitWhenOneLineIsHuge() {
        String head = ToolResultStandIn.head("y".repeat(200_000));

        assertEquals(ToolResultStandIn.HEAD_CHARS, head.length());
    }

    @Test
    void emptyAndNullResults_haveAStandInToo() {
        assertTrue(ToolResultStandIn.of("t", "").contains("Size: 0 bytes, 0 lines."));
        assertTrue(ToolResultStandIn.of("t", null).contains("Size: 0 bytes, 0 lines."));
    }

    @Test
    void lineCount_countsALastLineWithoutANewline_andNotATrailingNewline() {
        assertEquals(0, ToolResultStandIn.lineCount(""));
        assertEquals(1, ToolResultStandIn.lineCount("a"));
        assertEquals(1, ToolResultStandIn.lineCount("a\n"));
        assertEquals(2, ToolResultStandIn.lineCount("a\nb"));
    }

    @Test
    void size_isCountedInBytesNotCharacters() {
        assertTrue(ToolResultStandIn.of("t", "\u00e9\u00e9").contains("Size: 4 bytes"));
    }
}
