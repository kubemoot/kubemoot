package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.io.IOException;
import java.time.Instant;
import java.util.OptionalLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** An agent whose access does not include storing lists its calls and posts what fits, and writes nothing. */
class ToolCallLedgerUnstoredTest {

    private static final Instant T0 = Instant.parse("2026-11-16T12:00:00Z");

    private final ToolCallLedger ledger = ToolCallLedger.unstoredForLoop("th", "obs-advisor", OptionalLong.empty());

    private String record(String tool, String args, String result, boolean error) {
        return ledger.record(new ToolCallLedger.Call(tool, "id", args, T0, 4), result, error, 1_000_000);
    }

    @Test
    void thereIsNoLedgerWithoutAThread() {
        assertNull(ToolCallLedger.unstoredForLoop(null, "a", OptionalLong.empty()));
        assertNull(ToolCallLedger.unstoredForLoop(" ", "a", OptionalLong.empty()));
        assertNotNull(ledger);
    }

    @Test
    void aCallIsListedWithItsArgumentsAndItsResultAndNoKey() {
        assertNull(record("artifact_select", "{\"q\": \"events\"}", "node-a NotReady", false), "no key: nothing was stored");

        String manifest = ledger.manifest();

        assertTrue(manifest.startsWith("[WORKSPACE agent=obs-advisor calls=1 errors=0 stored=no]\n"), manifest);
        assertTrue(manifest.contains("[CALL tool=artifact_select args={\"q\": \"events\"} type=text/plain bytes=15 "
                + "rows=unknown error=false stored=no]\n[RESULT]\nnode-a NotReady\n[END RESULT]"), manifest);
        assertFalse(manifest.contains("key="), manifest);
        assertFalse(manifest.contains(CallManifest.FOOTER_PREFIX), "no results are stored under any key: " + manifest);
        assertFalse(manifest.contains("only in its file"), manifest);
        assertTrue(ledger.holdsOutput());
    }

    @Test
    void anEmptyResultGetsTheEmptyBlock() {
        record("workspace_grep", "{}", "  \n", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("bytes=3 rows=unknown error=false stored=no]\n" + CallManifest.EMPTY_RESULT_TEXT),
                manifest);
    }

    @Test
    void aFailedResultGetsTheFailedBlock() {
        record("t", "{}", "boom", true);

        assertTrue(ledger.manifest().contains("error=true stored=no]\n[FAILED RESULT]\nboom\n[END RESULT]"), ledger.manifest());
    }

    @Test
    void aResultTooLargeToShowSaysItIsNotStoredAndNotShown() {
        String large = "x".repeat(DiscussionRoom.perResultChars(OptionalLong.empty()) + 1);
        record("big", "{\"a\": 1}", large, false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("[NOT STORED tool=big args={\"a\": 1} error=false - " + CallManifest.NOT_KEPT_NOTE + "]"),
                manifest);
        assertFalse(manifest.contains(large), "a result is never cut or partly shown");
        assertFalse(manifest.contains("only in its file"), manifest);
        assertFalse(manifest.contains("key="), manifest);
    }

    @Test
    void theResultsOfManyCallsShareTheContributionRoomLikeStoredOnes() {
        for (int i = 0; i < 30; i++) {
            record("t" + i, "{}", "r".repeat(DiscussionRoom.perResultChars(OptionalLong.empty()) - 1), false);
        }

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("[NOT STORED tool="), "later results do not fit the room: " + manifest.length());
        assertTrue(manifest.length() <= ToolCallLedger.MAX_MANIFEST_CHARS + CallManifest.MAX_INLINE_CHARS, "" + manifest.length());
    }

    @Test
    void aFailedCallWithNoTextIsAnUnstoredCallLine() {
        record("t", "{}", "", true);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("[CALL tool=t args={} type=text/plain bytes=0 rows=unknown error=true stored=no]"), manifest);
        assertFalse(manifest.contains("NOT STORED"), manifest);
    }

    @Test
    void aResultHoldingACallMarkerIsNotPosted() {
        record("t", "{}", "see " + CallManifest.UNSTORED_CALL_PREFIX + "x", false);

        assertTrue(ledger.manifest().contains("[NOT STORED tool=t"), ledger.manifest());
    }

    @Test
    void aFailedCallWithNoTextPostsTheNoTextBlockSoEveryLineHasABlock() {
        record("t", "{}", " ", true);

        assertTrue(ledger.manifest().contains("error=true stored=no]\n" + CallManifest.FAILED_NO_TEXT_TEXT), ledger.manifest());
    }

    @Test
    void droppedCallsSayTheirResultsAreNotStoredAndNotAvailable() {
        for (int i = 0; i < 100; i++) {
            record("events_list", "{\"n\": " + i + "}", "", false);
        }

        String manifest = ledger.manifest();

        assertTrue(manifest.length() <= ToolCallLedger.MAX_MANIFEST_CHARS, "" + manifest.length());
        assertTrue(CallManifest.OMITTED_UNSTORED_LINE.matcher(manifest).find(), manifest);
        assertFalse(CallManifest.OMITTED_LINE.matcher(manifest).find(), "no prefix to point at: " + manifest);
    }

    @Test
    void aSharedFixtureOfManifestsIsWhatTheLedgerWrites() throws IOException {
        File file = new File("../test/fixtures/unstored-manifests.json");
        var mapper = new ObjectMapper();
        JsonNode fixture = mapper.readTree(file);
        boolean update = System.getenv("UPDATE_UNSTORED_MANIFESTS") != null;
        for (JsonNode c : fixture.get("cases")) {
            String manifest = manifestOf(c.get("calls"));
            if (update) {
                ((ObjectNode) c).put("manifest", manifest);
            } else {
                assertEquals(c.get("manifest").asText(), manifest, c.get("name").asText());
            }
        }
        if (update) {
            mapper.writerWithDefaultPrettyPrinter().writeValue(file, fixture);
        }
    }

    private static String manifestOf(JsonNode calls) {
        var one = ToolCallLedger.unstoredForLoop("th", "obs-advisor", OptionalLong.empty());
        for (JsonNode call : calls) {
            String result = call.has("resultChars") ? "x".repeat(call.get("resultChars").asInt())
                    : call.get("result").asText();
            for (int i = 0; i < (call.has("repeat") ? call.get("repeat").asInt() : 1); i++) {
                one.record(new ToolCallLedger.Call(call.get("tool").asText(), "id", call.get("args").asText(), T0, 4),
                        result, call.path("error").asBoolean(false), 1_000_000);
            }
        }
        return one.manifest();
    }

    @Test
    void aStoringLedgerKeepsItsKeysAndFooter() {
        var storing = new ToolCallLedger(new ChatServiceCallFilesTest.FakeStore(), "th", "a");
        storing.record(new ToolCallLedger.Call("t", "id", "{}", T0, 1), "[]", false, 1_000_000);

        String manifest = storing.manifest();

        assertTrue(manifest.contains("[CALL key=a/001-t tool=t"), manifest);
        assertTrue(manifest.contains(CallManifest.FOOTER_PREFIX), manifest);
        assertFalse(manifest.contains("stored=no"), manifest);
        assertEquals(1, storing.size());
    }
}
