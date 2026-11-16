package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

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
