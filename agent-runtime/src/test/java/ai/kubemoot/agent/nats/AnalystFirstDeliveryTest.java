package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallHeaders;
import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.chat.PromptWindow;
import ai.kubemoot.agent.chat.SimplePrompt;
import com.fasterxml.jackson.databind.JsonNode;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.api.ObjectInfo;
import io.nats.client.impl.Headers;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.OptionalLong;
import java.util.function.Function;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/**
 * A whole thread whose tooler stored its calls one by one, from the first review to the answer: the
 * analysts get the catalog, the coordinator computes what they read, and the synthesizer is given
 * the analysts' results and only the stored results no analyst read. Where those do not fit the
 * picked model's room, refinement notes rounds carry the rest.
 */
class AnalystFirstDeliveryTest extends CoordinatorThreadTestBase {

    private static final String THREAD = "t-ana";
    private static final String DIR = "ns/crew/" + THREAD + "/k8s-workloads/";
    private static final String RRD_PATTERN = DIR + "*-get_vm_rrd_data";
    private static final String BUCKET = "kubemoot_discussion_artifacts";
    private static final int RRD_BYTES = 22_000;

    private final Map<String, String> stored = new LinkedHashMap<>();
    private final Map<String, String> argsOf = new LinkedHashMap<>();
    private final List<String> fetched = Collections.synchronizedList(new ArrayList<>());
    private ObjectStore store;
    private final List<SimplePrompt> prompts = Collections.synchronizedList(new ArrayList<>());

    @Override
    protected boolean withBus() {
        return true;
    }

    @Override
    protected String resumesJson() {
        return """
                [{"name":"k8s-metrics","description":"Kubernetes metrics tooler","tools":["pods_top"]},
                 {"name":"k8s-workloads","description":"Kubernetes workloads tooler","tools":["resources_list"]},
                 {"name":"k8s-analyst","role":"analyst","description":"Reads what the toolers gathered","tools":["workspace_query","execute_code"]}]
                """;
    }

    // --- the stored calls ---

    private static String listKey() {
        return DIR + "001-list_vms";
    }

    private static String rrdKey(int n) {
        return DIR + String.format("%03d", n + 1) + "-get_vm_rrd_data";
    }

    private static String rrd(int n) {
        return "UNIQUE-RRD-" + n + "\n" + ("cpu,mem,disk 0.5,100,3\n".repeat(RRD_BYTES / 24));
    }

    private void storeProxmoxShape() {
        stored.put(listKey(), "vmid,name\n101,web\n102,db\n");
        argsOf.put(listKey(), "{\"node\":\"pve\"}");
        for (int n = 1; n <= 9; n++) {
            stored.put(rrdKey(n), rrd(n));
            argsOf.put(rrdKey(n), "{\"node\":\"pve\",\"vmid\":" + (100 + n) + "}");
        }
    }

    private static String agentOf(String key) {
        String[] parts = key.split("/");
        return parts[parts.length - 2];
    }

    private static String keyOf(String agent, int n, String tool) {
        return "ns/crew/" + THREAD + "/" + agent + "/" + String.format("%03d", n) + "-" + tool;
    }

    /** Stores one call of {@code agent} whose result is {@code bytes} long (empty for 0), and returns its key. */
    private String put(String agent, int n, String tool, int bytes) {
        String key = keyOf(agent, n, tool);
        String head = "UNIQUE-" + agent + "-" + n + "-" + tool + "\n";
        String body = head + "cpu,mem,disk 0.5,100,3\n".repeat(bytes / 22 + 1);
        stored.put(key, bytes == 0 ? "" : body.substring(0, Math.min(bytes, body.length())));
        argsOf.put(key, "{\"n\":" + n + "}");
        return key;
    }

    private String putAll(String agent, int from, String tool, int count, int bytes) {
        String last = null;
        for (int n = from; n < from + count; n++) {
            last = put(agent, n, tool, bytes);
        }
        return last;
    }

    private static String patternOf(String agent, String tool) {
        return "ns/crew/" + THREAD + "/" + agent + "/*-" + tool;
    }

    private static String toolOf(String key) {
        return key.substring(key.indexOf('-', key.lastIndexOf('/')) + 1);
    }

    private String manifest() {
        var sb = new StringBuilder("[WORKSPACE agent=k8s-workloads calls=" + stored.size() + " errors=0 prefix=" + DIR + "]\n");
        stored.forEach((key, body) -> sb.append("[CALL key=").append(key).append(" tool=").append(toolOf(key))
                .append(" format=text bytes=").append(body.length()).append(" rows=unknown error=false]\n"));
        return sb.append("The results are stored under these keys and are not shown here. Read a key with workspace_query.")
                .toString();
    }

    private ObjectInfo infoOf(String key) {
        var h = new Headers();
        h.put(CallHeaders.OPERATION_NAME, CallHeaders.OPERATION_EXECUTE_TOOL);
        h.put(CallHeaders.TOOL_NAME, toolOf(key));
        h.put(CallHeaders.AGENT_NAME, agentOf(key));
        h.put(CallHeaders.TOOL_CALL_ARGUMENTS, argsOf.getOrDefault(key, "{}"));
        h.put(CallHeaders.ERROR, "false");
        h.put(CallHeaders.BYTES, Integer.toString(stored.get(key).length()));
        h.put(CallHeaders.FORMAT, "text");
        var info = mock(ObjectInfo.class);
        when(info.isDeleted()).thenReturn(false);
        when(info.getObjectName()).thenReturn(key);
        when(info.getHeaders()).thenReturn(h);
        when(info.getSize()).thenReturn((long) stored.get(key).length());
        return info;
    }

    /** The object store lists and serves the stored calls; every fetch of a result is counted. */
    private void storeIsOpen(long window) throws Exception {
        Connection conn = natsProvider.getConnection();
        ObjectStore os = mock(ObjectStore.class);
        store = os;
        when(conn.objectStore(BUCKET)).thenReturn(os);
        when(os.getList()).thenAnswer(inv -> stored.keySet().stream().map(this::infoOf).toList());
        doAnswer(inv -> {
            String key = inv.getArgument(0);
            fetched.add(key);
            ((OutputStream) inv.getArgument(1)).write(stored.get(key).getBytes(StandardCharsets.UTF_8));
            return null;
        }).when(os).get(anyString(), any(OutputStream.class));
        when(chatService.estimateTokens(any())).thenAnswer(inv -> (inv.<String>getArgument(0).length() + 3) / 4L);
        when(chatService.perCallArtifactsOn()).thenReturn(true);
        when(chatService.simpleCallContextTokens()).thenReturn(OptionalLong.of(window));
        when(chatService.simpleLlmCallFor(any(), any(), any())).thenAnswer(call -> {
            Function<PromptWindow, SimplePrompt> promptFor = call.getArgument(2);
            var prompt = promptFor.apply(new PromptWindow() {
                @Override
                public OptionalLong tokens() {
                    return OptionalLong.of(window);
                }

                @Override
                public OptionalLong roomAfter(String system, String user) {
                    return OptionalLong.of(Math.max(0, window - (system.length() + user.length() + 6) / 4L - window / 8));
                }
            });
            prompts.add(prompt);
            boolean notes = prompt.user().contains("STORED RESULTS (round");
            return new ChatService.SimpleLlmResult(notes ? "- [noted] findings" : "Two VMs.", 100, 20);
        });
    }

    // --- driving a thread ---

    private void toReview(String t) throws Exception {
        send(t, "human", "thread_start", "Which VMs use the most CPU?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        send(t, "k8s-metrics", "agree", "11 pods", null);
        send(t, "k8s-workloads", "agree", manifest(), null);
        settle(t, EVALUATION_TO_REVIEW);
    }

    private static String reads(String... keys) throws Exception {
        return MAPPER.writeValueAsString(Map.of("readKeys", List.of(keys)));
    }

    private Map<String, Object> delivery(String t) {
        JsonNode d = forThread(t).stream().filter(e -> e.type().equals("synthesis_done")).findFirst().orElseThrow()
                .data();
        return Map.of("rounds", d.path("rounds").asInt(), "delivery", d.path("delivery"));
    }

    private static List<String> texts(JsonNode array) {
        var out = new ArrayList<String>();
        array.forEach(n -> out.add(n.asText()));
        return out;
    }

    private String allPrompts() {
        synchronized (prompts) {
            return String.join("\n=====\n", prompts.stream().map(SimplePrompt::user).toList());
        }
    }

    // --- part 1: the catalog goes to the analysts with the review prompt ---

    @Test
    void theFirstReviewCarriesTheCompleteCatalogBuiltFromTheObjectHeaders() throws Exception {
        storeProxmoxShape();
        storeIsOpen(32_768);

        toReview(THREAD);

        String content = publishedOfType("review_ready").get(0).body().path("content").asText();
        assertTrue(content.contains("[CATALOG calls=10 errors=0"), content);
        assertTrue(content.contains("[GROUP agent=k8s-workloads tool=get_vm_rrd_data calls=9"), content);
        assertTrue(content.contains("pattern=" + RRD_PATTERN), content);
        assertTrue(content.contains("[CATALOG-CALL key=" + rrdKey(9)) && content.contains("\"vmid\":109"), content);
        assertFalse(content.substring(content.indexOf("[CATALOG calls=")).contains("[CALL key="),
                "a catalog line is never read as a tooler's manifest");
    }

    @Test
    void withPerCallArtifactsOffTheFirstReviewIsUnchangedAndTheBucketIsNotListedForACatalog() throws Exception {
        storeProxmoxShape();
        storeIsOpen(32_768);
        when(chatService.perCallArtifactsOn()).thenReturn(false);

        toReview(THREAD);

        String content = publishedOfType("review_ready").get(0).body().path("content").asText();
        assertFalse(content.contains("[CATALOG"), content);
        assertTrue(content.contains("[k8s-workloads]"), "the contributions are still there");
    }

    @Test
    void withPerCallArtifactsOffASynthesisNeverListsTheBucketAndDeliversEveryResultAsBefore() throws Exception {
        storeProxmoxShape();
        storeIsOpen(131_072);
        when(chatService.perCallArtifactsOn()).thenReturn(false);
        toReview(THREAD);

        send(THREAD, "k8s-analyst", "agree", "checked", reads(DIR + "*"));
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        verify(store, never()).getList();
        assertEquals(10, fetched.size(), "every result is read, since no coverage is computed");
        assertFalse(allPrompts().contains("[COVERED key="));
        assertTrue(allPrompts().contains("UNIQUE-RRD-1\n"));
    }

    // --- part 4: delivery order ---

    @Test
    void aProxmoxShapedThreadFinishesInOneRoundWhenTheAnalystAggregatedTheRrdCallsWithAStarQuery() throws Exception {
        storeProxmoxShape();
        storeIsOpen(32_768);
        toReview(THREAD);

        send(THREAD, "k8s-analyst", "agree", "web 0.5, db 0.5 (SUM over _arg_vmid)", reads(RRD_PATTERN));
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        var done = delivery(THREAD);
        assertEquals(1, done.get("rounds"));
        assertEquals(1, prompts.size(), "one model call, no refinement");
        assertFalse(allPrompts().contains("UNIQUE-RRD-"), "covered evidence is never given to the synthesizer raw");
        assertTrue(allPrompts().contains("[COVERED key=" + rrdKey(1) + " tool=get_vm_rrd_data by=k8s-analyst]"));
        assertTrue(allPrompts().contains("vmid,name"), "the VM list no analyst read is delivered whole");
        assertTrue(allPrompts().contains("[COVERAGE analyst=k8s-analyst read=9 of 10 stored calls: get_vm_rrd_data 9 of 9]"));
        assertEquals(List.of(listKey()), fetched, "only the uncovered result is read from the store");
        JsonNode d = (JsonNode) done.get("delivery");
        assertEquals(9, d.path("covered").size());
        assertEquals(List.of(listKey()), texts(d.path("uncovered")));
        assertTrue(d.path("stepMillis").has("catalog") && d.path("stepMillis").has("read") && d.path("stepMillis").has("round1"));
    }

    @Test
    void whenTheAnalystsCoveredEverythingTheSynthesizerReadsNoRawEvidence() throws Exception {
        storeProxmoxShape();
        storeIsOpen(32_768);
        toReview(THREAD);

        send(THREAD, "k8s-analyst", "agree", "web and db, computed", reads(DIR + "*"));
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        assertEquals(1, prompts.size());
        assertTrue(fetched.isEmpty(), "nothing is fetched from the store: " + fetched);
        assertFalse(allPrompts().contains("UNIQUE-RRD-") || allPrompts().contains("vmid,name"));
        assertTrue(allPrompts().contains("[COVERAGE read by no analyst: none]"));
        assertEquals(0, ((JsonNode) delivery(THREAD).get("delivery")).path("uncovered").size());
    }

    @Test
    void uncoveredEvidenceThatFitsIsDeliveredWholeInOneRound() throws Exception {
        storeProxmoxShape();
        storeIsOpen(131_072);
        toReview(THREAD);

        send(THREAD, "k8s-analyst", "agree", "checked", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        assertEquals(1, prompts.size());
        assertEquals(1, delivery(THREAD).get("rounds"));
        for (int n = 1; n <= 9; n++) {
            assertTrue(allPrompts().contains("UNIQUE-RRD-" + n + "\n"), "result " + n);
        }
        assertEquals(10, ((JsonNode) delivery(THREAD).get("delivery")).path("uncovered").size());
        assertEquals(1, publishedOfType("review_ready").size(), "the thread is reviewed once");
    }

    @Test
    void uncoveredEvidenceThatDoesNotFitGoesStraightToNotesRoundsAndTheThreadIsReviewedOnce() throws Exception {
        storeProxmoxShape();
        storeIsOpen(16_384);
        toReview(THREAD);
        send(THREAD, "k8s-analyst", "agree", "looked at the VM list", reads(listKey()));

        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        assertEquals(1, publishedOfType("review_ready").size(), "the thread never goes back to review");
        assertEquals(1, forThread(THREAD).stream().filter(e -> e.type().equals("review_started")).count());
        assertTrue((int) delivery(THREAD).get("rounds") > 1, "notes rounds carry what does not fit");
        JsonNode d = (JsonNode) delivery(THREAD).get("delivery");
        assertEquals(List.of(listKey()), texts(d.path("covered")));
        assertEquals(9, d.path("uncovered").size());
        assertFalse(d.has("convened"), "a delivery records no convening");
        assertFalse(d.path("stepMillis").has("convene"));
        assertTrue(allPrompts().contains("[COVERED key=" + listKey()));
    }

    // --- the fit check and the fill agree (thread d1bea316) ---

    /** The stored calls of thread d1bea316: two 106,746 byte pods_list results and 11 small ones, 9,916 bytes together. */
    private void storeD1bea316() {
        put("k8s-workloads", 1, "pods_list", 106_746);
        put("k8s-workloads", 2, "events_list", 9_108);
        put("k8s-workloads", 3, "pods_list", 106_746);
        put("k8s-workloads", 4, "pods_list_in_namespace", 0);
        put("k8s-workloads", 5, "pods_list_in_namespace", 0);
        put("proxmox-pve", 1, "list_nodes", 666);
        put("proxmox-pve", 2, "get_node_status", 2_072);
        put("proxmox-pve", 3, "get_node_status", 2_167);
        put("proxmox-pve", 4, "get_node_status", 2_162);
        put("proxmox-pve", 5, "list_storage", 843);
        put("proxmox-pve", 6, "get_storage_content", 2);
        put("proxmox-pve", 7, "get_storage_content", 2);
        put("proxmox-pve", 8, "get_storage_content", 2_423);
        put("proxmox-pve", 9, "get_storage_content", 243);
        put("proxmox-pve", 10, "get_storage_content", 2);
    }

    @Test
    void theSmallResultsOfThreadD1bea316FitOneRound() throws Exception {
        storeD1bea316();
        storeIsOpen(32_768);
        toReview(THREAD);

        // the first review's analyst read the two big pods_list calls with a pattern, and the two small calls
        send(THREAD, "k8s-analyst", "agree", "pods, events, nodes", reads(patternOf("k8s-workloads", "pods_list"),
                keyOf("k8s-workloads", 2, "events_list"), keyOf("proxmox-pve", 1, "list_nodes")));
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        assertEquals(1, publishedOfType("review_ready").size(), "11 results of 9,916 bytes fit the room");
        var done = delivery(THREAD);
        assertEquals(1, done.get("rounds"));
        assertEquals(1, prompts.size());
        JsonNode d = (JsonNode) done.get("delivery");
        assertEquals(11, d.path("uncovered").size());
        assertEquals(4, d.path("covered").size());
        assertTrue(allPrompts().contains("UNIQUE-proxmox-pve-8-get_storage_content"), "the 11 results are delivered whole");
    }

    @Test
    void whenTheTwoBigResultsAreUncoveredTheyDoNotFitAndNotesRoundsCarryThem() throws Exception {
        storeD1bea316();
        storeIsOpen(32_768);
        toReview(THREAD);
        send(THREAD, "k8s-analyst", "agree", "events and nodes", reads(keyOf("k8s-workloads", 2, "events_list"),
                keyOf("proxmox-pve", 1, "list_nodes")));
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        assertEquals(1, publishedOfType("review_ready").size(), "no second review");
        var done = delivery(THREAD);
        assertTrue((int) done.get("rounds") > 1, "the 213 KB of pods_list does not fit one round");
        JsonNode d = (JsonNode) done.get("delivery");
        assertEquals(13, d.path("uncovered").size());
        assertEquals(2, d.path("covered").size());
    }

    @Test
    void theNotesRoundsAreToldTheCatalogCountsAndTheRuleThatCountsComeFromATool() throws Exception {
        storeProxmoxShape();
        storeIsOpen(16_384);
        toReview(THREAD);

        send(THREAD, "k8s-analyst", "agree", "checked", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        assertEquals(1, publishedOfType("review_ready").size());
        assertTrue((int) delivery(THREAD).get("rounds") > 1, "refinement carries what does not fit");
        assertTrue(allPrompts().contains("[CATALOG calls=10 errors=0"), "the catalog counts are given");
        assertTrue(allPrompts().contains("[COVERAGE read by no analyst: 10 calls"));
        assertTrue(allPrompts().contains("NEVER derive a count"), "the rule that counts come from a tool");
    }

    @Test
    void anAnalystThatAnsweredNoDataCoversNothing() throws Exception {
        storeProxmoxShape();
        storeIsOpen(131_072);
        toReview(THREAD);

        send(THREAD, "k8s-analyst", "agree", "NO_DATA", reads(DIR + "*"));
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        assertEquals(10, ((JsonNode) delivery(THREAD).get("delivery")).path("uncovered").size());
        assertTrue(allPrompts().contains("UNIQUE-RRD-1\n"));
    }
}
