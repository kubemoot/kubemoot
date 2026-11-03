package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.List;
import java.util.OptionalLong;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class ThreadCatalogTest {

    private static final String PREFIX = "ns/crew/t1/";

    private static ThreadCatalog.Entry entry(String agent, int n, String tool, String args, long rows, long bytes,
                                             boolean error) {
        return new ThreadCatalog.Entry(PREFIX + agent + "/" + String.format("%03d", n) + "-" + tool, agent, tool, args,
                rows, bytes, "text", error);
    }

    private static ThreadCatalog proxmox() {
        var list = new ArrayList<ThreadCatalog.Entry>();
        list.add(entry("qm", 1, "list_vms", "{\"node\":\"pve\"}", 2, 40, false));
        for (int n = 2; n <= 10; n++) {
            list.add(entry("qm", n, "get_vm_rrd_data", "{\"node\":\"pve\",\"vmid\":" + (99 + n) + "}", 60, 22_000, false));
        }
        list.add(entry("pve", 1, "get_node_status", "{}", -1, 900, true));
        return new ThreadCatalog(PREFIX, list);
    }

    @Test
    void theTotalsLineCountsEveryCallErrorAndByte() {
        String text = proxmox().render(1_000_000);

        assertTrue(text.startsWith("[CATALOG calls=11 errors=1 bytes=198940 groups=3 prefix=" + PREFIX + "]\n"), text);
    }

    @Test
    void everyCallHasALineWithItsToolArgumentsFormatRowsBytesAndError() {
        String text = proxmox().render(1_000_000);

        assertTrue(text.contains("[CATALOG-CALL key=" + PREFIX + "qm/002-get_vm_rrd_data tool=get_vm_rrd_data "
                + "args={\"node\":\"pve\",\"vmid\":101} format=text rows=60 bytes=22000 error=false]"), text);
        assertTrue(text.contains("[CATALOG-CALL key=" + PREFIX + "pve/001-get_node_status tool=get_node_status args={} "
                + "format=text rows=unknown bytes=900 error=true]"), text);
    }

    @Test
    void aGroupLineHoldsTheCountsAndThePatternThatReadsTheGroup() {
        String text = proxmox().render(1_000_000);

        assertTrue(text.contains("[GROUP agent=qm tool=get_vm_rrd_data calls=9 errors=0 bytes=198000 rows=540 pattern="
                + PREFIX + "qm/*-get_vm_rrd_data]"), text);
        assertTrue(text.contains("[GROUP agent=pve tool=get_node_status calls=1 errors=1 bytes=900 rows=unknown pattern="
                + PREFIX + "pve/*-get_node_status]"), text);
    }

    @Test
    void whenTheCallLinesDoNotFitOnlyTheGroupLinesAreGivenAndNoGroupIsDropped() {
        String text = proxmox().render(900);

        assertFalse(text.contains("[CATALOG-CALL"), text);
        assertEquals(3, text.split("\\[GROUP ", -1).length - 1);
        assertTrue(text.contains("[CATALOG calls=11"), "the totals stay: " + text);
        assertTrue(text.contains("SELECT DISTINCT _call, _args"), "the legend says how to list the calls");
    }

    @Test
    void theLegendNamesTheThreeWaysToReadAndTheCallColumns() {
        String text = proxmox().render(1_000_000);

        for (String part : List.of("workspace_query", "/artifacts/<key>", "_call", "_args", "_arg_<name>")) {
            assertTrue(text.contains(part), part);
        }
    }

    @Test
    void aCatalogLineIsNeverReadAsATooleManifestLine() {
        assertFalse(ai.kubemoot.agent.chat.CallManifest.CALL_LINE.matcher(proxmox().render(1_000_000)).find());
    }

    @Test
    void theCountsLineNamesTheCallsPerTool() {
        assertEquals("[CATALOG calls=11 errors=1 bytes=198940 tools={get_node_status=1, list_vms=1, get_vm_rrd_data=9}]",
                proxmox().counts());
    }

    @Test
    void anEmptyCatalogIsEmpty() {
        var catalog = new ThreadCatalog(PREFIX, List.of());

        assertTrue(catalog.isEmpty());
        assertEquals(0, catalog.calls());
    }

    @Test
    void theRoomForTheCatalogIsAQuarterOfTheWindowOrAFixedSizeWhenNoneIsKnown() {
        assertEquals(24_576, ThreadCatalog.roomChars(OptionalLong.of(32_768)));
        assertEquals(8_000, ThreadCatalog.roomChars(OptionalLong.empty()));
    }

    @Test
    void callsAreListedInKeyOrderWhateverOrderTheyWereGiven() {
        var reversed = new ArrayList<>(proxmox().entries());
        java.util.Collections.reverse(reversed);

        assertEquals(proxmox().entries(), new ThreadCatalog(PREFIX, reversed).entries());
    }
}
