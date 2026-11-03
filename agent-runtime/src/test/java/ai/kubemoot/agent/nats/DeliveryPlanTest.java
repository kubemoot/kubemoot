package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.List;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class DeliveryPlanTest {

    private static ThreadCatalog.Entry entry(String agent, int n, String tool, long bytes, boolean error) {
        return new ThreadCatalog.Entry("ns/crew/t1/" + agent + "/" + String.format("%03d", n) + "-" + tool, agent, tool,
                "{}", -1, bytes, "text", error);
    }

    private static List<ThreadCatalog.Entry> uncovered() {
        var list = new ArrayList<ThreadCatalog.Entry>();
        for (int n = 1; n <= 9; n++) {
            list.add(entry("qm", n, "get_vm_rrd_data", 22_000, false));
        }
        list.add(entry("k8s", 1, "pods_list", 90_000, false));
        return list;
    }

    private static final List<String> ANALYSTS = List.of("compute", "advisor");

    @Test
    void nothingUncoveredMeansSynthesize() {
        var plan = DeliveryPlan.decide(List.of(), false, ANALYSTS, 0);

        assertFalse(plan.convenes());
    }

    @Test
    void uncoveredResultsThatFitTheRoomMeanSynthesize() {
        assertFalse(DeliveryPlan.decide(uncovered(), true, ANALYSTS, 0).convenes());
    }

    @Test
    void resultsThatDoNotFitConveneTheQueryCapableAnalysts() {
        var plan = DeliveryPlan.decide(uncovered(), false, ANALYSTS, 0);

        assertTrue(plan.convenes());
        assertEquals(2, plan.assignments().size());
    }

    @Test
    void withoutAQueryCapableAnalystTheRefinePathStays() {
        assertFalse(DeliveryPlan.decide(uncovered(), false, List.of(), 0).convenes());
    }

    @Test
    void analystsAreConvenedOnlyOncePerThread() {
        assertFalse(DeliveryPlan.decide(uncovered(), false, ANALYSTS, 1).convenes());
    }

    @Test
    void groupsAreSharedOutLargestFirstSoTheReadersFinishTogether() {
        var assignments = DeliveryPlan.assign(uncovered(), ANALYSTS);

        assertEquals("compute", assignments.get(0).analyst());
        assertEquals(java.util.Map.of("ns/crew/t1/qm/*-get_vm_rrd_data", 9), assignments.get(0).patterns());
        assertEquals("advisor", assignments.get(1).analyst());
        assertEquals(java.util.Map.of("ns/crew/t1/k8s/*-pods_list", 1), assignments.get(1).patterns());
    }

    @Test
    void oneGroupGoesToOneAnalystAndTheOtherGetsNoAssignment() {
        var onlyRrd = uncovered().subList(0, 9);

        var assignments = DeliveryPlan.assign(onlyRrd, ANALYSTS);

        assertEquals(1, assignments.size());
    }

    @Test
    void aToolErrorIsNotAssigned() {
        var list = new ArrayList<>(uncovered());
        list.add(entry("pve", 1, "get_node_status", 100, true));

        var patterns = DeliveryPlan.assign(list, ANALYSTS).stream().flatMap(a -> a.patterns().keySet().stream()).toList();

        assertFalse(patterns.stream().anyMatch(p -> p.contains("get_node_status")), patterns.toString());
    }
}
