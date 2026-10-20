package kubemoot.ai.mcpgateway.service;

import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.core.read.ListAppender;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.FeedbackEntry;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ServerRegistration.ServerStatus;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.slf4j.LoggerFactory;
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
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A streamable-HTTP server may answer each POST with application/json or text/event-stream,
 * and may never issue a session id. The gateway reads either, by the reply's Content-Type.
 */
class McpClientManagerReplyTypeTest {

    private static final String JSON = "application/json";
    private static final String SSE = "text/event-stream";
    private static final String SERVER = "reply-types";

    /** The exact initialize result a stateless, JSON-answering server sends. */
    private static final Map<String, Object> INITIALIZE_RESULT = Map.of(
        "protocolVersion", "2025-06-18",
        "capabilities", Map.of("experimental", Map.of(), "tools", Map.of("listChanged", false)),
        "serverInfo", Map.of("name", "mcp-content-discovery-external", "version", "1.30.0"));

    private final ObjectMapper mapper = new ObjectMapper().findAndRegisterModules();
    private final List<String> sessionsSeen = new CopyOnWriteArrayList<>();
    private final ListAppender<ILoggingEvent> logs = new ListAppender<>();
    private final Logger managerLog = (Logger) LoggerFactory.getLogger(McpClientManager.class);
    private McpClientManager manager;
    private HttpServer backend;
    private volatile Map<String, String> replyTypes;
    private volatile boolean issueSession;
    private volatile boolean garbled;
    private volatile List<Map<String, Object>> tools;

    @BeforeEach
    void start() throws IOException {
        manager = new McpClientManager(new McpGatewayProperties(), WebClient.builder(), mapper);
        tools = List.of(Map.of("name", "search_content", "description", "search",
            "inputSchema", Map.of("type", "object")));
        backend = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        backend.createContext("/", this::receive);
        backend.setExecutor(Executors.newCachedThreadPool());
        backend.start();
        logs.start();
        managerLog.addAppender(logs);
    }

    @AfterEach
    void stop() {
        managerLog.detachAppender(logs);
        manager.shutdown();
        backend.stop(0);
    }

    @Test
    void aStatelessServerAnsweringJsonWithNoSessionIdConnectsListsToolsAndAnswersCalls() {
        replyTypes = Map.of("initialize", JSON, "tools/list", JSON, "tools/call", JSON);
        issueSession = false;

        ServerRegistration server = register();

        assertConnectedWithTools(server);
        McpMessage reply = call(server);
        assertNull(reply.error());
        assertTrue(String.valueOf(reply.result()).contains("found it"), String.valueOf(reply.result()));
        assertTrue(sessionsSeen.stream().allMatch(String::isEmpty), "no session id is invented: " + sessionsSeen);
        assertFalse(logs.list.stream().anyMatch(e -> e.getFormattedMessage().contains("lost its session")));
    }

    @Test
    void aServerAnsweringEventStreamsWithASessionIdKeepsWorking() {
        replyTypes = Map.of("initialize", SSE, "tools/list", SSE, "tools/call", SSE);
        issueSession = true;

        ServerRegistration server = register();

        assertConnectedWithTools(server);
        assertNull(call(server).error());
        assertTrue(sessionsSeen.contains("sess-1"), "later calls carry the issued session: " + sessionsSeen);
    }

    @Test
    void aServerMixingReplyTypesPerRequestWorks() {
        replyTypes = Map.of("initialize", JSON, "tools/list", SSE, "tools/call", JSON);
        issueSession = false;

        ServerRegistration server = register();

        assertConnectedWithTools(server);
        assertNull(call(server).error());
    }

    @Test
    void aServerWhoseToolListStaysEmptyIsReportedClearly() {
        replyTypes = Map.of("initialize", JSON, "tools/list", JSON, "tools/call", JSON);
        issueSession = false;
        tools = List.of();

        ServerRegistration server = register();
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED);
        awaitTrue("reported", () -> logs.list.stream().anyMatch(e -> e.getFormattedMessage().contains("lists no tools")));

        List<FeedbackEntry> feedback = manager.consumeFeedback(server.id());
        assertTrue(feedback.stream().anyMatch(f -> String.valueOf(f).contains("lists no tools")), feedback.toString());
    }

    @Test
    void reRegisteringAServerWithNoToolsConnectsAgainAndPicksUpToolsOnceItHasThem() {
        replyTypes = Map.of("initialize", JSON, "tools/list", JSON, "tools/call", JSON);
        issueSession = false;
        tools = List.of();
        ServerRegistration server = register();
        awaitTrue("connected empty", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED);

        tools = List.of(Map.of("name", "search_content", "description", "search", "inputSchema", Map.of("type", "object")));
        awaitTrue("reconnect on re-registration", () -> {
            register();
            return !manager.getToolsForServer(server.id()).isEmpty();
        });
    }

    @Test
    void anEmptyOrMalformedJsonReplyFailsTheConnectInsteadOfHanging() {
        replyTypes = Map.of("initialize", JSON, "tools/list", JSON, "tools/call", JSON);
        issueSession = false;
        garbled = true;

        ServerRegistration server = register();

        awaitTrue("reported as an error", () -> manager.getServer(server.id()).status() == ServerStatus.ERROR
            || manager.getServer(server.id()).status() == ServerStatus.CONNECTED);
        assertTrue(manager.getToolsForServer(server.id()).isEmpty());
    }

    private ServerRegistration register() {
        return manager.registerServer(SERVER, "http://127.0.0.1:" + backend.getAddress().getPort() + "/mcp", "http");
    }

    private void assertConnectedWithTools(ServerRegistration server) {
        awaitTrue("connected with its tools", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        assertEquals("search_content", manager.getToolsForServer(server.id()).get(0).name());
    }

    private McpMessage call(ServerRegistration server) {
        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(7, "tools/call", Map.of("name", "search_content", "arguments", Map.of())))
            .block(Duration.ofSeconds(30));
        assertNotNull(reply);
        return reply;
    }

    private void receive(HttpExchange exchange) throws IOException {
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        String method = request.path("method").asText();
        String session = exchange.getRequestHeaders().getFirst("Mcp-Session-Id");
        sessionsSeen.add(session == null ? "" : session);
        JsonNode id = request.get("id");
        if (id == null || id.isNull()) {
            exchange.sendResponseHeaders(202, -1);
            exchange.close();
            return;
        }
        String json = mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id, "result", resultFor(method)));
        String type = replyTypes.getOrDefault(method, JSON);
        if (garbled) {
            json = "not json";
        }
        String body = SSE.equals(type) ? "event: message\ndata: " + json + "\n\n" : json;
        byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", type);
        if (issueSession && "initialize".equals(method)) {
            exchange.getResponseHeaders().add("Mcp-Session-Id", "sess-1");
        }
        exchange.sendResponseHeaders(200, bytes.length);
        try (OutputStream out = exchange.getResponseBody()) {
            out.write(bytes);
        }
    }

    private Object resultFor(String method) {
        return switch (method) {
            case "initialize" -> INITIALIZE_RESULT;
            case "tools/list" -> Map.of("tools", tools);
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "found it")));
        };
    }

    private static void awaitTrue(String what, BooleanSupplier condition) {
        Boolean met = Mono.fromSupplier(condition::getAsBoolean)
            .filter(Boolean::booleanValue)
            .repeatWhenEmpty(300, attempts -> attempts.delayElements(Duration.ofMillis(50)))
            .block(Duration.ofSeconds(20));
        assertTrue(Boolean.TRUE.equals(met), "timed out waiting for: " + what);
    }
}
