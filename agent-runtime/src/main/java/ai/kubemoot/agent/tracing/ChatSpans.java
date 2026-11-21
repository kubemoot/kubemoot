package ai.kubemoot.agent.tracing;

import dev.langchain4j.model.chat.listener.ChatModelErrorContext;
import dev.langchain4j.model.chat.listener.ChatModelListener;
import dev.langchain4j.model.chat.listener.ChatModelRequestContext;
import dev.langchain4j.model.chat.listener.ChatModelResponseContext;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.context.Context;

import java.util.List;

/**
 * One span per model call, named {@code chat <model>} and carrying the OpenTelemetry GenAI
 * attributes: operation, provider, request and response model, token counts, finish reasons.
 * The Quarkus LangChain4j extension fills the same attributes, but only on models it builds;
 * the runtime builds its chat models itself (one per model server), so this listener is set on
 * those. The span is a child of the span current when the call starts and is never made
 * current itself, so a streamed call that finishes on another thread leaves no context behind.
 *
 * <p>The prompt and the reply go in the span only with content capture on ({@link AgentTrace#captureContent}).</p>
 */
public final class ChatSpans implements ChatModelListener {

    static final AttributeKey<String> OPERATION = Attrs.OPERATION;
    static final AttributeKey<String> PROVIDER = AttributeKey.stringKey("gen_ai.provider.name");
    static final AttributeKey<String> REQUEST_MODEL = AttributeKey.stringKey("gen_ai.request.model");
    static final AttributeKey<String> RESPONSE_MODEL = AttributeKey.stringKey("gen_ai.response.model");
    static final AttributeKey<String> RESPONSE_ID = AttributeKey.stringKey("gen_ai.response.id");
    static final AttributeKey<Long> INPUT_TOKENS = AttributeKey.longKey("gen_ai.usage.input_tokens");
    static final AttributeKey<Long> OUTPUT_TOKENS = AttributeKey.longKey("gen_ai.usage.output_tokens");
    static final AttributeKey<List<String>> FINISH = AttributeKey.stringArrayKey("gen_ai.response.finish_reasons");
    static final AttributeKey<String> INPUT = AttributeKey.stringKey("gen_ai.input.messages");
    static final AttributeKey<String> OUTPUT = AttributeKey.stringKey("gen_ai.output.messages");

    private static final String SPAN_KEY = "kubemoot.chat.span";

    @Override
    public void onRequest(ChatModelRequestContext context) {
        try {
            var request = context.chatRequest();
            String model = request.parameters() == null ? null : request.parameters().modelName();
            var builder = AgentTrace.tracer().spanBuilder("chat " + (model == null ? "" : model).trim())
                    .setParent(Context.current()).setSpanKind(SpanKind.CLIENT)
                    .setAttribute(OPERATION, "chat");
            if (context.modelProvider() != null) {
                builder.setAttribute(PROVIDER, context.modelProvider().name().toLowerCase(java.util.Locale.ROOT).replace("_", ""));
            }
            if (model != null) {
                builder.setAttribute(REQUEST_MODEL, model);
            }
            if (AgentTrace.captureContent()) {
                builder.setAttribute(INPUT, String.valueOf(request.messages()));
            }
            context.attributes().put(SPAN_KEY, builder.startSpan());
        } catch (RuntimeException e) {
            // Tracing never changes the call.
        }
    }

    @Override
    public void onResponse(ChatModelResponseContext context) {
        Span span = take(context.attributes());
        if (span == null) {
            return;
        }
        try {
            describe(span, context.chatResponse());
        } finally {
            span.end();
        }
    }

    @Override
    public void onError(ChatModelErrorContext context) {
        Span span = take(context.attributes());
        if (span == null) {
            return;
        }
        AgentTrace.fail(span, context.error());
        span.end();
    }

    private static void describe(Span span, ChatResponse response) {
        if (response == null) {
            return;
        }
        var meta = response.metadata();
        if (meta != null) {
            setIf(span, RESPONSE_ID, meta.id());
            setIf(span, RESPONSE_MODEL, meta.modelName());
            if (meta.finishReason() != null) {
                span.setAttribute(FINISH, List.of(meta.finishReason().name().toLowerCase(java.util.Locale.ROOT)));
            }
            tokens(span, meta.tokenUsage());
        }
        if (AgentTrace.captureContent() && response.aiMessage() != null && response.aiMessage().text() != null) {
            span.setAttribute(OUTPUT, response.aiMessage().text());
        }
    }

    private static void tokens(Span span, TokenUsage usage) {
        if (usage == null) {
            return;
        }
        if (usage.inputTokenCount() != null) {
            span.setAttribute(INPUT_TOKENS, usage.inputTokenCount().longValue());
        }
        if (usage.outputTokenCount() != null) {
            span.setAttribute(OUTPUT_TOKENS, usage.outputTokenCount().longValue());
        }
    }

    private static void setIf(Span span, AttributeKey<String> key, String value) {
        if (value != null && !value.isEmpty()) {
            span.setAttribute(key, value);
        }
    }

    private static Span take(java.util.Map<Object, Object> attributes) {
        return attributes.remove(SPAN_KEY) instanceof Span span ? span : null;
    }
}
