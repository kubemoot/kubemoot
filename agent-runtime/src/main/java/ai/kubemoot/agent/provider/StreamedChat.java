package ai.kubemoot.agent.provider;

import dev.langchain4j.model.chat.StreamingChatModel;
import dev.langchain4j.model.chat.request.ChatRequest;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.chat.response.PartialResponse;
import dev.langchain4j.model.chat.response.PartialResponseContext;
import dev.langchain4j.model.chat.response.PartialThinking;
import dev.langchain4j.model.chat.response.PartialThinkingContext;
import dev.langchain4j.model.chat.response.PartialToolCall;
import dev.langchain4j.model.chat.response.PartialToolCallContext;
import dev.langchain4j.model.chat.response.StreamingChatResponseHandler;
import dev.langchain4j.model.chat.response.StreamingHandle;

import java.time.Duration;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;

/**
 * One chat call sent as a stream and timed: how long until the first token came
 * back (of any kind: text, thinking, or a tool call) and how long the whole reply
 * took. Any OpenAI-compatible server streams, so these timings are the same on
 * every engine. Time to the first token covers reading the prompt, plus loading
 * the model when the call was cold; the rest is generation.
 *
 * A call that fails or runs past its time cancels its stream, so the connection
 * closes. The cancel handle arrives with the first token: a stream that stalls before
 * any token is bounded by the model's own HTTP timeout instead.
 */
public final class StreamedChat {

    /** The reply, the milliseconds to its first token, and its total milliseconds. */
    public record Timed(ChatResponse response, long firstTokenMs, long totalMs) {
        /** Milliseconds spent generating after the first token. */
        public long generateMs() {
            return Math.max(totalMs - firstTokenMs, 0L);
        }
    }

    /** Extra wait beyond the model's own HTTP timeout before giving up on a stream. */
    static final Duration GRACE = Duration.ofSeconds(5);

    private StreamedChat() {}

    /**
     * Sends {@code request} and waits for the whole reply. A failed call throws what
     * the model reported; a reply that does not finish within {@code timeout} plus a
     * short grace throws {@link IllegalStateException}.
     */
    public static Timed send(StreamingChatModel model, ChatRequest request, Duration timeout) {
        long start = System.currentTimeMillis();
        var handler = new Handler(start, new AtomicLong(-1L), new AtomicReference<>(), new CompletableFuture<>());
        model.chat(request, handler);
        try {
            ChatResponse response = handler.done().get(timeout.plus(GRACE).toMillis(), TimeUnit.MILLISECONDS);
            long total = System.currentTimeMillis() - start;
            long first = handler.firstAt().get();
            return new Timed(response, first < 0 ? total : first, total);
        } catch (ExecutionException e) {
            handler.cancel();
            throw e.getCause() instanceof RuntimeException r ? r : new IllegalStateException(e.getCause());
        } catch (TimeoutException e) {
            handler.cancel();
            throw new IllegalStateException("no complete reply within " + timeout.toSeconds() + "s", e);
        } catch (InterruptedException e) {
            handler.cancel();
            Thread.currentThread().interrupt();
            throw new IllegalStateException("interrupted waiting for a reply", e);
        }
    }

    /**
     * Marks the first token of any kind, keeps the stream's cancel handle, and completes
     * on the whole reply or an error.
     */
    private record Handler(long start, AtomicLong firstAt, AtomicReference<StreamingHandle> handle,
                           CompletableFuture<ChatResponse> done) implements StreamingChatResponseHandler {

        private void mark(StreamingHandle h) {
            handle.compareAndSet(null, h);
            firstAt.compareAndSet(-1L, System.currentTimeMillis() - start);
        }

        /** Cancels the stream when its handle has arrived. */
        void cancel() {
            StreamingHandle h = handle.get();
            if (h != null && !h.isCancelled()) {
                h.cancel();
            }
        }

        @Override
        public void onPartialResponse(PartialResponse response, PartialResponseContext context) {
            mark(context.streamingHandle());
        }

        @Override
        public void onPartialThinking(PartialThinking thinking, PartialThinkingContext context) {
            mark(context.streamingHandle());
        }

        @Override
        public void onPartialToolCall(PartialToolCall toolCall, PartialToolCallContext context) {
            mark(context.streamingHandle());
        }

        @Override
        public void onCompleteResponse(ChatResponse response) {
            done.complete(response);
        }

        @Override
        public void onError(Throwable error) {
            done.completeExceptionally(error);
        }
    }
}
