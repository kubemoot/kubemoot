package ai.kubemoot.agent.chat;

import java.util.List;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class AnalystReadsTest {

    private static final String KEY = "ns/crew/t1/k8s-workloads/001-resources_list";
    private static final String PATTERN = "ns/crew/t1/proxmox-qm/*-get_vm_rrd_data";

    @Test
    void aQueryReadsEveryKeyOfItsArtifactsMap() {
        String args = "{\"artifacts\":{\"vms\":\"" + KEY + "\",\"rrd\":\"" + PATTERN + "\"},\"sql\":\"SELECT 1\"}";

        assertEquals(List.of(KEY, PATTERN), AnalystReads.keysOf("workspace_query", args));
    }

    @Test
    void aQueryWhoseArtifactsMapCameAsAStringIsReadToo() {
        String args = "{\"artifacts\":\"{\\\"vms\\\": \\\"" + KEY + "\\\"}\",\"sql\":\"SELECT 1\"}";

        assertEquals(List.of(KEY), AnalystReads.keysOf("workspace_query", args));
    }

    @Test
    void anArtifactToolReadsItsKey() {
        assertEquals(List.of(KEY), AnalystReads.keysOf("artifact_count", "{\"key\":\"" + KEY + "\"}"));
        assertEquals(List.of(KEY), AnalystReads.keysOf("artifact_jq", "{\"key\":\"" + KEY + "\",\"expr\":\".\"}"));
    }

    @Test
    void codeThatNamesAnArtifactPathAsTextReadsThatKeyOrPattern() {
        String code = "{\"code\":\"import glob\\nfor f in glob.glob('/artifacts/" + PATTERN
                + "'): pass\\nopen('/artifacts/" + KEY + "').read()\"}";

        assertEquals(List.of(PATTERN, KEY), AnalystReads.keysOf("execute_code", code));
    }

    @Test
    void aPathTheCodeBuildsAtRunTimeIsNotTraceable() {
        String code = "{\"code\":\"base = '/artifacts/'\\nopen(base + name)\"}";

        assertEquals(List.of(), AnalystReads.keysOf("execute_code", code));
    }

    @Test
    void otherToolsAndBadArgumentsReadNothing() {
        assertEquals(List.of(), AnalystReads.keysOf("pods_list", "{\"key\":\"" + KEY + "\"}"));
        assertEquals(List.of(), AnalystReads.keysOf("workspace_query", "not json"));
        assertEquals(List.of(), AnalystReads.keysOf("workspace_query", null));
        assertEquals(List.of(), AnalystReads.keysOf(null, "{}"));
        assertEquals(List.of(), AnalystReads.keysOf("artifact_head", "{\"key\":\" \"}"));
    }

    @Test
    void onlyASuccessfulCallIsRecordedAndTakeClearsTheThread() {
        var reads = new AnalystReads();
        String args = "{\"key\":\"" + KEY + "\"}";

        reads.record("t1", "artifact_head", args, false);
        assertEquals(List.of(), reads.take("t1"));

        reads.record("t1", "artifact_head", args, true);
        reads.record("t1", "artifact_head", args, true);
        reads.record("t2", "artifact_tail", "{\"key\":\"other\"}", true);
        assertEquals(List.of(KEY), reads.take("t1"), "a key once, in order");
        assertEquals(List.of(), reads.take("t1"), "taken");
        assertEquals(List.of("other"), reads.take("t2"));
    }

    @Test
    void aMissingThreadRecordsNothing() {
        var reads = new AnalystReads();

        reads.record(null, "artifact_head", "{\"key\":\"k\"}", true);
        reads.record(" ", "artifact_head", "{\"key\":\"k\"}", true);

        assertEquals(List.of(), reads.take(null));
        assertEquals(List.of(), reads.take(" "));
    }

    @Test
    void aThreadKeepsAtMostTheKeyLimit() {
        var reads = new AnalystReads();
        for (int i = 0; i < AnalystReads.MAX_KEYS + 50; i++) {
            reads.record("t1", "artifact_head", "{\"key\":\"k" + i + "\"}", true);
        }

        assertEquals(AnalystReads.MAX_KEYS, reads.take("t1").size());
    }
}
