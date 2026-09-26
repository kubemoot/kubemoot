package kubemoot.ai.mcpgateway.controller;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;

/**
 * Focused unit tests for {@link ToolController#extractContent(Object)}.
 * Plain JUnit (no Spring context) so the pure extraction logic is exercised directly.
 */
class ToolControllerTest {

    @Test
    void nullResultReturnsEmptyString() {
        assertEquals("", ToolController.extractContent(null));
    }

    @Test
    void singleTextItemIsExtracted() {
        Object result = Map.of("content", List.of(Map.of("type", "text", "text", "hello")));
        assertEquals("hello", ToolController.extractContent(result));
    }

    @Test
    void multipleTextItemsAreJoinedByNewline() {
        Object result = Map.of("content", List.of(
            Map.of("text", "line1"),
            Map.of("text", "line2")
        ));
        assertEquals("line1\nline2", ToolController.extractContent(result));
    }

    @Test
    void itemsWithoutTextAreSkipped() {
        java.util.List<Object> items = new java.util.ArrayList<>();
        items.add(Map.of("type", "image"));
        items.add(Map.of("text", "kept"));
        items.add("not-a-map");
        Object result = Map.of("content", items);
        assertEquals("kept", ToolController.extractContent(result));
    }

    @Test
    void allItemsWithoutTextYieldEmptyString() {
        Object result = Map.of("content", List.of(Map.of("type", "image")));
        assertEquals("", ToolController.extractContent(result));
    }

    @Test
    void mapWithoutListContentFallsBackToToString() {
        Map<String, Object> result = Map.of("content", "plain-string");
        assertEquals(result.toString(), ToolController.extractContent(result));
    }

    @Test
    void nonMapResultFallsBackToToString() {
        assertEquals("42", ToolController.extractContent(42));
    }

    // --- sanitizeArguments: drop an invalid now-ish time on execute_query ---

    @Test
    void sanitizeDropsNowParenTimeOnExecuteQuery() {
        Map<String, Object> args = Map.of("query", "rate(apiserver_request_total[1m])", "time", "now()");
        Map<String, Object> out = ToolController.sanitizeArguments("execute_query", args);
        assertFalse(out.containsKey("time"), "invalid time=now() must be dropped so the query runs at now");
        assertEquals("rate(apiserver_request_total[1m])", out.get("query"), "the query is preserved");
    }

    @Test
    void sanitizeDropsBareNowAndEmptyTime() {
        assertFalse(ToolController.sanitizeArguments("execute_query",
                new java.util.HashMap<>(Map.of("query", "up", "time", "now"))).containsKey("time"), "time=now dropped");
        assertFalse(ToolController.sanitizeArguments("execute_query",
                new java.util.HashMap<>(Map.of("query", "up", "time", " NOW() "))).containsKey("time"),
                "whitespace/case-insensitive now() dropped");
        Map<String, Object> emptyTime = new java.util.HashMap<>();
        emptyTime.put("query", "up");
        emptyTime.put("time", "");
        assertFalse(ToolController.sanitizeArguments("execute_query", emptyTime).containsKey("time"), "empty time dropped");
    }

    @Test
    void sanitizeKeepsValidTimestamp() {
        Map<String, Object> args = Map.of("query", "up", "time", "2026-07-08T00:00:00Z");
        assertEquals(args, ToolController.sanitizeArguments("execute_query", args),
                "a real RFC3339 timestamp is left untouched");
    }

    @Test
    void sanitizeLeavesOtherToolsAndRangeQueryUntouched() {
        Map<String, Object> range = Map.of("query", "up", "start", "now", "end", "now");
        assertEquals(range, ToolController.sanitizeArguments("execute_range_query", range),
                "range query start/end are required and must not be stripped");
        Map<String, Object> listArgs = Map.of("filter_pattern", "apiserver", "time", "now()");
        assertEquals(listArgs, ToolController.sanitizeArguments("list_metrics", listArgs),
                "non-query tools are untouched");
    }
}
