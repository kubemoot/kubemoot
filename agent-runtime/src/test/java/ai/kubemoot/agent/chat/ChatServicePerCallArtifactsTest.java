package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.agent.tool.ToolExecutionRequest;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import dev.langchain4j.service.tool.ToolExecutor;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Function;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * With per-call artifacts on, a raw-output tooler stores each tool result as its own workspace
 * object and its contribution is the manifest of those objects. With them off, the contribution is
 * the glued tool output, unchanged.
 */
class ChatServicePerCallArtifactsTest {

    private static final String THREAD = "t1";
    private static final String AGENT = "test-agent";

    private ChatModel chatModel;
    private McpClientService mcpClient;
    private FakeStore store;

    /** An in-memory workspace: keys are {@code ns/crew/<thread>/<agent>/<nnn>-<tool>}. */
    static final class FakeStore implements CallArtifactStore {
        final Map<String, byte[]> bodies = new LinkedHashMap<>();
        final Map<String, Map<String, String>> headers = new LinkedHashMap<>();
        int next;
        boolean failWrites;

        @Override
        public String reserveKey(String threadId, String agent, String tool) {
            return "ns/crew/" + threadId + "/" + agent + "/" + String.format("%03d", ++next) + "-" + tool;
        }

        @Override
        public boolean put(String threadId, String agent, String tool, String key, byte[] body,
                           Map<String, String> hdrs) {
            if (failWrites) {
                return false;
            }
            bodies.put(key, body);
            headers.put(key, hdrs);
            return true;
        }

        String body(String key) {
            return new String(bodies.get(key), StandardCharsets.UTF_8);
        }
    }

    @BeforeEach
    void setUp() {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        store = new FakeStore();
    }

    private ChatService service() {
        var service = new ChatService(chatModel, mock(RagClient.class), mcpClient, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(8, "tooler", false, false),
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
        service.callArtifactStore = store;
        service.perCallArtifacts = true;
        return service;
    }

    private void registerTools(Function<ToolExecutionRequest, String> body, String... names) {
        var tools = new LinkedHashMap<ToolSpecification, ToolExecutor>();
        for (String name : names) {
            var executor = mock(ToolExecutor.class);
            when(executor.execute(any(), any())).thenAnswer(inv -> body.apply(inv.getArgument(0)));
            tools.put(ToolSpecification.builder().name(name).description(name).build(), executor);
        }
        when(mcpClient.getToolSpecifications()).thenReturn(tools);
    }

    /** The model asks for all of {@code calls} in one turn, then answers with text. */
    private void modelCalls(ToolExecutionRequest... calls) {
        var first = mock(ChatResponse.class);
        when(first.aiMessage()).thenReturn(AiMessage.from(List.of(calls)));
        when(first.tokenUsage()).thenReturn(new TokenUsage(100, 20));
        var second = mock(ChatResponse.class);
        when(second.aiMessage()).thenReturn(new AiMessage("done"));
        when(second.tokenUsage()).thenReturn(new TokenUsage(120, 5));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(first, second);
    }

    private static ToolExecutionRequest call(String id, String tool, String args) {
        return ToolExecutionRequest.builder().id(id).name(tool).arguments(args).build();
    }

    private static List<ChatMessage> question() {
        return new ArrayList<>(List.of(UserMessage.from("how many pods?")));
    }

    private ChatService.ToolLoopResult run(ChatService service, long window) {
        var ledger = ToolCallLedger.forLoop(true, store, THREAD, AGENT);
        return service.callWithToolLoop(question(), chatModel, "s", "test", true, window, ledger);
    }

    @Test
    void eachCallIsItsOwnObjectWithHeadersAndTheContributionIsTheManifest() {
        registerTools(r -> "{\"items\":[{\"a\":1},{\"a\":2},{\"a\":3}]}", "pods_list");
        modelCalls(call("call_9", "pods_list", "{\"namespace\":\"kube-system\"}"));
        Instant before = Instant.now();

        var result = run(service(), 0);

        assertEquals(1, store.bodies.size());
        String key = store.bodies.keySet().iterator().next();
        assertEquals("ns/crew/t1/test-agent/001-pods_list", key);
        assertEquals("{\"items\":[{\"a\":1},{\"a\":2},{\"a\":3}]}", store.body(key), "the tool's own text, no [tool] header");
        var h = store.headers.get(key);
        assertEquals("execute_tool", h.get(CallHeaders.OPERATION_NAME));
        assertEquals("pods_list", h.get(CallHeaders.TOOL_NAME));
        assertEquals("call_9", h.get(CallHeaders.TOOL_CALL_ID));
        assertEquals("{\"namespace\":\"kube-system\"}", h.get(CallHeaders.TOOL_CALL_ARGUMENTS));
        assertEquals(AGENT, h.get(CallHeaders.AGENT_NAME));
        assertEquals("false", h.get(CallHeaders.ERROR));
        assertNull(h.get(CallHeaders.ERROR_TYPE));
        assertEquals("3", h.get(CallHeaders.ROWS));
        assertEquals("json", h.get(CallHeaders.FORMAT));
        assertEquals(String.valueOf(store.bodies.get(key).length), h.get(CallHeaders.BYTES));
        assertFalse(Instant.parse(h.get(CallHeaders.STARTED)).isBefore(before.minusSeconds(1)));
        assertNotNull(h.get(CallHeaders.DURATION_MS));

        assertTrue(result.contributionIsToolOutput());
        assertTrue(result.text().startsWith("[WORKSPACE agent=test-agent calls=1 errors=0 prefix="
                + "ns/crew/t1/test-agent/]"), result.text());
        assertTrue(result.text().contains("[CALL key=" + key + " tool=pods_list args={\"namespace\":\"kube-system\"} "
                + "format=json bytes=" + store.bodies.get(key).length + " rows=3 error=false fields=a]"), result.text());
        assertFalse(result.text().contains("{\"a\":1}"), "no result data in the manifest");
    }

    @Test
    void anErrorCallIsStoredAndFlaggedNextToAGoodOne() {
        registerTools(r -> r.name().equals("bad") ? "{\"error\": \"forbidden\"}" : "[{\"x\":1}]", "good", "bad");
        modelCalls(call("c1", "good", "{}"), call("c2", "bad", "{\"k\":1}"));

        var result = run(service(), 0);

        var keys = new ArrayList<>(store.bodies.keySet());
        assertEquals(2, keys.size());
        var bad = store.headers.get(keys.get(1));
        assertEquals("true", bad.get(CallHeaders.ERROR));
        assertEquals("tool_error", bad.get(CallHeaders.ERROR_TYPE));
        assertEquals("{\"error\": \"forbidden\"}", store.body(keys.get(1)));
        assertTrue(result.text().contains("calls=2 errors=1"), result.text());
        assertTrue(result.text().contains("tool=bad args={\"k\":1} format=json bytes=22 rows=unknown error=true fields=error]"), result.text());
    }

    @Test
    void textResultHasFormatTextAndNoRowCount() {
        registerTools(r -> "NAME   READY\npod-a  1/1\npod-b  1/1", "pods_list");
        modelCalls(call("c1", "pods_list", "{}"));

        var result = run(service(), 0);

        var h = store.headers.values().iterator().next();
        assertEquals("text", h.get(CallHeaders.FORMAT));
        assertFalse(h.containsKey(CallHeaders.ROWS));
        assertTrue(result.text().contains("rows=unknown"));
    }

    @Test
    void aMissingModelCallIdFallsBackToTheObjectName() {
        registerTools(r -> "[]", "pods_list");
        modelCalls(call(null, "pods_list", "{}"));

        run(service(), 0);

        assertEquals("001-pods_list", store.headers.values().iterator().next().get(CallHeaders.TOOL_CALL_ID));
    }

    @Test
    void aStandInForALargeResultNamesTheCallsArtifact() {
        registerTools(r -> "[" + "{\"row\":1},".repeat(3_000) + "{\"row\":2}]", "pods_list");
        var captured = new ArrayList<List<ChatMessage>>();
        var first = mock(ChatResponse.class);
        when(first.aiMessage()).thenReturn(AiMessage.from(List.of(call("c1", "pods_list", "{}"))));
        when(first.tokenUsage()).thenReturn(new TokenUsage(100, 20));
        var second = mock(ChatResponse.class);
        when(second.aiMessage()).thenReturn(new AiMessage("done"));
        when(second.tokenUsage()).thenReturn(new TokenUsage(120, 5));
        var responses = new ArrayList<>(List.of(first, second));
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            captured.add(new ArrayList<>(((ChatRequest) inv.getArgument(0)).messages()));
            return responses.remove(0);
        });

        var result = run(service(), 2_000);

        String key = store.bodies.keySet().iterator().next();
        String prompt = captured.get(1).toString();
        assertTrue(prompt.contains("Stored as workspace artifact " + key + "."), prompt);
        assertTrue(ToolResultStandIn.isStandIn(prompt));
        assertTrue(store.body(key).length() > 30_000, "the object holds the full result");
        assertTrue(result.text().contains("[CALL key=" + key), "the contribution is still the manifest");
    }

    @Test
    void aFailedWriteLeavesTheGluedOutputAsTheContribution() {
        store.failWrites = true;
        registerTools(r -> "[{\"x\":1}]", "pods_list");
        modelCalls(call("c1", "pods_list", "{}"));

        var result = run(service(), 0);

        assertEquals("[pods_list]\n[{\"x\":1}]", result.text());
    }

    @Test
    void everyCallAnError_theGatherStillFails() {
        registerTools(r -> "{\"error\": \"boom\"}", "pods_list");
        modelCalls(call("c1", "pods_list", "{}"));

        var failure = assertThrows(ToolCallFailure.class, () -> run(service(), 0));

        assertEquals(ToolCallFailure.FailureType.GATHER_FAILED, failure.failureType());
    }

    @Test
    void withoutALedger_theContributionIsTheGluedOutputAndNothingIsStored() {
        registerTools(r -> "[{\"x\":1}]", "pods_list", "nodes_list");
        modelCalls(call("c1", "pods_list", "{}"), call("c2", "nodes_list", "{}"));

        var result = service().callWithToolLoop(question(), chatModel, "s", "test", true, 0L);

        assertEquals("[pods_list]\n[{\"x\":1}]\n\n[nodes_list]\n[{\"x\":1}]", result.text());
        assertTrue(store.bodies.isEmpty());
    }

    @Test
    void theFlagOffMeansNoLedgerForALoop() {
        assertNull(ToolCallLedger.forLoop(false, store, THREAD, AGENT));
        assertNull(ToolCallLedger.forLoop(true, null, THREAD, AGENT));
        assertNull(ToolCallLedger.forLoop(true, store, null, AGENT));
        assertNull(ToolCallLedger.forLoop(true, store, " ", AGENT));
        assertNotNull(ToolCallLedger.forLoop(true, store, THREAD, AGENT));
    }

    @Test
    void theManifestLinesAreRecognizedByTheComputeContract() {
        var messages = new ArrayList<ChatMessage>(List.of(UserMessage.from(
                "[WORKSPACE agent=a calls=1 errors=0 prefix=ns/crew/t1/a/]\n"
                        + "[CALL key=ns/crew/t1/a/001-pods_list tool=pods_list args={} bytes=2 rows=0 error=false]")));

        assertEquals(List.of("/artifacts/ns/crew/t1/a/001-pods_list"), ChatService.artifactPathsInInput(messages));
    }
}
