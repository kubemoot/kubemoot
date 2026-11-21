package ai.kubemoot.agent.tracing;

import io.nats.client.Connection;
import io.nats.client.impl.Headers;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.StatusCode;
import io.opentelemetry.context.Context;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;

class AgentTraceTest {

    private static final String PARENT = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01";
    private static final String TRACE_ID = "0af7651916cd43dd8448eb211c80319c";

    private static Headers headers(String traceparent) {
        return new Headers().put(AgentTrace.TRACEPARENT, traceparent);
    }

    @Test
    void aTurnIsAConsumerSpanWithTheSidecarsNameAndAttributes() {
        try (var trace = new TraceFixture()) {
            Span turn = AgentTrace.startTurn(Context.root(), "t1", "advisory_ready", "k8s-agent");
            turn.end();
            var data = trace.span("turn");
            assertEquals(SpanKind.CONSUMER, data.getKind());
            assertEquals("t1", data.getAttributes().get(AgentTrace.THREAD));
            assertEquals("advisory_ready", data.getAttributes().get(AgentTrace.TRIGGER));
            assertEquals("k8s-agent", data.getAttributes().get(AgentTrace.AGENT));
        }
    }

    @Test
    void aTurnContinuesTheTraceTheMessageCarried() {
        try (var trace = new TraceFixture()) {
            Context parent = AgentTrace.extract(headers(PARENT));
            AgentTrace.startTurn(parent, "t1", "reply", "a").end();
            var data = trace.span("turn");
            assertEquals(TRACE_ID, data.getTraceId());
            assertEquals("b7ad6b7169203331", data.getParentSpanId());
        }
    }

    @Test
    void publishingWritesTheCurrentContextToTheNatsHeaders() {
        try (var trace = new TraceFixture()) {
            Span turn = AgentTrace.startTurn(AgentTrace.extract(headers(PARENT)), "t1", "reply", "a");
            var conn = mock(Connection.class);
            try (var ignored = turn.makeCurrent()) {
                AgentTrace.publish(conn, "subject", "{}".getBytes());
            }
            turn.end();
            var captured = ArgumentCaptor.forClass(Headers.class);
            verify(conn).publish(eq("subject"), captured.capture(), any(byte[].class));
            String written = captured.getValue().getFirst(AgentTrace.TRACEPARENT);
            assertTrue(written.startsWith("00-" + TRACE_ID + "-" + turn.getSpanContext().getSpanId()), written);
        }
    }

    @Test
    void aPublishWithNoTraceStaysAPlainPublish() {
        var conn = mock(Connection.class);
        AgentTrace.publish(conn, "subject", "{}".getBytes());
        verify(conn).publish(eq("subject"), any(byte[].class));
    }

    @Test
    void withNoSdkTheIncomingTraceContextPassesThroughUnchanged() {
        AgentTrace.reset();
        Span turn = AgentTrace.startTurn(AgentTrace.extract(headers(PARENT)), "t1", "reply", "a");
        Headers out;
        try (var ignored = turn.makeCurrent()) {
            out = AgentTrace.inject(null);
        }
        assertNotNull(out);
        assertEquals(PARENT, out.getFirst(AgentTrace.TRACEPARENT));
    }

    @Test
    void bodyFieldsCarryTheTraceparentOnlyInsideATrace() {
        assertTrue(AgentTrace.bodyFields().isEmpty());
        try (var trace = new TraceFixture()) {
            Span turn = AgentTrace.startTurn(Context.root(), "t1", "reply", "a");
            try (var ignored = turn.makeCurrent()) {
                assertTrue(AgentTrace.bodyFields().get(AgentTrace.TRACEPARENT).contains(turn.getSpanContext().getTraceId()));
            }
            turn.end();
        }
    }

    @Test
    void aToolCallIsNamedAndTypedWithoutContentWhenCaptureIsOff() {
        try (var trace = new TraceFixture()) {
            Span call = AgentTrace.startToolCall("kubectl_get", "call-1", "{\"secret\":\"arg\"}");
            AgentTrace.endToolCall(call, "secret result", null);
            var data = trace.span("execute_tool kubectl_get");
            assertEquals("execute_tool", data.getAttributes().get(AgentTrace.OPERATION));
            assertEquals("kubectl_get", data.getAttributes().get(AgentTrace.TOOL_NAME));
            assertEquals("call-1", data.getAttributes().get(AgentTrace.TOOL_CALL_ID));
            assertEquals("function", data.getAttributes().get(AgentTrace.TOOL_TYPE));
            assertNull(data.getAttributes().get(AgentTrace.TOOL_ARGUMENTS));
            assertNull(data.getAttributes().get(AgentTrace.TOOL_RESULT));
        }
    }

    @Test
    void aToolCallCarriesArgumentsAndResultWhenCaptureIsOn() {
        try (var trace = new TraceFixture()) {
            System.setProperty(AgentTrace.CONTENT_PROPERTY, "true");
            Span call = AgentTrace.startToolCall("kubectl_get", "call-1", "{\"a\":1}");
            AgentTrace.endToolCall(call, "result", null);
            var data = trace.span("execute_tool kubectl_get");
            assertEquals("{\"a\":1}", data.getAttributes().get(AgentTrace.TOOL_ARGUMENTS));
            assertEquals("result", data.getAttributes().get(AgentTrace.TOOL_RESULT));
        }
    }

    @Test
    void aFailedToolCallMarksTheSpanFailed() {
        try (var trace = new TraceFixture()) {
            Span call = AgentTrace.startToolCall("t", "", null);
            AgentTrace.endToolCall(call, null, new IllegalStateException("down"));
            var data = trace.span("execute_tool t");
            assertEquals(StatusCode.ERROR, data.getStatus().getStatusCode());
            assertNull(data.getAttributes().get(AgentTrace.TOOL_CALL_ID));
        }
    }

    @Test
    void contentCaptureIsOffUnlessTheSwitchIsTrue() {
        System.clearProperty(AgentTrace.CONTENT_PROPERTY);
        assertFalse(AgentTrace.captureContent() && System.getenv(AgentTrace.CONTENT_ENV) == null);
        System.setProperty(AgentTrace.CONTENT_PROPERTY, "yes");
        assertFalse(AgentTrace.captureContent());
        System.setProperty(AgentTrace.CONTENT_PROPERTY, " true ");
        assertTrue(AgentTrace.captureContent());
        System.clearProperty(AgentTrace.CONTENT_PROPERTY);
    }

    @Test
    void aFailureQuotesNoContentWhenCaptureIsOff() {
        try (var trace = new TraceFixture()) {
            Span call = AgentTrace.startToolCall("t", "", null);
            AgentTrace.endToolCall(call, null, new IllegalStateException("bad token hunter2"));
            var data = trace.span("execute_tool t");
            assertTrue(data.getEvents().isEmpty(), "no exception event, which would carry message and stack");
            assertEquals("java.lang.IllegalStateException", data.getAttributes().get(
                    io.opentelemetry.api.common.AttributeKey.stringKey("exception.type")));
            assertFalse(data.toString().contains("hunter2"));
        }
    }

    @Test
    void aFailureIsRecordedInFullWhenCaptureIsOn() {
        try (var trace = new TraceFixture()) {
            System.setProperty(AgentTrace.CONTENT_PROPERTY, "true");
            Span call = AgentTrace.startToolCall("t", "", null);
            AgentTrace.endToolCall(call, null, new IllegalStateException("detail"));
            assertEquals("exception", trace.span("execute_tool t").getEvents().get(0).getName());
        }
    }

    @Test
    void anAckedPublishCarriesTheTraceContextAndStaysPlainWithoutOne() throws Exception {
        var js = mock(io.nats.client.JetStream.class);
        var options = io.nats.client.PublishOptions.builder().build();
        AgentTrace.publishAcked(js, "s", null, "{}".getBytes(), options);
        verify(js).publish(eq("s"), any(byte[].class), eq(options));

        try (var trace = new TraceFixture()) {
            Span turn = AgentTrace.startTurn(AgentTrace.extract(headers(PARENT)), "t1", "reply", "a");
            try (var ignored = turn.makeCurrent()) {
                AgentTrace.publishAcked(js, "s2", null, "{}".getBytes(), options);
            }
            turn.end();
            var captured = ArgumentCaptor.forClass(Headers.class);
            verify(js).publish(eq("s2"), captured.capture(), any(byte[].class), eq(options));
            assertTrue(captured.getValue().getFirst(AgentTrace.TRACEPARENT).contains(TRACE_ID));
        }
    }
}
