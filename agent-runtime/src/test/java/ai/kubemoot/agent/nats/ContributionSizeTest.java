package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import ai.kubemoot.agent.chat.LedgerManifests;
import org.junit.jupiter.api.Test;

import java.util.OptionalLong;
import java.util.regex.Matcher;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** A contribution at its largest fits the content cap whole, so the cut that guards the bus never falls inside a result block. */
class ContributionSizeTest {

    private static final OptionalLong HUGE_WINDOW = OptionalLong.of(10_000_000);

    /** A manifest whose posted results fill the contribution's whole room. */
    private static String fullManifest() {
        String[] results = new String[40];
        for (int i = 0; i < results.length; i++) {
            results[i] = "row-" + i + " " + "r".repeat(1_000);
        }
        return LedgerManifests.unstored(HUGE_WINDOW, results);
    }

    @Test
    void aMaximalStatementAndAFullManifestFitTheCapAndKeepEveryBlockClosed() {
        String manifest = fullManifest();
        String content = DiscussionSubscriber.contribution("s".repeat(DiscussionSubscriber.STATEMENT_MAX_CHARS * 2), manifest);

        assertTrue(content.length() <= DiscussionSubscriber.CONTENT_MAX_CHARS,
                content.length() + " > " + DiscussionSubscriber.CONTENT_MAX_CHARS);
        assertTrue(content.endsWith(manifest), "the manifest is never cut");
        assertTrue(manifest.length() > CallManifest.MAX_CHARS, "the posted results make it larger than the descriptions alone: " + manifest.length());
        assertEquals(count(content, "[RESULT]"), count(content, "[END RESULT]"), "every block is closed");
    }

    @Test
    void theManifestAtItsLargestIsWithinTheCapWhateverTheStatement() {
        String manifest = fullManifest();

        assertTrue(DiscussionSubscriber.STATEMENT_MAX_CHARS + 3 + 2 + manifest.length() <= DiscussionSubscriber.CONTENT_MAX_CHARS);
        assertTrue(manifest.length() <= CallManifest.MAX_CHARS + CallManifest.MAX_INLINE_CHARS, "" + manifest.length());
    }

    private static int count(String text, String part) {
        Matcher m = java.util.regex.Pattern.compile(java.util.regex.Pattern.quote(part)).matcher(text);
        int n = 0;
        while (m.find()) {
            n++;
        }
        return n;
    }
}
