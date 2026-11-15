package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ToolOverride;
import kubemoot.ai.mcpgateway.model.ToolOverride.ArgumentRule;
import kubemoot.ai.mcpgateway.model.ToolOverride.ParameterOverride;
import io.micrometer.core.instrument.Metrics;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
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

/** A call that breaks an argument rule is answered by the gateway and never reaches the server. */
class McpClientManagerArgumentRulesTest {

    private static final String PARAM = "fieldSelector";
    private static final String MESSAGE = "ASSERT use field=value";
    private static final Map<String, Object> SCHEMA = Map.of("type", "object", "properties", Map.of(
        PARAM, Map.of("type", "string"), "limit", Map.of("type", "integer")));

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

    private static List<ToolOverride> overrides(String reject) {
        return List.of(new ToolOverride("resources_list", null, Map.of(PARAM,
            new ParameterOverride("d", List.of(new ArgumentRule(reject, MESSAGE))))));
    }

    @Test
    void aMatchingCallGetsTheMessageAsAToolErrorAndIsNotForwarded() {
        ServerRegistration server = registered(overrides("[<>]"));

        McpMessage reply = call(server, Map.of(PARAM, "status.capacity.memory>4"));

        assertEquals(42, reply.id());
        Map<?, ?> result = (Map<?, ?>) reply.result();
        assertEquals(Boolean.TRUE, result.get("isError"));
        Map<?, ?> content = (Map<?, ?>) ((List<?>) result.get("content")).get(0);
        assertEquals(MESSAGE, content.get("text"));
        assertTrue(calls.isEmpty(), "the server must not see a refused call");
    }

    @Test
    void aNonMatchingAbsentOrNonStringArgumentIsForwarded() {
        ServerRegistration server = registered(overrides("[<>]"));

        call(server, Map.of(PARAM, "metadata.name=a"));
        call(server, Map.of("limit", 5));
        call(server, Map.of(PARAM, 7));

        assertEquals(3, calls.size());
    }

    @Test
    void aBadPatternFailsClosedForItsParameterOnly() {
        ServerRegistration server = registered(overrides("("));

        McpMessage refused = call(server, Map.of(PARAM, "((anything"));
        McpMessage other = call(server, Map.of("limit", 3));

        assertEquals(Boolean.TRUE, ((Map<?, ?>) refused.result()).get("isError"));
        assertTrue(((Map<?, ?>) other.result()).get("isError") == null);
        assertEquals(1, calls.size(), "only the call without the broken parameter reached the server");
    }

    @Test
    void rulesAreCompiledOnlyWhenTheOverridesChange() {
        SimpleMeterRegistry registry = new SimpleMeterRegistry();
        Metrics.addRegistry(registry);
        try {
            registered(overrides("("));
            manager.registerServer("k8s", url(), "http", overrides("("));
            manager.registerServer("k8s", url(), "http", overrides("("));
            assertEquals(1.0, registry.get("mcpgateway.argument_rules.invalid").counter().count());
            manager.registerServer("k8s", url(), "http", overrides("[a"));
            assertEquals(2.0, registry.get("mcpgateway.argument_rules.invalid").counter().count());
        } finally {
            Metrics.removeRegistry(registry);
        }
    }

    @Test
    void reRegisteringReplacesTheRules() {
        ServerRegistration server = registered(overrides("[<>]"));
        manager.registerServer("k8s", url(), "http", overrides("zzz"));

        call(server, Map.of(PARAM, "a>b"));

        assertEquals(1, calls.size(), "the old rule is gone, the call is forwarded");
    }

    private String url() {
        return "http://127.0.0.1:" + backend.getAddress().getPort();
    }

    private ServerRegistration registered(List<ToolOverride> overrides) {
        ServerRegistration server = manager.registerServer("k8s", url(), "http", overrides);
        Boolean listed = Mono.fromSupplier(() -> !manager.getToolsForServer(server.id()).isEmpty())
            .filter(Boolean::booleanValue)
            .repeatWhenEmpty(300, attempts -> attempts.delayElements(Duration.ofMillis(50)))
            .block(Duration.ofSeconds(20));
        assertTrue(Boolean.TRUE.equals(listed), "timed out waiting for the tool list");
        return server;
    }

    private McpMessage call(ServerRegistration server, Map<String, Object> arguments) {
        McpMessage reply = manager.forwardRequest(server.id(), McpMessage.request(42, "tools/call",
            Map.of("name", "resources_list", "arguments", arguments))).block(Duration.ofSeconds(30));
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
                Map.of("name", "resources_list", "description", "list", "inputSchema", SCHEMA)));
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "ok")));
        };
    }
}
