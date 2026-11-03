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

    @Test
    void theSecondReviewNamesEachAssignmentAndCarriesNoData() {
        var patterns = new LinkedHashMap<String, Integer>();
        patterns.put("ns/crew/t1/qm/*-get_vm_rrd_data", 9);

        String text = AnalystReadouts.secondReviewContent(List.of(new DeliveryPlan.Assignment("compute", patterns)));

        assertTrue(text.contains("[ASSIGN analyst=compute pattern=ns/crew/t1/qm/*-get_vm_rrd_data calls=9]"), text);
        assertTrue(text.contains("WHEN you are named in an assignment") && text.contains("NEVER answer from this message alone"));
        assertFalse(text.contains("{{"), "every place is filled");
    }
}
