package ai.kubemoot.agent.modelserver;

import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.ChatRequestOptions;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.output.TokenUsage;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.Supplier;

/**
 * A chat model that records each request it sends in {@link PerformanceHistory}:
 * its duration, token counts, and outcome, in the {@link CallContext} the call was
 * placed in. Requests are marked cold until the first one succeeds, since a cold
 * load that fails or times out leaves the model still loading. Recording never
 * changes the reply or the exception the model returns.
 */
public final class ObservedChatModel implements ChatModel {

    private static final Logger log = LoggerFactory.getLogger(ObservedChatModel.class);

    private final ChatModel delegate;
    private final PerformanceHistory history;
    private final CallContext context;
    private final AtomicBoolean cold;

    public ObservedChatModel(ChatModel delegate, PerformanceHistory history, CallContext context) {
        this.delegate = delegate;
        this.history = history;
        this.context = context;
        this.cold = new AtomicBoolean(context.coldStart());
    }

    @Override
    public ChatResponse chat(ChatRequest request) {
        return observe(() -> delegate.chat(request));
    }

    @Override
    public ChatResponse chat(ChatRequest request, ChatRequestOptions options) {
        return observe(() -> delegate.chat(request, options));
    }

    @Override
    public dev.langchain4j.model.chat.request.ChatRequestParameters defaultRequestParameters() {
        return delegate.defaultRequestParameters();
    }

    @Override
    public java.util.Set<dev.langchain4j.model.chat.Capability> supportedCapabilities() {
        return delegate.supportedCapabilities();
    }

    @Override
    public dev.langchain4j.model.ModelProvider provider() {
        return delegate.provider();
    }

    private ChatResponse observe(Supplier<ChatResponse> send) {
        boolean coldNow = cold.get();
        long start = System.currentTimeMillis();
        ChatResponse response = null;
        try {
            response = send.get();
            cold.set(false);
            return response;
        } finally {
            record(response, coldNow, start);
        }
    }

    private void record(ChatResponse response, boolean coldNow, long start) {
        try {
            TokenUsage usage = response == null ? null : response.tokenUsage();
            CallContext sent = context.withColdStart(coldNow);
            history.record(sent.measured(response != null, start,
                    System.currentTimeMillis() - start,
                    tokens(usage == null ? null : usage.inputTokenCount()),
                    tokens(usage == null ? null : usage.outputTokenCount())), sent.call());
        } catch (RuntimeException e) {
            log.debug("Recording a {} request on {} failed: {}", context.model(), context.server(), e.getMessage());
        }
    }

    private static long tokens(Integer n) {
        return n == null ? 0L : n;
    }
}
