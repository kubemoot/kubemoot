package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.Message;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Map;

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
        NatsConnectionProvider nats = mock(NatsConnectionProvider.class);
        when(nats.getConnection()).thenReturn(conn);
        return new NatsModelLifecycle(nats, JSON, "analyst", Duration.ofSeconds(5));
    }

    @Test
    void theSubjectEscapesTheProviderNameLikeTheOperatorsKeyToken() {
        assertEquals("kubemoot.provider.model.vllm-rig0", NatsModelLifecycle.subjectFor("vllm-rig0"));
        assertEquals("kubemoot.provider.model.vllm_2erig0", NatsModelLifecycle.subjectFor("vllm.rig0"));
    }

    @Test
    void readyAsksTheOperatorAndReadsItsAnswer() throws Exception {
        Connection conn = mock(Connection.class);
        Message answer = reply("{\"ok\":true,\"state\":\"awake\",\"switchMs\":1200}");
        when(conn.request(anyString(), any(byte[].class), any(Duration.class))).thenReturn(answer);

        ModelLifecycle.Reply r = over(conn).ready("vllm-rig0", "qwen3:14b");

        assertTrue(r.ok());
        assertEquals("awake", r.state());
        assertEquals(1200L, r.switchMs());
        ArgumentCaptor<byte[]> body = ArgumentCaptor.forClass(byte[].class);
        verify(conn).request(org.mockito.ArgumentMatchers.eq("kubemoot.provider.model.vllm-rig0"), body.capture(),
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
        when(conn.request(anyString(), any(byte[].class), any(Duration.class))).thenReturn(answer);
        assertTrue(over(conn).release("p", "m").ok());
        ArgumentCaptor<byte[]> body = ArgumentCaptor.forClass(byte[].class);
        verify(conn).request(anyString(), body.capture(), any(Duration.class));
        assertEquals("release", JSON.readTree(body.getValue()).path("action").asText());
    }

    @Test
    void aRefusalCarriesTheOperatorsReason() throws Exception {
        Connection conn = mock(Connection.class);
        Message answer = reply("{\"ok\":false,\"error\":\"no vLLM server serves model m\"}");
        when(conn.request(anyString(), any(byte[].class), any(Duration.class))).thenReturn(answer);
        ModelLifecycle.Reply r = over(conn).ready("p", "m");
        assertFalse(r.ok());
        assertEquals("no vLLM server serves model m", r.error());
    }

    @Test
    void everyWayTheOperatorCannotAnswerIsAFailureWithAReason() throws Exception {
        Connection silent = mock(Connection.class);
        when(silent.request(anyString(), any(byte[].class), any(Duration.class))).thenReturn(null);
        assertTrue(over(silent).ready("p", "m").error().contains("did not answer"));

        Connection nobody = mock(Connection.class);
        Message status = mock(Message.class);
        when(status.isStatusMessage()).thenReturn(true);
        when(nobody.request(anyString(), any(byte[].class), any(Duration.class))).thenReturn(status);
        assertTrue(over(nobody).ready("p", "m").error().contains("no operator is listening"));

        Connection broken = mock(Connection.class);
        when(broken.request(anyString(), any(byte[].class), any(Duration.class))).thenThrow(new IllegalStateException("closed"));
        assertFalse(over(broken).ready("p", "m").ok());

        Connection garbled = mock(Connection.class);
        Message answer = reply("not json");
        when(garbled.request(anyString(), any(byte[].class), any(Duration.class))).thenReturn(answer);
        assertTrue(over(garbled).ready("p", "m").error().contains("could not be read"));

        assertTrue(over(null).ready("p", "m").error().contains("NATS is not available"));
        assertFalse(new NatsModelLifecycle(null, JSON, null).ready("p", "m").ok());
    }

    @Test
    void anInterruptedWaitKeepsTheInterruptAndFails() throws Exception {
        Connection conn = mock(Connection.class);
        when(conn.request(anyString(), any(byte[].class), any(Duration.class))).thenThrow(new InterruptedException());
        try {
            assertFalse(over(conn).ready("p", "m").ok());
            assertTrue(Thread.currentThread().isInterrupted());
        } finally {
            Thread.interrupted(); // clear the flag for later tests
        }
    }

    @Test
    void switchCostsAreParsedAndPlacementPrefersTheObservedWakeToTheLoadPenalty() throws Exception {
        ProviderState p = ProviderSelector.fromJson(JSON.readTree(
                "{\"name\":\"v\",\"engine\":\"vllm\",\"endpoint\":\"\",\"switchCostMs\":{\"qwen3:14b\":2000}}"));
        assertEquals(2.0, p.switchSeconds("qwen3:14b").orElseThrow(), 1e-9);
        assertTrue(p.switchSeconds("other").isEmpty());
        assertTrue(p.switchSeconds(null).isEmpty());
        assertEquals(2.0 / PlacementCostModel.DEFAULT_CALL_SECONDS, ProviderSelector.wakeCost(p, "qwen3:14b"), 1e-9);
        assertEquals(ProviderSelector.COLD_LOAD_PENALTY, ProviderSelector.wakeCost(p, "other"));
        ProviderState none = new ProviderState("o", "http://o", 1, 0, 0, java.util.List.of(), true, "", 0L, Map.of());
        assertTrue(none.switchSeconds("m").isEmpty());
    }

    @Test
    void theSubjectPrefixIsTheOneTheContractPublishes() throws Exception {
        String contract = java.nio.file.Files.readString(
                java.nio.file.Path.of("..", "operator", "pkg", "bus", "schema", "asyncapi.yaml"));
        assertTrue(contract.contains("address: " + NatsModelLifecycle.SUBJECT_PREFIX + "{provider}"),
                "the runtime and the contract must name the same subject");
        String keys = java.nio.file.Files.readString(
                java.nio.file.Path.of("..", "operator", "pkg", "bus", "keys.go"));
        assertTrue(keys.contains("\"" + NatsModelLifecycle.SUBJECT_PREFIX + "\""),
                "the operator must subscribe on the same prefix");
    }
}
