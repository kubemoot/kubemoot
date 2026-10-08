package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ServerRegistration.ServerStatus;
import kubemoot.ai.mcpgateway.model.ToolInfo;
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
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A streamable HTTP MCP server that is not reachable when the gateway first connects, or
 * that goes away and comes back, is connected again by the next request to it, without a
 * gateway restart and without waiting for the operator to register it again.
 */
class McpClientManagerRetryTest {

    private static final String SERVER = "late-mcp";
    private static final List<String> TOOLS = List.of("pods_list", "resources_list");

    private final ObjectMapper mapper = new ObjectMapper();
    private final AtomicInteger initializes = new AtomicInteger();
    private McpClientManager manager;
    private HttpServer backend;
    private int port;
    private String url;

    @BeforeEach
    void start() throws IOException {
        manager = new McpClientManager(new McpGatewayProperties(), WebClient.builder(), mapper);
        backend = startBackend(0);
        port = backend.getAddress().getPort();
        url = "http://127.0.0.1:" + port;
        backend.stop(0);
    }

    @AfterEach
    void stop() {
        manager.shutdown();
        backend.stop(0);
    }

    @Test
    void aRequestAfterAFailedFirstConnectionConnectsAndIsAnswered() throws IOException {
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("the first connect fails", () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);
        backend = startBackend(port);

        McpMessage reply = call(server);

        assertNull(reply.error(), "the request connects first and is answered");
        awaitTrue("the tools are listed", () -> names(manager.getToolsForServer(server.id())).equals(TOOLS));
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED);
    }

    @Test
    void aRequestToAServerThatIsStillDownFailsWithAnErrorMessageNotAHang() {
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("the first connect fails", () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);

        McpMessage reply = call(server);

        assertNotNull(reply.error(), "the caller gets an error");
        assertEquals(ServerStatus.ERROR, manager.getServer(server.id()).status());
    }

    @Test
    void aRequestAfterTheServerWentAwayAndCameBackIsAnswered() throws IOException {
        backend = startBackend(port);
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());

        backend.stop(0);
        assertNotNull(call(server).error(), "a request while it is down reports the failure");
        assertEquals(ServerStatus.ERROR, manager.getServer(server.id()).status());

        backend = startBackend(port);
        McpMessage reply = call(server);

        assertNull(reply.error());
        awaitTrue("connected again", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED);
    }

    @Test
    void aConnectedServerIsNotReconnectedByARequest() throws IOException {
        backend = startBackend(port);
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        int before = initializes.get();

        assertNull(call(server).error());
        assertNull(call(server).error());

        assertEquals(before, initializes.get(), "no new session for a healthy server");
    }

    @Test
    void requestsThatArriveTogetherAfterAFailedConnectionAllWaitForOneConnect() throws IOException {
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("the first connect fails", () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);
        backend = startBackend(port);
        int before = initializes.get();

        List<McpMessage> replies = Flux.range(0, 5)
            .flatMap(i -> manager.forwardRequest(server.id(), McpMessage.request(10 + i, "tools/call",
                Map.of("name", "pods_list", "arguments", Map.of()))))
            .collectList()
            .block(Duration.ofSeconds(30));

        assertNotNull(replies);
        assertEquals(5, replies.size());
        assertTrue(replies.stream().allMatch(r -> r.error() == null), "every request is answered");
        assertEquals(1, initializes.get() - before, "one connect served all of them");
    }

    private McpMessage call(ServerRegistration server) {
        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(7, "tools/call", Map.of("name", "pods_list", "arguments", Map.of())))
            .block(Duration.ofSeconds(30));
        assertNotNull(reply);
        return reply;
    }

    private HttpServer startBackend(int bindPort) throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", bindPort), 0);
        server.createContext("/mcp", this::receive);
        server.setExecutor(Executors.newCachedThreadPool());
        server.start();
        return server;
    }

    private void receive(HttpExchange exchange) throws IOException {
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        String method = request.path("method").asText();
        JsonNode id = request.get("id");
        if (id == null || id.isNull()) {
            exchange.sendResponseHeaders(202, -1);
            exchange.close();
            return;
        }
        if ("initialize".equals(method)) {
            initializes.incrementAndGet();
        }
        String json = mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id, "result", resultFor(method)));
        byte[] body = ("event: message\ndata: " + json + "\n\n").getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
        exchange.getResponseHeaders().add("Mcp-Session-Id", "s-" + initializes.get());
        exchange.sendResponseHeaders(200, body.length);
        try (OutputStream out = exchange.getResponseBody()) {
            out.write(body);
        }
    }

    private static Object resultFor(String method) {
        return switch (method) {
            case "initialize" -> Map.of("protocolVersion", "2024-11-05",
                "capabilities", Map.of("tools", Map.of()),
                "serverInfo", Map.of("name", SERVER, "version", "1"));
            case "tools/list" -> Map.of("tools", TOOLS.stream()
                .map(n -> Map.of("name", n, "description", n, "inputSchema", Map.of("type", "object")))
                .toList());
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "ok")));
        };
    }

    private static List<String> names(List<ToolInfo> tools) {
        return tools.stream().map(ToolInfo::name).toList();
    }

    private static void awaitTrue(String what, BooleanSupplier condition) {
        Boolean met = Mono.fromSupplier(condition::getAsBoolean)
            .filter(Boolean::booleanValue)
            .repeatWhenEmpty(300, attempts -> attempts.delayElements(Duration.ofMillis(50)))
            .block(Duration.ofSeconds(20));
        assertTrue(Boolean.TRUE.equals(met), "timed out waiting for: " + what);
    }
}
