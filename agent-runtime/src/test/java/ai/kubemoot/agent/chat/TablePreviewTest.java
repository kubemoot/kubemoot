package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A node list is about 3,000 characters a row because of its LABELS column. The preview shows
 * every row by its leading columns, names the columns left out, and cuts nothing else.
 */
class TablePreviewTest {

    private static final String LABELS = ("beta.kubernetes.io/arch=amd64,kubernetes.io/hostname=%s,"
            + "node.kubernetes.io/instance-type=talos,topology.kubernetes.io/zone=rig0,").repeat(24);

    /** A kubectl table of nodes, columns padded the way the printer pads them. */
    private static String nodes(int count, boolean oneNotReady) {
        StringBuilder out = new StringBuilder(String.format(
                "%-18s %-10s %-15s %-6s %-9s %-15s %s%n", "NAME", "STATUS", "ROLES", "AGE", "VERSION", "INTERNAL-IP", "LABELS"));
        for (int i = 1; i <= count; i++) {
            String status = oneNotReady && i == count ? "NotReady" : "Ready";
            out.append(String.format("%-18s %-10s %-15s %-6s %-9s %-15s %s%n", "node-" + i, status,
                    i == 1 ? "control-plane" : "<none>", "5d", "v1.36.0", "10.0.0." + i, LABELS.replace("%s", "node-" + i)));
        }
        return out.toString();
    }

    @Test
    void everyRowOfAWideTableIsShownByItsLeadingColumnsAndTheLabelsColumnIsNamedNotShown() {
        String text = nodes(14, true);
        assertTrue(text.length() > 40_000, "the table is the size of the evidence: " + text.length());

        String preview = TablePreview.of(text);

        assertTrue(preview.startsWith("14 rows. Columns shown: NAME, STATUS, ROLES, AGE, VERSION, INTERNAL-IP."), preview);
        assertTrue(preview.contains("Columns left out: LABELS"), preview);
        assertTrue(preview.contains("node-14                    NotReady") || preview.contains("NotReady"), preview);
        for (int i = 1; i <= 14; i++) {
            assertTrue(preview.contains("node-" + i + " "), "row " + i + " is shown: " + preview);
        }
        assertFalse(preview.contains("beta.kubernetes.io"), "no label is shown");
        assertTrue(preview.length() < 2_000, "the preview is small: " + preview.length());
    }

    @Test
    void aRowIsNeverShownInPart() {
        String preview = TablePreview.of(nodes(3, false));
        String[] lines = preview.split("\n");

        // line 0 is the summary, line 1 the header, then the rows: each ends where a column ends
        assertTrue(lines[2].endsWith("10.0.0.1"), lines[2]);
        assertTrue(lines[3].endsWith("10.0.0.2"), lines[3]);
    }

    @Test
    void aTableWithNothingToLeaveOutIsShownWhole() {
        String text = "NAME      READY   STATUS    RESTARTS   AGE\ncoredns   1/1     Running   0          5d\netcd      1/1     Running   2          5d\n";

        String preview = TablePreview.of(text);

        assertTrue(preview.startsWith("2 rows. Columns shown: NAME, READY, STATUS, RESTARTS, AGE."), preview);
        assertFalse(preview.contains("left out"), preview);
        assertTrue(preview.contains("etcd      1/1     Running   2          5d"), preview);
    }

    @Test
    void aTableWithManyRowsSaysHowManyItShows() {
        String preview = TablePreview.of(nodes(100, false));

        assertTrue(preview.startsWith("100 rows."), preview);
        assertTrue(preview.contains("(first 40 of 100 rows)"), preview);
        assertTrue(preview.length() <= TablePreview.MAX_CHARS + 100, "bounded: " + preview.length());
    }

    @Test
    void theFirstColumnIsKeptEvenWhenItAloneIsWide() {
        String text = "NAME" + " ".repeat(300) + "STATUS\n" + "x".repeat(250) + "   Ready\n";

        String preview = TablePreview.of(text);

        assertTrue(preview.contains("Columns shown: NAME."), preview);
        assertTrue(preview.contains("Columns left out: STATUS"), preview);
    }

    @Test
    void whatIsNotAKubectlTableHasNoPreview() {
        assertNull(TablePreview.of(null));
        assertNull(TablePreview.of(""));
        assertNull(TablePreview.of("{\"items\":[]}"));
        assertNull(TablePreview.of("plain text\nmore text"));
        assertNull(TablePreview.of("NAME"));
    }

    @Test
    void aStandInForATableShowsThePreviewInPlaceOfTheFirstCharacters() {
        String stand = ToolResultStandIn.of("resources_list", nodes(14, true), true, "k8s-nodes/001-resources_list");

        assertTrue(stand.contains("Stored as workspace artifact k8s-nodes/001-resources_list."), stand);
        assertTrue(stand.contains("Table preview: 14 rows."), stand);
        assertTrue(stand.contains("NotReady"), "the agent that stands in for the crew sees the exception: " + stand);
        assertFalse(stand.contains("First 20 lines"), stand);
    }

    @Test
    void aStandInForAnythingElseKeepsItsHead() {
        String stand = ToolResultStandIn.of("logs", "line one\nline two\n", true);

        assertTrue(stand.contains("First 20 lines (cut at 1500 characters):"), stand);
        assertFalse(stand.contains("Table preview"), stand);
    }

    @Test
    void anErrorForATooLargeTableCarriesThePreviewSoTheAgentIsNotLeftWithNothing() {
        var budget = new ToolResultBudget(8_000, 500, new CharsPerToken());
        String error = budget.admit("workspace_head", nodes(14, true), false).text();

        assertTrue(error.contains("The result was not kept."), error);
        assertTrue(error.contains("Table preview: 14 rows."), error);
        assertTrue(error.contains("NotReady"), error);
    }

    @Test
    void theRowsOfAKubectlTableAreCountedWithoutTheContinueToken() {
        String text = "NAME   STATUS\na      Ready\nb      Ready\n# continue: abc\n";

        assertEquals(2, ToolResultShape.of(text).rows());
        assertEquals(-1, ToolResultShape.of("plain text").rows());
    }
}
