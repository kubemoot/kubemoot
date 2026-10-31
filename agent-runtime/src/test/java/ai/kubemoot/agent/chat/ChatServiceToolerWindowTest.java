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

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.function.Function;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

/**
 * A tooler whose tool results are each replaced by a short stand-in still fills its window
 * with those stand-ins. It stops and posts what it gathered once less than the reply
 * reserve is left, instead of sending a prompt the engine rejects.
 */
class ChatServiceToolerWindowTest {

    private static final long WINDOW = 32_768;

    private ChatModel chatModel;
    private McpClientService mcpClient;
    private final CharsPerToken ratio = new CharsPerToken();

    @BeforeEach
    void setUp() {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
    }

    private ChatService service(int maxIterations) {
        return new ChatService(chatModel, mock(RagClient.class), mcpClient, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(maxIterations, "tooler", false, true),
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
    }

    /** Registers the tools in {@code names}; each returns what {@code body} makes of the call. */
    private AtomicInteger registerTools(List<String> names, Function<ToolExecutionRequest, String> body) {
        var executed = new AtomicInteger();
        var tools = new LinkedHashMap<ToolSpecification, ToolExecutor>();
        for (String name : names) {
            var executor = mock(ToolExecutor.class);
            when(executor.execute(any(), any())).thenAnswer(inv -> {
                executed.incrementAndGet();
                return body.apply(inv.getArgument(0));
            });
            tools.put(ToolSpecification.builder().name(name).description(name).build(), executor);
        }
        when(mcpClient.getToolSpecifications()).thenReturn(tools);
        return executed;
    }

    /**
     * The model calls the next tool in {@code names} (cycling) with new arguments every turn. Each
     * turn reports a prompt size as the engine would, and the largest prompt sent is recorded.
     */
    private AtomicLong modelCallsADifferentToolEveryTurn(List<String> names, AtomicInteger turns) {
        var largestPrompt = new AtomicLong();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            ChatRequest request = inv.getArgument(0);
            long prompt = PromptSize.nextPromptTokens(request.messages(), List.of(), 0, 0, 0, ratio);
            largestPrompt.accumulateAndGet(prompt, Math::max);
            int turn = turns.getAndIncrement();
            var call = ToolExecutionRequest.builder().id("c" + turn).name(names.get(turn % names.size()))
                    .arguments("{\"vmid\":" + turn + "}").build();
            var response = mock(ChatResponse.class);
            when(response.aiMessage()).thenReturn(AiMessage.from(List.of(call)));
            when(response.tokenUsage()).thenReturn(new TokenUsage((int) prompt, 20));
            return response;
        });
        return largestPrompt;
    }

    private static List<ChatMessage> question(int promptChars) {
        return new ArrayList<>(List.of(UserMessage.from("q".repeat(promptChars))));
    }

    /** About 10,000 tokens, the size of one proxmox-vm-metrics result. */
    private static String vmMetrics(ToolExecutionRequest request) {
        return "[" + request.arguments() + "]\n" + "m".repeat(30_000);
    }

    @Test
    void manyLargeResults_theToolerStopsBeforeTheWindowOverflowsAndPostsEveryResult() {
        var executed = registerTools(List.of("proxmox-vm-metrics"), ChatServiceToolerWindowTest::vmMetrics);
        var turns = new AtomicInteger();
        var largestPrompt = modelCallsADifferentToolEveryTurn(List.of("proxmox-vm-metrics"), turns);

        var result = service(15).callWithToolLoop(question(54_000), chatModel, "vllm-rig1", "test", true, WINDOW);

        int gathered = executed.get();
        assertTrue(gathered >= 2 && gathered < 15, "stopped for room after " + gathered + " calls");
        assertEquals(gathered, turns.get(), "no model call after the last result");
        for (int vmid = 0; vmid < gathered; vmid++) {
            assertTrue(result.text().contains("[{\"vmid\":" + vmid + "}]\n" + "m".repeat(30_000)),
                    "result of call " + vmid + " is in the contribution");
        }
        assertTrue(largestPrompt.get() <= WINDOW, "no prompt larger than the window was sent: " + largestPrompt.get());
    }

    @Test
    void fifteenLargeResultsFromAnEmptyishWindow_stopBeforeTheReserveIsGone() {
        var executed = registerTools(List.of("proxmox-vm-metrics"), ChatServiceToolerWindowTest::vmMetrics);
        var turns = new AtomicInteger();
        var largestPrompt = modelCallsADifferentToolEveryTurn(List.of("proxmox-vm-metrics"), turns);

        service(15).callWithToolLoop(question(300), chatModel, "vllm-rig1", "test", true, WINDOW);

        assertTrue(largestPrompt.get() <= WINDOW - ToolResultBudget.replyReserve(WINDOW),
                "every prompt sent left the reply reserve: " + largestPrompt.get());
        assertTrue(executed.get() >= 1);
    }

    @Test
    void everyResultAnError_theGatherFailsInsteadOfPostingErrorText() {
        var names = List.of("vm-a", "vm-b", "vm-c");
        var executed = registerTools(names, r -> "{\"error\": \"" + "e".repeat(30_000) + "\"}");
        var turns = new AtomicInteger();
        modelCallsADifferentToolEveryTurn(names, turns);

        var failure = assertThrows(ToolCallFailure.class,
                () -> service(15).callWithToolLoop(question(21_000), chatModel, "s", "test", true, 8_192));

        assertEquals(ToolCallFailure.FailureType.GATHER_FAILED, failure.failureType());
        assertTrue(failure.getMessage().contains("every tool call failed"), failure.getMessage());
        assertTrue(executed.get() >= 1 && executed.get() < 4, "stopped by room, not by the failure caps");
    }

    @Test
    void nonTooler_withTheSameLittleRoom_keepsGoing() {
        registerTools(List.of("proxmox-vm-metrics"), r -> "{\"ok\": true}");
        var first = mock(ChatResponse.class);
        var call = ToolExecutionRequest.builder().id("c0").name("proxmox-vm-metrics").arguments("{}").build();
        when(first.aiMessage()).thenReturn(AiMessage.from(List.of(call)));
        // The engine reports a prompt that leaves less than the reply reserve (1,024 of 8,192).
        when(first.tokenUsage()).thenReturn(new TokenUsage(7_500, 20));
        var second = mock(ChatResponse.class);
        when(second.aiMessage()).thenReturn(new AiMessage("answer"));
        when(second.tokenUsage()).thenReturn(new TokenUsage(7_600, 20));
        when(chatModel.chat(any(ChatRequest.class))).thenReturn(first, second);

        var result = service(5).callWithToolLoop(question(30), chatModel, "s", "test", false, 8_192);

        assertEquals("answer", result.text());
        verify(chatModel, times(2)).chat(any(ChatRequest.class));
    }

    @Test
    void nonTooler_whenThePromptDoesNotFit_stillFailsWithContextExceeded() {
        registerTools(List.of("t"), r -> "{\"ok\": true}");

        var failure = assertThrows(ToolCallFailure.class,
                () -> service(5).callWithToolLoop(question(40_000), chatModel, "s", "test", false, 8_192));

        assertEquals(ToolCallFailure.FailureType.CONTEXT_EXCEEDED, failure.failureType());
        verifyNoInteractions(chatModel);
    }

    @Test
    void toolerWithSmallResults_neverStopsForRoom() {
        var executed = registerTools(List.of("t"), r -> "{\"small\": " + r.arguments() + "}");
        var turns = new AtomicInteger();
        modelCallsADifferentToolEveryTurn(List.of("t"), turns);

        var result = service(6).callWithToolLoop(question(300), chatModel, "s", "test", true, WINDOW);

        assertEquals(6, executed.get(), "every iteration ran: the iteration cap ended the loop");
        assertEquals(6, turns.get());
        assertTrue(result.text().contains("{\"small\": {\"vmid\":5}}"), result.text());
    }
}
