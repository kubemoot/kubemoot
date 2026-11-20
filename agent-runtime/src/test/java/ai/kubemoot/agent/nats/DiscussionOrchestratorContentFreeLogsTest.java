package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.logging.LogCapture;
import ai.kubemoot.agent.logging.LogContent;
import io.nats.client.Connection;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * A thread starts with a question and an agent fails with a tool message: the coordinator's logs
 * carry the thread and the sizes, and the question and the secret only with KUBEMOOT_LOG_CONTENT on.
 */
class DiscussionOrchestratorContentFreeLogsTest {

    private static final String THREAD = "t-logs";
    private static final String QUESTION = "why does the invoice for ACME-7731 fail";
    private static final String SECRET = "sk-test0123456789abcdefABCDEF";

    private DiscussionOrchestrator orchestrator;

    @BeforeEach
    void setUp() {
        var nats = mock(NatsConnectionProvider.class);
        when(nats.getConnection()).thenReturn(mock(Connection.class));
        when(nats.scope()).thenReturn(new CrewScope("crew-ns", "demo"));
        orchestrator = new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("general"),
                mock(ChatService.class), mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
    }

    @AfterEach
    void switchOff() {
        System.clearProperty(LogContent.PROPERTY);
    }

    private String runTurn() {
        try (var capture = new LogCapture()) {
            orchestrator.handleMessage("{\"messageId\":\"m-start\",\"threadId\":\"" + THREAD
                    + "\",\"agentName\":\"human\",\"messageType\":\"thread_start\",\"content\":\"" + QUESTION
                    + "\",\"channel\":\"general\",\"metadata\":{\"userQuery\":\"" + QUESTION + " " + SECRET + "\"}}");
            orchestrator.handleMessage("{\"messageId\":\"m-fail\",\"threadId\":\"" + THREAD
                    + "\",\"agentName\":\"tooler\",\"messageType\":\"failure\",\"channel\":\"general\","
                    + "\"content\":\"the tool said " + SECRET + " for " + QUESTION + "\"}");
            return capture.text();
        }
    }

    @Test
    void theLogsCarryNeitherTheQuestionNorTheSecretByDefault() {
        String logs = runTurn();

        assertTrue(logs.contains("reported failure"), "the turn logged nothing of the failure: " + logs);
        assertFalse(logs.contains(QUESTION), logs);
        assertFalse(logs.contains(SECRET), logs);
    }

    @Test
    void theFailureTextIsLoggedWithTheSwitchOn() {
        System.setProperty(LogContent.PROPERTY, "true");

        String logs = runTurn();

        assertTrue(logs.contains(SECRET), logs);
    }
}
