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

    /** The manifest without the results posted in it: the descriptions the size cap applies to. */
    private static String descriptions(String manifest) {
        return CallManifest.RESULT_BLOCK.matcher(manifest).replaceAll("");
    }

    private String recordCall(String tool, String args, String result, boolean error) {
        return ledger.recordCall(new ToolCallLedger.Call(tool, "id", args, T0, 12), result, error, 1_000_000);
    }

    @Test
    void aCallWhoseNumberCannotBeReservedIsListedAsNotStoredAndWritesNothing() {
        var unreadable = new ChatServiceCallFilesTest.FakeStore();
        unreadable.failReserve = true;
        var l = new ToolCallLedger(unreadable, "th", "agent-x");

        String key = l.recordCall(new ToolCallLedger.Call("pods_list", "id", "{}", T0, 12), "[{\"x\":1}]", false, 1_000_000);

        assertNull(key);
        assertTrue(unreadable.bodies.isEmpty(), "nothing is written under a number that could repeat an earlier one");
        assertTrue(l.manifest().contains("NOT STORED"), l.manifest());
    }

    @Test
    void theManifestIsTheSameTextForTheSameCalls() {
        recordCall("pods_list", "{\"namespace\":\"a\"}", "[{\"x\":1}]", false);
        var other = new ToolCallLedger(new ChatServiceCallFilesTest.FakeStore(), "th", "agent-x");
        other.recordCall(new ToolCallLedger.Call("pods_list", "id", "{\"namespace\":\"a\"}", T0, 99),
                "[{\"x\":1}]", false, 1_000_000);

        assertEquals(other.manifest(), ledger.manifest(), "time and duration are not in the manifest");
    }

    @Test
    void theManifestNamesThePrefixAndHowToReadAToolAsOneTable() {
        recordCall("pods_list", "{}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("prefix=agent-x/]"), manifest);
        assertTrue(manifest.contains("the key agent-x/*-<tool>"), manifest);
        assertFalse(manifest.contains("ns/crew/th"), "keys are relative to the thread: " + manifest);
        assertTrue(manifest.contains("workspace_query"), manifest);
    }

    @Test
    void anOverlongManifestDropsArgumentsThenOldestLinesAndStaysInline() {
        for (int i = 0; i < 120; i++) {
            recordCall("tool_number_" + i, "{\"argument\":\"" + "v".repeat(200) + "\"}", "[{\"x\":1}]", false);
        }

        String manifest = descriptions(ledger.manifest());

        assertTrue(manifest.length() <= ToolCallLedger.MAX_MANIFEST_CHARS, "length " + manifest.length());
        assertTrue(manifest.contains("calls=120"), "the header counts every call");
        assertTrue(manifest.contains("more calls under the prefix]"), manifest);
        assertTrue(manifest.contains("tool_number_0 "), "the first calls are listed");
    }

    @Test
    void aFewCallsKeepTheirArgumentsCutToASummary() {
        recordCall("t", "{\"a\":\n   \"" + "z".repeat(200) + "\"}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("args={\"a\": \"" + "z".repeat(CallManifest.ARGS_SUMMARY_CHARS - 7) + "... (args cut at "
                + CallManifest.ARGS_SUMMARY_CHARS + " of 209 chars)"), manifest);
        assertFalse(manifest.contains("\n   "), "arguments are on one line");
    }

    @Test
    void argumentsUnderTheLimitAreNotCutAndCarryNoMarker() {
        String args = "{\"k\": \"" + "a".repeat(CallManifest.ARGS_SUMMARY_CHARS - 9) + "\"}";
        assertEquals(CallManifest.ARGS_SUMMARY_CHARS, args.length());
        recordCall("t", args, "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains(" args=" + args + " type="), manifest);
        assertFalse(manifest.contains("cut at"), manifest);
        assertFalse(manifest.contains("..."), manifest);
    }

    @Test
    void argumentsOneOverTheLimitCarryTheMarker() {
        String args = "a".repeat(CallManifest.ARGS_SUMMARY_CHARS + 1);
        recordCall("t", args, "[]", false);

        assertTrue(ledger.manifest().contains(" args=" + "a".repeat(CallManifest.ARGS_SUMMARY_CHARS)
                + "... (args cut at 60 of 61 chars) type="), ledger.manifest());
    }

    @Test
    void unicodeArgumentsAreCutOnACharacterBoundaryAndCountedInCharacters() {
        String emoji = new String(Character.toChars(0x1F600));
        String args = emoji.repeat(CallManifest.ARGS_SUMMARY_CHARS + 5);
        recordCall("t", args, "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains(" args=" + emoji.repeat(CallManifest.ARGS_SUMMARY_CHARS)
                + "... (args cut at 60 of 65 chars) type="), manifest);
        assertFalse(manifest.contains("\uFFFD"), "no split surrogate pair");
    }

    @Test
    void manyEmptyCallsStayWithinTheSizeOfAContributionAndAreCounted() {
        for (int i = 0; i < 100; i++) {
            recordCall("workspace_grep", "{\"pattern\":\"p" + i + "\"}", "", false);
        }

        String manifest = ledger.manifest();

        assertTrue(manifest.length() <= ToolCallLedger.MAX_MANIFEST_CHARS + CallManifest.MAX_INLINE_CHARS,
                "length " + manifest.length());
        assertTrue(manifest.length() <= ToolCallLedger.MAX_MANIFEST_CHARS, "empty blocks count against the descriptions");
        assertTrue(manifest.contains("calls=100"), manifest);
        assertTrue(CallManifest.OMITTED_LINE.matcher(manifest).find(), manifest);
    }

    @Test
    void whenCallsAreDroppedACallWithDataKeepsItsLineBeforeAnEmptyOne() {
        for (int i = 0; i < 60; i++) {
            recordCall("tool_" + i, "{}", "", false);
        }
        recordCall("the_data", "{}", "pod-a Running", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("tool=the_data"), manifest);
        assertTrue(manifest.contains("pod-a Running"), manifest);
        assertTrue(CallManifest.OMITTED_LINE.matcher(manifest).find(), manifest);
    }

    @Test
    void theNotStoredLineCarriesTheSameMarker() {
        String args = "q".repeat(CallManifest.ARGS_SUMMARY_CHARS + 10);
        store.failWrites = true;
        recordCall("t", args, "[]", false);

        assertTrue(ledger.manifest().contains("(args cut at 60 of 70 chars)"), ledger.manifest());
    }

    @Test
    void squareBracketsInArgumentsCannotEndTheCallLineEarly() {
        recordCall("t", "{\"ids\":[1,2]}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("args={\"ids\":(1,2)}"), manifest);
        var line = CallManifest.CALL_LINE.matcher(manifest);
        assertTrue(line.find());
        assertEquals("t", line.group(2));
        assertEquals(manifest.indexOf("error=false]") + "error=false]".length(), line.end(), "the whole line matches");
    }

    @Test
    void anyResultOverTheCapIsStoredAsPlainText() {
        for (String result : new String[] {"name: a\nitems:\n" + "  - x\n".repeat(20), "a,b\n" + "1,2\n".repeat(20),
                "NAME   READY\n" + "a      1/1\n".repeat(20)}) {
            String key = ledger.recordCall(new ToolCallLedger.Call("t", "i", "{}", T0, 1), result, false, 20);

            assertEquals("text/plain", store.headers.get(key).get(CallHeaders.CONTENT_TYPE), result);
            assertTrue(ledger.manifest().contains("type=text/plain "), "the [CALL] line shows the stored type");
        }
    }

    @Test
    void emptyArgumentsShowAsAnEmptyObject() {
        assertEquals("{}", CallManifest.argsSummary(null));
        assertEquals("{}", CallManifest.argsSummary("  "));
    }

    @Test
    void aResultOverTheCapIsCutMarkedAndFlagged() {
        String key = ledger.recordCall(new ToolCallLedger.Call("t", "i", "{}", T0, 1), "[" + "1,".repeat(50) + "1]",
                false, 20);

        var h = store.headers.get(key);
        assertEquals("true", h.get(CallHeaders.TRUNCATED));
        assertEquals("text/plain", h.get(CallHeaders.CONTENT_TYPE), "a cut list is no longer valid JSON");
        assertTrue(store.body(key).endsWith(" ...[truncated]"));
    }

    @Test
    void argumentsInTheHeaderAreCut() {
        String key = recordCall("t", "x".repeat(5_000), "[]", false);

        assertEquals(CallHeaders.MAX_ARGUMENTS_CHARS + " ...[truncated]".length(),
                store.headers.get(key).get(CallHeaders.TOOL_CALL_ARGUMENTS).length());
    }

    @Test
    void aCallWhoseWriteFailedIsCountedAndNamedNotStored() {
        assertFalse(ledger.holdsOutput());
        recordCall("t", "{}", "[]", false);
        assertTrue(ledger.holdsOutput());
        store.failWrites = true;
        assertNull(recordCall("u", "{\"a\":1}", "[]", true));
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
        recordCall("t", "{}", "[]", false);

        String manifest = ledger.manifest();

        assertEquals("", ledger.prefix());
        assertFalse(manifest.contains("*-<tool>"), "there is no key pattern to name: " + manifest);
        assertTrue(manifest.contains("[NOT STORED tool=t"), manifest);
        assertFalse(manifest.contains("[CALL key="), manifest);
    }

    @Test
    void shapeReadsJsonListsAndKubernetesLists() {
        assertEquals(new ToolResultShape("application/json", 2), ToolResultShape.of("[1,2]"));
        assertEquals(new ToolResultShape("application/json", 1), ToolResultShape.of(" {\"items\":[{}]}"));
        assertEquals(new ToolResultShape("application/json", -1, java.util.List.of("error")), ToolResultShape.of("{\"error\":\"x\"}"));
        assertEquals(new ToolResultShape("text/plain", -1), ToolResultShape.of("{broken"));
        assertEquals(new ToolResultShape("text/plain", -1), ToolResultShape.of("NAME"), "one column is not a table");
        assertEquals(new ToolResultShape("text/plain", -1), ToolResultShape.of(null));
    }

    @Test
    void shapeReadsTheColumnsOfATableAndTheFieldsOfAJsonList() {
        var table = ToolResultShape.of("NAME      READY   NOMINATED NODE\npod-a     1/1     <none>\n");
        assertEquals("text/plain; profile=kubectl-table", table.type());
        assertEquals(java.util.List.of("name", "ready", "nominated_node"), table.columns());

        var list = ToolResultShape.of("[{\"name\":\"a\",\"ns\":\"x\"},{\"name\":\"b\",\"extra\":1}]");
        assertEquals("application/json", list.type());
        assertEquals(2, list.rows());
        assertEquals(java.util.List.of("name", "ns"), list.columns(), "the first row's keys");

        assertEquals(java.util.List.of("metadata", "spec"), ToolResultShape.of("{\"items\":[{\"metadata\":{},\"spec\":{}}]}").columns());
        assertEquals("text/plain", ToolResultShape.of("Error: pods is forbidden\nsecond line").type());
        assertEquals("text/plain", ToolResultShape.of("Name  Ready\nx  y").type(), "titles are upper case");
    }

    @Test
    void theManifestLineSaysTheFormatAndNamesTheColumns() {
        recordCall("pods_list", "{}", "NAME      READY   STATUS\npod-a     1/1     Running\n", false);
        recordCall("nodes_list", "{}", "[{\"name\":\"n1\",\"taints\":[]}]", false);
        recordCall("logs", "{}", "plain log line", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("type=text/plain; profile=kubectl-table bytes=") && manifest.contains("columns=name,ready,status]"), manifest);
        assertTrue(manifest.contains("type=application/json bytes=") && manifest.contains("fields=name,taints]"), manifest);
        assertTrue(manifest.contains("tool=logs call=agent-x/003-logs.call args={} type=text/plain bytes=14 rows=unknown error=false]"), manifest);
    }

    @Test
    void everyColumnOfAWideTableIsListedAndABracketCannotEndTheLine() {
        String header = java.util.stream.IntStream.rangeClosed(1, 14).mapToObj(i -> "COL" + i)
                .collect(java.util.stream.Collectors.joining("  "));
        recordCall("wide", "{}", header + "\nrow\n", false);
        recordCall("odd", "{}", "[{\"" + "k".repeat(40) + "\":1,\"a]b\":2}]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("columns=col1,col2,col3,col4,col5,col6,col7,col8,col9,col10,col11,col12,col13,col14]"), manifest);
        assertTrue(manifest.contains("fields=" + "k".repeat(40) + ",a)b]"), manifest);
        var lines = CallManifest.CALL_LINE.matcher(manifest);
        int found = 0;
        while (lines.find()) {
            assertTrue(lines.group().endsWith("]") && lines.group().indexOf(']') == lines.group().length() - 1, lines.group());
            found++;
        }
        assertEquals(2, found);
    }

    @Test
    void aTableWiderThanTheListLimitCountsTheRest() {
        String header = java.util.stream.IntStream.rangeClosed(1, ColumnNames.MAX_LISTED + 3).mapToObj(i -> "C" + i)
                .collect(java.util.stream.Collectors.joining("  "));
        recordCall("wide", "{}", header + "\nrow\n", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains(",c" + ColumnNames.MAX_LISTED + ",+3more]"), manifest);
        assertFalse(manifest.contains("c" + (ColumnNames.MAX_LISTED + 1) + ","), manifest);
    }

    @Test
    void aCrowdedManifestListsFewerColumnsBeforeItDropsThemAll() {
        String header = java.util.stream.IntStream.rangeClosed(1, 50).mapToObj(i -> "COLUMN" + i)
                .collect(java.util.stream.Collectors.joining("  "));
        for (int i = 0; i < 9; i++) {
            recordCall("wide_" + i, "{}", header + "\nrow\n", false);
        }

        String manifest = ledger.manifest();

        assertTrue(descriptions(manifest).length() <= ToolCallLedger.MAX_MANIFEST_CHARS, "length " + manifest.length());
        assertTrue(manifest.contains("columns=column1,column2,column3,column4,column5,column6,column7,column8,column9,"
                + "column10,column11,column12,+38more]"), manifest);
    }

    @Test
    void theFooterSaysWhereEveryColumnIsListed() {
        recordCall("pods_list", "{}", "NAME  READY\na  1/1\n", false);

        assertTrue(ledger.manifest().contains("workspace_describe lists every one"), ledger.manifest());
    }

    @Test
    void aCutResultIsTextWithNoFields() {
        ledger.recordCall(new ToolCallLedger.Call("t", "i", "{}", T0, 1), "[{\"a\":1}," + "{\"a\":1},".repeat(50) + "{\"a\":1}]",
                false, 20);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("type=text/plain "), manifest);
        assertFalse(manifest.contains("fields="), manifest);
    }

    @Test
    void aTightManifestDropsArgumentsBeforeColumns() {
        for (int i = 0; i < 20; i++) {
            recordCall("tool_number_" + i, "{\"argument\":\"" + "v".repeat(200) + "\"}",
                    "NAME  READY  STATUS\nx  1/1  Running\n", false);
        }

        String manifest = descriptions(ledger.manifest());

        assertTrue(manifest.length() <= ToolCallLedger.MAX_MANIFEST_CHARS, "length " + manifest.length());
        assertFalse(manifest.contains("args="), manifest);
        assertFalse(manifest.contains("tool_number_0 call="), "the working's key goes with the arguments: " + manifest);
        assertTrue(manifest.contains("followed by .call"), "the footer still says where each working is: " + manifest);
        assertTrue(manifest.contains("columns=name,ready,status"), manifest);
    }

    @Test
    void theFooterNamesTheWaysToReadAKeyIncludingExecuteCode() {
        recordCall("pods_list", "{}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("workspace_query") && manifest.contains("workspace_head")
                && manifest.contains("execute_code over the file /kubemoot/workspace/<key>"), manifest);
    }

    @Test
    void headersAreInTheOrderTheyAreDocumented() {
        String key = recordCall("t", "{}", "[]", true);
        Map<String, String> h = new LinkedHashMap<>(store.headers.get(key));

        assertEquals("gen_ai.operation.name", h.keySet().iterator().next());
        assertEquals("tool_error", h.get("error.type"));
    }

    @Test
    void theWorkingIsStoredWholeUnderTheResultKeyWithCallAppended() {
        String program = "{\"language\":\"python\",\"code\":\"" + "x = 1\\n".repeat(200) + "\"}";
        String key = recordCall("execute_code", program, "1", false);

        assertEquals("ns/crew/th/agent-x/001-execute_code", key);
        assertEquals(program, store.body(key + ".call"));
        Map<String, String> h = store.headers.get(key + ".call");
        assertEquals(key, h.get(CallHeaders.WORKING_OF));
        assertEquals("execute_code", h.get(CallHeaders.TOOL_NAME));
        assertEquals("application/json", h.get(CallHeaders.CONTENT_TYPE));
        assertNull(h.get(CallHeaders.OPERATION_NAME), "a working is not a tool result");
        assertNull(h.get(CallHeaders.TRUNCATED));
    }

    @Test
    void theCallLineNamesTheWorkingKeyRelativeToTheThread() {
        recordCall("pods_list", "{\"namespace\":\"a\"}", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("[CALL key=agent-x/001-pods_list tool=pods_list call=agent-x/001-pods_list.call args="),
                manifest);
        assertTrue(manifest.contains(CallManifest.WORKING_NOTE), "the footer says what a call= key holds: " + manifest);
        var m = CallManifest.CALL_LINE.matcher(manifest);
        assertTrue(m.find());
        assertEquals("agent-x/001-pods_list", m.group(1), "readers still find the result's key");
        assertEquals("pods_list", m.group(2));
    }

    @Test
    void aWorkingOverTheSizeCapIsCutAndMarkedLikeAResult() {
        String args = "{\"code\":\"" + "y".repeat(500) + "\"}";
        String key = ledger.recordCall(new ToolCallLedger.Call("execute_code", "id", args, T0, 1), "ok", false, 100);

        String body = store.body(key + ".call");
        assertTrue(body.length() < args.length(), body);
        assertTrue(body.endsWith(TextCut.MARKER), body);
        assertEquals("true", store.headers.get(key + ".call").get(CallHeaders.TRUNCATED));
        assertEquals("text/plain", store.headers.get(key + ".call").get(CallHeaders.CONTENT_TYPE),
                "cut JSON is no longer JSON");
    }

    @Test
    void aWorkingThatCannotBeWrittenIsSaidToBeUnavailable() {
        store.failWorkings = true;
        String key = recordCall("pods_list", "{}", "[]", false);

        assertNotNull(key, "the result is still stored");
        assertTrue(ledger.manifest().contains("tool=pods_list call=unavailable args={}"), ledger.manifest());
    }

    @Test
    void aWorkingCountsNoCallAndGetsNoNumberOfItsOwn() {
        recordCall("a", "{}", "[]", false);
        String second = recordCall("b", "{}", "[]", false);

        assertEquals("ns/crew/th/agent-x/002-b", second);
        assertEquals(2, ledger.size());
        assertTrue(ledger.manifest().contains("calls=2 "), ledger.manifest());
    }
}
