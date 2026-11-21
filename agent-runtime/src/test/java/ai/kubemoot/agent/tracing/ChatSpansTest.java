package ai.kubemoot.agent.tracing;

import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.ModelProvider;
import dev.langchain4j.model.chat.listener.ChatModelErrorContext;
import dev.langchain4j.model.chat.listener.ChatModelRequestContext;
import dev.langchain4j.model.chat.listener.ChatModelResponseContext;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.FinishReason;
import dev.langchain4j.model.output.TokenUsage;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.StatusCode;
import org.junit.jupiter.api.Test;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

class ChatSpansTest {

    private final ChatSpans listener = new ChatSpans();

    private static ChatRequest request() {
        return ChatRequest.builder().messages(UserMessage.from("what is the secret")).modelName("qwen3:32b").build();
    }

    private static ChatResponse response() {
        return ChatResponse.builder().aiMessage(AiMessage.from("the answer"))
                .modelName("qwen3-32b-served").id("resp-1").finishReason(FinishReason.STOP)
                .tokenUsage(new TokenUsage(120, 30)).build();
    }

    private void call(Map<Object, Object> attrs, ChatResponse reply) {
        listener.onRequest(new ChatModelRequestContext(request(), ModelProvider.OPEN_AI, attrs));
        listener.onResponse(new ChatModelResponseContext(reply, request(), ModelProvider.OPEN_AI, attrs));
    }

    @Test
    void aModelCallHasTheGenAiAttributesAndTokenCounts() {
        try (var trace = new TraceFixture()) {
            call(new HashMap<>(), response());
            var data = trace.span("chat qwen3:32b");
            assertEquals(SpanKind.CLIENT, data.getKind());
            assertEquals("chat", data.getAttributes().get(ChatSpans.OPERATION));
            assertEquals("openai", data.getAttributes().get(ChatSpans.PROVIDER));
            assertEquals("qwen3:32b", data.getAttributes().get(ChatSpans.REQUEST_MODEL));
            assertEquals("qwen3-32b-served", data.getAttributes().get(ChatSpans.RESPONSE_MODEL));
            assertEquals("resp-1", data.getAttributes().get(ChatSpans.RESPONSE_ID));
            assertEquals(120L, data.getAttributes().get(ChatSpans.INPUT_TOKENS));
            assertEquals(30L, data.getAttributes().get(ChatSpans.OUTPUT_TOKENS));
            assertEquals(List.of("stop"), data.getAttributes().get(ChatSpans.FINISH));
        }
    }

    @Test
    void noPromptOrReplyTextWhenCaptureIsOff() {
        try (var trace = new TraceFixture()) {
            call(new HashMap<>(), response());
            var data = trace.span("chat qwen3:32b");
            assertNull(data.getAttributes().get(ChatSpans.INPUT));
            assertNull(data.getAttributes().get(ChatSpans.OUTPUT));
        }
    }

    @Test
    void promptAndReplyAreRecordedWhenCaptureIsOn() {
        try (var trace = new TraceFixture()) {
            System.setProperty(AgentTrace.CONTENT_PROPERTY, "true");
            call(new HashMap<>(), response());
            var data = trace.span("chat qwen3:32b");
            assertTrue(data.getAttributes().get(ChatSpans.INPUT).contains("what is the secret"));
            assertEquals("the answer", data.getAttributes().get(ChatSpans.OUTPUT));
        }
    }

    @Test
    void theSpanIsAChildOfTheCurrentSpanAndNeverLeftCurrent() {
        try (var trace = new TraceFixture()) {
            Span turn = AgentTrace.startTurn(io.opentelemetry.context.Context.root(), "t1", "x", "a");
            try (var ignored = turn.makeCurrent()) {
                call(new HashMap<>(), response());
                assertEquals(turn.getSpanContext().getSpanId(), Span.current().getSpanContext().getSpanId());
            }
            turn.end();
            assertEquals(turn.getSpanContext().getSpanId(), trace.span("chat qwen3:32b").getParentSpanId());
        }
    }

    @Test
    void aFailedCallMarksTheSpanFailed() {
        try (var trace = new TraceFixture()) {
            var attrs = new HashMap<Object, Object>();
            listener.onRequest(new ChatModelRequestContext(request(), ModelProvider.OPEN_AI, attrs));
            listener.onError(new ChatModelErrorContext(new RuntimeException("timeout"), request(),
                    ModelProvider.OPEN_AI, attrs));
            assertEquals(StatusCode.ERROR, trace.span("chat qwen3:32b").getStatus().getStatusCode());
        }
    }

    @Test
    void aResponseWithNoRequestSpanIsIgnored() {
        try (var trace = new TraceFixture()) {
            listener.onResponse(new ChatModelResponseContext(response(), request(), ModelProvider.OPEN_AI,
                    new HashMap<>()));
            assertTrue(trace.spans().isEmpty());
        }
    }
}
