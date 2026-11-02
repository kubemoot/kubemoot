package ai.kubemoot.agent.nats;

import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class EvidenceQueueTest {

    /** Four characters to a token, flat. */
    private static long flat(String s) {
        return (s.length() + 3) / 4;
    }

    private static StoredResult result(int i, String content) {
        return new StoredResult("k" + i, "tool" + i, "[CALL key=k" + i + " tool=tool" + i + "]", content);
    }

    private static EvidenceQueue queue(StoredResult... results) {
        return new EvidenceQueue(List.of(results), EvidenceQueueTest::flat);
    }

    @Test
    void anUnreadableResultIsNotQueued() {
        var q = queue(result(1, null), result(2, "x"));

        assertEquals(1, q.size());
    }

    @Test
    void fillTakesWholeResultsInOrderAndPassesOverOneThatDoesNotFit() {
        var q = queue(result(1, "a".repeat(400)), result(2, "b".repeat(40)), result(3, "c".repeat(40)));

        var batch = q.fill(40);

        assertEquals(List.of("k2", "k3"), batch.slices().stream().map(EvidenceQueue.Slice::key).toList());
        q.commit(batch);
        assertEquals(1, q.size(), "the one that did not fit is still waiting");
        assertFalse(q.isRead("k1"));
        assertTrue(q.isRead("k2"));
    }

    @Test
    void planningChangesNothingUntilItIsCommitted() {
        var q = queue(result(1, "a"), result(2, "b"));

        q.fill(1_000);
        q.batch(1_000);

        assertEquals(2, q.size());
    }

    @Test
    void batchSplitsAResultThatCannotFitAnEmptyRoundAtLineBoundaries() {
        String text = ("0123456789".repeat(9) + "\n").repeat(40);
        var q = queue(result(1, text));
        var got = new StringBuilder();
        int parts = 0;

        while (!q.isEmpty()) {
            var batch = q.batch(100);
            assertEquals(1, batch.size());
            var slice = batch.slices().get(0);
            parts++;
            assertEquals(parts, slice.part());
            assertTrue(slice.parts() >= slice.part(), slice.label(true));
            assertTrue(slice.text().endsWith("\n") || q.size() == 1 && slice.text().length() == text.length() - got.length(),
                    "cut at a line break");
            assertTrue(flat(slice.render(true)) <= 100, "a part fits its round: " + flat(slice.render(true)));
            got.append(slice.text());
            q.commit(batch);
        }

        assertEquals(text, got.toString());
        assertTrue(parts > 3);
    }

    @Test
    void theLastPartSaysItIsTheLast() {
        var q = queue(result(1, ("x".repeat(99) + "\n").repeat(10)));
        EvidenceQueue.Slice last = null;
        while (!q.isEmpty()) {
            var batch = q.batch(120);
            last = batch.slices().get(0);
            q.commit(batch);
        }

        assertEquals(last.part(), last.parts());
        assertTrue(last.label(false).contains("part " + last.part() + " of " + last.part()), last.label(false));
    }

    @Test
    void batchLeavesAnOversizeResultThatIsNotFirstForTheNextRound() {
        var q = queue(result(1, "a".repeat(40)), result(2, "b".repeat(4_000)), result(3, "c".repeat(40)));

        var batch = q.batch(100);

        assertEquals(List.of("k1", "k3"), batch.slices().stream().map(EvidenceQueue.Slice::key).toList());
        q.commit(batch);
        var next = q.batch(100);
        assertEquals("k2", next.slices().get(0).key());
        assertTrue(next.slices().get(0).isPart());
    }

    @Test
    void aSingleLongLineIsCutAtTheCharacterWhenNoLineBreakFits() {
        var q = queue(result(1, "y".repeat(2_000)));
        var batch = q.batch(100);

        var slice = batch.slices().get(0);

        assertTrue(slice.text().length() > 0 && slice.text().length() < 2_000);
        assertTrue(flat(slice.render(true)) <= 100);
    }

    @Test
    void nothingFitsWhenTheRoomIsSmallerThanALabel() {
        var q = queue(result(1, "y".repeat(2_000)));

        assertTrue(q.batch(3).isEmpty());
        assertTrue(q.batch(0).isEmpty());
        assertEquals(1, q.size());
    }

    @Test
    void cutTakesEveryPendingResultCutAtTheLimit() {
        var q = queue(result(1, "a".repeat(50)), result(2, "b".repeat(10)));

        var slices = q.cut(20).slices();

        assertEquals("a".repeat(20) + ManifestInliner.CUT_NOTE, slices.get(0).text());
        assertEquals("b".repeat(10), slices.get(1).text());
    }

    @Test
    void aPartlyReadResultIsReportedAsSuch() {
        var q = queue(result(1, ("z".repeat(99) + "\n").repeat(20)));

        q.commit(q.batch(120));

        assertTrue(q.partlyRead("k1"));
        assertFalse(q.isRead("k1"));
    }

    @Test
    void sliceLabelsShowTheKeyOnlyInANotesRound() {
        var whole = new EvidenceQueue.Slice("k", "pods_list", "body", 0, 0);
        var part = new EvidenceQueue.Slice("k", "pods_list", "body", 2, 3);

        assertEquals("[pods_list]\nbody", whole.render(false));
        assertEquals("[pods_list key=k]\nbody", whole.render(true));
        assertEquals("[pods_list key=k part 2 of 3]\nbody", part.render(true));
        assertEquals("[pods_list part 2 of 3]\nbody", part.render(false));
    }

    @Test
    void storedResultsAreListedFromCallLinesOnceEach() {
        String text = "x [CALL key=a/1-t tool=t format=text bytes=1 rows=2 error=false] y\n"
                + "[CALL key=a/2-u tool=u args={} format=json bytes=1 rows=2 error=false fields=a,b]\n"
                + "[CALL key=a/1-t tool=t format=text bytes=1 rows=2 error=false]";
        var read = new java.util.ArrayList<String>();

        var results = StoredResult.listFrom(text, key -> {
            read.add(key);
            return "body of " + key;
        });

        assertEquals(List.of("a/1-t", "a/2-u"), results.stream().map(StoredResult::key).toList());
        assertEquals(List.of("t", "u"), results.stream().map(StoredResult::tool).toList());
        assertEquals(List.of("a/1-t", "a/2-u"), read, "a key named twice is read once");
        assertTrue(results.get(1).callLine().endsWith("fields=a,b]"));
    }

    @Test
    void promptTemplatesFillTheirPlacesInOnePass() {
        String out = RefinePrompts.render("a {{x}} b {{y}} c {{z}}", Map.of("x", "{{y}}", "y", "$1"));

        assertEquals("a {{y}} b $1 c {{z}}", out, "inserted text is not read as a place; unknown places stay");
    }

    @Test
    void theNotesRoundPromptsCarryTheContextTheNotesAndTheBatch() {
        String first = RefinePrompts.round(1, "THE QUESTION", null, "BATCH ONE");
        String next = RefinePrompts.round(2, "THE QUESTION", "- [k1] 7 pods", "BATCH TWO");

        assertTrue(first.contains("THE QUESTION") && first.contains("BATCH ONE") && first.contains("round 1"), first);
        assertFalse(first.contains("WORKING NOTES SO FAR"), first);
        assertTrue(next.contains("WORKING NOTES SO FAR:\n- [k1] 7 pods") && next.contains("BATCH TWO"), next);
        assertTrue(next.contains("NEVER drop a finding without a reason"), next);
        assertFalse(first.contains("{{") || next.contains("{{"), "no place left unfilled");
        assertTrue(RefinePrompts.system().contains("exactly as the result shows it")
                && RefinePrompts.system().contains("[<call key>]"), RefinePrompts.system());
        assertTrue(RefinePrompts.finalNotes("N").contains("N") && !RefinePrompts.finalNotes("N").contains("{{"));
    }
}
