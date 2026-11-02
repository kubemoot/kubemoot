package ai.kubemoot.agent.nats;

import java.util.Map;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class ManifestInlinerTest {

    private static final String K1 = "ns/crew/t1/k8s/001-pods_list";
    private static final String K2 = "ns/crew/t1/k8s/002-events_list";

    private static final String MANIFEST = "[WORKSPACE agent=k8s calls=2 errors=0 prefix=ns/crew/t1/k8s/]\n"
            + "[CALL key=" + K1 + " tool=pods_list args={\"a\":(1,2)} bytes=9 rows=2 error=false]\n"
            + "[CALL key=" + K2 + " tool=events_list args={} bytes=5 rows=unknown error=false]\n"
            + "The results are stored under these keys and are not shown here. Read a key with workspace_query.";

    @Test
    void eachCallLineBecomesItsStoredResultAndTheHeaderAndFooterGoAway() {
        var stored = Map.of(K1, "[{\"a\":1},{\"a\":2}]", K2, "NAME  AGE\nx  1d");

        String out = ManifestInliner.inline("Findings:\n" + MANIFEST, stored::get, 10_000);

        assertEquals("Findings:\n[pods_list]\n[{\"a\":1},{\"a\":2}]\n[events_list]\nNAME  AGE\nx  1d", out);
    }

    @Test
    void anUnreadableResultBecomesTheUnavailableNoticeNotAnEmptyReference() {
        String out = ManifestInliner.inline(MANIFEST, key -> K1.equals(key) ? "[1]" : null, 10_000);

        assertTrue(out.contains("[pods_list]\n[1]"), out);
        assertTrue(out.contains(DiscussionOrchestrator.artifactUnavailableNotice(K2)), out);
        assertFalse(out.contains("[CALL key="), out);
    }

    @Test
    void callsPastTheTotalCapAreNamedNotInlined() {
        String out = ManifestInliner.inline(MANIFEST, key -> "x".repeat(50), 40);

        assertTrue(out.contains("[pods_list]\n" + "x".repeat(50)), out);
        assertTrue(out.contains("[events_list] not inlined, the synthesis input is full; key " + K2), out);
    }

    @Test
    void textWithoutAManifestIsReturnedUnchanged() {
        String plain = "[ARTIFACT key=a bytes=1] and [pods_list]\nrows";

        assertSame(plain, ManifestInliner.inline(plain, key -> fail("read"), 10));
        assertNull(ManifestInliner.inline(null, key -> fail("read"), 10));
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
