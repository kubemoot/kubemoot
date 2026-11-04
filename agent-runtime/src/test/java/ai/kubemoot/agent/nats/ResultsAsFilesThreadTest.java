package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallHeaders;
import ai.kubemoot.agent.chat.ChatService;
import com.fasterxml.jackson.databind.JsonNode;
import io.nats.client.Connection;
import io.nats.client.ObjectStore;
import io.nats.client.api.ObjectInfo;
import io.nats.client.impl.Headers;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.OptionalLong;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/**
 * A whole thread whose agents stored every tool result as a file: the first review carries the list of
 * every file, and the synthesizer is given the discussion only (the contributions with their file
 * descriptions), never the content of a file.
 */
class ResultsAsFilesThreadTest extends CoordinatorThreadTestBase {

    private static final String THREAD = "t-files";
    private static final String DIR = "ns/crew/" + THREAD + "/k8s-workloads/";
    private static final String BUCKET = "kubemoot_discussion_artifacts";
    private static final String SECRET_ROW = "UNIQUE-ROW-ONLY-IN-THE-FILE";

    private final Map<String, String> stored = new LinkedHashMap<>();
    private final List<String> fetched = Collections.synchronizedList(new ArrayList<>());
    private final List<String> synthesisPrompts = Collections.synchronizedList(new ArrayList<>());
    private ObjectStore store;

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

    private static String podsKey() {
        return DIR + "001-pods_list";
    }

    private static String callLine() {
        return "[CALL key=" + podsKey() + " tool=pods_list args={\"namespace\":\"a\"} format=json bytes=300 rows=3 "
                + "error=false fields=name,cpu]";
    }

    private static String manifest(String line) {
        return "[WORKSPACE agent=k8s-workloads calls=1 errors=0 prefix=" + DIR + "]\n" + line
                + "\nThe results are stored under these keys and are not shown here. Read a key with workspace_query.";
    }

    private ObjectInfo infoOf(String key) {
        var h = new Headers();
        h.put(CallHeaders.OPERATION_NAME, CallHeaders.OPERATION_EXECUTE_TOOL);
        h.put(CallHeaders.TOOL_NAME, "pods_list");
        h.put(CallHeaders.AGENT_NAME, "k8s-workloads");
        h.put(CallHeaders.TOOL_CALL_ARGUMENTS, "{\"namespace\":\"a\"}");
        h.put(CallHeaders.ERROR, "false");
        h.put(CallHeaders.BYTES, Integer.toString(stored.get(key).length()));
        h.put(CallHeaders.FORMAT, "json");
        var info = mock(ObjectInfo.class);
        when(info.isDeleted()).thenReturn(false);
        when(info.getObjectName()).thenReturn(key);
        when(info.getHeaders()).thenReturn(h);
        when(info.getSize()).thenReturn((long) stored.get(key).length());
        return info;
    }

    /** The object store lists the stored files and counts every read of one; the model calls are captured. */
    private void storeIsOpen() throws Exception {
        stored.put(podsKey(), "[{\"name\":\"" + SECRET_ROW + "\"}]");
        Connection conn = natsProvider.getConnection();
        store = mock(ObjectStore.class);
        when(conn.objectStore(BUCKET)).thenReturn(store);
        when(store.getList()).thenAnswer(inv -> stored.keySet().stream().map(this::infoOf).toList());
        doAnswer(inv -> {
            fetched.add(inv.getArgument(0));
            return null;
        }).when(store).get(anyString(), any(java.io.OutputStream.class));
        when(chatService.simpleCallContextTokens()).thenReturn(OptionalLong.of(32_768));
        when(chatService.simpleLlmCallWithTokens(any(), anyString())).thenAnswer(call -> {
            String prompt = call.getArgument(1);
            if (prompt.contains("Discussion signals")) {
                synthesisPrompts.add(prompt);
            }
            return new ChatService.SimpleLlmResult(prompt.contains("Discussion signals") ? "Two pods." : selection, 100, 20);
        });
    }

    private void toReview(String contributionOfWorkloads) throws Exception {
        send(THREAD, "human", "thread_start", "Which pods use the most CPU?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(THREAD);
        send(THREAD, "k8s-metrics", "agree", "11 pods", null);
        send(THREAD, "k8s-workloads", "agree", contributionOfWorkloads, null);
        settle(THREAD, EVALUATION_TO_REVIEW);
    }

    private String synthesisPrompt() {
        assertEquals(1, synthesisPrompts.size(), "one synthesis call");
        return synthesisPrompts.get(0);
    }

    @Test
    void theFirstReviewCarriesTheListOfEveryFileBuiltFromTheObjectHeaders() throws Exception {
        storeIsOpen();

        toReview(manifest(callLine()));

        String content = publishedOfType("review_ready").get(0).body().path("content").asText();
        assertTrue(content.contains("[CATALOG calls=1 errors=0"), content);
        assertTrue(content.contains("[CATALOG-CALL key=" + podsKey()), content);
        assertFalse(content.substring(content.indexOf("[CATALOG calls=")).contains("[CALL key="),
                "a catalog line is never read as an agent's file description");
    }

    @Test
    void theSynthesizerGetsTheDiscussionWithFileDescriptionsAndNoFileContent() throws Exception {
        storeIsOpen();
        toReview(manifest(callLine()));
        send(THREAD, "k8s-analyst", "agree", "pod a-1 uses the most CPU, 0.9 cores", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        String prompt = synthesisPrompt();

        assertTrue(prompt.contains("pod a-1 uses the most CPU, 0.9 cores"), "the analyst's fact is in the discussion");
        assertTrue(prompt.contains(callLine()), "the description of the file is in the discussion");
        assertTrue(prompt.contains("DEFINE DOMAIN synthesis-files"), "the rules for file descriptions come with them");
        assertTrue(prompt.contains("UNKNOWN"), prompt);
        assertFalse(prompt.contains("[WORKSPACE"), "the header speaks to an agent that can read files");
        assertFalse(prompt.contains("are not shown here"), "the closing line speaks to an agent that can read files");
        assertFalse(prompt.contains(SECRET_ROW), "the content of a file never reaches the synthesizer");
        assertTrue(fetched.isEmpty(), "no file is read for the synthesis: " + fetched);
    }

    @Test
    void aFileNoAgentStatedAnythingFromIsStillDescribedForTheAnswerToNameIt() throws Exception {
        storeIsOpen();
        toReview(manifest(callLine()));
        send(THREAD, "k8s-analyst", "agree", "checked the thread", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        String prompt = synthesisPrompt();

        assertTrue(prompt.contains("tool=pods_list args={\"namespace\":\"a\"} format=json bytes=300 rows=3"), prompt);
        assertTrue(prompt.contains("fields=name,cpu]"), "the data dictionary is in the description");
        assertEquals(1, publishedOfType("review_ready").size(), "the thread is reviewed once");
    }

    @Test
    void aNotStoredLineBringsTheRulesToo() throws Exception {
        storeIsOpen();
        toReview("[WORKSPACE agent=k8s-workloads calls=1 errors=0 prefix=]\n"
                + "[NOT STORED tool=pods_list args={} error=false - the result could not be written to the workspace "
                + "and is not available]");
        send(THREAD, "k8s-analyst", "agree", "no data", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        String prompt = synthesisPrompt();

        assertTrue(prompt.contains("[NOT STORED tool=pods_list"), prompt);
        assertTrue(prompt.contains("DEFINE DOMAIN synthesis-files"), prompt);
    }

    @Test
    void aThreadWhoseContributionsDescribeNoFilesGetsNoFileRules() throws Exception {
        storeIsOpen();
        toReview("12 pods in namespace a");
        send(THREAD, "k8s-analyst", "agree", "confirmed 12", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        String prompt = synthesisPrompt();

        assertFalse(prompt.contains("synthesis-files"), prompt);
        assertTrue(prompt.contains("12 pods in namespace a"));
    }

    @Test
    void theSynthesisEventRecordsNoRoundsOrDeliveryAndTheAnswerHasNoInternalLine() throws Exception {
        storeIsOpen();
        toReview(manifest(callLine()));
        send(THREAD, "k8s-analyst", "agree", "pod a-1", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        awaitTypes(THREAD, 1, "synthesis_done");

        JsonNode done = forThread(THREAD).stream().filter(e -> e.type().equals("synthesis_done")).findFirst()
                .orElseThrow().data();

        assertFalse(done.has("rounds") || done.has("delivery") || done.has("filledBytes"), done.toString());
        assertFalse(done.path("response").asText().contains("[CALL key="));
    }
}
