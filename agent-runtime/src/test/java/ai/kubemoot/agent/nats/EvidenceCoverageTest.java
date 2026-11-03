package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class EvidenceCoverageTest {

    private static final String DIR = "ns/crew/t1/qm/";
    private static final String RRD_PATTERN = DIR + "*-get_vm_rrd_data";

    private static ThreadCatalog.Entry entry(int n, String tool, boolean error) {
        return new ThreadCatalog.Entry(DIR + String.format("%03d", n) + "-" + tool, "qm", tool, "{}", -1, 100, "text",
                error);
    }

    private static ThreadCatalog catalog() {
        var list = new ArrayList<ThreadCatalog.Entry>();
        list.add(entry(1, "list_vms", false));
        for (int n = 2; n <= 10; n++) {
            list.add(entry(n, "get_vm_rrd_data", false));
        }
        list.add(entry(11, "get_node_status", true));
        return new ThreadCatalog("ns/crew/t1/", list);
    }

    private static EvidenceCoverage coverage(Map<String, Set<String>> reads, String... successful) {
        return EvidenceCoverage.compute(catalog(), reads, List.of(successful));
    }

    @Test
    void aStarPatternCoversEveryCallItMatches() {
        var c = coverage(Map.of("compute", Set.of(RRD_PATTERN)), "compute");

        assertEquals(9, c.covered().size());
        assertEquals(Set.of("compute"), c.analystsOf(DIR + "005-get_vm_rrd_data"));
        assertFalse(c.isCovered(DIR + "001-list_vms"));
    }

    @Test
    void aSingleKeyCoversOnlyThatCall() {
        var c = coverage(Map.of("compute", Set.of(DIR + "001-list_vms")), "compute");

        assertEquals(List.of(DIR + "001-list_vms"), c.covered().stream().map(ThreadCatalog.Entry::key).toList());
    }

    @Test
    void aDirectoryCoversEveryCallBelowIt() {
        var c = coverage(Map.of("compute", Set.of(DIR)), "compute");

        assertEquals(10, c.covered().size());
    }

    @Test
    void aStarDoesNotCrossAPathSeparator() {
        assertFalse(EvidenceCoverage.matches("ns/crew/*", DIR + "001-list_vms"));
        assertTrue(EvidenceCoverage.matches("ns/crew/t1/*/001-list_vms", DIR + "001-list_vms"));
    }

    @Test
    void regexCharactersInASpecAreLiteral() {
        assertFalse(EvidenceCoverage.matches("ns/crew/t1/qm/001-list.vms", DIR + "001-list_vms"));
        assertFalse(EvidenceCoverage.matches("", DIR + "001-list_vms"));
        assertFalse(EvidenceCoverage.matches(null, DIR + "001-list_vms"));
    }

    @Test
    void aToolErrorIsNeverCovered() {
        var c = coverage(Map.of("compute", Set.of(DIR + "*")), "compute");

        assertFalse(c.isCovered(DIR + "011-get_node_status"));
        assertEquals(1, c.uncovered().size());
    }

    @Test
    void readsOfAnAnalystThatDidNotAnswerWithDataCountForNothing() {
        var c = coverage(Map.of("compute", Set.of(DIR + "*")), "someone-else");

        assertTrue(c.covered().isEmpty());
        assertEquals(11, c.uncovered().size());
    }

    @Test
    void twoAnalystsReadingTheSameCallAreBothNamed() {
        var c = coverage(Map.of("a", Set.of(RRD_PATTERN), "b", Set.of(DIR + "002-get_vm_rrd_data")), "a", "b");

        assertEquals(Set.of("a", "b"), c.analystsOf(DIR + "002-get_vm_rrd_data"));
        assertEquals(Set.of("a"), c.analystsOf(DIR + "003-get_vm_rrd_data"));
    }

    @Test
    void theSummaryNamesWhatEachAnalystReadAndWhatNoOneDid() {
        var c = coverage(Map.of("compute", Set.of(RRD_PATTERN)), "compute");

        assertEquals("[COVERAGE analyst=compute read=9 of 11 stored calls: get_vm_rrd_data 9 of 9]\n"
                + "[COVERAGE read by no analyst: 2 calls ({get_node_status=1, list_vms=1}), 200 bytes]", c.summary());
    }

    @Test
    void whenNoAnalystReadAnythingTheSummaryIsOneLineWithTheCounts() {
        var c = coverage(Map.of());

        assertEquals("[COVERAGE read by no analyst: 11 calls ({get_node_status=1, get_vm_rrd_data=9, list_vms=1}), 1100 bytes]",
                c.summary());
    }
}
