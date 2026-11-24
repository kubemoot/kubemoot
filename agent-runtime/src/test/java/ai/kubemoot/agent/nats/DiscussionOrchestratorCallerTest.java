package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import ai.kubemoot.agent.requester.Principal;
import ai.kubemoot.agent.requester.Requester;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.isNull;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The coordinator's prompts end with the turn context line, which is given the principal that asked the thread. */
class DiscussionOrchestratorCallerTest {

    private ChatService chat;
    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() {
        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        var triageModel = mock(AgentProperties.TriageModel.class);
        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("coordinator");
        when(properties.triageModel()).thenReturn(triageModel);
        when(triageModel.timeoutSeconds()).thenReturn(120);
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.channels()).thenReturn(Optional.of("general"));
        chat = mock(ChatService.class);
        orchestrator = new DiscussionOrchestrator(mock(NatsConnectionProvider.class), properties, chat,
                new DiscussionMetrics(new SimpleMeterRegistry()), mock(LatencyTracker.class), mock(ResumeSearchClient.class));
    }

    @Test
    void theSystemPromptLineIsBuiltFromTheThreadsRequesterAndPrincipal() {
        var state = new DiscussionOrchestrator.ThreadState("t1");
        var asker = new Principal("kubernetes", "kubernetes", "handle0123", "alice");
        state.principal = asker;
        var requester = new Requester("UTC", null, "2026-11-20");
        state.requester = requester;
        when(chat.turnContextLine(requester, asker)).thenReturn("THE LINE");

        assertTrue(orchestrator.systemPromptFor(state).endsWith("THE LINE"));
        verify(chat).turnContextLine(requester, asker);
    }

    @Test
    void aThreadWithNoPrincipalGivesTheLineNone() {
        var state = new DiscussionOrchestrator.ThreadState("t2");
        when(chat.turnContextLine(any(), isNull())).thenReturn("PLAIN");

        assertEquals("PLAIN", orchestrator.systemPromptFor(state).strip());
        verify(chat).turnContextLine(any(), isNull());
    }
}
