package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.agent.tool.ToolExecutionRequest;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import dev.langchain4j.service.tool.ToolExecutor;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.AdditionalAnswers;

import java.util.LinkedHashMap;
import java.util.List;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * Every agent in a discussion stores every tool call: its result and its working (the arguments
 * exactly as the model sent them, the program of an execute_code call included), so any agent can
 * review the work behind a finding. Agents that used to store nothing (a declared read or none
 * access, the advisors) store too; only a runtime without a store lists its calls unstored.
 */
class ChatServiceCallWorkingTest {

    private static final String THREAD = "t1";
    private static final String RESULT = "{\"items\":[{\"a\":1},{\"a\":2}]}";
    private static final String PROGRAM_ARGS = "{\"language\":\"python\",\"code\":\"import csv\\nrows = list(csv.reader("
            + "open('inputs/k8s/001-resources_get')))\\nprint(len(rows))\\n\",\"inputs\":[\"k8s/001-resources_get\"]}";

    private ChatModel chatModel;
    private McpClientService mcpClient;
    private ChatServiceCallFilesTest.FakeStore store;

    @BeforeEach
    void setUp() {
        setUp("pods_list", "{}");
    }

    private void setUp(String tool, String arguments) {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        store = new ChatServiceCallFilesTest.FakeStore();
        var executor = mock(ToolExecutor.class);
        when(executor.execute(any(), any())).thenReturn(RESULT);
        var tools = new LinkedHashMap<ToolSpecification, ToolExecutor>();
        tools.put(ToolSpecification.builder().name(tool).description(tool).build(), executor);
        when(mcpClient.getToolSpecifications()).thenReturn(tools);
        var first = mock(ChatResponse.class);
        when(first.aiMessage()).thenReturn(AiMessage.from(List.of(
                ToolExecutionRequest.builder().id("c1").name(tool).arguments(arguments).build())));
        when(first.tokenUsage()).thenReturn(new TokenUsage(100, 20));
        var second = mock(ChatResponse.class);
        when(second.aiMessage()).thenReturn(new AiMessage("done"));
        when(second.tokenUsage()).thenReturn(new TokenUsage(120, 5));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(first, second);
    }

    private ChatService.ChatResult runAs(String role, CallArtifactStore withStore) {
        var props = ChatServiceToolLoopTest.stubProperties(8, role);
        var rag = mock(RagClient.class);
        when(rag.queryForContext(org.mockito.ArgumentMatchers.anyString())).thenReturn("");
        var service = new ChatService(chatModel, rag, mcpClient, mock(DiscussionOrchestrator.class), props,
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
        service.callArtifactStore = withStore;
        return service.directChat(new ChatService.ChatRequest("conv", "list pods", null, THREAD), true);
    }

    @Test
    void anAdvisorThatUsedToStoreNothingStoresItsResultAndItsWorking() {
        var result = runAs("advisor", store);

        assertEquals(List.of("ns/crew/t1/test-agent/001-pods_list", "ns/crew/t1/test-agent/001-pods_list.call"),
                List.copyOf(store.bodies.keySet()));
        assertEquals(RESULT, store.body("ns/crew/t1/test-agent/001-pods_list"));
        assertEquals("{}", store.body("ns/crew/t1/test-agent/001-pods_list.call"));
        String files = result.files();
        assertTrue(files.contains("[CALL key=test-agent/001-pods_list tool=pods_list call=test-agent/001-pods_list.call args={} "),
                files);
        assertFalse(files.contains("stored=no"), files);
    }

    @Test
    void anExecuteCodeCallStoresItsWholeProgramUnderTheDerivedKey() {
        setUp("execute_code", PROGRAM_ARGS);

        var result = runAs("advisor", store);

        String working = "ns/crew/t1/test-agent/001-execute_code.call";
        assertEquals(PROGRAM_ARGS, store.body(working), "the program is stored whole, not cut at the summary's 60 chars");
        assertEquals("ns/crew/t1/test-agent/001-execute_code", store.headers.get(working).get(CallHeaders.WORKING_OF));
        assertNull(store.headers.get(working).get(CallHeaders.OPERATION_NAME),
                "a working is never listed or read as a tool result");
        assertTrue(result.files().contains("call=test-agent/001-execute_code.call"), result.files());
        assertTrue(result.files().contains("(args cut at 60 of"), "the preview stays short: " + result.files());
    }

    @Test
    void aToolerStoresItsWorkingBesideItsResult() {
        runAs("tooler", store);

        assertTrue(store.bodies.containsKey("ns/crew/t1/test-agent/001-pods_list.call"), store.bodies.keySet().toString());
    }

    @Test
    void withoutAStoreAnAdvisorListsItsCallsUnstoredWithTheirResults() {
        var result = runAs("advisor", null);

        String files = result.files();
        assertTrue(files.contains("[WORKSPACE agent=test-agent calls=1 errors=0 stored=no]"), files);
        assertTrue(files.contains(" stored=no]\n[RESULT]\n" + RESULT + "\n[END RESULT]"), files);
        assertFalse(files.contains("key="), "no key points at nothing: " + files);
    }

    @Test
    void withoutAStoreAToolerKeepsGatheringItsRawOutputAndListsNoFiles() {
        var result = runAs("tooler", null);

        assertEquals("", result.files());
        assertTrue(result.response().contains(RESULT), result.response());
    }

    @Test
    void aWorkingThatCannotBeWrittenDoesNotBreakTheCallOrLoseItsResult() {
        store.failWorkings = true;

        var result = runAs("advisor", store);

        assertEquals("done", result.response());
        assertEquals(RESULT, store.body("ns/crew/t1/test-agent/001-pods_list"));
        assertFalse(store.bodies.containsKey("ns/crew/t1/test-agent/001-pods_list.call"));
        assertTrue(result.files().contains("tool=pods_list call=unavailable args={}"), result.files());
    }

    @Test
    void aStoreThatRefusesEveryWriteDoesNotBreakTheCall() {
        store.failWrites = true;

        var result = runAs("advisor", store);

        assertEquals("done", result.response());
        assertTrue(store.bodies.isEmpty());
        assertTrue(result.files().contains("[NOT STORED tool=pods_list"), result.files());
    }

    @Test
    void theWorkingIsNotPutInTheModelsContextOrTheContributionBeyondItsKey() {
        setUp("execute_code", PROGRAM_ARGS);

        var result = runAs("advisor", store);

        assertFalse(result.files().contains("print(len(rows))"), "only the 60-character preview, never the program: " + result.files());
    }
}
