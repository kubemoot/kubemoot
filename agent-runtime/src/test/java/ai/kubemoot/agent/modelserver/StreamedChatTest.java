package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import dev.langchain4j.data.message.UserMessage;
import dev.langchain4j.model.chat.request.ChatRequest;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.jupiter.api.Assertions.*;

/** A streamed call against a server that streams the OpenAI way, with pauses the timings must show. */
class StreamedChatTest {

    private static final long FIRST_TOKEN_DELAY_MS = 300;
    private static final long GENERATE_DELAY_MS = 200;
    /** Arrival jitter: the client times events as it reads them, not as the server writes them. */
    private static final long JITTER_MS = 60;

    private HttpServer server;

    @AfterEach
    void stop() {
        if (server != null) {
            server.stop(0);
        }
    }

    private String serve(com.sun.net.httpserver.HttpHandler handler) throws IOException {
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", handler);
        server.start();
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    private static void event(OutputStream out, String json) throws IOException {
        out.write(("data: " + json + "\n\n").getBytes(StandardCharsets.UTF_8));
        out.flush();
    }

    private static String delta(String content) {
        return "{\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,"
                + "\"delta\":{\"content\":\"" + content + "\"},\"finish_reason\":null}]}";
    }

    private static void pause(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    /** Streams "OK." after a pause, then more text after another, then usage and [DONE]. */
    private static void streamReply(HttpExchange exchange, AtomicReference<String> body) throws IOException {
        body.set(new String(exchange.getRequestBody().readAllBytes(), StandardCharsets.UTF_8));
        exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
        exchange.sendResponseHeaders(200, 0);
        try (OutputStream out = exchange.getResponseBody()) {
            pause(FIRST_TOKEN_DELAY_MS);
            event(out, delta("OK"));
            pause(GENERATE_DELAY_MS);
            event(out, delta("."));
            event(out, "{\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{"
                    + "\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}");
            event(out, "{\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[],"
                    + "\"usage\":{\"prompt_tokens\":25,\"completion_tokens\":2,\"total_tokens\":27}}");
            out.write("data: [DONE]\n\n".getBytes(StandardCharsets.UTF_8));
        }
    }

    private static ChatRequest hello() {
        return ChatRequest.builder().messages(UserMessage.from("Say OK.")).build();
    }

    @Test
    void timesTheFirstTokenAndTheGeneration_andReadsUsage() throws IOException {
        var body = new AtomicReference<String>();
        String endpoint = serve(exchange -> streamReply(exchange, body));
        var model = new ChatModelPool().streamingForEndpoint(ChatEngines.OLLAMA, endpoint, "qwen3:8b", 0.3, 256,
                Duration.ofSeconds(10), false);

        var timed = StreamedChat.send(model, hello(), Duration.ofSeconds(10));

        assertEquals("OK.", timed.response().aiMessage().text());
        // The test server's chunk buffering moves time between the two phases, so the
        // checks are the first pause, the whole reply, and generation as the rest.
        assertTrue(timed.firstTokenMs() >= FIRST_TOKEN_DELAY_MS - JITTER_MS, "first token after " + timed.firstTokenMs());
        assertTrue(timed.totalMs() >= FIRST_TOKEN_DELAY_MS + GENERATE_DELAY_MS - JITTER_MS, "reply took " + timed.totalMs());
        assertEquals(timed.totalMs(), timed.firstTokenMs() + timed.generateMs());
        assertEquals(25, timed.response().tokenUsage().inputTokenCount());
        assertEquals(2, timed.response().tokenUsage().outputTokenCount());
        assertEquals(2, timed.chunks(), "one chunk per delivered token");
        var sent = new ObjectMapper().readTree(body.get());
        assertTrue(sent.path("stream").asBoolean(), "the request asks for a stream");
        assertTrue(sent.path("stream_options").path("include_usage").asBoolean(),
                "the stream must end with token usage: the performance history and CharsPerToken read it");
        assertEquals("none", sent.path("reasoning_effort").asText());
    }

    @Test
    void aServerErrorIsThrown() throws IOException {
        String endpoint = serve(exchange -> {
            byte[] reply = "{\"error\":{\"message\":\"model 'nope' not found\"}}".getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(404, reply.length);
            exchange.getResponseBody().write(reply);
            exchange.close();
        });
        var model = new ChatModelPool().streamingForEndpoint(ChatEngines.OLLAMA, endpoint, "nope", 0.3, 256,
                Duration.ofSeconds(10), null);
        var e = assertThrows(RuntimeException.class, () -> StreamedChat.send(model, hello(), Duration.ofSeconds(10)));
        assertTrue(String.valueOf(e.getMessage()).contains("not found"), e.getMessage());
    }

    @Test
    void aReplyThatNeverFinishesTimesOut() throws IOException {
        String endpoint = serve(exchange -> {
            exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
            exchange.sendResponseHeaders(200, 0);
            event(exchange.getResponseBody(), delta("still"));
            // Longer than the call's timeout plus StreamedChat's grace.
            pause(StreamedChat.GRACE.toMillis() + 5_000);
            exchange.close();
        });
        var model = new ChatModelPool().streamingForEndpoint(ChatEngines.OLLAMA, endpoint, "m", 0.3, 256,
                Duration.ofSeconds(30), null);
        var e = assertThrows(IllegalStateException.class,
                () -> StreamedChat.send(model, hello(), Duration.ofMillis(100)));
        assertTrue(e.getMessage().contains("no complete reply"), e.getMessage());
    }

    /** A stream that runs past its time is cancelled: the server sees its connection close. */
    @Test
    void aTimedOutStreamIsCancelled() throws Exception {
        var closed = new java.util.concurrent.CountDownLatch(1);
        String endpoint = serve(exchange -> {
            exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
            exchange.sendResponseHeaders(200, 0);
            OutputStream out = exchange.getResponseBody();
            try {
                for (int i = 0; i < 100; i++) {
                    event(out, delta("tick"));
                    pause(200);
                }
            } catch (IOException e) {
                closed.countDown();
            }
        });
        var model = new ChatModelPool().streamingForEndpoint(ChatEngines.OLLAMA, endpoint, "m", 0.3, 256,
                Duration.ofSeconds(30), null);

        assertThrows(IllegalStateException.class, () -> StreamedChat.send(model, hello(), Duration.ofMillis(100)));

        assertTrue(closed.await(10, java.util.concurrent.TimeUnit.SECONDS),
                "the client closed the stream after giving up on it");
    }

    @Test
    void aWarmCallsFirstTokenIsPromptTime_aColdOnesIsNot() {
        var timed = new StreamedChat.Timed(null, 400, 1_400);
        var warm = new CallContext("p", "m", CallObservation.KIND_SIMPLE, false, 1).streamed(true, 0L, timed, 25, 50);
        assertEquals(400, warm.promptEvalMs());
        assertEquals(1_000, warm.evalMs());
        assertEquals(CallObservation.UNKNOWN, warm.loadMs());
        var cold = new CallContext("p", "m", CallObservation.KIND_SIMPLE, true, 1).streamed(true, 0L, timed, 25, 50);
        assertEquals(CallObservation.UNKNOWN, cold.promptEvalMs(), "a cold call's first token includes the load");
        assertEquals(1_000, cold.evalMs());
        var empty = new CallContext("p", "m", CallObservation.KIND_SIMPLE, false, 1).streamed(true, 0L, timed, 25, 0);
        assertEquals(CallObservation.UNKNOWN, empty.evalMs(), "no output, no generation speed");
        var shortReply = new CallContext("p", "m", CallObservation.KIND_SIMPLE, false, 1)
                .streamed(true, 0L, timed, 25, CallContext.MIN_TIMED_TOKENS - 1);
        assertEquals(CallObservation.UNKNOWN, shortReply.evalMs(),
                "a short reply's rate would be overstated: its first token is not timed as generation");
    }

    @Test
    void aModelThatReasonsOutOfSightGeneratesTokensItNeverDelivers_soItsTimingsAreNotRecorded() {
        // 258 tokens generated, 2 delivered: the first delivered token came after the reasoning.
        var hidden = new StreamedChat.Timed(null, 4_415, 4_477, 2);
        var obs = new CallContext("p", "m", CallObservation.KIND_TRIAGE, false, 3).streamed(true, 0L, hidden, 1_413, 258);

        assertEquals(CallObservation.UNKNOWN, obs.evalMs(), "62 ms over 258 tokens would read as 4,000 tokens per second");
        assertEquals(CallObservation.UNKNOWN, obs.promptEvalMs(), "the wait was reasoning, not reading the prompt");
        assertEquals(CallObservation.UNKNOWN, obs.firstTokenMs());
        assertEquals(4_477, obs.durationMs(), "the duration and the tokens are still true");
        assertEquals(258, obs.outputTokens());
    }

    @Test
    void aStreamThatDeliveredItsTokensIsTimed_andSoIsOneWhoseChunksWereNotCounted() {
        var delivered = new CallContext("p", "m", CallObservation.KIND_SIMPLE, false, 1)
                .streamed(true, 0L, new StreamedChat.Timed(null, 400, 1_400, 48), 25, 50);
        assertEquals(1_000, delivered.evalMs());
        assertEquals(400, delivered.firstTokenMs());

        var uncounted = new CallContext("p", "m", CallObservation.KIND_SIMPLE, false, 1)
                .streamed(true, 0L, new StreamedChat.Timed(null, 400, 1_400), 25, 50);
        assertEquals(1_000, uncounted.evalMs());

        assertTrue(CallContext.deliveredItsTokens(new StreamedChat.Timed(null, 1, 2, 80), 100), "exactly four fifths");
        assertFalse(CallContext.deliveredItsTokens(new StreamedChat.Timed(null, 1, 2, 79), 100));
        assertTrue(CallContext.deliveredItsTokens(new StreamedChat.Timed(null, 1, 2, 0), 0), "no tokens to account for");
    }

    @Test
    void aFailedStreamHasNoTimings() {
        var failed = new CallContext("p", "m", CallObservation.KIND_SIMPLE, false, 1)
                .streamed(false, 0L, new StreamedChat.Timed(null, 400, 1_400, 48), 25, 50);
        assertEquals(CallObservation.UNKNOWN, failed.firstTokenMs());
        assertEquals(CallObservation.UNKNOWN, failed.evalMs());
    }
}
