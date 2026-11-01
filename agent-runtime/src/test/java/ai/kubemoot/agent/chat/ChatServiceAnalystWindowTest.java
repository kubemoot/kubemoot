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
import dev.langchain4j.data.message.ToolExecutionResultMessage;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import dev.langchain4j.service.tool.ToolExecutor;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Function;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

/**
 * An agent that reads its own tool results (an analyst such as compute) never sends a prompt
 * past its window: numeric results are counted at their real density, and when the prompt
 * would still not fit, or the server refuses it, the largest earlier results are replaced with
 * a note and the turn goes on.
 */
class ChatServiceAnalystWindowTest {

    private static final long WINDOW = 32_768;
    private static final String REJECTION = "HTTP 400: This model's maximum context length is 32768 tokens. "
            + "However, your prompt contains at least 32769 input tokens, please reduce the length.";

    private ChatModel chatModel;
    private McpClientService mcpClient;

    @BeforeEach
    void setUp() {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
    }

    private ChatService service() {
        return new ChatService(chatModel, mock(RagClient.class), mcpClient, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(8, "analyst", false, true),
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
    }

    private void registerTool(String name, Function<ToolExecutionRequest, String> body) {
        var executor = mock(ToolExecutor.class);
        when(executor.execute(any(), any())).thenAnswer(inv -> body.apply(inv.getArgument(0)));
        var tools = new LinkedHashMap<ToolSpecification, ToolExecutor>();
        tools.put(ToolSpecification.builder().name(name).description(name).build(), executor);
        when(mcpClient.getToolSpecifications()).thenReturn(tools);
    }

    private static ChatResponse toolCall(int turn, int promptTokens) {
        var call = ToolExecutionRequest.builder().id("c" + turn).name("artifact_head")
                .arguments("{\"key\":\"k" + turn + "\"}").build();
        return response(AiMessage.from(List.of(call)), promptTokens);
    }

    private static ChatResponse response(AiMessage message, int promptTokens) {
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(message);
        when(response.tokenUsage()).thenReturn(new TokenUsage(promptTokens, 20));
        return response;
    }

    private static List<ChatMessage> question() {
        return new ArrayList<>(List.of(UserMessage.from("how many VMs run on rig0?")));
    }

    /** The tool results in the request, as the model would see them. */
    private static List<String> resultTexts(ChatRequest request) {
        return request.messages().stream().filter(ToolExecutionResultMessage.class::isInstance)
                .map(m -> ((ToolExecutionResultMessage) m).text()).toList();
    }

    @Test
    void aNumericResultThatFitsByLengthButNotByTokens_isReplacedByAnErrorAndNeverSent() {
        // 30,000 characters of numbers is about 21,000 tokens; at a flat 3 characters per token it
        // would count as 10,000 and be admitted into the 32,768-token window next to the prompt.
        String numeric = "{\"cpu\":0.1234567,\"mem\":1048576,\"t\":1761953400}\n".repeat(600);
        registerTool("artifact_head", r -> numeric);
        var seen = new ArrayList<List<String>>();
        var turns = new AtomicInteger();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            seen.add(resultTexts(inv.getArgument(0)));
            int turn = turns.getAndIncrement();
            return turn == 0 ? toolCall(0, 24_000) : response(new AiMessage("answer"), 24_100);
        });

        var result = service().callWithToolLoop(question(), chatModel, "s", "test", false, WINDOW);

        assertEquals("answer", result.text());
        String shown = seen.get(1).get(0);
        assertTrue(shown.contains("larger than the room left"), shown);
        assertTrue(shown.contains("aggregate"), "the error points to counting or SQL: " + shown);
    }

    @Test
    void theServerRefusesAPrompt_theLargestEarlierResultBecomesANoteAndTheTurnIsTriedAgain() {
        registerTool("artifact_head", r -> "m".repeat(24_000));
        var seen = new ArrayList<List<String>>();
        var turns = new AtomicInteger();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            seen.add(resultTexts(inv.getArgument(0)));
            int turn = turns.getAndIncrement();
            if (turn == 2) {
                throw new RuntimeException(REJECTION);
            }
            return turn < 2 ? toolCall(turn, 100) : response(new AiMessage("answer"), 9_000);
        });

        var result = service().callWithToolLoop(question(), chatModel, "s", "test", false, WINDOW);

        assertEquals("answer", result.text());
        assertEquals(4, seen.size(), "two tool turns, the refused turn, and the turn tried again");
        assertEquals(2, seen.get(2).size());
        assertTrue(seen.get(3).stream().anyMatch(ToolResultCompaction::isNote), "a result was replaced: " + seen.get(3));
        assertTrue(seen.get(3).stream().anyMatch(t -> t.length() == 24_000), "the other result was kept");
    }

    @Test
    void aServerThatKeepsRefusing_failsAfterTheRecoveryLimit() {
        registerTool("artifact_head", r -> "m".repeat(24_000));
        var failure = new RuntimeException(REJECTION);
        var turns = new AtomicInteger();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            int turn = turns.getAndIncrement();
            if (turn >= 2) {
                throw failure;
            }
            return toolCall(turn, 100);
        });

        var thrown = assertThrows(RuntimeException.class,
                () -> service().callWithToolLoop(question(), chatModel, "s", "test", false, WINDOW));

        assertSame(failure, thrown);
        assertEquals(2 + 1 + ChatService.MAX_REJECTION_RECOVERIES, turns.get());
    }

    @Test
    void theEngineReportsAPromptPastTheWindow_earlierResultsAreReplacedInsteadOfFailing() {
        registerTool("artifact_head", r -> "m".repeat(15_000));
        var seen = new ArrayList<List<String>>();
        var turns = new AtomicInteger();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            seen.add(resultTexts(inv.getArgument(0)));
            int turn = turns.getAndIncrement();
            if (turn == 0) {
                return toolCall(0, 1_000);
            }
            return turn == 1 ? toolCall(1, 40_000) : response(new AiMessage("answer"), 3_000);
        });

        var result = service().callWithToolLoop(question(), chatModel, "s", "test", false, WINDOW);

        assertEquals("answer", result.text());
        assertTrue(seen.get(2).stream().anyMatch(ToolResultCompaction::isNote), "the old result is a note: " + seen.get(2));
    }

    @Test
    void aToolerKeepsItsOwnRule_noCompactionOnRejection() {
        registerTool("artifact_head", r -> "m".repeat(24_000));
        var failure = new RuntimeException(REJECTION);
        when(chatModel.chat(any(ChatRequest.class))).thenThrow(failure);

        var thrown = assertThrows(RuntimeException.class,
                () -> service().callWithToolLoop(question(), chatModel, "s", "test", true, WINDOW));

        assertSame(failure, thrown);
    }
}
