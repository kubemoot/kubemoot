package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.mockito.AdditionalAnswers;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** The system prompt the coordinator's own model calls use names the tools the agent registered. */
class DiscussionOrchestratorSystemPromptTest {

    @TempDir
    Path dir;

    private DiscussionOrchestrator orchestrator(String prompt, List<String> tools) throws Exception {
        var file = Files.writeString(dir.resolve("system.txt"), prompt);
        var props = mock(AgentProperties.class,
                AdditionalAnswers.delegatesTo(DiscussionOrchestratorTest.stubProperties("general")));
        doReturn(Optional.of(file.toString())).when(props).systemPromptFile();
        var chatService = mock(ChatService.class);
        when(chatService.getToolNames()).thenReturn(tools);
        return new DiscussionOrchestrator(mock(NatsConnectionProvider.class), props, chatService,
                mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
    }

    @Test
    void thePlaceholderIsReplacedWithTheRegisteredTools() throws Exception {
        assertEquals("tools: a_tool, b_tool",
                orchestrator("tools: ${agent.tools}", List.of("b_tool", "a_tool")).loadSystemPrompt());
    }

    @Test
    void noRegisteredToolsRendersNone() throws Exception {
        assertEquals("tools: none", orchestrator("tools: ${agent.tools}", List.of()).loadSystemPrompt());
    }

    @Test
    void aPromptWithoutThePlaceholderIsUnchanged() throws Exception {
        assertEquals("ALWAYS use your list tool.",
                orchestrator("ALWAYS use your list tool.", List.of("a_tool")).loadSystemPrompt());
    }
}
