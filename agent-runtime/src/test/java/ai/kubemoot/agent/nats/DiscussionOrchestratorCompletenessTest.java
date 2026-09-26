package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for the synthesis completeness-contract helpers in DiscussionOrchestrator:
 * extracting the expected entity names from an inlined kubectl-style table, finding the
 * omitted ones in a draft, and deciding whether the contract applies. All pure/static.
 */
class DiscussionOrchestratorCompletenessTest {

    private static final String TABLE =
            "[resources_list]\n"
            + "APIVERSION   KIND        NAME          STATUS   AGE\n"
            + "v1           Namespace   arc-runners   Active   114d\n"
            + "v1           Namespace   cert-manager  Active   115d\n"
            + "v1           Namespace   default       Active   200d\n";

    @Test
    void expectedItemTables_readsTheNameColumnPastMarkerAndHeader() {
        assertEquals(List.of(List.of("arc-runners", "cert-manager", "default")),
                DiscussionOrchestrator.expectedItemTables(TABLE),
                "the [marker] and the header are skipped; the NAME column of each data row is read");
    }

    @Test
    void expectedItemTables_emptyWhenNoNameTable() {
        assertTrue(DiscussionOrchestrator.expectedItemTables("just some prose\nno table here\n").isEmpty());
        assertTrue(DiscussionOrchestrator.expectedItemTables(null).isEmpty());
    }

    @Test
    void expectedItemTables_blankLineEndsTheTableSoProseIsNotRead() {
        // Prose AFTER a table (separated by a blank) must not be read as data rows.
        String withProse = TABLE + "\nThis analysis shows the namespaces are healthy.\n";
        assertEquals(List.of(List.of("arc-runners", "cert-manager", "default")),
                DiscussionOrchestrator.expectedItemTables(withProse),
                "a blank line ends the table; the prose sentence is not extracted");
    }

    @Test
    void expectedItemTables_filtersPunctuationGarbageToNameShape() {
        // A data row whose NAME-column token is punctuation garbage is dropped.
        String messy = "APIVERSION KIND NAME\n"
                + "v1 Namespace arc-runners\n"
                + "note: (likely) policies.\n";  // NAME-col token "(likely)" is not name-shaped
        assertEquals(List.of(List.of("arc-runners")),
                DiscussionOrchestrator.expectedItemTables(messy),
                "only DNS-name-shaped tokens are kept");
    }

    @Test
    void expectedItemTables_keepsEachSectionSeparate() {
        // A namespace table and a workloads table are separate groups, not merged.
        String twoTables = TABLE + "\n"
                + "[resources_list]\nAPIVERSION KIND NAME\napps/v1 Deployment nginx\napps/v1 Deployment redis\n";
        var tables = DiscussionOrchestrator.expectedItemTables(twoTables);
        assertEquals(2, tables.size(), "two distinct tables");
        assertEquals(List.of("nginx", "redis"), tables.get(1));
    }

    @Test
    void expectedItemTables_resetsAtAllCapsHeaderWithoutNameColumn() {
        String twoBlocks = TABLE + "CONDITION   REASON      MESSAGE\nTrue        Available   ok\n";
        assertEquals(List.of(List.of("arc-runners", "cert-manager", "default")),
                DiscussionOrchestrator.expectedItemTables(twoBlocks),
                "the NAME-less header ends the table; 'ok'/'available' are not read as names");
    }

    @Test
    void nameColumnIndex_findsNameInAnAllCapsHeaderOnly() {
        assertEquals(2, DiscussionOrchestrator.nameColumnIndex("APIVERSION KIND NAME STATUS AGE"));
        assertEquals(0, DiscussionOrchestrator.nameColumnIndex("NAME READY STATUS"));
        assertEquals(-1, DiscussionOrchestrator.nameColumnIndex("v1 Namespace arc-runners"),
                "a data row (mixed case) is not a header");
        assertEquals(-1, DiscussionOrchestrator.nameColumnIndex("APIVERSION KIND STATUS"),
                "an all-caps header without a NAME column returns -1");
    }

    @Test
    void missingNames_returnsExpectedNotPresentCaseInsensitive() {
        var expected = List.of("arc-runners", "cert-manager", "ollama-rig1");
        assertEquals(List.of("ollama-rig1"),
                DiscussionOrchestrator.missingNames(expected, "We have Arc-Runners and cert-manager."));
        assertTrue(DiscussionOrchestrator.missingNames(expected, "arc-runners cert-manager ollama-rig1").isEmpty());
        assertEquals(expected, DiscussionOrchestrator.missingNames(expected, null));
    }

    @Test
    void missingNames_prefixNameNotSatisfiedBySubstringOfLongerName() {
        // Only the longer hyphenated names appear; the shorter PREFIX names must still be
        // reported missing - the exact ollama-rig0/rig1 and crew-x/crew-x-y case.
        var expected = List.of("ollama", "ollama-rig0", "crew-homelab-pilot", "crew-homelab-pilot-prose");
        String draft = "- ollama-rig0: inference host\n- crew-homelab-pilot-prose: prose crew\n";
        var missing = DiscussionOrchestrator.missingNames(expected, draft);
        assertTrue(missing.contains("ollama"), "'ollama' is not present just because 'ollama-rig0' is");
        assertTrue(missing.contains("crew-homelab-pilot"), "the prefix name is not matched inside the longer one");
        assertFalse(missing.contains("ollama-rig0"), "the longer name IS present");
        assertFalse(missing.contains("crew-homelab-pilot-prose"), "the longer name IS present");
    }

    @Test
    void draftMentions_wholeTokenOnly() {
        assertTrue(DiscussionOrchestrator.draftMentions("- nats: message broker", "nats"));
        assertFalse(DiscussionOrchestrator.draftMentions("- ollama-rig0: host", "ollama"),
                "hyphen-adjacent substring is not a whole-token mention");
        assertTrue(DiscussionOrchestrator.draftMentions("Uses NATS for messaging", "nats"),
                "a standalone word matches case-insensitively (prose mention cannot be distinguished)");
    }

    @Test
    void enumeratedTable_picksTheTableTheDraftIsEnumerating() {
        var namespaces = List.of("arc-runners", "cert-manager", "default", "harbor", "keda", "nats");
        var workloads = List.of("nginx", "redis", "postgres", "grafana", "prometheus", "loki");
        var tables = List.of(namespaces, workloads);
        // draft lists the namespaces (not the workloads) -> the namespace table is chosen.
        var draft = "arc-runners cert-manager default harbor keda (truncated)";
        assertEquals(namespaces, DiscussionOrchestrator.enumeratedTable(tables, draft),
                "the table whose names the draft mostly lists is selected; workloads are ignored");
    }

    @Test
    void enumeratedTable_emptyWhenDraftEnumeratesNoTable() {
        var namespaces = List.of("a1", "a2", "a3", "a4", "a5", "a6");
        var tables = List.of(namespaces);
        assertTrue(DiscussionOrchestrator.enumeratedTable(tables, "There are 6 items.").isEmpty(),
                "a count answer names none -> no table is being enumerated");
        assertTrue(DiscussionOrchestrator.enumeratedTable(List.of(List.of("a1", "a2")), "a1 a2").isEmpty(),
                "a table smaller than 5 is never enforced");
        assertTrue(DiscussionOrchestrator.enumeratedTable(tables, null).isEmpty());
    }

    @Test
    void enumeratedTable_catchesTruncatedInventoryBelowMajority() {
        // A 20-name table where the draft lists only 8 (40%, below any majority) must STILL
        // be enforced - a badly-truncated inventory is exactly when the contract is needed.
        var big = new java.util.ArrayList<String>();
        for (int i = 1; i <= 20; i++) {
            big.add(String.format("nsentry%02d", i));
        }
        String draft = "nsentry01 nsentry02 nsentry03 nsentry04 nsentry05 nsentry06 nsentry07 nsentry08 (truncated)";
        assertEquals(big, DiscussionOrchestrator.enumeratedTable(List.of(big), draft),
                "8 of 20 present clears the absolute floor of 5, so the truncated inventory is enforced");
    }

    @Test
    void isNameShaped_acceptsDnsNamesRejectsPunctuation() {
        assertTrue(DiscussionOrchestrator.isNameShaped("crew-homelab-pilot-prose"));
        assertTrue(DiscussionOrchestrator.isNameShaped("ollama-rig0"));
        assertFalse(DiscussionOrchestrator.isNameShaped("policies."));
        assertFalse(DiscussionOrchestrator.isNameShaped("(likely)"));
        assertFalse(DiscussionOrchestrator.isNameShaped("MESSAGE"));
    }

    @Test
    void completenessReprompt_namesTheOmittedEntriesAndKeepsTheDraft() {
        String rp = DiscussionOrchestrator.completenessReprompt("INPUT", "DRAFT", List.of("ollama-rig1", "crew-test"));
        assertTrue(rp.contains("DRAFT"), "the draft is carried so the model keeps what it has");
        assertTrue(rp.contains("ollama-rig1") && rp.contains("crew-test"), "the omitted names are stated");
        assertTrue(rp.contains("EVERY entry"), "it demands the complete list");
    }
}
