package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.provider.CapacityWait;
import ai.kubemoot.agent.rag.RagClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import dev.langchain4j.service.tool.ToolExecutor;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/**
 * The one tool-free turn a concurrence reply is: one model call, no tool offered
 * even when the agent has tools, no tool run, and no knowledge retrieval.
 */
class ChatServiceAnswerOnceTest {

    private ChatModel chatModel;
    private McpClientService mcpClient;
    private RagClient ragClient;
    private ToolExecutor toolExecutor;
    private AgentHeartbeatService heartbeat;

    @BeforeEach
    void setUp() {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        ragClient = mock(RagClient.class);
        toolExecutor = mock(ToolExecutor.class);
        heartbeat = mock(AgentHeartbeatService.class);
        var toolSpec = ToolSpecification.builder().name("execute_code").description("Run code").build();
        when(mcpClient.getToolSpecifications()).thenReturn(Map.of(toolSpec, toolExecutor));
    }

    private ChatService service() {
        return service(null);
    }

    private ChatService service(ai.kubemoot.agent.memory.CrewMemoryClient memory) {
        return new ChatService(chatModel, ragClient, mcpClient, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(10, "analyst", true, false),
                heartbeat, new ObjectMapper(), null, null, memory, null, null, null, null, null);
    }

    private void modelReplies(String text) {
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage(text));
        when(response.tokenUsage()).thenReturn(new TokenUsage(120, 8));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(response);
    }

    @Test
    void callsTheModelOnce_andOffersNoTools() {
        modelReplies("I concur: all 28 namespaces are listed.");

        var result = service().answerOnce("t1", "Concurrence check: ...", CapacityWait.NONE);

        var sent = ArgumentCaptor.forClass(ChatRequest.class);
        verify(chatModel, times(1)).chat(sent.capture());
        var toolSpecs = sent.getValue().toolSpecifications();
        assertTrue(toolSpecs == null || toolSpecs.isEmpty(), "no tool is offered: " + toolSpecs);
        verifyNoInteractions(toolExecutor);
        verifyNoInteractions(ragClient);
        assertEquals("I concur: all 28 namespaces are listed.", result.response());
        assertEquals(120, result.inputTokens());
        assertEquals(8, result.outputTokens());
        verify(heartbeat).recordInference();
    }

    @Test
    void sendsTheMessageAsIs_evenForAComputeContractAgent() {
        modelReplies("CONCERN: the count covers 20 of 28 namespaces");

        var result = service().answerOnce("t1", "the gathered results", CapacityWait.NONE);

        var sent = ArgumentCaptor.forClass(ChatRequest.class);
        verify(chatModel, times(1)).chat(sent.capture());
        var last = sent.getValue().messages().get(sent.getValue().messages().size() - 1);
        assertEquals("the gathered results", ((UserMessage) last).singleText());
        assertEquals("CONCERN: the count covers 20 of 28 namespaces", result.response(),
                "no compute re-prompt: the reply is taken as given");
    }

    @Test
    void removesTheReasoningBlock() {
        modelReplies("<think>check the count</think>\nCONCERN: two namespaces are missing");

        var result = service().answerOnce("t1", "results", CapacityWait.NONE);

        assertEquals("CONCERN: two namespaces are missing", result.response());
    }

    @Test
    void anEchoOfTheInstructions_comesBackEmpty() {
        modelReplies("Write the final answer to the user using only the tool results shown above.");

        assertEquals("", service().answerOnce("t1", "results", CapacityWait.NONE).response());
    }

    @Test
    void anEmptyReply_comesBackEmpty() {
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(new AiMessage(""));
        when(response.tokenUsage()).thenReturn(new TokenUsage(120, 0));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(response);

        assertEquals("", service().answerOnce("t1", "results", CapacityWait.NONE).response());
        verify(chatModel, times(1)).chat(any(ChatRequest.class));
    }

    @Test
    void rememberDirectives_arePersistedAndRemoved_asOnTheToolLoopPath() {
        modelReplies("I concur.\nREMEMBER: namespaces are listed by k8s-config");
        var memory = mock(ai.kubemoot.agent.memory.CrewMemoryClient.class);
        when(memory.persistFromResponse(anyString(), anyString())).thenReturn("I concur.\n");

        var result = service(memory).answerOnce("t1", "results", CapacityWait.NONE);

        assertEquals("I concur.", result.response());
        verify(memory).persistFromResponse("I concur.\nREMEMBER: namespaces are listed by k8s-config", "test-agent");
    }

    @Test
    void aFailedCall_propagates() {
        when(chatModel.chat(any(ChatRequest.class))).thenThrow(new IllegalStateException("Connection refused"));

        var service = service();
        assertThrows(IllegalStateException.class, () -> service.answerOnce("t1", "results", CapacityWait.NONE));
        verify(ragClient, never()).queryForContext(anyString());
    }
}
