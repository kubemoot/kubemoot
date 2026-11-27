package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.AgentHeartbeatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator;
import ai.kubemoot.agent.rag.RagClient;
import ai.kubemoot.agent.signal.SignalScope;
import ai.kubemoot.agent.signal.SignalTestKinds;
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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The tool loop offers the built-in signal tool during a discussion turn and records its call. */
class ChatServiceSignalToolTest {

    private ChatModel chatModel;
    private McpClientService mcpClient;
    private ToolExecutor mcpExecutor;

    @BeforeEach
    void setUp() {
        chatModel = mock(ChatModel.class);
        mcpClient = mock(McpClientService.class);
        mcpExecutor = mock(ToolExecutor.class);
        when(mcpExecutor.execute(any(), any())).thenReturn("{\"items\":[\"rig0\",\"rig1\"]}");
        var tools = new LinkedHashMap<ToolSpecification, ToolExecutor>();
        tools.put(ToolSpecification.builder().name("resources_list").description("list").build(), mcpExecutor);
        when(mcpClient.getToolSpecifications()).thenReturn(tools);
    }

    private ChatService service(String role) {
        return new ChatService(chatModel, mock(RagClient.class), mcpClient, mock(DiscussionOrchestrator.class),
                ChatServiceToolLoopTest.stubProperties(8, role, false, false),
                mock(AgentHeartbeatService.class), new ObjectMapper(),
                null, null, null, null, null, null, null, null, null);
    }

    private static ChatResponse calls(String name, String arguments) {
        var call = ToolExecutionRequest.builder().id("c-" + name).name(name).arguments(arguments).build();
        return response(AiMessage.from(List.of(call)));
    }

    private static ChatResponse response(AiMessage message) {
        var response = mock(ChatResponse.class);
        when(response.aiMessage()).thenReturn(message);
        when(response.tokenUsage()).thenReturn(new TokenUsage(100, 20));
        return response;
    }

    private static List<ChatMessage> question() {
        return new ArrayList<>(List.of(UserMessage.from("how many nodes?")));
    }

    private static List<String> offered(ChatRequest request) {
        return request.toolSpecifications().stream().map(ToolSpecification::name).toList();
    }

    @Test
    void theSignalToolIsOfferedBesideTheMcpToolsAndItsCallIsTheVerdict() {
        var seen = new ArrayList<ChatRequest>();
        var turn = new AtomicInteger();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            seen.add(inv.getArgument(0));
            return switch (turn.getAndIncrement()) {
                case 0 -> calls("resources_list", "{}");
                case 1 -> calls("signal", "{\"kind\":\"agree\",\"statement\":\"2 nodes\"}");
                default -> response(new AiMessage("The cluster has 2 nodes: rig0 and rig1."));
            };
        });

        try (var scope = SignalScope.bind(SignalTestKinds.consent3())) {
            var result = service("analyst").callWithToolLoop(question(), chatModel, "s", "test", false, 0);

            assertEquals("The cluster has 2 nodes: rig0 and rig1.", result.text(), "the reply stays the finding");
            assertEquals("agree", scope.call().orElseThrow().kind());
        }
        assertEquals(List.of("resources_list", "signal"), offered(seen.get(0)));
        var answer = seen.get(2).messages().stream().filter(ToolExecutionResultMessage.class::isInstance)
                .map(m -> ((ToolExecutionResultMessage) m).text()).toList();
        assertTrue(answer.contains("{\"recorded\":\"agree\"}"), "the model is told its verdict was recorded: " + answer);
    }

    @Test
    void outsideADiscussionTurnTheToolIsNotOffered() {
        var seen = new ArrayList<ChatRequest>();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            seen.add(inv.getArgument(0));
            return response(new AiMessage("2 nodes"));
        });

        service("analyst").callWithToolLoop(question(), chatModel, "s", "test", false, 0);

        assertEquals(List.of("resources_list"), offered(seen.get(0)));
    }

    @Test
    void aSignalCallIsNotAToolCallOfTheToolersWork() {
        var turn = new AtomicInteger();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> switch (turn.getAndIncrement()) {
            case 0 -> calls("signal", "{\"kind\":\"stand_aside\",\"statement\":\"not my domain\"}");
            default -> response(new AiMessage("Nothing to add."));
        });

        try (var scope = SignalScope.bind(SignalTestKinds.consent3())) {
            var result = service("tooler").callWithToolLoop(question(), chatModel, "s", "test", true, 0);

            assertFalse(result.contributionIsToolOutput(), "no tool ran, so there is no raw output to post");
            assertEquals("stand_aside", scope.call().orElseThrow().kind());
        }
        verify(mcpExecutor, never()).execute(any(), any());
    }

    @Test
    void aRefusedCallIsAnsweredWithWhatToFixAndRecordsNothing() {
        var seen = new ArrayList<ChatRequest>();
        var turn = new AtomicInteger();
        when(chatModel.chat(any(ChatRequest.class))).thenAnswer(inv -> {
            seen.add(inv.getArgument(0));
            return switch (turn.getAndIncrement()) {
                case 0 -> calls("signal", "{\"kind\":\"question\",\"statement\":\"recheck?\"}");
                default -> response(new AiMessage("done"));
            };
        });

        try (var scope = SignalScope.bind(SignalTestKinds.consent3())) {
            service("analyst").callWithToolLoop(question(), chatModel, "s", "test", false, 0);
            assertTrue(scope.call().isEmpty());
        }
        var answer = seen.get(1).messages().stream().filter(ToolExecutionResultMessage.class::isInstance)
                .map(m -> ((ToolExecutionResultMessage) m).text()).findFirst().orElse("");
        assertTrue(answer.contains("error"), answer);
    }
}
