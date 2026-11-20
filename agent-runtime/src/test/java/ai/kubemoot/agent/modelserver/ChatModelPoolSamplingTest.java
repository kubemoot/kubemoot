package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.config.AgentProperties;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import dev.langchain4j.model.chat.StreamingChatModel;
import dev.langchain4j.model.chat.response.ChatResponse;
import dev.langchain4j.model.chat.response.StreamingChatResponseHandler;
import io.smallrye.config.PropertiesConfigSource;
import io.smallrye.config.SmallRyeConfig;
import io.smallrye.config.SmallRyeConfigBuilder;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.*;

/**
 * An agent that sets no temperature sends none, so the model server applies the
 * model's own published setting; an agent that sets one still sends it. The
 * requests are recorded off the wire, not read from the builder.
 */
class ChatModelPoolSamplingTest {

    private static final String COMPLETION = """
            {"id":"c1","object":"chat.completion","created":1,"model":"m",
             "choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
             "usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}
            """;

    private static final String STREAM = """
            data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"pong"},"finish_reason":null}]}

            data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

            data: [DONE]

            """;

    private final List<String> bodies = new CopyOnWriteArrayList<>();
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
        bodies.add(body);
        boolean stream = body.matches("(?s).*\"stream\"\\s*:\\s*true.*");
        byte[] out = (stream ? STREAM : COMPLETION).getBytes(StandardCharsets.UTF_8);
        ex.getResponseHeaders().add("Content-Type", stream ? "text/event-stream" : "application/json");
        ex.sendResponseHeaders(200, out.length);
        try (OutputStream os = ex.getResponseBody()) {
            os.write(out);
        }
    }

    private String blockingRequest(Double temperature) {
        new ChatModelPool().forEndpoint(ChatEngines.forType("vllm"), endpoint, "m", temperature, 64,
                Duration.ofSeconds(10), null).chat("ping");
        assertEquals(1, bodies.size());
        return bodies.get(0);
    }

    private String streamingRequest(Double temperature) throws Exception {
        StreamingChatModel model = new ChatModelPool().streamingForEndpoint(ChatEngines.forType("vllm"), endpoint,
                "m", temperature, 64, Duration.ofSeconds(10), null);
        CompletableFuture<ChatResponse> done = new CompletableFuture<>();
        model.chat("ping", new StreamingChatResponseHandler() {
            @Override
            public void onPartialResponse(String partial) {
                // the complete response carries the text
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
        done.get(10, TimeUnit.SECONDS);
        assertEquals(1, bodies.size());
        return bodies.get(0);
    }

    @Test
    void anUnsetTemperatureIsAbsentFromTheBlockingRequest() {
        String body = blockingRequest(null);
        assertFalse(body.contains("temperature"), body);
        assertTrue(body.contains("\"max_tokens\""), "the other settings still travel: " + body);
    }

    @Test
    void anUnsetTemperatureIsAbsentFromTheStreamingRequest() throws Exception {
        assertFalse(streamingRequest(null).contains("temperature"));
    }

    @Test
    void aSetTemperatureIsSentOnTheBlockingRequest() {
        assertTrue(blockingRequest(0.7).matches("(?s).*\"temperature\"\\s*:\\s*0\\.7\\b.*"));
    }

    @Test
    void aSetTemperatureIsSentOnTheStreamingRequest() throws Exception {
        assertTrue(streamingRequest(0.1).matches("(?s).*\"temperature\"\\s*:\\s*0\\.1\\b.*"));
    }

    @Test
    void zeroIsAValueAndIsSent() {
        assertTrue(blockingRequest(0.0).matches("(?s).*\"temperature\"\\s*:\\s*0(\\.0)?\\b.*"));
    }

    @Test
    void unsetAndSetTemperatureBuildDistinctModels() {
        ChatModelPool pool = new ChatModelPool();
        var unset = pool.forEndpoint(ChatEngines.forType("vllm"), endpoint, "m", null, 64, Duration.ofSeconds(1), null);
        var set = pool.forEndpoint(ChatEngines.forType("vllm"), endpoint, "m", 0.3, 64, Duration.ofSeconds(1), null);
        assertNotSame(unset, set);
        assertSame(unset, pool.forEndpoint(ChatEngines.forType("vllm"), endpoint, "m", null, 64,
                Duration.ofSeconds(1), null));
    }

    private static AgentProperties.Model modelConfig(Map<String, String> extra) {
        Map<String, String> props = new java.util.HashMap<>(Map.of("kubemoot.agent-name", "a", "kubemoot.agent-description", "d"));
        props.putAll(extra);
        SmallRyeConfig config = new SmallRyeConfigBuilder().addDefaultInterceptors()
                .withMapping(AgentProperties.class)
                .withSources(new PropertiesConfigSource(props, "test", 500))
                .build();
        return config.getConfigMapping(AgentProperties.class).model();
    }

    @Test
    void theConfigHasNoTemperatureUnlessTheOperatorSetsOne() {
        assertTrue(modelConfig(Map.of()).temperature().isEmpty());
    }

    @Test
    void theOperatorsTemperatureReachesTheConfig() {
        assertEquals(0.1, modelConfig(Map.of("kubemoot.model.temperature", "0.1")).temperature().orElseThrow());
    }

    @Test
    void anUnparsableTemperatureFailsTheConfigRatherThanSendingAGuess() {
        var failure = assertThrows(RuntimeException.class,
                () -> modelConfig(Map.of("kubemoot.model.temperature", "hot")));
        assertTrue(failure.getMessage().contains("hot"), failure.getMessage());
    }
}
