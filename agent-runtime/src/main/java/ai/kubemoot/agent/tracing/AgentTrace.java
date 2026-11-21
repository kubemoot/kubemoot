package ai.kubemoot.agent.tracing;

import io.nats.client.Connection;
import io.nats.client.JetStream;
import io.nats.client.JetStreamApiException;
import io.nats.client.PublishOptions;
import io.nats.client.impl.Headers;
import io.opentelemetry.api.OpenTelemetry;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanBuilder;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.StatusCode;
import io.opentelemetry.api.trace.Tracer;
import io.opentelemetry.api.trace.propagation.W3CTraceContextPropagator;
import io.opentelemetry.context.Context;
import io.opentelemetry.context.propagation.TextMapGetter;
import io.opentelemetry.context.propagation.TextMapSetter;

import java.io.IOException;

/**
 * The agent runtime's tracing in one place: the spans a turn and a tool call make, and the W3C
 * trace context that rides on every discussion message in the {@code traceparent} NATS header,
 * so one discussion is one trace whichever harness its agents run on. The agent sidecar writes
 * the same span names and attributes and the same header.
 *
 * <p>The runtime installs its tracer at startup ({@link TracingSetup}). Until then, and whenever no
 * collector is configured, the tracer is the OpenTelemetry no-op, which still hands an incoming
 * trace context on unchanged: the trace flows through an agent that exports nothing.</p>
 *
 * <p>Content (a prompt, an answer, a tool's arguments and result) is never put in a span unless the
 * installer sets {@value #CONTENT_ENV} to {@code true}. That switch is separate from
 * {@code KUBEMOOT_LOG_CONTENT}: a trace backend has its own retention and readers, so turning on
 * content for a debugging session in the logs does not send text to it.</p>
 */
public final class AgentTrace {

    /** The environment variable that turns content in spans on. */
    public static final String CONTENT_ENV = "KUBEMOOT_TRACE_CONTENT";
    /** The system property that does the same, for tests. */
    public static final String CONTENT_PROPERTY = "kubemoot.trace.content";
    /** The header that carries the trace context on a NATS message, as on an HTTP request. */
    public static final String TRACEPARENT = "traceparent";
    /** The W3C header for vendor trace state, carried beside {@link #TRACEPARENT}. */
    public static final String TRACESTATE = "tracestate";

    /** The span name the sidecar uses for one pass of an agent over a message. */
    public static final String TURN = "turn";

    public static final AttributeKey<String> THREAD = Attrs.THREAD;
    public static final AttributeKey<String> TRIGGER = AttributeKey.stringKey("kubemoot.trigger");
    public static final AttributeKey<String> AGENT = AttributeKey.stringKey("kubemoot.agent");
    static final AttributeKey<String> OPERATION = Attrs.OPERATION;
    static final AttributeKey<String> TOOL_NAME = AttributeKey.stringKey("gen_ai.tool.name");
    static final AttributeKey<String> TOOL_CALL_ID = AttributeKey.stringKey("gen_ai.tool.call.id");
    static final AttributeKey<String> TOOL_TYPE = AttributeKey.stringKey("gen_ai.tool.type");
    static final AttributeKey<String> TOOL_ARGUMENTS = AttributeKey.stringKey("gen_ai.tool.call.arguments");
    static final AttributeKey<String> TOOL_RESULT = AttributeKey.stringKey("gen_ai.tool.call.result");

    private static final AttributeKey<String> EXCEPTION_TYPE = AttributeKey.stringKey("exception.type");
    private static final String SCOPE = "ai.kubemoot.agent-runtime";
    private static final W3CTraceContextPropagator PROPAGATOR = W3CTraceContextPropagator.getInstance();

    private static final TextMapGetter<Headers> HEADER_GETTER = new TextMapGetter<>() {
        @Override
        public Iterable<String> keys(Headers carrier) {
            return carrier == null ? java.util.List.of() : carrier.keySet();
        }

        @Override
        public String get(Headers carrier, String key) {
            return carrier == null ? null : carrier.getFirst(key);
        }
    };

    private static final TextMapSetter<Headers> HEADER_SETTER = (carrier, key, value) -> {
        if (carrier != null) {
            carrier.put(key, value);
        }
    };

    private static volatile Tracer tracer = OpenTelemetry.noop().getTracer(SCOPE);

    private AgentTrace() {
    }

    /** Installs the tracer every span of the runtime is made with. */
    public static void install(Tracer installed) {
        tracer = installed == null ? OpenTelemetry.noop().getTracer(SCOPE) : installed;
    }

    /** Back to the no-op tracer; for tests. */
    public static void reset() {
        install(null);
    }

    /** The tracer spans are made with. */
    public static Tracer tracer() {
        return tracer;
    }

    /** Whether the installer has turned content in spans on. Off unless set to {@code true}. */
    public static boolean captureContent() {
        String value = System.getProperty(CONTENT_PROPERTY);
        if (value == null) {
            value = System.getenv(CONTENT_ENV);
        }
        return Boolean.parseBoolean(value == null ? "" : value.trim());
    }

    // ---- propagation on NATS headers ----

    /** The context a message's headers carry; the current one when they carry none. */
    public static Context extract(Headers headers) {
        return PROPAGATOR.extract(Context.current(), headers, HEADER_GETTER);
    }

    /**
     * The headers to publish with: {@code base} (or new headers) plus the current trace context.
     * Returns null when there is nothing to add and no base, so a publish without either stays
     * a plain publish.
     */
    public static Headers inject(Headers base) {
        Headers headers = base;
        if (Span.current().getSpanContext().isValid()) {
            headers = headers == null ? new Headers() : headers;
            PROPAGATOR.inject(Context.current(), headers, HEADER_SETTER);
        }
        return headers;
    }

    /** The current trace context as the message body fields {@code traceparent} and {@code tracestate}. */
    public static java.util.Map<String, String> bodyFields() {
        Headers headers = inject(null);
        var fields = new java.util.LinkedHashMap<String, String>();
        if (headers != null) {
            putIfPresent(fields, TRACEPARENT, headers.getFirst(TRACEPARENT));
            putIfPresent(fields, TRACESTATE, headers.getFirst(TRACESTATE));
        }
        return fields;
    }

    private static void putIfPresent(java.util.Map<String, String> into, String key, String value) {
        if (value != null && !value.isEmpty()) {
            into.put(key, value);
        }
    }

    /** Publishes {@code payload} with the current trace context in the headers. */
    public static void publish(Connection conn, String subject, byte[] payload) {
        publish(conn, subject, null, payload);
    }

    /** Publishes with {@code base} headers plus the current trace context. */
    public static void publish(Connection conn, String subject, Headers base, byte[] payload) {
        Headers headers = inject(base);
        if (headers == null) {
            conn.publish(subject, payload);
        } else {
            conn.publish(subject, headers, payload);
        }
    }

    /** Publishes through JetStream with {@code base} headers plus the current trace context. */
    public static void publishAcked(JetStream js, String subject, Headers base, byte[] payload,
                                    PublishOptions options) throws IOException, JetStreamApiException {
        Headers headers = inject(base);
        if (headers == null) {
            js.publish(subject, payload, options);
        } else {
            js.publish(subject, headers, payload, options);
        }
    }

    // ---- spans ----

    /**
     * Starts a turn: one pass of this agent over a message, a child of the context the message
     * carried. The caller makes it current, and ends it.
     */
    public static Span startTurn(Context parent, String threadId, String trigger, String agent) {
        return tracer.spanBuilder(TURN).setParent(parent).setSpanKind(SpanKind.CONSUMER)
                .setAttribute(THREAD, Attrs.nullToEmpty(threadId))
                .setAttribute(TRIGGER, Attrs.nullToEmpty(trigger))
                .setAttribute(AGENT, Attrs.nullToEmpty(agent))
                .startSpan();
    }

    /**
     * Starts a tool call, a child of the current span. With content capture on the arguments are
     * recorded; the result goes in with {@link #endToolCall}.
     */
    public static Span startToolCall(String toolName, String callId, String arguments) {
        SpanBuilder builder = tracer.spanBuilder("execute_tool " + toolName)
                .setSpanKind(SpanKind.INTERNAL)
                .setAttribute(OPERATION, "execute_tool")
                .setAttribute(TOOL_NAME, Attrs.nullToEmpty(toolName))
                .setAttribute(TOOL_TYPE, "function");
        if (callId != null && !callId.isEmpty()) {
            builder.setAttribute(TOOL_CALL_ID, callId);
        }
        if (captureContent() && arguments != null) {
            builder.setAttribute(TOOL_ARGUMENTS, arguments);
        }
        return builder.startSpan();
    }

    /** Ends a tool call; {@code failure} (null when it worked) marks the span failed. */
    public static void endToolCall(Span span, String result, Throwable failure) {
        if (captureContent() && result != null) {
            span.setAttribute(TOOL_RESULT, result);
        }
        fail(span, failure);
        span.end();
    }

    /**
     * Marks {@code span} failed with {@code failure}; nothing when it is null. A failure's message
     * and stack trace can quote the arguments or the reply of the call that failed, so without
     * content capture only the exception's class is recorded.
     */
    public static void fail(Span span, Throwable failure) {
        if (failure == null) {
            return;
        }
        if (captureContent()) {
            span.recordException(failure);
        } else {
            span.setAttribute(EXCEPTION_TYPE, failure.getClass().getName());
        }
        span.setStatus(StatusCode.ERROR, failure.getClass().getSimpleName());
    }

}
