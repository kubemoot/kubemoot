package ai.kubemoot.agent.modelserver;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.StreamingChatModel;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.chat.response.StreamingChatResponseHandler;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.*;

/**
 * The chat models reach a plain-HTTP server with the request body intact and
 * without asking to upgrade to HTTP/2, which uvicorn (vLLM's server) refuses by
 * dropping the body.
 */
class ChatModelPoolHttpTest {

    private static final String COMPLETION = """
            {"id":"c1","object":"chat.completion","created":1,"model":"qwen3:14b",
             "choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
             "usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}
            """;

    private static final String STREAM = """
            data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"qwen3:14b","choices":[{"index":0,"delta":{"role":"assistant","content":"pong"},"finish_reason":null}]}

            data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"qwen3:14b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

            data: [DONE]

            """;

    private record Seen(String protocol, String upgrade, String body) {}

    private final List<Seen> seen = new CopyOnWriteArrayList<>();
    private HttpServer server;
    private String endpoint;

    @BeforeEach
    void start() throws IOException {
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", this::answer);
        server.start();
        endpoint = "http://127.0.0.1:" + server.getAddress().getPort();
    }

    @AfterEach
    void stop() {
        server.stop(0);
    }

    private void answer(HttpExchange ex) throws IOException {
        String body = new String(ex.getRequestBody().readAllBytes(), StandardCharsets.UTF_8);
        seen.add(new Seen(ex.getProtocol(), ex.getRequestHeaders().getFirst("Upgrade"), body));
        boolean stream = body.matches("(?s).*\"stream\"\\s*:\\s*true.*");
        byte[] out = (stream ? STREAM : COMPLETION).getBytes(StandardCharsets.UTF_8);
        ex.getResponseHeaders().add("Content-Type", stream ? "text/event-stream" : "application/json");
        ex.sendResponseHeaders(200, out.length);
        try (OutputStream os = ex.getResponseBody()) {
            os.write(out);
        }
    }

    @Test
    void chatModelSendsTheBodyOverHttp11WithoutAnUpgradeRequest() {
        ChatModel model = new ChatModelPool().forEndpoint(ChatEngines.forType("vllm"), endpoint, "qwen3:14b",
                0.3, 64, Duration.ofSeconds(10), null);

        assertEquals("pong", model.chat("ping"));

        assertOneHttp11RequestWithBody();
    }

    @Test
    void streamingChatModelSendsTheBodyOverHttp11WithoutAnUpgradeRequest() throws Exception {
        StreamingChatModel model = new ChatModelPool().streamingForEndpoint(ChatEngines.forType("vllm"), endpoint,
                "qwen3:14b", 0.3, 64, Duration.ofSeconds(10), null);
        CompletableFuture<ChatResponse> done = new CompletableFuture<>();

        model.chat("ping", new StreamingChatResponseHandler() {
            @Override
            public void onPartialResponse(String partial) {
                // the complete response below carries the text
            }

            @Override
            public void onCompleteResponse(ChatResponse response) {
                done.complete(response);
            }

            @Override
            public void onError(Throwable error) {
                done.completeExceptionally(error);
            }
        });

        assertEquals("pong", done.get(10, TimeUnit.SECONDS).aiMessage().text());
        assertOneHttp11RequestWithBody();
    }

    private void assertOneHttp11RequestWithBody() {
        assertEquals(1, seen.size());
        Seen request = seen.get(0);
        assertEquals("HTTP/1.1", request.protocol());
        assertNull(request.upgrade(), "the client must not ask a plain-HTTP server to upgrade");
        assertTrue(request.body().contains("\"ping\""), "the request body must arrive: " + request.body());
    }
}
