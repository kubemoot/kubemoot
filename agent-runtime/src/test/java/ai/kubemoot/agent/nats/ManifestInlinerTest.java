package ai.kubemoot.agent.nats;

import java.util.Map;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class ManifestInlinerTest {

    private static final String K1 = "ns/crew/t1/k8s/001-pods_list";
    private static final String K2 = "ns/crew/t1/k8s/002-events_list";

    private static final String LINE1 = "[CALL key=" + K1 + " tool=pods_list args={\"a\":(1,2)} bytes=9 rows=2 error=false]";
    private static final String LINE2 = "[CALL key=" + K2 + " tool=events_list args={} bytes=5 rows=unknown error=false]";

    private static final String HEADER = "[WORKSPACE agent=k8s calls=2 errors=0 prefix=ns/crew/t1/k8s/]\n";
    private static final String FOOTER = "\nThe results are stored under these keys and are not shown here. Read a key with workspace_query.";
    private static final String MANIFEST = HEADER + LINE1 + "\n" + LINE2 + FOOTER;

    @Test
    void cutLeavesAShortTextAndMarksALongOne() {
        assertEquals("abc", ManifestInliner.cut("abc", 3));
        assertEquals("ab" + ManifestInliner.CUT_NOTE, ManifestInliner.cut("abc", 2));
    }

    @Test
    void theHeaderAndClosingLineGoAwayAndTheCallLinesStayUnlessAsked() {
        String out = ManifestInliner.strip("Findings:\n" + MANIFEST, false);

        assertTrue(out.contains(LINE1) && out.contains(LINE2), out);
        assertFalse(out.contains("[WORKSPACE"), out);
        assertFalse(out.contains("not shown here"), out);
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
