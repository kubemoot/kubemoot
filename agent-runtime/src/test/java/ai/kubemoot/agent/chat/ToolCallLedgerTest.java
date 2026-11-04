package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

class ToolCallLedgerTest {

    private static final Instant T0 = Instant.parse("2026-11-01T12:00:00Z");

    private final ChatServiceCallFilesTest.FakeStore store = new ChatServiceCallFilesTest.FakeStore();
    private final ToolCallLedger ledger = new ToolCallLedger(store, "th", "agent-x");

    private String record(String tool, String args, String result, boolean error) {
        return ledger.record(new ToolCallLedger.Call(tool, "id", args, T0, 12), result, error, 1_000_000);
    }

    @Test
    void theManifestIsTheSameTextForTheSameCalls() {
        record("pods_list", "{\"namespace\":\"a\"}", "[{\"x\":1}]", false);
        var other = new ToolCallLedger(new ChatServiceCallFilesTest.FakeStore(), "th", "agent-x");
        other.record(new ToolCallLedger.Call("pods_list", "id", "{\"namespace\":\"a\"}", T0, 99),
                "[{\"x\":1}]", false, 1_000_000);

        assertEquals(other.manifest(), ledger.manifest(), "time and duration are not in the manifest");
    }

    @Test
    void theManifestNamesThePrefixAndHowToReadAToolAsOneTable() {
        record("pods_list", "{}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("prefix=ns/crew/th/agent-x/]"), manifest);
        assertTrue(manifest.contains("ns/crew/th/agent-x/*-<tool>"), manifest);
        assertTrue(manifest.contains("workspace_query"), manifest);
    }

    @Test
    void anOverlongManifestDropsArgumentsThenOldestLinesAndStaysInline() {
        for (int i = 0; i < 120; i++) {
            record("tool_number_" + i, "{\"argument\":\"" + "v".repeat(200) + "\"}", "[{\"x\":1}]", false);
        }

        String manifest = ledger.manifest();

        assertTrue(manifest.length() <= ToolCallLedger.MAX_MANIFEST_CHARS, "length " + manifest.length());
        assertTrue(manifest.contains("calls=120"), "the header counts every call");
        assertTrue(manifest.contains("more calls under the prefix]"), manifest);
        assertTrue(manifest.contains("tool_number_0 "), "the first calls are listed");
    }

    @Test
    void aFewCallsKeepTheirArgumentsCutToASummary() {
        record("t", "{\"a\":\n   \"" + "z".repeat(200) + "\"}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("args={\"a\": \"" + "z".repeat(ToolCallLedger.ARGS_SUMMARY_CHARS - 7) + "..."),
                manifest);
        assertFalse(manifest.contains("\n   "), "arguments are on one line");
    }

    @Test
    void squareBracketsInArgumentsCannotEndTheCallLineEarly() {
        record("t", "{\"ids\":[1,2]}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("args={\"ids\":(1,2)}"), manifest);
        var line = CallManifest.CALL_LINE.matcher(manifest);
        assertTrue(line.find());
        assertEquals("t", line.group(2));
        assertEquals(manifest.indexOf("error=false]") + "error=false]".length(), line.end(), "the whole line matches");
    }

    @Test
    void emptyArgumentsShowAsAnEmptyObject() {
        assertEquals("{}", ToolCallLedger.summary(null));
        assertEquals("{}", ToolCallLedger.summary("  "));
    }

    @Test
    void aResultOverTheCapIsCutMarkedAndFlagged() {
        String key = ledger.record(new ToolCallLedger.Call("t", "i", "{}", T0, 1), "[" + "1,".repeat(50) + "1]",
                false, 20);

        var h = store.headers.get(key);
        assertEquals("true", h.get(CallHeaders.TRUNCATED));
        assertEquals("text", h.get(CallHeaders.FORMAT), "a cut list is no longer valid JSON");
        assertTrue(store.body(key).endsWith(" ...[truncated]"));
    }

    @Test
    void argumentsInTheHeaderAreCut() {
        String key = record("t", "x".repeat(5_000), "[]", false);

        assertEquals(CallHeaders.MAX_ARGUMENTS_CHARS + " ...[truncated]".length(),
                store.headers.get(key).get(CallHeaders.TOOL_CALL_ARGUMENTS).length());
    }

    @Test
    void aCallWhoseWriteFailedIsCountedAndNamedNotStored() {
        assertFalse(ledger.holdsOutput());
        record("t", "{}", "[]", false);
        assertTrue(ledger.holdsOutput());
        store.failWrites = true;
        assertNull(record("u", "{\"a\":1}", "[]", true));
        assertEquals(2, ledger.size());

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("calls=2 errors=1"), manifest);
        assertTrue(manifest.contains("[NOT STORED tool=u args={\"a\":1} error=true"), manifest);
        assertTrue(CallManifest.NOT_STORED_LINE.matcher(manifest).find());
        assertTrue(manifest.contains("prefix=" + ledger.prefix()) && !ledger.prefix().isEmpty(), manifest);
    }

    @Test
    void aLedgerWhoseEveryWriteFailedHasNoPrefixAndStillDescribesTheCalls() {
        store.failWrites = true;
        record("t", "{}", "[]", false);

        String manifest = ledger.manifest();

        assertEquals("", ledger.prefix());
        assertFalse(manifest.contains("*-<tool>"), "there is no key pattern to name: " + manifest);
        assertTrue(manifest.contains("[NOT STORED tool=t"), manifest);
        assertFalse(manifest.contains("[CALL key="), manifest);
    }

    @Test
    void shapeReadsJsonListsAndKubernetesLists() {
        assertEquals(new ToolResultShape("json", 2), ToolResultShape.of("[1,2]"));
        assertEquals(new ToolResultShape("json", 1), ToolResultShape.of(" {\"items\":[{}]}"));
        assertEquals(new ToolResultShape("json", -1, java.util.List.of("error")), ToolResultShape.of("{\"error\":\"x\"}"));
        assertEquals(new ToolResultShape("text", -1), ToolResultShape.of("{broken"));
        assertEquals(new ToolResultShape("text", -1), ToolResultShape.of("NAME"), "one column is not a table");
        assertEquals(new ToolResultShape("text", -1), ToolResultShape.of(null));
    }

    @Test
    void shapeReadsTheColumnsOfATableAndTheFieldsOfAJsonList() {
        var table = ToolResultShape.of("NAME      READY   NOMINATED NODE\npod-a     1/1     <none>\n");
        assertEquals("text-table", table.format());
        assertEquals(java.util.List.of("NAME", "READY", "NOMINATED NODE"), table.columns());

        var list = ToolResultShape.of("[{\"name\":\"a\",\"ns\":\"x\"},{\"name\":\"b\",\"extra\":1}]");
        assertEquals("json", list.format());
        assertEquals(2, list.rows());
        assertEquals(java.util.List.of("name", "ns"), list.columns(), "the first row's keys");

        assertEquals(java.util.List.of("metadata", "spec"), ToolResultShape.of("{\"items\":[{\"metadata\":{},\"spec\":{}}]}").columns());
        assertEquals("text", ToolResultShape.of("Error: pods is forbidden\nsecond line").format());
        assertEquals("text", ToolResultShape.of("Name  Ready\nx  y").format(), "titles are upper case");
    }

    @Test
    void theManifestLineSaysTheFormatAndNamesTheColumns() {
        record("pods_list", "{}", "NAME      READY   STATUS\npod-a     1/1     Running\n", false);
        record("nodes_list", "{}", "[{\"name\":\"n1\",\"taints\":[]}]", false);
        record("logs", "{}", "plain log line", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("format=text-table bytes=") && manifest.contains("columns=NAME,READY,STATUS]"), manifest);
        assertTrue(manifest.contains("format=json bytes=") && manifest.contains("fields=name,taints]"), manifest);
        assertTrue(manifest.contains("tool=logs args={} format=text bytes=14 rows=unknown error=false]"), manifest);
    }

    @Test
    void aLongColumnListAndLongNamesAreCutAndABracketCannotEndTheLine() {
        String header = java.util.stream.IntStream.rangeClosed(1, 14).mapToObj(i -> "COL" + i)
                .collect(java.util.stream.Collectors.joining("  "));
        record("wide", "{}", header + "\nrow\n", false);
        record("odd", "{}", "[{\"" + "k".repeat(40) + "\":1,\"a]b\":2}]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("columns=COL1,COL2,COL3,COL4,COL5,COL6,COL7,COL8,COL9,COL10,COL11,COL12,+2more]"), manifest);
        assertTrue(manifest.contains("fields=" + "k".repeat(30) + "...,a)b]"), manifest);
        var lines = CallManifest.CALL_LINE.matcher(manifest);
        int found = 0;
        while (lines.find()) {
            assertTrue(lines.group().endsWith("]") && lines.group().indexOf(']') == lines.group().length() - 1, lines.group());
            found++;
        }
        assertEquals(2, found);
    }

    @Test
    void aCutResultIsTextWithNoFields() {
        ledger.record(new ToolCallLedger.Call("t", "i", "{}", T0, 1), "[{\"a\":1}," + "{\"a\":1},".repeat(50) + "{\"a\":1}]",
                false, 20);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("format=text "), manifest);
        assertFalse(manifest.contains("fields="), manifest);
    }

    @Test
    void aTightManifestDropsArgumentsBeforeColumns() {
        for (int i = 0; i < 20; i++) {
            record("tool_number_" + i, "{\"argument\":\"" + "v".repeat(200) + "\"}",
                    "NAME  READY  STATUS\nx  1/1  Running\n", false);
        }

        String manifest = ledger.manifest();

        assertTrue(manifest.length() <= ToolCallLedger.MAX_MANIFEST_CHARS, "length " + manifest.length());
        assertFalse(manifest.contains("args="), manifest);
        assertTrue(manifest.contains("columns=NAME,READY,STATUS"), manifest);
    }

    @Test
    void theFooterNamesTheWaysToReadAKeyIncludingExecuteCode() {
        record("pods_list", "{}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("workspace_query") && manifest.contains("artifact_*")
                && manifest.contains("execute_code over the file /artifacts/<key>"), manifest);
    }

    @Test
    void headersAreInTheOrderTheyAreDocumented() {
        String key = record("t", "{}", "[]", true);
        Map<String, String> h = new LinkedHashMap<>(store.headers.get(key));

        assertEquals("gen_ai.operation.name", h.keySet().iterator().next());
        assertEquals("tool_error", h.get("error.type"));
    }
}
