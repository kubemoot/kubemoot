package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
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
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.atLeastOnce;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * An agent whose declared tools are not on the tool list the runtime fetched reports a failure
 * before any model call, instead of answering from nothing.
 */
class ChatServiceDeclaredToolsTest {

    private static final String SEARCH = "search_oreilly_content";
    private static final String GATEWAY = "the MCP gateway at http://gateway:8080";

    private ChatModel chatModel;
    private McpClientService mcpClient;
    private final Map<ToolSpecification, ToolExecutor> offered = new LinkedHashMap<>();

    @BeforeEach
    void setUp() {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        when(mcpClient.getToolSpecifications()).thenAnswer(call -> Map.copyOf(offered));
        when(mcpClient.describeToolSource()).thenReturn(GATEWAY);
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage("answer"));
        when(response.tokenUsage()).thenReturn(new TokenUsage(10, 5));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(response);
    }

    private void offer(String... names) {
        for (String name : names) {
            offered.put(ToolSpecification.builder().name(name).description(name).build(), mock(ToolExecutor.class));
        }
    }

    private ChatService service(String role, List<String> enabled, List<String> disabled) {
        var base = ChatServiceToolLoopTest.stubProperties(3, role, false, false);
        var props = mock(AgentProperties.class, AdditionalAnswers.delegatesTo(base));
        doReturn(enabled.isEmpty() ? Optional.empty() : Optional.of(enabled)).when(props).enabledTools();
        doReturn(disabled.isEmpty() ? Optional.empty() : Optional.of(disabled)).when(props).disabledTools();
        var rag = mock(RagClient.class);
        when(rag.queryForContext(anyString())).thenReturn("");
        return new ChatService(chatModel, rag, mcpClient, mock(DiscussionOrchestrator.class), props,
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
    }

    private static ChatService.ChatRequest question() {
        return new ChatService.ChatRequest("thread-1", "find me books on Kubernetes operators");
    }

    @Test
    void declaredToolsPresent_theModelIsCalled() {
        offer(SEARCH, "other_tool");
        var result = service("tooler", List.of(SEARCH), List.of()).directChat(question(), true);

        assertNotNull(result.response());
        verify(chatModel, atLeastOnce()).chat(any(ChatRequest.class));
    }

    @Test
    void declaredToolMissing_failsNamingTheToolAndSource_withoutCallingTheModel() {
        offer("other_tool");
        var service = service("tooler", List.of(SEARCH), List.of());

        var failure = assertThrows(ToolCallFailure.class, () -> service.directChat(question(), true));

        assertEquals(ToolCallFailure.FailureType.TOOLS_UNAVAILABLE, failure.failureType());
        assertEquals(SEARCH, failure.toolName());
        assertEquals(GATEWAY, failure.lastErrorContent());
        assertTrue(failure.getMessage().contains(SEARCH), failure.getMessage());
        assertTrue(failure.getMessage().contains(GATEWAY), failure.getMessage());
        verify(chatModel, never()).chat(any(ChatRequest.class));
    }

    @Test
    void gatewayUnreachable_failsWithTheGatewayError_withoutCallingTheModel() {
        when(mcpClient.describeToolSource())
                .thenReturn(GATEWAY + " (gateway unreachable: ConnectException refused)");
        var service = service("tooler", List.of(SEARCH), List.of());

        var failure = assertThrows(ToolCallFailure.class, () -> service.directChat(question(), true));

        assertEquals(ToolCallFailure.FailureType.TOOLS_UNAVAILABLE, failure.failureType());
        assertTrue(failure.getMessage().contains("gateway unreachable"), failure.getMessage());
        verify(chatModel, never()).chat(any(ChatRequest.class));
    }

    @Test
    void noDeclaredTools_isUnaffectedWhenNoToolIsOffered() {
        var result = service("tooler", List.of(), List.of()).directChat(question(), true);

        assertNotNull(result.response());
        verify(chatModel, atLeastOnce()).chat(any(ChatRequest.class));
    }

    @Test
    void aToolerNeedsEveryDeclaredTool_andNamesOnlyTheMissingOnes() {
        offer("get_pods");
        var service = service("tooler", List.of("get_pods", "get_nodes", "get_events"), List.of());

        var failure = assertThrows(ToolCallFailure.class, () -> service.directChat(question(), true));

        assertEquals("get_nodes,get_events", failure.toolName());
        assertEquals(2, failure.failureCount());
        assertTrue(failure.getMessage().contains("get_nodes, get_events"), failure.getMessage());
        assertFalse(failure.getMessage().contains("get_pods"), failure.getMessage());
        verify(chatModel, never()).chat(any(ChatRequest.class));
    }

    @Test
    void anAdvisorWithSomeDeclaredToolsMissing_stillAnswers() {
        offer("get_pods");
        var result = service("generic", List.of("get_pods", "get_nodes"), List.of()).directChat(question(), true);

        assertNotNull(result.response());
        verify(chatModel, atLeastOnce()).chat(any(ChatRequest.class));
    }

    @Test
    void anAdvisorWithAllDeclaredToolsMissing_fails() {
        offer("other_tool");
        var service = service("generic", List.of("get_pods", "get_nodes"), List.of());

        var failure = assertThrows(ToolCallFailure.class, () -> service.directChat(question(), true));

        assertEquals(ToolCallFailure.FailureType.TOOLS_UNAVAILABLE, failure.failureType());
        verify(chatModel, never()).chat(any(ChatRequest.class));
    }

    @Test
    void aDeclaredToolThatIsAlsoDisabled_isNotRequired() {
        offer("other_tool");
        var result = service("tooler", List.of(SEARCH), List.of(SEARCH)).directChat(question(), true);

        assertNotNull(result.response());
    }
}
