package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ServerRegistration.ServerStatus;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.locks.LockSupport;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The state of a streamable HTTP upstream is visible on its registration (status, the reason
 * of the last failed connect, how many connects failed in a row), a down upstream is tried again
 * by the next request once its backoff ends, and a call on a session the upstream no longer
 * knows (it restarted) opens a new session. After a 404 the call is sent once more; after a 500
 * (what an MCP gateway in front of the upstream answers) only a read of the catalog is, and a
 * tools/call is never sent twice.
 */
class McpClientManagerBackoffTest {

    private static final String SERVER = "backoff-mcp";
    private static final Duration BACKOFF = Duration.ofMillis(400);

    private final ObjectMapper mapper = new ObjectMapper();
    private final AtomicInteger initializes = new AtomicInteger();
    private final AtomicInteger calls = new AtomicInteger();
    private final Set<String> knownSessions = ConcurrentHashMap.newKeySet();
    private volatile int staleStatus = 404;
    private McpClientManager manager;
    private HttpServer backend;
    private int port;
    private String url;

    @BeforeEach
    void start() throws IOException {
        McpGatewayProperties properties = new McpGatewayProperties();
        properties.getReconnect().setInitialBackoff(BACKOFF);
        properties.getReconnect().setMaxBackoff(Duration.ofSeconds(2));
        manager = new McpClientManager(properties, WebClient.builder(), mapper);
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
    void aServerDownAtFirstConnectionShowsItsStateAndRecoversWhenItComesUp() throws IOException {
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("the first connect fails", () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);

        ServerRegistration failed = manager.getServer(server.id());
        assertNotNull(failed.statusReason(), "the failure has a reason");
        assertEquals(1, failed.failedConnects());
        assertNotNull(failed.nextConnectAt());

        backend = startBackend(port);
        awaitTrue("a request is answered once the backoff ends", () -> call(server).error() == null);
        awaitTrue("the server shows connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED);

        ServerRegistration ready = manager.getServer(server.id());
        assertEquals(ServerStatus.CONNECTED, ready.status());
        assertNull(ready.statusReason());
        assertEquals(0, ready.failedConnects());
        assertNull(ready.nextConnectAt());
    }

    @Test
    void aRequestInsideTheBackoffIsRefusedWithTheReasonAndStartsNoConnect() throws IOException {
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("the first connect fails", () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);
        backend = startBackend(port);
        int before = initializes.get();

        McpMessage reply = call(server);

        assertNotNull(reply.error(), "the server came up but the backoff has not ended");
        assertTrue(reply.error().message().contains("connects again"), reply.error().message());
        assertEquals(before, initializes.get(), "a refused request does not try the upstream");
    }

    @Test
    void consecutiveFailuresLengthenTheBackoffUpToTheMaximum() {
        McpGatewayProperties.Reconnect reconnect = new McpGatewayProperties.Reconnect();
        reconnect.setInitialBackoff(Duration.ofSeconds(1));
        reconnect.setMaxBackoff(Duration.ofSeconds(30));

        assertEquals(Duration.ofSeconds(1), reconnect.backoffAfter(1));
        assertEquals(Duration.ofSeconds(4), reconnect.backoffAfter(3));
        assertEquals(Duration.ofSeconds(30), reconnect.backoffAfter(6));
        assertEquals(Duration.ofSeconds(30), reconnect.backoffAfter(500));
    }

    @Test
    void aShortDropOfTheNetworkFailsTheCallsDuringItAndTheNextCallAfterItIsAnswered() throws IOException {
        backend = startBackend(port);
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());

        backend.stop(0);
        assertNotNull(call(server).error(), "a call during the drop reports the failure");
        assertEquals(ServerStatus.ERROR, manager.getServer(server.id()).status());
        assertNotNull(manager.getServer(server.id()).statusReason());

        backend = startBackend(port);
        awaitTrue("the session is open again", () -> call(server).error() == null);
        assertEquals(ServerStatus.CONNECTED, manager.getServer(server.id()).status());
    }

    @Test
    void aCallOnASessionTheRestartedUpstreamForgotIsRetriedOnceOnANewSessionAfter404() throws IOException {
        assertStaleSessionIsRetriedOnce(404);
    }

    @Test
    void aToolCallAnswered500IsNotSentAgainButTheNextCallUsesANewSession() throws IOException {
        backend = startBackend(port);
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        staleStatus = 500;
        knownSessions.clear();
        int initializesBefore = initializes.get();
        int callsBefore = calls.get();

        McpMessage first = call(server);

        assertNotNull(first.error(), "the error goes to the caller");
        assertEquals(1, calls.get() - callsBefore, "the tool call is sent once, never again");
        assertEquals(1, initializes.get() - initializesBefore, "a new session is opened");

        assertNull(call(server).error(), "the next call works on the new session");
        assertEquals(2, calls.get() - callsBefore);
    }

    @Test
    void aCatalogReadAnswered500IsSentAgainOnANewSession() throws IOException {
        backend = startBackend(port);
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        staleStatus = 500;
        knownSessions.clear();
        int initializesBefore = initializes.get();

        McpMessage reply = manager.forwardRequest(server.id(), McpMessage.request(9, "resources/list", Map.of()))
            .block(Duration.ofSeconds(30));

        assertNotNull(reply);
        assertNull(reply.error(), "a read of the catalog is answered on the new session");
        assertEquals(1, initializes.get() - initializesBefore);
    }

    private void assertStaleSessionIsRetriedOnce(int status) throws IOException {
        backend = startBackend(port);
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        staleStatus = status;
        knownSessions.clear();
        int initializesBefore = initializes.get();
        int callsBefore = calls.get();

        McpMessage reply = call(server);

        assertNull(reply.error(), "the call is answered on the new session");
        assertEquals(1, initializes.get() - initializesBefore, "one new session");
        assertEquals(2, calls.get() - callsBefore, "the call is sent once more, not more");
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
        String session = exchange.getRequestHeaders().getFirst("Mcp-Session-Id");
        if ("tools/call".equals(method)) {
            calls.incrementAndGet();
        }
        if (!"initialize".equals(method) && (session == null || !knownSessions.contains(session))) {
            exchange.sendResponseHeaders(staleStatus, -1);
            exchange.close();
            return;
        }
        if (id == null || id.isNull()) {
            exchange.sendResponseHeaders(202, -1);
            exchange.close();
            return;
        }
        String issued = null;
        if ("initialize".equals(method)) {
            issued = "s-" + initializes.incrementAndGet();
            knownSessions.add(issued);
            exchange.getResponseHeaders().add("Mcp-Session-Id", issued);
        }
        String json = mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id, "result", resultFor(method)));
        byte[] body = ("event: message\ndata: " + json + "\n\n").getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
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
            case "tools/list" -> Map.of("tools", List.of(
                Map.of("name", "pods_list", "description", "pods", "inputSchema", Map.of("type", "object"))));
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "ok")));
        };
    }

    /** Polls on the test thread, so the condition may block on a call. */
    private static void awaitTrue(String what, BooleanSupplier condition) {
        long deadline = System.nanoTime() + Duration.ofSeconds(20).toNanos();
        while (!condition.getAsBoolean()) {
            if (System.nanoTime() > deadline) {
                throw new AssertionError("timed out waiting for: " + what);
            }
            LockSupport.parkNanos(Duration.ofMillis(50).toNanos());
        }
    }
}
