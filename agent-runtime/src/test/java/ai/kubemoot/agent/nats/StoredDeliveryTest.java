package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class StoredDeliveryTest {

    private static final String DIR = "ns/crew/t1/qm/";
    private static final String HEAD = "Discussion thread t1\n[tooler k8s-workloads] manifest\n";

    private static String key(int n, String tool) {
        return DIR + String.format("%03d", n) + "-" + tool;
    }

    private static ThreadCatalog.Entry entry(int n, String tool) {
        return new ThreadCatalog.Entry(key(n, tool), "qm", tool, "{}", -1, 10, "text", false);
    }

    private static ThreadCatalog catalog() {
        var list = new ArrayList<ThreadCatalog.Entry>();
        list.add(entry(1, "list_vms"));
        for (int n = 2; n <= 4; n++) {
            list.add(entry(n, "get_vm_rrd_data"));
        }
        return new ThreadCatalog("ns/crew/t1/", list);
    }

    private static String thread() {
        var sb = new StringBuilder(HEAD);
        sb.append("[CALL key=").append(key(1, "list_vms")).append(" tool=list_vms format=text bytes=10 rows=2 error=false]\n");
        for (int n = 2; n <= 4; n++) {
            sb.append("[CALL key=").append(key(n, "get_vm_rrd_data"))
                    .append(" tool=get_vm_rrd_data format=text bytes=10 rows=unknown error=false]\n");
        }
        return sb.toString();
    }

    private final Set<String> fetched = ConcurrentHashMap.newKeySet();

    private String read(String key) {
        fetched.add(key);
        return "BODY of " + key;
    }

    private StoredDelivery prepare(Map<String, Set<String>> reads, String... analysts) {
        var coverage = EvidenceCoverage.compute(catalog(), reads, List.of(analysts));
        return StoredDelivery.prepare("CTX\n\n", thread(), catalog(), coverage, this::read);
    }

    @Test
    void coveredCallsAreNeverReadAndStandAsOneLineNamingTheAnalyst() {
        var delivery = prepare(Map.of("compute", Set.of(DIR + "*-get_vm_rrd_data")), "compute");

        assertEquals(Set.of(key(1, "list_vms")), fetched);
        assertEquals(List.of(key(1, "list_vms")), delivery.results().stream().map(StoredResult::key).toList());
        assertTrue(delivery.conversation().contains("[COVERED key=" + key(2, "get_vm_rrd_data")
                + " tool=get_vm_rrd_data by=compute]"));
        assertFalse(delivery.conversation().contains("BODY"), "results are put in later, by the rounds");
    }

    @Test
    void theCoverageBlockHeadsTheThreadAfterTheContext() {
        var delivery = prepare(Map.of("compute", Set.of(DIR + "*-get_vm_rrd_data")), "compute");

        String text = delivery.conversation();
        assertTrue(text.startsWith("CTX\n\n"), text);
        int block = text.indexOf("[COVERAGE analyst=compute read=3 of 4 stored calls: get_vm_rrd_data 3 of 3]");
        assertTrue(block > 0 && block < text.indexOf("Discussion thread t1"), text);
        assertTrue(text.contains("[CATALOG calls=4 errors=0"), "the counts are given too");
    }

    @Test
    void whenEverythingIsCoveredNothingIsReadAndNoResultIsListed() {
        var delivery = prepare(Map.of("compute", Set.of(DIR + "*")), "compute");

        assertTrue(fetched.isEmpty());
        assertTrue(delivery.results().isEmpty());
        assertTrue(delivery.uncoveredKeys().isEmpty());
        assertEquals(4, delivery.coveredKeys().size());
    }

    @Test
    void whenNoAnalystReadAnythingEveryResultIsReadOnceAndListed() {
        var delivery = prepare(Map.of());

        assertEquals(4, fetched.size());
        assertEquals(4, delivery.results().size());
        assertEquals(4, delivery.uncoveredKeys().size());
        assertFalse(delivery.conversation().contains("[COVERED key="));
    }

    @Test
    void anUnreadableResultIsListedAsUnreadable() {
        var coverage = EvidenceCoverage.compute(catalog(), Map.of(), List.of());

        var delivery = StoredDelivery.prepare("", thread(), catalog(), coverage, key -> null);

        assertEquals(4, delivery.results().size());
        assertTrue(delivery.results().stream().noneMatch(StoredResult::readable));
    }

    @Test
    void withoutACatalogTheThreadIsDeliveredAsBeforeWithNoCoverageBlock() {
        var empty = new ThreadCatalog("ns/crew/t1/", List.of());
        var coverage = EvidenceCoverage.compute(empty, Map.of(), List.of());

        var delivery = StoredDelivery.prepare("CTX\n\n", thread(), empty, coverage, this::read);

        assertFalse(delivery.conversation().contains("[COVERAGE"));
        assertEquals(4, delivery.results().size());
    }
}
