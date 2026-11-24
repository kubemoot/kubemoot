package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.OptionalLong;
import java.util.regex.Matcher;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A result small enough for the discussion follows its description whole; a larger one stays in its
 * file. The sizes follow from the readers' window ({@link DiscussionRoom}); a result is never cut.
 */
class ToolCallLedgerInlineTest {

    private static final Instant T0 = Instant.parse("2026-11-04T12:00:00Z");
    private static final OptionalLong WINDOW_32K = OptionalLong.of(32_768);
    private static final OptionalLong WINDOW_128K = OptionalLong.of(131_072);

    private final ChatServiceCallFilesTest.FakeStore store = new ChatServiceCallFilesTest.FakeStore();

    private ToolCallLedger ledger(OptionalLong window) {
        return new ToolCallLedger(store, "th", "k8s-workloads", window);
    }

    private static String recordCall(ToolCallLedger ledger, String tool, String result, boolean error) {
        return ledger.recordCall(new ToolCallLedger.Call(tool, "id", "{}", T0, 5), result, error, 1_000_000);
    }

    private static String table(int bytes) {
        String row = "default  batch/v1  CronJob  backup-database  0 2 * * *  False  0  2h  main  img\n";
        return ("NAMESPACE  APIVERSION  KIND  NAME  SCHEDULE  SUSPEND  ACTIVE  AGE  CONTAINERS  IMAGES\n"
                + row.repeat(bytes / row.length() + 1)).substring(0, bytes);
    }

    @Test
    void theThreeResultsOfThreadOneSmallOnesGoInTheLargeOneStaysInItsFile() {
        var ledger = ledger(WINDOW_32K);
        String cronJobs = table(856);
        String jobs = "JOBS-ONLY\n" + table(25_652);
        String events = "No events found";
        recordCall(ledger, "resources_list", cronJobs, false);
        recordCall(ledger, "resources_list", jobs, false);
        recordCall(ledger, "events_list", events, false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("[RESULT]\n" + cronJobs + "\n[END RESULT]"), "the 856 byte table is posted");
        assertTrue(manifest.contains("[RESULT]\n" + events + "\n[END RESULT]"), "the short result is posted");
        assertFalse(manifest.contains("JOBS-ONLY"), "the 26 KB table stays in its file");
        assertTrue(manifest.contains("[CALL key=k8s-workloads/002-resources_list tool=resources_list"),
                "its description is there");
        assertTrue(manifest.contains("bytes=25662"), manifest);
        assertEquals(2, count(manifest, "[RESULT]"));
    }

    @Test
    void theLargeTableStillStaysInItsFileOnA128kWindow() {
        var ledger = ledger(WINDOW_128K);
        recordCall(ledger, "resources_list", table(25_662), false);

        assertFalse(ledger.manifest().contains("[RESULT]"), "12,288 characters is one result's share on 128k");
    }

    @Test
    void aResultAtTheLimitGoesInAndOneOverItDoesNot() {
        int limit = DiscussionRoom.perResultChars(WINDOW_32K);
        var at = ledger(WINDOW_32K);
        recordCall(at, "t", "x".repeat(limit), false);
        var over = ledger(WINDOW_32K);
        recordCall(over, "t", "x".repeat(limit + 1), false);

        assertTrue(at.manifest().contains("[RESULT]"));
        assertFalse(over.manifest().contains("[RESULT]"));
    }

    @Test
    void aResultThatDoesNotFitIsNeverCutItIsAbsent() {
        var ledger = ledger(WINDOW_32K);
        String big = "row\n".repeat(2_000);
        recordCall(ledger, "t", big, false);

        String manifest = ledger.manifest();

        assertFalse(manifest.contains("row\nrow"), "not a single row of it, not a part");
        assertEquals(big, store.body(store.bodies.keySet().iterator().next()), "the file holds all of it");
    }

    @Test
    void severalSmallResultsFitUntilTheContributionsTotalIsUsed() {
        var ledger = ledger(WINDOW_32K);
        int total = DiscussionRoom.perContributionChars(WINDOW_32K);
        String result = "y".repeat(1_000);
        int calls = total / (result.length() + "[RESULT]\n\n[END RESULT]".length() + 1) + 3;
        for (int i = 0; i < calls; i++) {
            recordCall(ledger, "t", result, false);
        }

        int posted = count(ledger.manifest(), "[RESULT]");

        assertTrue(posted >= 4, "several small results fit: " + posted);
        assertTrue(posted < calls, "the total stops it: " + posted + " of " + calls);
        assertTrue(resultChars(ledger.manifest()) <= total, "inside the contribution's share");
    }

    @Test
    void aResultThatDoesNotFitLeavesTheRoomForALaterSmallOne() {
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "big", "z".repeat(DiscussionRoom.perResultChars(WINDOW_32K) + 1), false);
        recordCall(ledger, "small", "ok", false);

        String manifest = ledger.manifest();

        assertEquals(1, count(manifest, "[RESULT]"));
        assertTrue(manifest.contains("[RESULT]\nok\n[END RESULT]"));
    }

    @Test
    void aFailedCallPostsItsErrorTextInAFailedBlockAnOkResultInAResultBlock() {
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "bad", "object not found", true);
        recordCall(ledger, "good", "3 pods", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("[FAILED RESULT]\nobject not found\n[END RESULT]"), manifest);
        assertTrue(manifest.contains("[RESULT]\n3 pods\n[END RESULT]"), manifest);
        Matcher failed = CallManifest.FAILED_RESULT_BLOCK.matcher(manifest);
        assertTrue(failed.find());
        assertEquals("object not found", failed.group(1));
    }

    @Test
    void aResultComesBackExactlyFromItsBlockTrailingNewlineAndAll() {
        var ledger = ledger(WINDOW_32K);
        String result = "line one\n\nline [three]\n";
        recordCall(ledger, "t", result, false);

        Matcher block = CallManifest.RESULT_BLOCK.matcher(ledger.manifest());

        assertTrue(block.find());
        assertEquals(result, block.group(1));
    }

    @Test
    void aResultThatHoldsTheClosingMarkerItselfStaysInItsFile() {
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "t", "a\n[END RESULT]\nb", false);

        assertFalse(ledger.manifest().contains("[RESULT]"));
    }

    @Test
    void aResultThatLooksLikeACallLineStaysInItsFile() {
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "t", "x [CALL key=a/001-b tool=b] y", false);

        assertFalse(ledger.manifest().contains("[RESULT]"));
    }

    @Test
    void anEmptyOrBlankResultGetsTheEmptyBlock() {
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "a", "", false);
        recordCall(ledger, "b", "  \n ", false);
        recordCall(ledger, "c", null, false);

        String manifest = ledger.manifest();

        assertEquals(3, manifest.split(java.util.regex.Pattern.quote(CallManifest.EMPTY_RESULT_TEXT), -1).length - 1,
                manifest);
        assertFalse(manifest.contains("[RESULT]"), "an empty result is not a result block: " + manifest);
        assertTrue(manifest.contains("calls=3"));
    }

    @Test
    void theEmptyBlockFollowsItsCallLine() {
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "workspace_grep", "", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("bytes=0 rows=unknown error=false]\n" + CallManifest.EMPTY_RESULT_TEXT), manifest);
    }

    @Test
    void theEmptyBlockIsPostedEvenWhenTheContributionHasNoRoomLeft() {
        var ledger = ledger(OptionalLong.of(1));
        recordCall(ledger, "big", "x".repeat(10), false);
        recordCall(ledger, "none", "", false);

        assertTrue(ledger.manifest().contains(CallManifest.EMPTY_RESULT_TEXT), ledger.manifest());
    }

    @Test
    void aFailedCallWithNoTextHasNoEmptyBlock() {
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "a", "", true);

        assertFalse(ledger.manifest().contains(CallManifest.EMPTY_RESULT_OPEN), ledger.manifest());
    }

    @Test
    void anEmptyJsonListStaysAResultBlockWithItsText() {
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "a", "[]", false);

        String manifest = ledger.manifest();

        assertTrue(manifest.contains("[RESULT]\n[]\n[END RESULT]"), manifest);
        assertFalse(manifest.contains(CallManifest.EMPTY_RESULT_OPEN), manifest);
    }

    @Test
    void aResultCutAtTheStorageCapIsNotPosted() {
        var ledger = ledger(WINDOW_32K);
        ledger.recordCall(new ToolCallLedger.Call("t", "i", "{}", T0, 1), "w".repeat(100), false, 50);

        assertFalse(ledger.manifest().contains("[RESULT]"), "the file itself holds only part of it");
    }

    @Test
    void aCallThatCouldNotBeStoredPostsNoResult() {
        store.failWrites = true;
        var ledger = ledger(WINDOW_32K);
        recordCall(ledger, "t", "small", false);

        String manifest = ledger.manifest();

        assertFalse(manifest.contains("[RESULT]"), "nothing backs it, so it is not offered as data");
        assertTrue(manifest.contains("[NOT STORED tool=t"));
    }

    @Test
    void anUnknownWindowUsesTheFixedRoom() {
        var ledger = ledger(OptionalLong.empty());
        recordCall(ledger, "fits", "a".repeat(DiscussionRoom.perResultChars(OptionalLong.empty())), false);
        recordCall(ledger, "over", "b".repeat(DiscussionRoom.perResultChars(OptionalLong.empty()) + 1), false);

        assertEquals(1, count(ledger.manifest(), "[RESULT]"));
    }

    @Test
    void aLongManifestKeepsTheLineOfEveryCallThatPostsAResult() {
        var ledger = ledger(WINDOW_32K);
        for (int i = 0; i < 90; i++) {
            recordCall(ledger, "tool_number_" + i, i == 89 ? "last result" : "z".repeat(5_000), false);
        }

        String manifest = ledger.manifest();
        String descriptions = CallManifest.RESULT_BLOCK.matcher(manifest).replaceAll("");

        assertTrue(descriptions.length() <= ToolCallLedger.MAX_MANIFEST_CHARS, "length " + descriptions.length());
        assertTrue(manifest.contains("more calls under the prefix]"), "calls without a result were counted out");
        assertTrue(manifest.contains("[CALL key=k8s-workloads/090-tool_number_89 tool=tool_number_89"),
                "the call whose result is posted keeps its line");
        assertTrue(manifest.contains("[RESULT]\nlast result\n[END RESULT]"));
    }

    @Test
    void theDescriptionsPassedToTheSynthesizerStillCarryTheKeysRelativeToTheThread() {
        var ledger = ledger(WINDOW_32K);
        String key = recordCall(ledger, "pods_list", "ok", false);

        assertEquals("ns/crew/th/k8s-workloads/001-pods_list", key, "the store keeps the long key");
        assertTrue(ledger.manifest().contains("[CALL key=k8s-workloads/001-pods_list "));
        assertEquals("k8s-workloads/", ledger.prefix());
    }

    private static int count(String text, String needle) {
        int n = 0;
        for (int i = text.indexOf(needle); i >= 0; i = text.indexOf(needle, i + 1)) {
            n++;
        }
        return n;
    }

    private static int resultChars(String manifest) {
        int total = 0;
        Matcher m = CallManifest.RESULT_BLOCK.matcher(manifest);
        while (m.find()) {
            total += m.group().length();
        }
        return total;
    }
}
