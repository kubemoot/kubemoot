package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

/** The coordinator feeds its thread's messages to the receipt and publishes it on thread_close. */
class DiscussionReceiptWiringTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String THREAD = "t-1";
    private static final String COORDINATOR = "test-coordinator";

    private DiscussionOrchestrator orchestrator;
    private Connection conn;

    @BeforeEach
    void setUp() {
        var nats = mock(NatsConnectionProvider.class);
        conn = mock(Connection.class);
        when(nats.getConnection()).thenReturn(conn);
        when(nats.scope()).thenReturn(new CrewScope("crew-ns", "demo"));
        orchestrator = new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("kubernetes"),
                mock(ChatService.class), mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
    }

    private void send(String id, String agent, String type, String metadataJson) {
        orchestrator.handleMessage("{\"messageId\":\"" + id + "\",\"threadId\":\"" + THREAD
                + "\",\"agentName\":\"" + agent + "\",\"messageType\":\"" + type
                + "\",\"content\":\"\",\"channel\":\"kubernetes\""
                + (metadataJson == null ? "" : ",\"metadata\":" + metadataJson) + "}");
    }

    private JsonNode publishedClose() throws Exception {
        orchestrator.publishThreadClose(THREAD, "kubernetes");
        var payload = ArgumentCaptor.forClass(byte[].class);
        verify(conn, atLeastOnce()).publish(anyString(), payload.capture());
        return MAPPER.readTree(payload.getValue());
    }

    @Test
    void theCloseCarriesAReceiptBuiltFromTheThreadsMessages() throws Exception {
        send("m1", "human", "thread_start", null);
        send("m2", COORDINATOR, "advisory", null);
        send("m3", COORDINATOR, "triage_result", null);
        send("m4", "k8s", "agree", "{\"inputTokens\":100,\"outputTokens\":20,\"modelsUsed\":[\"qwen3:14b@gpu-a\"]}");
        send("m4", "k8s", "agree", "{\"inputTokens\":100,\"outputTokens\":20,\"modelsUsed\":[\"qwen3:14b@gpu-a\"]}");
        send("m5", "pve", "failure", "{\"lastError\":\"evaluation deadline passed\"}");

        JsonNode close = publishedClose();

        assertEquals("thread_close", close.path("messageType").asText());
        JsonNode receipt = close.path("metadata").path("receipt");
        assertEquals(2, receipt.path("agents").size());
        assertEquals(100, receipt.path("tokens").path("agentsIn").asLong(), "a duplicate message counts once");
        assertEquals(1, receipt.path("consensus").path("agree").asInt());
        assertEquals(1, receipt.path("consensus").path("failure").asInt());
        JsonNode pve = null;
        for (JsonNode a : receipt.path("agents")) {
            if ("pve".equals(a.path("agent").asText())) {
                pve = a;
            }
        }
        assertNotNull(pve);
        assertEquals("evaluation deadline passed", pve.path("reason").asText());
        assertTrue(receipt.path("phasesMs").has("advisory"), "the coordinator's own advisory reaches the receipt");
        assertTrue(receipt.path("phasesMs").has("selection"));
        assertEquals("qwen3:14b@gpu-a", receipt.path("placements").get(0).path("place").asText());
    }

    @Test
    void theCoordinatorsOwnTerminalTypeMessages_areNotCountedAsAnAgent() throws Exception {
        send("m1", COORDINATOR, "failure", "{\"lastError\":\"x\"}");
        send("m2", "k8s", "stand_aside", "{\"reason\":\"outside my domain\"}");

        JsonNode agents = publishedClose().path("metadata").path("receipt").path("agents");
        assertEquals(1, agents.size());
        assertEquals("k8s", agents.get(0).path("agent").asText());
    }

    @Test
    void aReplyThatReopensTheThread_startsANewReceipt() throws Exception {
        send("m1", "k8s", "agree", "{\"inputTokens\":100}");
        var state = orchestrator.threadForTest(THREAD);
        state.phase = DiscussionOrchestrator.Phase.CLOSED;
        send("m2", "human", "reply", null);
        send("m3", "pve", "agree", "{\"inputTokens\":7}");

        JsonNode receipt = orchestrator.receiptFor(state);
        assertEquals(1, receipt.path("agents").size());
        assertEquals("pve", receipt.path("agents").get(0).path("agent").asText());
        assertEquals(7, receipt.path("tokens").path("agentsIn").asLong());
    }

    @Test
    void withoutAConnection_nothingIsPublished_andNothingThrows() {
        var nats = mock(NatsConnectionProvider.class);
        var quiet = new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("kubernetes"),
                mock(ChatService.class), mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
        assertDoesNotThrow(() -> quiet.publishThreadClose(THREAD, "kubernetes"));
        verify(conn, never()).publish(anyString(), any(byte[].class));
    }

    private static ChatService.SimpleLlmResult placedResult(String policy) {
        var call = new ai.kubemoot.agent.modelserver.PlacedCall("gpu-a", "qwen3:14b",
                new ai.kubemoot.agent.modelserver.PlacementDecision(policy, "because", null, null));
        return new ChatService.SimpleLlmResult("answer", 10, 5,
                new ai.kubemoot.agent.chat.ModelUse("qwen3:14b", "gpu-a", call));
    }

    @Test
    void theCoordinatorsPlacedSynthesisCalls_reachTheReceiptWithTheirPhase() {
        var chat = mock(ChatService.class);
        when(chat.simpleLlmCallWithTokens(anyString(), anyString())).thenReturn(placedResult("learned"));
        var nats = mock(NatsConnectionProvider.class);
        when(nats.getConnection()).thenReturn(conn);
        when(nats.scope()).thenReturn(new CrewScope("crew-ns", "demo"));
        var orch = new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("kubernetes"), chat,
                mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
        orch.handleMessage("{\"messageId\":\"m1\",\"threadId\":\"" + THREAD + "\",\"agentName\":\"human\","
                + "\"messageType\":\"thread_start\",\"content\":\"\",\"channel\":\"kubernetes\"}");

        orch.synthesizeWithCompleteness("sys", "conversation", THREAD);

        JsonNode calls = orch.receiptFor(orch.threadForTest(THREAD)).path("calls");
        assertEquals(1, calls.size());
        assertEquals("synthesis", calls.get(0).path("phase").asText());
        assertEquals("learned", calls.get(0).path("policy").asText());
        assertFalse(calls.get(0).path("agent").asText().isEmpty(), "named for the coordinator");
    }

    @Test
    void aCoordinatorCallTheSchedulerDidNotPlace_orOfAThreadNotKnown_isLeftOut() {
        var chat = mock(ChatService.class);
        when(chat.simpleLlmCallWithTokens(anyString(), anyString())).thenReturn(
                new ChatService.SimpleLlmResult("answer", 10, 5, new ai.kubemoot.agent.chat.ModelUse("m", "")));
        var nats = mock(NatsConnectionProvider.class);
        when(nats.getConnection()).thenReturn(conn);
        var orch = new DiscussionOrchestrator(nats, DiscussionOrchestratorTest.stubProperties("kubernetes"), chat,
                mock(DiscussionMetrics.class), mock(LatencyTracker.class),
                mock(ai.kubemoot.agent.rag.ResumeSearchClient.class));
        orch.handleMessage("{\"messageId\":\"m1\",\"threadId\":\"" + THREAD + "\",\"agentName\":\"human\","
                + "\"messageType\":\"thread_start\",\"content\":\"\",\"channel\":\"kubernetes\"}");

        orch.synthesizeWithCompleteness("sys", "conversation", THREAD);
        assertDoesNotThrow(() -> orch.synthesizeWithCompleteness("sys", "conversation", "no-such-thread"));

        assertFalse(orch.receiptFor(orch.threadForTest(THREAD)).has("calls"));
    }
}
