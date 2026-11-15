package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.DiscussionFacts.StoredCall;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The stored results read for a discussion that holds no fact: shown whole when small, cut and said so when large. */
class StoredResultsTest {

    private static final StoredCall MEMORY = new StoredCall("k8s-metrics", "execute_query", "k8s-metrics/001-execute_query");
    private static final StoredCall PODS = new StoredCall("k8s-workloads", "resources_list", "k8s-workloads/001-resources_list");

    private static StoredResults.Reader readerOf(Map<String, String> files) {
        return files::get;
    }

    @Test
    void aSmallResultIsShownWholeAndCompact() {
        String pretty = "{\n  \"result\": [\n    {\"node\": \"w1\", \"free\": 12}\n  ]\n}";

        String out = StoredResults.render(List.of(MEMORY), readerOf(Map.of(MEMORY.key(), pretty)), 1_000, 4_000);

        assertTrue(out.contains("[STORED RESULT key=k8s-metrics/001-execute_query tool=execute_query"), out);
        assertTrue(out.contains("complete=true]\n{\"result\":[{\"node\":\"w1\",\"free\":12}]}\n[END STORED RESULT]"), out);
    }

    @Test
    void aLargeResultIsCutAtTheRoomAndSaysHowMuchWasShown() {
        String rows = "{\"rows\":[" + "{\"node\":\"worker-1\",\"free\":123456},".repeat(200) + "{}]}";

        String out = StoredResults.render(List.of(MEMORY), readerOf(Map.of(MEMORY.key(), rows)), 500, 4_000);

        assertTrue(out.contains("shown=500 complete=false]"), out);
        assertTrue(out.contains("chars=" + rows.length()), out);
        assertFalse(out.contains("{}]}"), "the end of the result is not shown: " + out);
        assertTrue(out.length() < 800, "length " + out.length());
    }

    @Test
    void theRoomOfAllResultsIsShared() {
        String text = "x".repeat(600);

        String out = StoredResults.render(List.of(MEMORY, PODS),
                readerOf(Map.of(MEMORY.key(), text, PODS.key(), text)), 500, 700);

        assertTrue(out.contains("key=k8s-metrics/001-execute_query"), out);
        assertFalse(out.contains("key=k8s-workloads/001-resources_list"), "no room left for it: " + out);
        assertTrue(out.contains("[1 more stored results not shown: no room left in the discussion]"),
                "a result left out is counted, not dropped silently: " + out);
    }

    @Test
    void aResultThatCannotBeReadIsSkippedAndTheOthersStillShow() {
        var files = new java.util.HashMap<String, String>();
        files.put(PODS.key(), "NAME READY\npod-a 1/1");

        String out = StoredResults.render(List.of(MEMORY, PODS), readerOf(files), 1_000, 4_000);

        assertFalse(out.contains("execute_query"), out);
        assertTrue(out.contains("pod-a 1/1"), out);
    }

    @Test
    void nothingReadableGivesAnEmptyString() {
        assertEquals("", StoredResults.render(List.of(MEMORY), readerOf(Map.of()), 1_000, 4_000));
        assertEquals("", StoredResults.render(List.of(MEMORY), readerOf(Map.of(MEMORY.key(), "  ")), 1_000, 4_000));
        assertEquals("", StoredResults.render(List.of(), readerOf(Map.of()), 1_000, 4_000));
    }

    @Test
    void textThatIsNotJsonAndBrokenJsonAreKeptAsTheyAre() {
        assertEquals("NAME READY\npod-a", StoredResults.compact("NAME READY\npod-a\n"));
        assertEquals("{\"a\": [1, 2 ...[truncated]", StoredResults.compact("{\"a\": [1, 2 ...[truncated]"));
        assertNull(StoredResults.compact(null));
    }
}
