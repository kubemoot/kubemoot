package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.nats.NatsLink;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.Message;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The request and reply the runtime uses to ask the operator to wake and sleep a model. */
class NatsModelLifecycleTest {

    private static final ObjectMapper JSON = new ObjectMapper();

    private static Message reply(String body) {
        Message m = mock(Message.class);
        when(m.getData()).thenReturn(body.getBytes(StandardCharsets.UTF_8));
        return m;
    }

    private static NatsModelLifecycle over(Connection conn) {
        NatsLink nats = mock(NatsLink.class);
        when(nats.getConnection()).thenReturn(conn);
        return new NatsModelLifecycle(nats, JSON, "analyst", Duration.ofSeconds(5));
    }

    @Test
    void theSubjectEscapesTheServerNameLikeTheOperatorsKeyToken() {
        assertEquals("kubemoot.server.model.vllm-rig0", NatsModelLifecycle.subjectFor("vllm-rig0"));
        assertEquals("kubemoot.server.model.vllm_2erig0", NatsModelLifecycle.subjectFor("vllm.rig0"));
    }

    @Test
    void readyAsksTheOperatorAndReadsItsAnswer() throws Exception {
        Connection conn = mock(Connection.class);
        Message answer = reply("{\"ok\":true,\"state\":\"awake\",\"switchMs\":1200}");
        when(conn.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(CompletableFuture.completedFuture(answer));

        ModelLifecycle.Reply r = over(conn).ready("vllm-rig0", "qwen3:14b");

        assertTrue(r.ok());
        assertEquals("awake", r.state());
        assertEquals(1200L, r.switchMs());
        ArgumentCaptor<byte[]> body = ArgumentCaptor.forClass(byte[].class);
        verify(conn).requestWithTimeout(org.mockito.ArgumentMatchers.eq("kubemoot.server.model.vllm-rig0"),
                body.capture(),
                any(Duration.class));
        JsonNode sent = JSON.readTree(body.getValue());
        assertEquals("ready", sent.path("action").asText());
        assertEquals("qwen3:14b", sent.path("model").asText());
        assertEquals("analyst", sent.path("agent").asText());
    }

    @Test
    void releaseSendsTheReleaseAction() throws Exception {
        Connection conn = mock(Connection.class);
        Message answer = reply("{\"ok\":true,\"state\":\"asleep\"}");
        when(conn.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(CompletableFuture.completedFuture(answer));
        assertTrue(over(conn).release("p", "m").ok());
        ArgumentCaptor<byte[]> body = ArgumentCaptor.forClass(byte[].class);
        verify(conn).requestWithTimeout(anyString(), body.capture(), any(Duration.class));
        assertEquals("release", JSON.readTree(body.getValue()).path("action").asText());
    }

    @Test
    void aRefusalCarriesTheOperatorsReason() throws Exception {
        Connection conn = mock(Connection.class);
        Message answer = reply("{\"ok\":false,\"error\":\"no vLLM server serves model m\"}");
        when(conn.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(CompletableFuture.completedFuture(answer));
        ModelLifecycle.Reply r = over(conn).ready("p", "m");
        assertFalse(r.ok());
        assertEquals("no vLLM server serves model m", r.error());
    }

    @Test
    void everyWayTheOperatorCannotAnswerIsAFailureWithAReason() throws Exception {
        Connection silent = mock(Connection.class);
        when(silent.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(CompletableFuture.failedFuture(new TimeoutException("timed out")));
        assertEquals(NatsModelLifecycle.NO_ANSWER, over(silent).ready("p", "m").error());

        // NATS cancels a request nobody subscribes to: that is not a timeout.
        Connection unheard = mock(Connection.class);
        CompletableFuture<Message> cancelled = new CompletableFuture<>();
        cancelled.cancel(true);
        when(unheard.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class))).thenReturn(cancelled);
        assertEquals(NatsModelLifecycle.NOT_LISTENING, over(unheard).ready("p", "m").error());

        Connection reported = mock(Connection.class);
        when(reported.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(CompletableFuture.failedFuture(new IllegalStateException("503 No Responders Available")));
        assertEquals(NatsModelLifecycle.NOT_LISTENING, over(reported).ready("p", "m").error());

        Connection refused = mock(Connection.class);
        when(refused.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(CompletableFuture.failedFuture(new IllegalStateException("permissions violation")));
        assertEquals("the operator could not be asked to ready m: permissions violation",
                over(refused).ready("p", "m").error());

        Connection nobody = mock(Connection.class);
        Message status = mock(Message.class);
        when(status.isStatusMessage()).thenReturn(true);
        when(nobody.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(CompletableFuture.completedFuture(status));
        assertTrue(over(nobody).ready("p", "m").error().contains("no operator is listening"));

        Connection broken = mock(Connection.class);
        when(broken.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenThrow(new IllegalStateException("closed"));
        assertFalse(over(broken).ready("p", "m").ok());

        Connection garbled = mock(Connection.class);
        Message answer = reply("not json");
        when(garbled.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(CompletableFuture.completedFuture(answer));
        assertTrue(over(garbled).ready("p", "m").error().contains("could not be read"));

        assertTrue(over(null).ready("p", "m").error().contains("NATS is not available"));
        assertFalse(new NatsModelLifecycle(null, JSON, null).ready("p", "m").ok());
    }

    @Test
    void aRequestNatsNeverEndsGivesUpAfterTheSafetyLimitAndIsCancelled() throws Exception {
        @SuppressWarnings("unchecked")
        CompletableFuture<Message> stuck = mock(CompletableFuture.class);
        when(stuck.get(org.mockito.ArgumentMatchers.anyLong(), any(TimeUnit.class)))
                .thenThrow(new TimeoutException());

        ModelLifecycle.Reply r = over(mock(Connection.class)).await(stuck, "ready", "m");

        assertEquals(NatsModelLifecycle.NO_ANSWER, r.error());
        verify(stuck).cancel(true);
    }

    @Test
    void reasonForNamesWhyTheRequestEnded() {
        assertEquals(NatsModelLifecycle.NO_ANSWER, NatsModelLifecycle.reasonFor(new TimeoutException(), "ready", "m"));
        assertEquals(NatsModelLifecycle.NOT_LISTENING,
                NatsModelLifecycle.reasonFor(new IllegalStateException("No Responders"), "ready", "m"));
        assertEquals("the operator could not be asked to ready m: no reason given",
                NatsModelLifecycle.reasonFor(null, "ready", "m"));
        assertEquals("the operator could not be asked to release m: no reason given",
                NatsModelLifecycle.reasonFor(new IllegalStateException(), "release", "m"));
        assertEquals("the operator could not be asked to ready m: port 5030 closed",
                NatsModelLifecycle.reasonFor(new IllegalStateException("port 5030 closed"), "ready", "m"));
    }

    @Test
    void anInterruptedWaitKeepsTheInterruptAndFails() throws Exception {
        Connection conn = mock(Connection.class);
        @SuppressWarnings("unchecked")
        CompletableFuture<Message> waiting = mock(CompletableFuture.class);
        when(waiting.get(org.mockito.ArgumentMatchers.anyLong(), any(TimeUnit.class)))
                .thenThrow(new InterruptedException());
        when(conn.requestWithTimeout(anyString(), any(byte[].class), any(Duration.class)))
                .thenReturn(waiting);
        try {
            assertFalse(over(conn).ready("p", "m").ok());
            assertTrue(Thread.currentThread().isInterrupted());
        } finally {
            Thread.interrupted(); // clear the flag for later tests
        }
    }

    @Test
    void switchCostsAreParsedAndPlacementPrefersTheObservedWakeToTheLoadPenalty() throws Exception {
        ServerState p = ServerSelector.fromJson(JSON.readTree(
                "{\"name\":\"v\",\"engine\":\"vllm\",\"endpoint\":\"\",\"switchCostMs\":{\"qwen3:14b\":2000}}"));
        assertEquals(2.0, p.switchSeconds("qwen3:14b").orElseThrow(), 1e-9);
        assertTrue(p.switchSeconds("other").isEmpty());
        assertTrue(p.switchSeconds(null).isEmpty());
        assertEquals(2.0 / PlacementCostModel.DEFAULT_CALL_SECONDS, ServerSelector.wakeCost(p, "qwen3:14b"), 1e-9);
        assertEquals(ServerSelector.COLD_LOAD_PENALTY, ServerSelector.wakeCost(p, "other"));
        ServerState none = new ServerState("o", "http://o", 1, 0, 0, java.util.List.of(), true, "", 0L, Map.of());
        assertTrue(none.switchSeconds("m").isEmpty());
    }

    @Test
    void theSubjectPrefixIsTheOneTheContractPublishes() throws Exception {
        String contract = java.nio.file.Files.readString(
                java.nio.file.Path.of("..", "operator", "pkg", "bus", "schema", "asyncapi.yaml"));
        assertTrue(contract.contains("address: " + NatsModelLifecycle.SUBJECT_PREFIX + "{server}"),
                "the runtime and the contract must name the same subject");
        String keys = java.nio.file.Files.readString(
                java.nio.file.Path.of("..", "operator", "pkg", "bus", "keys.go"));
        assertTrue(keys.contains("\"" + NatsModelLifecycle.SUBJECT_PREFIX + "\""),
                "the operator must subscribe on the same prefix");
    }
}
