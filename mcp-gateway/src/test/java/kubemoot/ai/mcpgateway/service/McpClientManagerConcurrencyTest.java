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
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Concurrent tool calls through one streamable-HTTP backend. The fake backend behaves
 * like kubernetes-mcp-server: while a request id is in flight on the session, a second
 * request with the same id is never answered.
 */
class McpClientManagerConcurrencyTest {

    private static final String SESSION = "session-1";
    private final ObjectMapper mapper = new ObjectMapper();
    private final Set<String> inFlight = ConcurrentHashMap.newKeySet();
    private final Set<String> seenCallIds = ConcurrentHashMap.newKeySet();
    /** Tool calls the backend holds until all have arrived; one unless a test expects more. */
    private final AtomicReference<CountDownLatch> arrivals = new AtomicReference<>(new CountDownLatch(1));
    private HttpServer backend;
    private McpClientManager manager;

    @BeforeEach
    void start() throws IOException {
        backend = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        backend.createContext("/mcp", this::handle);
        backend.setExecutor(Executors.newCachedThreadPool());
        backend.start();
        manager = new McpClientManager(new McpGatewayProperties(), WebClient.builder(), mapper);
    }

    @AfterEach
    void stop() {
        manager.shutdown();
        backend.stop(0);
    }

    @Test
    void concurrentCallsWithTheSameCallerIdAllComplete() throws Exception {
        String serverId = register();
        arrivals.set(new CountDownLatch(6));

        // Six callers that all chose the same id, as a millisecond timestamp does.
        List<McpMessage> replies = Flux.range(0, 6)
            .flatMap(i -> manager.forwardRequest(serverId,
                McpMessage.request(42L, "tools/call", Map.of("name", "echo", "arguments", Map.of("n", i)))))
            .collectList()
            .block(Duration.ofSeconds(10));

        assertNotNull(replies);
        assertEquals(6, replies.size());
        replies.forEach(reply -> {
            assertEquals(42L, ((Number) reply.id()).longValue(), "the caller's id comes back");
            assertTrue(reply.error() == null, "no error: " + reply.error());
        });
        assertEquals(6, seenCallIds.size(), "each upstream call carried its own id");
        assertFalse(seenCallIds.contains("42"), "the caller's id is not sent upstream");
    }

    @Test
    void aSingleCallGetsItsOwnIdBack() throws Exception {
        String serverId = register();
        McpMessage reply = manager.forwardRequest(serverId,
            McpMessage.request(7, "tools/call", Map.of("name", "echo", "arguments", Map.of())))
            .block(Duration.ofSeconds(5));
        assertNotNull(reply);
        assertEquals(7, ((Number) reply.id()).intValue());
    }

    private String register() {
        String url = "http://127.0.0.1:" + backend.getAddress().getPort();
        ServerRegistration registration = manager.registerServer("echo-server", url, "http");
        // Tool discovery runs in the background; poll until it has landed.
        var tools = Mono.fromSupplier(() -> manager.getToolsForServer(registration.id()))
            .filter(found -> !found.isEmpty())
            .repeatWhenEmpty(100, attempts -> attempts.delayElements(Duration.ofMillis(50)))
            .block(Duration.ofSeconds(10));
        assertNotNull(tools, "tools discovered");
        return registration.id();
    }

    private void handle(HttpExchange exchange) throws IOException {
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        String method = request.path("method").asText();
        JsonNode id = request.get("id");
        if (id == null || id.isNull()) {
            exchange.sendResponseHeaders(202, -1);
            exchange.close();
            return;
        }
        switch (method) {
            case "initialize" -> reply(exchange, id, Map.of("protocolVersion", "2025-03-26",
                "capabilities", Map.of("tools", Map.of()), "serverInfo", Map.of("name", "echo", "version", "1")));
            case "tools/list" -> reply(exchange, id, Map.of("tools", List.of(Map.of(
                "name", "echo", "description", "echo", "inputSchema", Map.of("type", "object")))));
            default -> callTool(exchange, id);
        }
    }

    private void callTool(HttpExchange exchange, JsonNode id) throws IOException {
        String key = id.asText();
        seenCallIds.add(key);
        arrivals.get().countDown();
        if (!inFlight.add(key)) {
            return; // a duplicate in-flight id: never answered, as the real server does
        }
        if (!awaitOverlap()) {
            return; // the calls never overlapped: leave this one unanswered so the test fails
        }
        inFlight.remove(key);
        reply(exchange, id, Map.of("content", List.of(Map.of("type", "text", "text", "ok"))));
    }

    /** Hold a call until every expected call has arrived, so they all overlap in flight. */
    private boolean awaitOverlap() {
        try {
            return arrivals.get().await(5, TimeUnit.SECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        }
    }

    private void reply(HttpExchange exchange, JsonNode id, Object result) throws IOException {
        String json = mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id, "result", result));
        byte[] body = ("event: message\ndata: " + json + "\n\n").getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
        exchange.getResponseHeaders().add("Mcp-Session-Id", SESSION);
        exchange.sendResponseHeaders(200, body.length);
        try (OutputStream out = exchange.getResponseBody()) {
            out.write(body);
        }
    }
}
