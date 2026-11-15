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
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import dev.langchain4j.service.tool.ToolExecutor;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.AdditionalAnswers;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.function.Function;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * The workspace access the operator declares decides whether an agent's tool results are stored:
 * none and read agents store nothing and every result returns inline, as it does outside a
 * discussion; write, readWrite and an undeclared agent store each result, as before access could be
 * declared.
 */
class ChatServiceWorkspaceAccessTest {

    private static final String THREAD = "t1";
    private static final String RESULT = "{\"items\":[{\"a\":1},{\"a\":2}]}";

    private ChatModel chatModel;
    private McpClientService mcpClient;
    private ChatServiceCallFilesTest.FakeStore store;

    @BeforeEach
    void setUp() {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        store = new ChatServiceCallFilesTest.FakeStore();
        var executor = mock(ToolExecutor.class);
        when(executor.execute(any(), any())).thenReturn(RESULT);
        var tools = new LinkedHashMap<ToolSpecification, ToolExecutor>();
        tools.put(ToolSpecification.builder().name("pods_list").description("pods").build(), executor);
        when(mcpClient.getToolSpecifications()).thenReturn(tools);
        var first = mock(ChatResponse.class);
        when(first.aiMessage()).thenReturn(AiMessage.from(List.of(
                ToolExecutionRequest.builder().id("c1").name("pods_list").arguments("{}").build())));
        when(first.tokenUsage()).thenReturn(new TokenUsage(100, 20));
        var second = mock(ChatResponse.class);
        when(second.aiMessage()).thenReturn(new AiMessage("done"));
        when(second.tokenUsage()).thenReturn(new TokenUsage(120, 5));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(first, second);
    }

    private ChatService.ChatResult runWith(Optional<String> access) {
        var base = ChatServiceToolLoopTest.stubProperties(8, "tooler", false, false);
        var props = mock(AgentProperties.class, AdditionalAnswers.delegatesTo(base));
        org.mockito.Mockito.doReturn(access).when(props).workspaceAccess();
        var rag = mock(RagClient.class);
        when(rag.queryForContext(org.mockito.ArgumentMatchers.anyString())).thenReturn("");
        var service = new ChatService(chatModel, rag, mcpClient, mock(DiscussionOrchestrator.class), props,
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
        service.callArtifactStore = store;
        return service.directChat(new ChatService.ChatRequest("conv", "list pods", null, THREAD), true);
    }

    @Test
    void anUndeclaredAgentStoresEveryResult() {
        runWith(Optional.empty());
        assertEquals(1, store.bodies.size());
    }

    @Test
    void writeStoresTheResult() {
        runWith(Optional.of("write"));
        assertEquals(1, store.bodies.size());
    }

    @Test
    void readWriteStoresTheResult() {
        runWith(Optional.of("readWrite"));
        assertEquals(1, store.bodies.size());
    }

    @Test
    void noneStoresNothingAndTheResultStaysInline() {
        var result = runWith(Optional.of("none"));
        assertTrue(store.bodies.isEmpty(), "none must not write to the workspace");
        assertEquals(0, store.next, "none must not even reserve a key");
        assertFalse(result.response().contains("[CALL key="), result.response());
    }

    @Test
    void readStoresNothing() {
        runWith(Optional.of("read"));
        assertTrue(store.bodies.isEmpty());
        assertEquals(0, store.next);
    }

    @Test
    void theDeclaredAccessIsReadOnceNotOnEveryToolLoop() {
        var base = ChatServiceToolLoopTest.stubProperties(8, "tooler", false, false);
        var props = mock(AgentProperties.class, AdditionalAnswers.delegatesTo(base));
        org.mockito.Mockito.doReturn(Optional.of("none")).when(props).workspaceAccess();
        var rag = mock(RagClient.class);
        when(rag.queryForContext(org.mockito.ArgumentMatchers.anyString())).thenReturn("");
        var service = new ChatService(chatModel, rag, mcpClient, mock(DiscussionOrchestrator.class), props,
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
        service.callArtifactStore = store;
        service.directChat(new ChatService.ChatRequest("c1", "list pods", null, THREAD), true);
        setUp();
        service.directChat(new ChatService.ChatRequest("c2", "list pods", null, THREAD), true);
        org.mockito.Mockito.verify(props, org.mockito.Mockito.times(1)).workspaceAccess();
    }
}
