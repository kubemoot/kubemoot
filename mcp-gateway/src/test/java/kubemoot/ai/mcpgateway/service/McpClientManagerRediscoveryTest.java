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
import kubemoot.ai.mcpgateway.model.ToolOverride;
import kubemoot.ai.mcpgateway.model.ToolOverride.ParameterOverride;
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
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A backing MCP server restarts behind the same URL with a different tool set (as a pod
 * replaced with new arguments does), and the gateway serves the new tool list. The fake
 * backend behaves like the mcp-bridge sidecar: GET /sse holds a stream open and announces
 * the session, POST /message answers 202 and sends the reply on that session's stream.
 * A restart ends every open stream and changes the tools the server lists.
 */
class McpClientManagerRediscoveryTest {

    private static final String SERVER = "kubernetes-mcp";
    private static final List<String> CORE_TOOLS = List.of("pods_list", "resources_list");
    private static final List<String> HELM_TOOLS = List.of("pods_list", "resources_list", "helm_list");

    private final ObjectMapper mapper = new ObjectMapper();
    private final Map<String, SseStream> streams = new ConcurrentHashMap<>();
    private final AtomicReference<List<String>> toolNames = new AtomicReference<>(CORE_TOOLS);
    private final AtomicInteger sseConnects = new AtomicInteger();
    private final AtomicInteger toolListings = new AtomicInteger();
    private final AtomicReference<String> httpSession = new AtomicReference<>("http-1");
    private final AtomicBoolean holdStreams = new AtomicBoolean();
    private final McpGatewayProperties properties = new McpGatewayProperties();
    private HttpServer backend;
    private McpClientManager manager;
    private String url;

    @BeforeEach
    void start() throws IOException {
        backend = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        backend.createContext("/sse", this::openStream);
        backend.createContext("/message", this::receiveMessage);
        backend.createContext("/mcp", this::receiveStreamableHttp);
        backend.setExecutor(Executors.newCachedThreadPool());
        backend.start();
        url = "http://127.0.0.1:" + backend.getAddress().getPort();
        manager = new McpClientManager(properties, WebClient.builder(), mapper);
    }

    @AfterEach
    void stop() {
        manager.shutdown();
        endAllStreams();
        backend.stop(0);
    }

    @Test
    void aServerReplacedBehindTheSameUrlIsReListedOnReRegistration() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);

        restartBackendWith(HELM_TOOLS);
        awaitTrue("the gateway notices the lost session",
            () -> manager.getServer(server.id()).status() == ServerStatus.DISCONNECTED);

        ServerRegistration again = manager.registerServer(SERVER, url, "stdio");

        assertEquals(server.id(), again.id(), "the same registration is kept");
        awaitTools(server.id(), HELM_TOOLS);
        assertEquals(SERVER, manager.getAllTools().stream()
            .filter(t -> t.name().equals("helm_list")).findFirst().orElseThrow().serverName());
        assertEquals(ServerStatus.CONNECTED, manager.getServer(server.id()).status());
    }

    @Test
    void aToolCallAfterTheRestartOpensANewSessionAndReListsTools() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        restartBackendWith(HELM_TOOLS);
        awaitTrue("the gateway notices the lost session",
            () -> manager.getServer(server.id()).status() == ServerStatus.DISCONNECTED);

        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(9, "tools/call", Map.of("name", "pods_list", "arguments", Map.of())))
            .block(Duration.ofSeconds(15));

        assertNotNull(reply);
        assertNull(reply.error(), "the call succeeds on the new session");
        awaitTools(server.id(), HELM_TOOLS);
    }

    @Test
    void aToolsListChangedNotificationReListsWithoutARestart() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        int connectsBefore = sseConnects.get();

        toolNames.set(HELM_TOOLS);
        streams.values().forEach(s -> s.send(
            "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}"));

        awaitTools(server.id(), HELM_TOOLS);
        assertEquals(connectsBefore, sseConnects.get(), "re-listed on the open session");
    }

    @Test
    void aReListThatReturnsNoToolsKeepsTheKnownToolsAndMarksTheServerForRetry() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);

        toolNames.set(List.of());
        manager.refreshTools(server.id());

        awaitTrue("the empty re-list is marked for another try",
            () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);
        assertEquals(CORE_TOOLS, names(manager.getToolsForServer(server.id())));
    }

    @Test
    void aReListOnTheSameSessionTakesTheNewReplyNotAnOlderOne() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);

        toolNames.set(HELM_TOOLS);
        manager.refreshTools(server.id());
        awaitTools(server.id(), HELM_TOOLS);

        toolNames.set(List.of("pods_list"));
        manager.refreshTools(server.id());
        awaitTools(server.id(), List.of("pods_list"));
    }

    @Test
    void aBurstOfReRegistrationsAfterARestartConnectsOnce() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        restartBackendWith(HELM_TOOLS);
        awaitTrue("the gateway notices the lost session",
            () -> manager.getServer(server.id()).status() == ServerStatus.DISCONNECTED);
        int connectsBefore = sseConnects.get();

        for (int i = 0; i < 10; i++) {
            manager.registerServer(SERVER, url, "stdio");
        }

        awaitTools(server.id(), HELM_TOOLS);
        assertEquals(connectsBefore + 1, sseConnects.get(), "one reconnect for the whole burst");
    }

    @Test
    void aConnectedServerWithAFreshListIsLeftAlone() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        int connectsBefore = sseConnects.get();
        int listingsBefore = toolListings.get();
        toolNames.set(HELM_TOOLS); // changed silently: no restart, no notification

        manager.registerServer(SERVER, url, "stdio");
        Mono.delay(Duration.ofMillis(500)).block(); // room for a re-list that must not happen

        assertEquals(connectsBefore, sseConnects.get());
        assertEquals(listingsBefore, toolListings.get());
        assertEquals(CORE_TOOLS, names(manager.getToolsForServer(server.id())));
    }

    @Test
    void theSafetyNetReListsAToolListOlderThanItsMaximumAge() {
        properties.setToolListMaxAge(Duration.ZERO);
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        toolNames.set(HELM_TOOLS); // changed silently: no restart, no notification

        manager.registerServer(SERVER, url, "stdio");

        awaitTools(server.id(), HELM_TOOLS);
    }

    @Test
    void aReplacementThatListsNoToolsKeepsTheKnownToolsAndIsRetried() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        restartBackendWith(List.of());
        awaitTrue("the gateway notices the lost session",
            () -> manager.getServer(server.id()).status() == ServerStatus.DISCONNECTED);

        manager.registerServer(SERVER, url, "stdio");
        awaitTrue("the empty reconnect is marked for another try",
            () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);
        assertEquals(CORE_TOOLS, names(manager.getToolsForServer(server.id())));

        toolNames.set(HELM_TOOLS);
        manager.registerServer(SERVER, url, "stdio");
        awaitTools(server.id(), HELM_TOOLS);
    }

    @Test
    void theEndOfAStreamThatWasAlreadyReplacedLeavesTheCurrentSessionAlone() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        int connectsBefore = sseConnects.get();

        manager.onSseStreamEnded(server.id(), new Object(), "closed");

        assertEquals(ServerStatus.CONNECTED, manager.getServer(server.id()).status());
        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(5, "tools/call", Map.of("name", "pods_list", "arguments", Map.of())))
            .block(Duration.ofSeconds(15));
        assertNotNull(reply);
        assertNull(reply.error());
        assertEquals(connectsBefore, sseConnects.get(), "the current session was used, not replaced");
    }

    @Test
    void aCallerThatGivesUpDuringReconnectLeavesTheServerReadyForTheNextReconnect() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        restartBackendWith(HELM_TOOLS);
        awaitTrue("the gateway notices the lost session",
            () -> manager.getServer(server.id()).status() == ServerStatus.DISCONNECTED);
        holdStreams.set(true); // the new session never announces itself

        manager.forwardRequest(server.id(),
                McpMessage.request(6, "tools/call", Map.of("name", "pods_list", "arguments", Map.of())))
            .subscribe()
            .dispose();

        awaitTrue("the abandoned reconnect is not left CONNECTING",
            () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);
        holdStreams.set(false);
        manager.registerServer(SERVER, url, "stdio");
        awaitTools(server.id(), HELM_TOOLS);
    }

    @Test
    void unregisteringAServerEndsItsStream() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        awaitTrue("a stream is open", () -> !streams.isEmpty());

        manager.unregisterServer(server.id());

        // The backend learns of a closed connection on its next write.
        awaitTrue("the stream is closed", () -> {
            streams.values().forEach(SseStream::ping);
            return streams.values().stream().allMatch(SseStream::isClosed);
        });
        assertTrue(manager.getToolsForServer(server.id()).isEmpty());
    }

    @Test
    void aStreamableHttpServerThatForgotTheSessionIsReopenedAndReListed() {
        ServerRegistration server = manager.registerServer(SERVER, url, "http");
        awaitTools(server.id(), CORE_TOOLS);

        // Restarted: the old session id is unknown (404) and the tools differ.
        toolNames.set(HELM_TOOLS);
        httpSession.set("http-2");

        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(3, "tools/call", Map.of("name", "pods_list", "arguments", Map.of())))
            .block(Duration.ofSeconds(15));

        assertNotNull(reply);
        assertNull(reply.error(), "the call is retried on a new session");
        awaitTools(server.id(), HELM_TOOLS);
    }

    @Test
    void registeredOverridesChangeTheDescriptionsTheGatewayServes() {
        ToolOverride override = new ToolOverride("helm_list", "truthful helm_list",
            Map.of("namespace", new ParameterOverride("truthful namespace")));
        ServerRegistration server = manager.registerServer(SERVER, url, "stdio", List.of(override));
        awaitTools(server.id(), CORE_TOOLS);

        ToolInfo untouched = tool(server.id(), "pods_list");
        assertEquals("pods_list", untouched.description());
        assertEquals("upstream namespace", propertyDescription(untouched, "namespace"));

        toolNames.set(HELM_TOOLS);
        manager.refreshTools(server.id());
        awaitTools(server.id(), HELM_TOOLS);
        ToolInfo helm = tool(server.id(), "helm_list");
        assertEquals("truthful helm_list", helm.description());
        assertEquals("truthful namespace", propertyDescription(helm, "namespace"));
        assertEquals("string", ((Map<?, ?>) ((Map<?, ?>) helm.inputSchema().get("properties"))
            .get("namespace")).get("type"), "parameter types are never changed");
    }

    @Test
    void anOverrideForAnUnknownToolIsIgnored() {
        ToolOverride override = new ToolOverride("no_such_tool", "x", Map.of());
        ServerRegistration server = manager.registerServer(SERVER, url, "stdio", List.of(override));

        awaitTools(server.id(), CORE_TOOLS);
        assertEquals("pods_list", tool(server.id(), "pods_list").description());
        assertEquals(ServerStatus.CONNECTED, awaitConnected(server.id()));
    }

    @Test
    void changedOverridesOnReRegistrationAreServedAfterAReList() {
        ServerRegistration server = registerAndAwait(CORE_TOOLS);
        assertEquals("pods_list", tool(server.id(), "pods_list").description());

        manager.registerServer(SERVER, url, "stdio",
            List.of(new ToolOverride("pods_list", "new text", Map.of())));

        awaitTrue("override served", () -> "new text".equals(tool(server.id(), "pods_list").description()));

        manager.registerServer(SERVER, url, "stdio", List.of());
        awaitTrue("override removed", () -> "pods_list".equals(tool(server.id(), "pods_list").description()));
    }

    // --- helpers -------------------------------------------------------------------

    private ToolInfo tool(String serverId, String name) {
        return manager.getToolsForServer(serverId).stream()
            .filter(t -> t.name().equals(name)).findFirst().orElseThrow();
    }

    private ServerStatus awaitConnected(String serverId) {
        awaitTrue("connected", () -> manager.getServer(serverId).status() == ServerStatus.CONNECTED);
        return manager.getServer(serverId).status();
    }

    private static String propertyDescription(ToolInfo tool, String parameter) {
        Map<?, ?> properties = (Map<?, ?>) tool.inputSchema().get("properties");
        return (String) ((Map<?, ?>) properties.get(parameter)).get("description");
    }

    private ServerRegistration registerAndAwait(List<String> expected) {
        ServerRegistration registration = manager.registerServer(SERVER, url, "stdio");
        awaitTools(registration.id(), expected);
        awaitTrue("connected", () -> manager.getServer(registration.id()).status() == ServerStatus.CONNECTED);
        return registration;
    }

    private void restartBackendWith(List<String> tools) {
        toolNames.set(tools);
        endAllStreams();
    }

    private void endAllStreams() {
        streams.values().forEach(SseStream::close);
    }

    private void awaitTools(String serverId, List<String> expected) {
        awaitTrue("tools " + expected, () -> names(manager.getToolsForServer(serverId)).equals(expected));
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

    // --- fake bridge ---------------------------------------------------------------

    private void openStream(HttpExchange exchange) throws IOException {
        sseConnects.incrementAndGet();
        String session = UUID.randomUUID().toString();
        exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
        exchange.sendResponseHeaders(200, 0);
        SseStream stream = new SseStream(exchange);
        streams.put(session, stream);
        if (holdStreams.get()) {
            stream.awaitClose();
            return;
        }
        stream.write("event: endpoint\ndata: /message?sessionId=" + session + "\n\n");
        stream.awaitClose();
    }

    private void receiveMessage(HttpExchange exchange) throws IOException {
        String query = exchange.getRequestURI().getQuery();
        String session = query == null ? "" : query.substring(query.indexOf('=') + 1);
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        SseStream stream = streams.get(session);
        if (stream == null || stream.isClosed()) {
            exchange.sendResponseHeaders(503, -1);
            exchange.close();
            return;
        }
        exchange.sendResponseHeaders(202, -1);
        exchange.close();
        JsonNode id = request.get("id");
        if (id != null && !id.isNull()) {
            stream.send(mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id,
                "result", resultFor(request.path("method").asText()))));
        }
    }

    /** Streamable HTTP: initialize opens the current session; any other session id is 404. */
    private void receiveStreamableHttp(HttpExchange exchange) throws IOException {
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        String method = request.path("method").asText();
        String session = exchange.getRequestHeaders().getFirst("Mcp-Session-Id");
        JsonNode id = request.get("id");
        if (!"initialize".equals(method) && session != null && !session.equals(httpSession.get())) {
            exchange.sendResponseHeaders(404, -1);
            exchange.close();
            return;
        }
        if (id == null || id.isNull()) {
            exchange.sendResponseHeaders(202, -1);
            exchange.close();
            return;
        }
        String json = mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id, "result", resultFor(method)));
        byte[] body = ("event: message\ndata: " + json + "\n\n").getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
        exchange.getResponseHeaders().add("Mcp-Session-Id", httpSession.get());
        exchange.sendResponseHeaders(200, body.length);
        try (OutputStream out = exchange.getResponseBody()) {
            out.write(body);
        }
    }

    private Object resultFor(String method) {
        return switch (method) {
            case "initialize" -> Map.of("protocolVersion", "2024-11-05",
                "capabilities", Map.of("tools", Map.of("listChanged", true)),
                "serverInfo", Map.of("name", SERVER, "version", "1"));
            case "tools/list" -> {
                toolListings.incrementAndGet();
                yield Map.of("tools", toolNames.get().stream()
                    .map(n -> Map.of("name", n, "description", n, "inputSchema", Map.of("type", "object", "properties",
                        Map.of("namespace", Map.of("type", "string", "description", "upstream namespace")))))
                    .toList());
            }
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "ok")));
        };
    }

    /** One open SSE response; writes are serialized, close ends the response. */
    private static final class SseStream {
        private final HttpExchange exchange;
        private final OutputStream out;
        private final CountDownLatch closed = new CountDownLatch(1);

        SseStream(HttpExchange exchange) {
            this.exchange = exchange;
            this.out = exchange.getResponseBody();
        }

        void ping() {
            write(": ping\n\n");
        }

        void send(String json) {
            write("event: message\ndata: " + json + "\n\n");
        }

        synchronized void write(String text) {
            if (isClosed()) {
                return;
            }
            try {
                out.write(text.getBytes(StandardCharsets.UTF_8));
                out.flush();
            } catch (IOException e) {
                close();
            }
        }

        synchronized void close() {
            if (isClosed()) {
                return;
            }
            closed.countDown();
            exchange.close();
        }

        boolean isClosed() {
            return closed.getCount() == 0;
        }

        void awaitClose() {
            try {
                closed.await();
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        }
    }
}
