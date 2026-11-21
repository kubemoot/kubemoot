package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.tracing.AgentTrace;
import ai.kubemoot.agent.tracing.TraceFixture;
import io.nats.client.Connection;
import io.nats.client.Message;
import io.nats.client.impl.Headers;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import java.nio.charset.StandardCharsets;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

/** A turn continues the trace the message carried, and what the agent publishes continues it again. */
class DiscussionSubscriberTraceTest {

    private static final String TRACE_ID = "0af7651916cd43dd8448eb211c80319c";
    private static final String PARENT = "00-" + TRACE_ID + "-b7ad6b7169203331-01";

    private final NatsConnectionProvider natsProvider = mock(NatsConnectionProvider.class);
    private final Connection conn = mock(Connection.class);

    private DiscussionSubscriber subscriber() {
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", null));
        when(natsProvider.getConnection()).thenReturn(conn);
        var props = DiscussionSubscriberHelpersTest.stubProperties("k8s-agent", "kubernetes", null, false);
        var metrics = new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
        return new DiscussionSubscriber(natsProvider, mock(ChatService.class), metrics, props,
                "http://localhost:11434");
    }

    private static Message message(String traceparent) {
        var msg = mock(Message.class);
        when(msg.getSubject()).thenReturn("kubemoot.discuss.ns-a.general.t1");
        when(msg.getData()).thenReturn(("{\"messageType\":\"advisory_ready\",\"threadId\":\"t1\","
                + "\"agentName\":\"coord\",\"messageId\":\"m1\",\"content\":\"c\","
                + "\"metadata\":{\"innerCircle\":[\"k8s-agent\"]}}").getBytes(StandardCharsets.UTF_8));
        when(msg.getHeaders()).thenReturn(traceparent == null ? null : new Headers().put(AgentTrace.TRACEPARENT, traceparent));
        return msg;
    }

    private static void awaitAck(Message msg) {
        verify(msg, timeout(5000)).ack();
    }

    @Test
    void theTurnIsAChildOfTheMessagesTraceWithTheTriggerAndAgent() {
        try (var trace = new TraceFixture()) {
            var msg = message(PARENT);
            subscriber().handleJetStreamMessage(msg);
            awaitAck(msg);
            var turn = trace.span("turn");
            assertEquals(TRACE_ID, turn.getTraceId());
            assertEquals("b7ad6b7169203331", turn.getParentSpanId());
            assertEquals("t1", turn.getAttributes().get(AgentTrace.THREAD));
            assertEquals("advisory_ready", turn.getAttributes().get(AgentTrace.TRIGGER));
            assertEquals("k8s-agent", turn.getAttributes().get(AgentTrace.AGENT));
        }
    }

    @Test
    void thePublishedSignalCarriesTheTraceInItsHeaderAndBody() throws Exception {
        try (var trace = new TraceFixture()) {
            var msg = message(PARENT);
            subscriber().handleJetStreamMessage(msg);
            awaitAck(msg);
            var headers = ArgumentCaptor.forClass(Headers.class);
            var body = ArgumentCaptor.forClass(byte[].class);
            verify(conn, atLeastOnce()).publish(any(String.class), headers.capture(), body.capture());
            String header = headers.getValue().getFirst(AgentTrace.TRACEPARENT);
            assertTrue(header.contains(TRACE_ID), header);
            String json = new String(body.getValue(), StandardCharsets.UTF_8);
            assertTrue(json.contains("\"traceparent\":\"" + header + "\""), json);
            var tree = new com.fasterxml.jackson.databind.ObjectMapper().readTree(json);
            for (String field : new String[]{"messageId", "threadId", "agentName", "messageType", "content",
                    "channel", "timestamp", "metadata"}) {
                assertTrue(tree.has(field), "the existing field " + field + " is still written");
            }
            assertNotNull(trace.span("publish " + new com.fasterxml.jackson.databind.ObjectMapper()
                    .readTree(json).path("messageType").asText()));
        }
    }

    @Test
    void aMessageWithNoTraceStartsATraceAtTheTurn() {
        try (var trace = new TraceFixture()) {
            var msg = message(null);
            subscriber().handleJetStreamMessage(msg);
            awaitAck(msg);
            var turn = trace.span("turn");
            assertFalse(turn.getParentSpanContext().isValid());
        }
    }
}
