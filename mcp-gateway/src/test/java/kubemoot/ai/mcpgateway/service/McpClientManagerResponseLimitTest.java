package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import kubemoot.ai.mcpgateway.config.McpConfig;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ServerRegistration.ServerStatus;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.boot.context.properties.source.MapConfigurationPropertySource;
import org.springframework.util.unit.DataSize;
import reactor.core.publisher.Mono;
import reactor.netty.resources.ConnectionProvider;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A tool reply is read whole up to mcp.gateway.max-response-size. At the limit it is delivered;
 * over it the caller gets a tool error naming the limit (and the size when the server declared
 * one), never a cut or garbled result.
 */
class McpClientManagerResponseLimitTest {

    private static final int LIMIT = 16 * 1024;
    private static final String SERVER = "limit-server";

    private final ObjectMapper mapper = new ObjectMapper().findAndRegisterModules();
    private McpClientManager manager;
    private ConnectionProvider provider;
    private HttpServer backend;
    private volatile int callBodyBytes;
    private volatile boolean chunked;

    @AfterEach
    void stop() {
        if (manager != null) {
            manager.shutdown();
        }
        if (provider != null) {
            provider.dispose();
        }
        if (backend != null) {
            backend.stop(0);
        }
    }

    @Test
    void defaultCoversRealToolResults() {
        long bytes = new McpGatewayProperties().getMaxResponseSize().toBytes();

        assertEquals(4L * 1024 * 1024, bytes);
        assertTrue(bytes > 1024 * 1024, "get_targets is over 1 MB");
    }

    @Test
    void theSettingBindsFromConfiguration() {
        var source = new MapConfigurationPropertySource(Map.of("mcp.gateway.max-response-size", "8MB"));

        var bound = new Binder(source).bind("mcp.gateway", McpGatewayProperties.class).get();

        assertEquals(8 * 1024 * 1024, bound.maxResponseBytes());
    }

    @Test
    void theSettingBindsFromTheEnvironmentSpelling() {
        var source = new MapConfigurationPropertySource(Map.of("mcp.gateway.maxresponsesize", "2MB"));

        var bound = new Binder(source).bind("mcp.gateway", McpGatewayProperties.class).get();

        assertEquals(2 * 1024 * 1024, bound.maxResponseBytes());
    }

    @Test
    void aSizeThatIsNotPositiveFailsStartupNamingTheKey() {
        var properties = new McpGatewayProperties();
        properties.setMaxResponseSize(DataSize.ofBytes(0));

        var error = assertThrows(IllegalStateException.class, properties::maxResponseBytes);

        assertTrue(error.getMessage().contains("mcp.gateway.max-response-size"), error.getMessage());
    }

    @Test
    void aReplyUnderTheLimitIsDelivered() throws IOException {
        start(false);
        callBodyBytes = LIMIT - 1;

        McpMessage reply = call();

        assertNull(reply.error(), String.valueOf(reply.error()));
    }

    @Test
    void aReplyExactlyAtTheLimitIsDelivered() throws IOException {
        start(false);
        callBodyBytes = LIMIT;

        McpMessage reply = call();

        assertNull(reply.error(), String.valueOf(reply.error()));
    }

    @Test
    void aReplyOverTheLimitWithADeclaredLengthIsAToolErrorNamingSizeAndLimit() throws IOException {
        start(false);
        callBodyBytes = LIMIT + 1;

        McpMessage reply = call();

        assertNotNull(reply.error());
        String message = String.valueOf(reply.error());
        assertTrue(message.contains("was " + (LIMIT + 1) + " bytes"), message);
        assertTrue(message.contains("limit of " + LIMIT + " bytes"), message);
        assertTrue(message.contains("mcp.gateway.max-response-size"), message);
        assertTrue(message.contains(SERVER), message);
    }

    @Test
    void aChunkedReplyOverTheLimitIsAToolErrorNamingTheLimit() throws IOException {
        start(true);
        callBodyBytes = LIMIT * 3;

        McpMessage reply = call();

        assertNotNull(reply.error());
        String message = String.valueOf(reply.error());
        assertTrue(message.contains("limit of " + LIMIT + " bytes"), message);
        assertTrue(message.contains("not delivered"), message);
        assertFalse(message.contains("Exceeded limit on max bytes to buffer"), message);
    }

    @Test
    void theMessageNamesAKnownSizeAndTheLimit() {
        String message = ResponseLimit.message("proxmox", 600_000, 262_144);

        assertEquals("The tool result from proxmox was 600000 bytes, over the gateway limit of 262144 bytes "
            + "(mcp.gateway.max-response-size) and was not delivered. "
            + "Ask for less: filter, select specific fields, or request a shorter range.", message);
    }

    @Test
    void anUnknownSizeIsSaidSoInTheMessage() {
        String message = ResponseLimit.message("proxmox", -1, 262_144);

        assertTrue(message.contains("full size is unknown"), message);
        assertTrue(message.contains("262144 bytes"), message);
    }

    private void start(boolean chunkedReplies) throws IOException {
        chunked = chunkedReplies;
        var properties = new McpGatewayProperties();
        properties.setMaxResponseSize(DataSize.ofBytes(LIMIT));
        var config = new McpConfig(properties);
        provider = config.mcpConnectionProvider();
        manager = new McpClientManager(properties, config.webClientBuilder(provider), mapper);
        backend = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        backend.createContext("/", this::receive);
        backend.setExecutor(Executors.newCachedThreadPool());
        backend.start();
    }

    private McpMessage call() {
        ServerRegistration server = manager.registerServer(SERVER,
            "http://127.0.0.1:" + backend.getAddress().getPort() + "/mcp", "http");
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(7, "tools/call", Map.of("name", "big", "arguments", Map.of())))
            .block(Duration.ofSeconds(30));
        assertNotNull(reply);
        return reply;
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
        String json = "tools/call".equals(method) ? paddedTo(callBodyBytes, id) : json(id, resultFor(method));
        byte[] bytes = json.getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().add("Content-Type", "application/json");
        boolean chunk = chunked && "tools/call".equals(method);
        exchange.sendResponseHeaders(200, chunk ? 0 : bytes.length);
        try (OutputStream out = exchange.getResponseBody()) {
            out.write(bytes);
        }
    }

    /** A tools/call reply whose body is exactly the given number of bytes. */
    private String paddedTo(int size, JsonNode id) throws IOException {
        int base = json(id, Map.of("content", List.of(Map.of("type", "text", "text", "")))).length();
        return json(id, Map.of("content", List.of(Map.of("type", "text", "text", "x".repeat(size - base)))));
    }

    private String json(JsonNode id, Object result) throws IOException {
        return mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id, "result", result));
    }

    private Object resultFor(String method) {
        return switch (method) {
            case "initialize" -> Map.of("protocolVersion", "2025-06-18",
                "capabilities", Map.of("tools", Map.of("listChanged", false)),
                "serverInfo", Map.of("name", "big", "version", "1"));
            default -> Map.of("tools", List.of(Map.of("name", "big", "description", "big",
                "inputSchema", Map.of("type", "object"))));
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
