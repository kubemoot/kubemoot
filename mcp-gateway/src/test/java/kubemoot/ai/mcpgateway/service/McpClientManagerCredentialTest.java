package kubemoot.ai.mcpgateway.service;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.core.read.ListAppender;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.RegisterServerRequest;
import kubemoot.ai.mcpgateway.model.ServerCredential;
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
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.Executors;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A server registered with a credential gets that header on every call to it (initialize,
 * tool listing, tool calls) and no other server does; the value is never logged or listed.
 */
class McpClientManagerCredentialTest {

    private static final String SECRET = "s3cr3t-token-value-9f2c";
    private static final String BEARER = "Bearer " + SECRET;
    private static final String AUTHORIZATION = "Authorization";
    private static final String HTTP = "http";
    private static final String KEYED = "keyed";

    /** One call a backend received: the JSON-RPC method, the request path and the headers under test. */
    private record Seen(String method, String path, String authorization, String apiKey, String session) { }

    private final ObjectMapper mapper = new ObjectMapper().findAndRegisterModules();
    private final List<Seen> keyed = new CopyOnWriteArrayList<>();
    private final List<Seen> open = new CopyOnWriteArrayList<>();
    private final ListAppender<ILoggingEvent> logs = new ListAppender<>();
    private final Logger root = (Logger) LoggerFactory.getLogger(Logger.ROOT_LOGGER_NAME);
    private Level rootLevel;
    private McpClientManager manager;
    private HttpServer keyedBackend;
    private HttpServer openBackend;

    @BeforeEach
    void start() throws IOException {
        manager = new McpClientManager(new McpGatewayProperties(), WebClient.builder(), mapper);
        keyedBackend = backend(keyed);
        openBackend = backend(open);
        rootLevel = root.getLevel();
        root.setLevel(Level.TRACE);
        logs.start();
        root.addAppender(logs);
    }

    @AfterEach
    void stop() {
        root.detachAppender(logs);
        root.setLevel(rootLevel);
        manager.shutdown();
        keyedBackend.stop(0);
        openBackend.stop(0);
    }

    @Test
    void theHeaderIsOnInitializeToolListingAndToolCallsOfThatServerOnly() {
        ServerRegistration withKey = register(url(keyedBackend, ""), new ServerCredential(AUTHORIZATION, BEARER));
        ServerRegistration without = manager.registerServer("open", url(openBackend, ""), HTTP);
        connected(withKey);
        connected(without);

        assertNull(call(withKey).error());
        assertNull(call(without).error());

        assertTrue(keyed.stream().map(Seen::method).toList()
            .containsAll(List.of("initialize", "tools/list", "tools/call")));
        assertTrue(keyed.stream().allMatch(s -> BEARER.equals(s.authorization())), "every call to the keyed server");
        assertTrue(keyed.stream().filter(s -> !"initialize".equals(s.method())).allMatch(s -> "s-1".equals(s.session())),
            "later calls carry the session id: " + keyed);
        assertFalse(open.isEmpty());
        assertTrue(open.stream().allMatch(s -> s.authorization() == null && s.apiKey() == null),
            "no call to another server carries it");
    }

    @Test
    void aCustomHeaderNameIsSentAsGiven() {
        ServerRegistration server = register(url(keyedBackend, ""), new ServerCredential("X-Api-Key", SECRET));
        connected(server);
        assertNull(call(server).error());

        assertTrue(keyed.stream().allMatch(s -> SECRET.equals(s.apiKey()) && s.authorization() == null));
    }

    @Test
    void aUrlThatAlreadyEndsInMcpIsCalledAtThatPathWithItsTrailingSlash() {
        ServerRegistration server = register(url(keyedBackend, "/api/v1/mcp/"),
            new ServerCredential(AUTHORIZATION, BEARER));
        connected(server);
        assertNull(call(server).error());

        assertTrue(keyed.stream().allMatch(s -> "/api/v1/mcp/".equals(s.path())), keyed.toString());
    }

    @Test
    void changingTheCredentialReRegistersTheServerWithTheNewOne() {
        ServerRegistration first = register(url(keyedBackend, ""), new ServerCredential(AUTHORIZATION, BEARER));
        connected(first);
        keyed.clear();

        ServerRegistration second = register(url(keyedBackend, ""),
            new ServerCredential(AUTHORIZATION, "Bearer rotated"));
        connected(second);
        assertNull(call(second).error());

        assertFalse(keyed.isEmpty());
        assertTrue(keyed.stream().allMatch(s -> "Bearer rotated".equals(s.authorization())));
    }

    @Test
    void theSameCredentialDoesNotReRegisterTheServer() {
        ServerRegistration first = register(url(keyedBackend, ""), new ServerCredential(AUTHORIZATION, BEARER));
        connected(first);
        ServerRegistration again = register(url(keyedBackend, ""), new ServerCredential(AUTHORIZATION, BEARER));

        assertEquals(first.id(), again.id());
    }

    @Test
    void theValueIsNeverLoggedListedOrPrinted() throws IOException {
        ServerCredential credential = new ServerCredential(AUTHORIZATION, BEARER);
        ServerRegistration server = register(url(keyedBackend, ""), credential);
        connected(server);
        call(server);
        register(url(keyedBackend, ""), credential);
        RegisterServerRequest request = mapper.readValue(
            "{\"name\":\"keyed\",\"url\":\"http://x\",\"auth\":{\"header\":\"Authorization\",\"value\":\""
                + BEARER + "\"}}", RegisterServerRequest.class);

        List<String> everything = new ArrayList<>();
        logs.list.forEach(e -> {
            everything.add(e.getFormattedMessage());
            everything.add(String.valueOf(e.getThrowableProxy()));
            everything.add(String.valueOf(e.getArgumentArray() == null ? "" : List.of(e.getArgumentArray())));
        });
        everything.add(mapper.writeValueAsString(manager.listServers()));
        everything.add(mapper.writeValueAsString(manager.getServer(server.id())));
        everything.add(credential.toString());
        everything.add(request.toString());

        assertFalse(logs.list.isEmpty());
        assertEquals(BEARER, request.auth().value(), "the request carries it to the manager");
        everything.forEach(text -> assertFalse(text.contains(SECRET), "found the token in: " + text));
    }

    @Test
    void aCredentialThatCouldSplitAHeaderIsRefused() {
        assertThrows(IllegalArgumentException.class, () -> new ServerCredential(AUTHORIZATION, "a\r\nX-Evil: 1"));
        assertThrows(IllegalArgumentException.class, () -> new ServerCredential("Bad Header", "v"));
        assertThrows(IllegalArgumentException.class, () -> new ServerCredential(AUTHORIZATION, " "));
        assertThrows(IllegalArgumentException.class, () -> new ServerCredential(null, "v"));
    }

    private ServerRegistration register(String url, ServerCredential credential) {
        return manager.registerServer(KEYED, url, HTTP, List.of(), credential);
    }

    private static String url(HttpServer server, String path) {
        return "http://127.0.0.1:" + server.getAddress().getPort() + path;
    }

    private void connected(ServerRegistration server) {
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
    }

    private McpMessage call(ServerRegistration server) {
        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(7, "tools/call", Map.of("name", "ping", "arguments", Map.of())))
            .block(Duration.ofSeconds(30));
        assertNotNull(reply);
        return reply;
    }

    private HttpServer backend(List<Seen> into) throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> receive(exchange, into));
        server.setExecutor(Executors.newCachedThreadPool());
        server.start();
        return server;
    }

    private void receive(HttpExchange exchange, List<Seen> into) throws IOException {
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        String method = request.path("method").asText();
        into.add(new Seen(method, exchange.getRequestURI().getPath(),
            exchange.getRequestHeaders().getFirst(AUTHORIZATION), exchange.getRequestHeaders().getFirst("X-Api-Key"),
            exchange.getRequestHeaders().getFirst("Mcp-Session-Id")));
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
                Map.of("name", "ping", "description", "ping", "inputSchema", Map.of("type", "object"))));
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "ok")));
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
