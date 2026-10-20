package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.config.McpConfig;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ServerRegistration.ServerStatus;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;
import reactor.netty.resources.ConnectionProvider;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.channels.ClosedChannelException;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The remote side of a pooled connection can close it while the gateway holds it idle. The
 * fake server here reads a request and closes the connection without answering, which is what
 * the gateway sees as a reset. initialize and tools/list are sent again once; tools/call is not.
 */
class McpClientManagerConnectionResetTest {

    private static final String SERVER = "reset-server";

    private final ObjectMapper mapper = new ObjectMapper().findAndRegisterModules();
    private final Map<String, AtomicInteger> requestsByMethod = new ConcurrentHashMap<>();
    private final Map<String, AtomicInteger> dropsLeft = new ConcurrentHashMap<>();
    private final List<Socket> sockets = new CopyOnWriteArrayList<>();
    private final AtomicInteger connectionsAccepted = new AtomicInteger();
    private ServerSocket backend;
    private McpClientManager manager;
    private ConnectionProvider provider;

    @BeforeEach
    void start() throws IOException {
        backend = new ServerSocket(0, 50, InetAddress.getLoopbackAddress());
        Thread.ofVirtual().start(this::acceptLoop);
    }

    @AfterEach
    void stop() throws IOException {
        if (manager != null) {
            manager.shutdown();
        }
        if (provider != null) {
            provider.dispose();
        }
        backend.close();
        sockets.forEach(s -> {
            try {
                s.close();
            } catch (IOException ignored) {
                // the socket is already closed
            }
        });
    }

    @Test
    void connectionLimitsDefaultBelowAHostedEdgeIdleTimeout() {
        var connections = new McpGatewayProperties().getConnections();

        assertEquals(Duration.ofSeconds(15), connections.getMaxIdleTime());
        assertEquals(Duration.ofSeconds(10), connections.getEvictInterval());
        assertEquals(Duration.ofMinutes(5), connections.getMaxLifeTime());
    }

    @Test
    void anIdleConnectionIsDroppedFromThePoolBeforeTheNextRequest() throws Exception {
        var properties = new McpGatewayProperties();
        properties.getConnections().setMaxIdleTime(Duration.ofMillis(300));
        properties.getConnections().setEvictInterval(Duration.ofMillis(100));
        build(properties);
        var server = register();
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED);
        int afterConnect = connectionsAccepted.get();

        Thread.sleep(1200);
        McpMessage reply = call(server);

        assertNull(reply.error(), String.valueOf(reply.error()));
        assertTrue(connectionsAccepted.get() > afterConnect,
            "the idle connection was evicted, so the call opened a new one");
        assertEquals(1, requests("tools/call"));
    }

    @Test
    void initializeIsSentAgainOnceAfterAReset() {
        dropsLeft.put("initialize", new AtomicInteger(1));
        build(new McpGatewayProperties());

        var server = register();

        awaitTrue("connected with tools", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        assertEquals(2, requests("initialize"));
    }

    @Test
    void toolsListIsSentAgainOnceAfterAReset() {
        dropsLeft.put("tools/list", new AtomicInteger(1));
        build(new McpGatewayProperties());

        var server = register();

        awaitTrue("connected with tools", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        assertEquals(2, requests("tools/list"));
    }

    @Test
    void aSecondResetInARowFailsTheConnect() throws Exception {
        dropsLeft.put("initialize", new AtomicInteger(2));
        build(new McpGatewayProperties());

        var server = register();

        awaitTrue("connect failed", () -> manager.getServer(server.id()).status() == ServerStatus.ERROR);
        Thread.sleep(300);
        assertEquals(2, requests("initialize"), "one send and one more, no third");
        assertTrue(manager.getToolsForServer(server.id()).isEmpty());
    }

    @Test
    void aToolCallIsNotSentAgainAfterAReset() {
        build(new McpGatewayProperties());
        var server = register();
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        dropsLeft.put("tools/call", new AtomicInteger(1));

        McpMessage reply = call(server);

        assertNotNull(reply.error(), "the reset is reported to the caller");
        assertEquals(1, requests("tools/call"), "a call that may have run is never sent twice");
    }

    @Test
    void onlyClosedConnectionFailuresCountAsResets() {
        var reset = new IOException("recvAddress(..) failed with error(-104): Connection reset by peer");

        assertTrue(McpClientManager.isConnectionReset(reset));
        assertTrue(McpClientManager.isConnectionReset(new RuntimeException("request failed", reset)));
        assertTrue(McpClientManager.isConnectionReset(new ClosedChannelException()));
        assertFalse(McpClientManager.isConnectionReset(new IOException("Connection refused")));
        assertFalse(McpClientManager.isConnectionReset(new IllegalStateException("bad reply")));
    }

    private void build(McpGatewayProperties properties) {
        var config = new McpConfig(properties);
        provider = config.mcpConnectionProvider();
        WebClient.Builder builder = config.webClientBuilder(provider);
        manager = new McpClientManager(properties, builder, mapper);
    }

    private ServerRegistration register() {
        return manager.registerServer(SERVER, "http://127.0.0.1:" + backend.getLocalPort() + "/mcp", "http");
    }

    private McpMessage call(ServerRegistration server) {
        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(7, "tools/call", Map.of("name", "search_content", "arguments", Map.of())))
            .block(Duration.ofSeconds(30));
        assertNotNull(reply);
        return reply;
    }

    private int requests(String method) {
        return requestsByMethod.getOrDefault(method, new AtomicInteger()).get();
    }

    private void acceptLoop() {
        while (!backend.isClosed()) {
            try {
                Socket socket = backend.accept();
                sockets.add(socket);
                connectionsAccepted.incrementAndGet();
                Thread.ofVirtual().start(() -> serve(socket));
            } catch (IOException e) {
                return;
            }
        }
    }

    /** Answers requests on one keep-alive connection until it is closed or a scripted drop closes it. */
    private void serve(Socket socket) {
        try (socket) {
            InputStream in = socket.getInputStream();
            OutputStream out = socket.getOutputStream();
            while (true) {
                String body = readRequestBody(in);
                if (body == null) {
                    return;
                }
                JsonNode request = mapper.readTree(body);
                String method = request.path("method").asText();
                requestsByMethod.computeIfAbsent(method, m -> new AtomicInteger()).incrementAndGet();
                if (shouldDrop(method)) {
                    return;
                }
                respond(out, request);
            }
        } catch (IOException e) {
            // the client closed the connection
        }
    }

    private boolean shouldDrop(String method) {
        AtomicInteger left = dropsLeft.get(method);
        return left != null && left.getAndUpdate(n -> Math.max(0, n - 1)) > 0;
    }

    private void respond(OutputStream out, JsonNode request) throws IOException {
        if (request.get("id") == null || request.get("id").isNull()) {
            out.write("HTTP/1.1 202 Accepted\r\nContent-Length: 0\r\n\r\n".getBytes(StandardCharsets.US_ASCII));
            out.flush();
            return;
        }
        Object result = switch (request.path("method").asText()) {
            case "initialize" -> Map.of("protocolVersion", "2025-06-18",
                "capabilities", Map.of("tools", Map.of("listChanged", false)),
                "serverInfo", Map.of("name", "fake", "version", "1"));
            case "tools/list" -> Map.of("tools", List.of(Map.of("name", "search_content",
                "description", "search", "inputSchema", Map.of("type", "object"))));
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "found it")));
        };
        byte[] payload = mapper.writeValueAsBytes(Map.of("jsonrpc", "2.0", "id", request.get("id"), "result", result));
        out.write(("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " + payload.length
            + "\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
        out.write(payload);
        out.flush();
    }

    /** Reads one HTTP request and returns its body, or null when the client closed the connection. */
    private static String readRequestBody(InputStream in) throws IOException {
        var head = new ByteArrayOutputStream();
        int last4 = 0;
        int b;
        while ((b = in.read()) != -1) {
            head.write(b);
            last4 = (last4 << 8) | b;
            if (last4 == 0x0D0A0D0A) {
                break;
            }
        }
        if (b == -1) {
            return null;
        }
        int length = 0;
        for (String line : head.toString(StandardCharsets.US_ASCII).split("\r\n")) {
            if (line.toLowerCase().startsWith("content-length:")) {
                length = Integer.parseInt(line.substring(line.indexOf(':') + 1).trim());
            }
        }
        return new String(in.readNBytes(length), StandardCharsets.UTF_8);
    }

    private static void awaitTrue(String what, BooleanSupplier condition) {
        Boolean met = Mono.fromSupplier(condition::getAsBoolean)
            .filter(Boolean::booleanValue)
            .repeatWhenEmpty(300, attempts -> attempts.delayElements(Duration.ofMillis(50)))
            .block(Duration.ofSeconds(20));
        assertTrue(Boolean.TRUE.equals(met), "timed out waiting for: " + what);
    }
}
