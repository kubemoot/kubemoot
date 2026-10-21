package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.Executors;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The call the backend receives carries the coerced values; the caller's reply and id are unchanged. */
class McpClientManagerCoercionTest {

    private static final String N_ITEMS = "n_items";
    private static final Map<String, Object> SEARCH_SCHEMA = Map.of("type", "object", "properties", Map.of(
        "query", Map.of("type", "string"),
        N_ITEMS, Map.of("type", "integer"),
        "content_types", Map.of("type", "array", "items", Map.of("type", "string"))));

    private final ObjectMapper mapper = new ObjectMapper().findAndRegisterModules();
    private final List<JsonNode> calls = new CopyOnWriteArrayList<>();
    private McpClientManager manager;
    private HttpServer backend;

    @BeforeEach
    void start() throws IOException {
        manager = new McpClientManager(new McpGatewayProperties(), WebClient.builder(), mapper);
        backend = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        backend.createContext("/", this::receive);
        backend.setExecutor(Executors.newCachedThreadPool());
        backend.start();
    }

    @AfterEach
    void stop() {
        manager.shutdown();
        backend.stop(0);
    }

    @Test
    void theBackendReceivesTheCoercedArguments() {
        ServerRegistration server = registered();

        McpMessage reply = call(server, Map.of("query", "kubernetes", N_ITEMS, "8",
            "content_types", "books,videos,courses"));

        assertEquals(42, reply.id(), "the caller's id comes back");
        JsonNode received = lastCallArguments();
        assertTrue(received.path(N_ITEMS).isIntegralNumber(), received.toString());
        assertEquals(8, received.path(N_ITEMS).asInt());
        assertEquals("kubernetes", received.path("query").asText());
        assertEquals(List.of("books", "videos", "courses"),
            mapper.convertValue(received.path("content_types"), List.class));
    }

    @Test
    void argumentsThatCannotBeCoercedReachTheBackendUnchanged() {
        ServerRegistration server = registered();

        call(server, Map.of(N_ITEMS, "many"));

        assertTrue(lastCallArguments().path(N_ITEMS).isTextual());
        assertEquals("many", lastCallArguments().path(N_ITEMS).asText());
    }

    private ServerRegistration registered() {
        ServerRegistration server = manager.registerServer("oreilly",
            "http://127.0.0.1:" + backend.getAddress().getPort(), "http");
        Boolean listed = Mono.fromSupplier(() -> !manager.getToolsForServer(server.id()).isEmpty())
            .filter(Boolean::booleanValue)
            .repeatWhenEmpty(300, attempts -> attempts.delayElements(Duration.ofMillis(50)))
            .block(Duration.ofSeconds(20));
        assertTrue(Boolean.TRUE.equals(listed), "timed out waiting for the tool list");
        return server;
    }

    private JsonNode lastCallArguments() {
        return calls.get(calls.size() - 1).path("params").path("arguments");
    }

    private McpMessage call(ServerRegistration server, Map<String, Object> arguments) {
        McpMessage reply = manager.forwardRequest(server.id(), McpMessage.request(42, "tools/call",
            Map.of("name", "search", "arguments", arguments))).block(Duration.ofSeconds(30));
        assertNotNull(reply);
        return reply;
    }

    private void receive(HttpExchange exchange) throws IOException {
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        String method = request.path("method").asText();
        if ("tools/call".equals(method)) {
            calls.add(request);
        }
        JsonNode id = request.get("id");
        if (id == null || id.isNull()) {
            exchange.sendResponseHeaders(202, -1);
            exchange.close();
            return;
        }
        String json = mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id, "result", resultFor(method)));
        byte[] body = ("event: message\ndata: " + json + "\n\n").getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
        exchange.getResponseHeaders().add("Mcp-Session-Id", "s-1");
        exchange.sendResponseHeaders(200, body.length);
        try (OutputStream out = exchange.getResponseBody()) {
            out.write(body);
        }
    }

    private static Object resultFor(String method) {
        return switch (method) {
            case "initialize" -> Map.of("protocolVersion", "2024-11-05",
                "capabilities", Map.of("tools", Map.of()),
                "serverInfo", Map.of("name", "fake", "version", "1"));
            case "tools/list" -> Map.of("tools", List.of(
                Map.of("name", "search", "description", "search", "inputSchema", SEARCH_SCHEMA)));
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "ok")));
        };
    }
}
