package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

class ToolCallLedgerTest {

    private static final Instant T0 = Instant.parse("2026-11-01T12:00:00Z");

    private final ChatServicePerCallArtifactsTest.FakeStore store = new ChatServicePerCallArtifactsTest.FakeStore();
    private final ToolCallLedger ledger = new ToolCallLedger(store, "th", "agent-x");

    private String record(String tool, String args, String result, boolean error) {
        return ledger.record(new ToolCallLedger.Call(tool, "id", args, T0, 12), result, error, 1_000_000);
    }

    @Test
    void theManifestIsTheSameTextForTheSameCalls() {
        record("pods_list", "{\"namespace\":\"a\"}", "[{\"x\":1}]", false);
        var other = new ToolCallLedger(new ChatServicePerCallArtifactsTest.FakeStore(), "th", "agent-x");
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
    void completeNeedsACallAndEveryWriteToHaveWorked() {
        assertFalse(ledger.complete());
        record("t", "{}", "[]", false);
        assertTrue(ledger.complete());
        store.failWrites = true;
        assertNull(record("t", "{}", "[]", false));
        assertFalse(ledger.complete());
        assertEquals(1, ledger.size());
    }

    @Test
    void shapeReadsJsonListsAndKubernetesLists() {
        assertEquals(new ToolResultShape(true, 2), ToolResultShape.of("[1,2]"));
        assertEquals(new ToolResultShape(true, 1), ToolResultShape.of(" {\"items\":[{}]}"));
        assertEquals(new ToolResultShape(true, -1), ToolResultShape.of("{\"error\":\"x\"}"));
        assertEquals(new ToolResultShape(false, -1), ToolResultShape.of("{broken"));
        assertEquals(new ToolResultShape(false, -1), ToolResultShape.of("NAME  READY"));
        assertEquals(new ToolResultShape(false, -1), ToolResultShape.of(null));
    }

    @Test
    void headersAreInTheOrderTheyAreDocumented() {
        String key = record("t", "{}", "[]", true);
        Map<String, String> h = new LinkedHashMap<>(store.headers.get(key));

        assertEquals("gen_ai.operation.name", h.keySet().iterator().next());
        assertEquals("tool_error", h.get("error.type"));
    }
}
