package ai.kubemoot.agent.nats;

import java.util.Map;
import java.util.OptionalLong;
import java.util.function.ToLongFunction;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class ManifestInlinerTest {

    private static final String K1 = "ns/crew/t1/k8s/001-pods_list";
    private static final String K2 = "ns/crew/t1/k8s/002-events_list";
    private static final String K3 = "ns/crew/t1/k8s/003-nodes_list";

    private static final String LINE1 = "[CALL key=" + K1 + " tool=pods_list args={\"a\":(1,2)} bytes=9 rows=2 error=false]";
    private static final String LINE2 = "[CALL key=" + K2 + " tool=events_list args={} bytes=5 rows=unknown error=false]";
    private static final String LINE3 = "[CALL key=" + K3 + " tool=nodes_list args={} bytes=5 rows=2 error=false]";

    private static final String HEADER = "[WORKSPACE agent=k8s calls=3 errors=0 prefix=ns/crew/t1/k8s/]\n";
    private static final String FOOTER = "\nThe results are stored under these keys and are not shown here. Read a key with workspace_query.";
    private static final String MANIFEST = HEADER + LINE1 + "\n" + LINE2 + "\n" + LINE3 + FOOTER;

    /** Four characters to a token, flat. */
    private static final ToLongFunction<String> FLAT = s -> (s.length() + 3) / 4;

    private static final int FALLBACK = 24_000;

    private static final Map<String, String> STORED = Map.of(K1, "[{\"a\":1},{\"a\":2}]", K2, "NAME  AGE\nx  1d",
            K3, "n1\nn2");

    @Test
    void resultsThatFitAreFilledInOrderAndTheHeaderAndFooterGoAway() {
        String out = ManifestInliner.inline("Findings:\n" + MANIFEST, STORED::get, FLAT, OptionalLong.of(10_000), FALLBACK);

        assertEquals("Findings:\n[pods_list]\n[{\"a\":1},{\"a\":2}]\n[events_list]\nNAME  AGE\nx  1d\n[nodes_list]\nn1\nn2", out);
    }

    @Test
    void resultsPastTheRoomKeepTheirCallLineAndANoteAndNoBody() {
        // Each filled result is "[tool]\n" + body: pods_list 29 chars (8 tokens), events_list 29 (8).
        // Room for the first two only.
        String out = ManifestInliner.inline(MANIFEST, STORED::get, FLAT, OptionalLong.of(16), FALLBACK);

        assertTrue(out.contains("[pods_list]\n[{\"a\":1},{\"a\":2}]"), out);
        assertTrue(out.contains("[events_list]\nNAME  AGE\nx  1d"), out);
        assertTrue(out.contains(LINE3 + "\n" + ManifestInliner.NOT_INCLUDED), out);
        assertFalse(out.contains("n1\nn2"), "no part of a result that did not fit: " + out);
        assertFalse(out.contains("not shown here"), "the closing line is gone");
    }

    @Test
    void theFirstResultThatDoesNotFitEndsTheFillAndTheRestAreNamedNotRead() {
        var read = new java.util.ArrayList<String>();
        String out = ManifestInliner.inline(MANIFEST, key -> {
            read.add(key);
            return "x".repeat(400);
        }, FLAT, OptionalLong.of(120), FALLBACK);

        assertEquals(java.util.List.of(K1, K2), read, "reading stops once a result does not fit");
        assertTrue(out.contains("[pods_list]\n" + "x".repeat(400)), out);
        assertTrue(out.contains(LINE2 + "\n" + ManifestInliner.NOT_INCLUDED), out);
        assertTrue(out.contains(LINE3 + "\n" + ManifestInliner.NOT_INCLUDED), out);
    }

    @Test
    void noRoomAtAllNamesEveryResult() {
        String out = ManifestInliner.inline(MANIFEST, STORED::get, FLAT, OptionalLong.of(0), FALLBACK);

        for (String line : new String[] {LINE1, LINE2, LINE3}) {
            assertTrue(out.contains(line + "\n" + ManifestInliner.NOT_INCLUDED), out);
        }
    }

    @Test
    void noRoomAtAllReadsNothing() {
        String out = ManifestInliner.inline(MANIFEST, key -> fail("read " + key), FLAT, OptionalLong.of(0), FALLBACK);

        assertTrue(out.contains(LINE1 + "\n" + ManifestInliner.NOT_INCLUDED), out);
    }

    @Test
    void anUnknownWindowFillsEachResultUpToTheFixedCharacterLimit() {
        String big = "y".repeat(FALLBACK + 500);
        String out = ManifestInliner.inline(MANIFEST, key -> K2.equals(key) ? big : STORED.get(key), FLAT,
                OptionalLong.empty(), FALLBACK);

        assertTrue(out.contains("[pods_list]\n[{\"a\":1},{\"a\":2}]"), "a small result is whole: " + out);
        assertTrue(out.contains("[events_list]\n" + "y".repeat(FALLBACK) + ManifestInliner.CUT_NOTE), "cut at the limit");
        assertFalse(out.contains("y".repeat(FALLBACK + 1)));
        assertTrue(out.contains("[nodes_list]\nn1\nn2"), "later results are still filled in: " + out);
        assertFalse(out.contains(ManifestInliner.NOT_INCLUDED));
    }

    @Test
    void numericJsonIsCountedDenseSoItFillsTheRoomSooner() {
        // 6,000 characters of numeric JSON: about 1,500 tokens flat, but one token per digit is about 4,000.
        String numeric = "{\"cpu\":0.1234567,\"mem\":1048576,\"t\":1761953400}\n".repeat(120);
        ToLongFunction<String> dense = s -> (long) Math.ceil((s.length() + digitsAndSymbols(s)) / 3.0);
        String line = "[CALL key=" + K1 + " tool=pods_list args={} bytes=9 rows=2 error=false]";

        String flatFits = ManifestInliner.inline(line, key -> numeric, FLAT, OptionalLong.of(2_000), FALLBACK);
        String denseDoesNot = ManifestInliner.inline(line, key -> numeric, dense, OptionalLong.of(2_000), FALLBACK);

        assertTrue(flatFits.contains(numeric), "a flat count lets it in");
        assertEquals(line + "\n" + ManifestInliner.NOT_INCLUDED, denseDoesNot);
    }

    private static long digitsAndSymbols(String s) {
        long extra = 0;
        for (char c : s.toCharArray()) {
            extra += Character.isDigit(c) ? 2 : (Character.isLetter(c) || Character.isWhitespace(c) ? 0 : 1);
        }
        return extra;
    }

    @Test
    void anUnreadableResultBecomesTheUnavailableNoticeAndDoesNotUseUpRoom() {
        String out = ManifestInliner.inline(MANIFEST, key -> K1.equals(key) ? null : STORED.get(key), FLAT,
                OptionalLong.of(10_000), FALLBACK);

        assertTrue(out.contains(DiscussionOrchestrator.artifactUnavailableNotice(K1)), out);
        assertTrue(out.contains("[events_list]\nNAME  AGE\nx  1d"), out);
        assertFalse(out.contains("[CALL key="), out);
    }

    @Test
    void textWithoutAManifestIsReturnedUnchanged() {
        String plain = "[ARTIFACT key=a bytes=1] and [pods_list]\nrows";

        assertSame(plain, ManifestInliner.inline(plain, key -> fail("read"), FLAT, OptionalLong.of(5), FALLBACK));
        assertNull(ManifestInliner.inline(null, key -> fail("read"), FLAT, OptionalLong.of(5), FALLBACK));
    }

    @Test
    void theUserFacingAnswerLosesTheManifestLinesAndKeepsItsOwnText() {
        String answer = "The cluster has 3 pods.\n" + MANIFEST;

        assertEquals("The cluster has 3 pods.\n", ManifestInliner.strip(answer, true).replaceAll("\\n+$", "\n"));
        assertTrue(ManifestInliner.strip(answer, false).contains("[CALL key="), "call lines stay unless asked");
        assertEquals("[WORKSPACE no end", ManifestInliner.strip("[WORKSPACE no end", true));
        assertEquals("The cluster has 3 pods.", DiscussionOrchestrator.publishedContent("synthesis", "The cluster has 3 pods."));
    }

    @Test
    void callNumbersAreReadFromKeysOfTheSameAgentOnly() {
        String prefix = "ns/crew/t1/k8s/";
        for (var e : Map.of(K1, 1, "ns/crew/t1/k8s/017-x", 17, "ns/crew/t1/k8s/agree-uuid", 0,
                "ns/crew/t1/k8s/sub/003-x", 0, "ns/crew/t1/k8s/-x", 0).entrySet()) {
            assertEquals(e.getValue(), DiscussionArtifacts.callNumberOf(e.getKey(), prefix), e.getKey());
        }
    }
}
