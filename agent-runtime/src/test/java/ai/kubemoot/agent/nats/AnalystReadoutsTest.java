package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class AnalystReadoutsTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void readKeysComeFromTheSignalsMetadata() throws Exception {
        var signal = MAPPER.readTree("{\"metadata\":{\"readKeys\":[\"a/b\",\"c/*\",\"\",7]}}");

        assertEquals(Set.of("a/b", "c/*"), AnalystReadouts.readKeysOf(signal));
    }

    @Test
    void aSignalWithoutReadKeysHasNone() throws Exception {
        assertTrue(AnalystReadouts.readKeysOf(MAPPER.readTree("{}")).isEmpty());
        assertTrue(AnalystReadouts.readKeysOf(MAPPER.readTree("{\"metadata\":{\"readKeys\":\"x\"}}")).isEmpty());
    }

    @Test
    void onlyTheNoDataSentinelIsNotAnAnswerWithData() {
        assertFalse(AnalystReadouts.answeredWithData("NO_DATA"));
        assertFalse(AnalystReadouts.answeredWithData("  TOOL_GAP \n"));
        assertFalse(AnalystReadouts.answeredWithData("<think>NO_DATA? no, 3</think>NO_DATA"));
        assertFalse(AnalystReadouts.answeredWithData(""));
        assertFalse(AnalystReadouts.answeredWithData(null));
        assertTrue(AnalystReadouts.answeredWithData("NO_DATA was not the case: 3 pods"));
        assertTrue(AnalystReadouts.answeredWithData("<think>NO_DATA</think>3 pods"));
    }

    @Test
    void queryCapableAnalystsAreTheAnalystsWithAQueryOrCodeTool() throws Exception {
        var resumes = MAPPER.readTree("""
                [{"name":"compute","role":"analyst","tools":["execute_code"]},
                 {"name":"advisor","role":"analyst","tools":["artifact_head","workspace_query"]},
                 {"name":"reader","role":"analyst","tools":["artifact_head"]},
                 {"name":"tooler-with-query","role":"tooler","tools":["workspace_query"]},
                 {"name":"","role":"analyst","tools":["workspace_query"]},
                 {"name":"bare","role":"analyst"}]
                """);

        assertEquals(List.of("compute", "advisor"), AnalystReadouts.queryCapable(resumes));
        assertEquals(List.of(), AnalystReadouts.queryCapable(null));
        assertEquals(List.of(), AnalystReadouts.queryCapable(MAPPER.readTree("{}")));
    }

    private static ThreadCatalog.Entry call(String agent, int n, String tool, String args, boolean error) {
        return new ThreadCatalog.Entry("ns/crew/t1/" + agent + "/" + String.format("%03d", n) + "-" + tool, agent, tool,
                args, 4, 300, "csv", error);
    }

    private static DeliveryPlan.Assignment assignment(String analyst, String pattern, int calls) {
        var patterns = new LinkedHashMap<String, Integer>();
        patterns.put(pattern, calls);
        return new DeliveryPlan.Assignment(analyst, patterns);
    }

    @Test
    void theSecondReviewTaskIsTheQuestionAndTheAssignedCallsWithTheirArguments() {
        var uncovered = List.of(
                call("qm", 1, "get_vm_rrd_data", "{\"vmid\":101}", false),
                call("qm", 2, "get_vm_rrd_data", "{\"vmid\":102}", false),
                call("qm", 3, "get_vm_rrd_data", "{\"vmid\":103}", true),
                call("k8s", 1, "pods_list", "{}", false));

        var tasks = AnalystReadouts.secondReviewTasks("Which node is out of disk?",
                List.of(assignment("compute", "ns/crew/t1/qm/*-get_vm_rrd_data", 2)), uncovered);

        String task = tasks.get("compute");
        assertTrue(task.contains("Which node is out of disk?"), task);
        assertTrue(task.contains("[ASSIGN analyst=compute pattern=ns/crew/t1/qm/*-get_vm_rrd_data calls=2]"), task);
        assertTrue(task.contains("[CATALOG-CALL key=ns/crew/t1/qm/001-get_vm_rrd_data tool=get_vm_rrd_data args={\"vmid\":101}"), task);
        assertTrue(task.contains("002-get_vm_rrd_data"), task);
        assertFalse(task.contains("003-get_vm_rrd_data"), "a tool error is not assigned");
        assertFalse(task.contains("pods_list"), "a call outside the pattern is not part of the task");
        assertTrue(task.contains("_call") && task.contains("_args"), "how a pattern reads as a table: " + task);
        assertTrue(task.contains("NEVER answer from this message alone"));
        assertFalse(task.contains("{{"), "every place is filled");
    }

    @Test
    void eachConvenedAnalystGetsOnlyItsOwnAssignment() {
        var uncovered = List.of(call("qm", 1, "get_vm_rrd_data", "{}", false), call("k8s", 1, "pods_list", "{}", false));

        var tasks = AnalystReadouts.secondReviewTasks("q?", List.of(
                assignment("compute", "ns/crew/t1/qm/*-get_vm_rrd_data", 1),
                assignment("advisor", "ns/crew/t1/k8s/*-pods_list", 1)), uncovered);

        assertEquals(List.of("compute", "advisor"), List.copyOf(tasks.keySet()));
        assertTrue(tasks.get("compute").contains("get_vm_rrd_data") && !tasks.get("compute").contains("pods_list"));
        assertTrue(tasks.get("advisor").contains("pods_list") && !tasks.get("advisor").contains("get_vm_rrd_data"));
        String content = AnalystReadouts.secondReviewContent(tasks);
        assertTrue(content.contains("analyst=compute") && content.contains("analyst=advisor"));
    }

    @Test
    void aMissingQuestionAndNoUncoveredCallsStillGiveATaskNamingThePattern() {
        var tasks = AnalystReadouts.secondReviewTasks(null,
                List.of(assignment("compute", "ns/crew/t1/qm/*-x", 3)), List.of());

        assertTrue(tasks.get("compute").contains("[ASSIGN analyst=compute pattern=ns/crew/t1/qm/*-x calls=3]"));
        assertFalse(tasks.get("compute").contains("{{") || tasks.get("compute").contains("null"));
    }
}
