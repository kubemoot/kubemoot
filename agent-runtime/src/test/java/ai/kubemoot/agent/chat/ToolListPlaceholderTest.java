package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.SystemMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import dev.langchain4j.service.tool.ToolExecutor;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.AdditionalAnswers;
import org.mockito.ArgumentCaptor;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.atLeastOnce;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * The tool-list placeholder: replaced with the tools the agent registered, left alone when a
 * prompt does not use it, and current when the gateway gains a tool after the agent started.
 */
class ToolListPlaceholderTest {

    private static final String PROMPT = "ASSERT your tools are ${agent.tools}. Use only these.";

    @Test
    void namesAreSortedAndJoined() {
        assertEquals("ASSERT your tools are a_tool, b_tool, c_tool. Use only these.",
                ToolListPlaceholder.render(PROMPT, () -> List.of("c_tool", "a_tool", "b_tool")));
    }

    @Test
    void noToolsRendersNone() {
        assertEquals("ASSERT your tools are none. Use only these.", ToolListPlaceholder.render(PROMPT, List::of));
    }

    @Test
    void everyOccurrenceIsReplacedAndDuplicatesAreListedOnce() {
        assertEquals("a, b / a, b", ToolListPlaceholder.render("${agent.tools} / ${agent.tools}", () -> List.of("b", "a", "a")));
    }

    @Test
    void aPromptWithoutThePlaceholderIsUnchangedAndLooksUpNoTools() {
        var prompt = "ASSERT use the tools you have. ${other.token} stays.";
        assertSame(prompt, ToolListPlaceholder.render(prompt, () -> {
            throw new AssertionError("tools must not be looked up");
        }));
        assertEquals(null, ToolListPlaceholder.render(null, List::of));
    }

    // --- through the ChatService, which knows the registered set ---

    private ChatModel chatModel;
    private McpClientService mcpClient;
    private final Map<ToolSpecification, ToolExecutor> offered = new LinkedHashMap<>();

    @BeforeEach
    void setUp() {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        when(mcpClient.getToolSpecifications()).thenAnswer(call -> Map.copyOf(offered));
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

    private ChatService service(String systemPrompt) throws java.io.IOException {
        var file = java.nio.file.Files.createTempFile("system", ".txt");
        file.toFile().deleteOnExit();
        java.nio.file.Files.writeString(file, systemPrompt);
        var base = ChatServiceToolLoopTest.stubProperties(3, "analyst", false, false);
        var props = mock(AgentProperties.class, AdditionalAnswers.delegatesTo(base));
        doReturn(Optional.of(file.toString())).when(props).systemPromptFile();
        var rag = mock(RagClient.class);
        when(rag.queryForContext(anyString())).thenReturn("");
        return new ChatService(chatModel, rag, mcpClient, mock(DiscussionOrchestrator.class), props,
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
    }

    private String systemMessageOfNextChat(ChatService service) {
        service.directChat(new ChatService.ChatRequest("c-" + System.nanoTime(), "hello"), false);
        var captor = ArgumentCaptor.forClass(ChatRequest.class);
        verify(chatModel, atLeastOnce()).chat(captor.capture());
        return captor.getValue().messages().stream()
                .filter(SystemMessage.class::isInstance).map(m -> ((SystemMessage) m).text()).findFirst().orElse("");
    }

    @Test
    void theSystemMessageCarriesTheToolsTheAgentRegistered() throws Exception {
        offer("execute_code", "workspace_query");
        var system = systemMessageOfNextChat(service(PROMPT));
        assertTrue(system.contains("your tools are execute_code, workspace_query. Use only"), system);
        assertFalse(system.contains(ToolListPlaceholder.TOKEN));
    }

    @Test
    void theSystemMessageSaysNoneWhenNothingIsRegistered() throws Exception {
        assertTrue(systemMessageOfNextChat(service(PROMPT)).contains("your tools are none. Use only"));
    }

    @Test
    void aPromptWithoutThePlaceholderReachesTheModelUnchanged() throws Exception {
        offer("execute_code");
        assertTrue(systemMessageOfNextChat(service("ALWAYS use your list tool.")).contains("ALWAYS use your list tool."));
    }

    @Test
    void aToolAddedLaterIsNamedInTheNextSystemMessage() throws Exception {
        offer("execute_code");
        var service = service(PROMPT);
        assertTrue(systemMessageOfNextChat(service).contains("your tools are execute_code. Use only"));

        offer("workspace_query");
        when(mcpClient.refreshGatewayToolsIfStale()).thenReturn(true);
        assertTrue(systemMessageOfNextChat(service).contains("your tools are execute_code, workspace_query. Use only"));
    }
}
