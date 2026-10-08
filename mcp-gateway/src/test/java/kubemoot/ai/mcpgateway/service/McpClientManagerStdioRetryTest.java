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
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The retry rules of a stdio (mcp-bridge) server: a reply that never arrives is sent once
 * more on a new session, a reset after the request was sent is not replayed, a server that
 * cannot be reached answers with "Server not reachable", and a retry is never repeated.
 */
class McpClientManagerStdioRetryTest {

    private static final String SERVER = "bridge-mcp";

    private final ObjectMapper mapper = new ObjectMapper();
    private final Map<String, Stream> streams = new ConcurrentHashMap<>();
    private final AtomicInteger toolCalls = new AtomicInteger();
    private final AtomicInteger silentCalls = new AtomicInteger();
    private final AtomicInteger resetCalls = new AtomicInteger();
    private McpClientManager manager;
    private HttpServer backend;
    private int port;
    private ServerRegistration server;

    @BeforeEach
    void start() throws IOException {
        manager = new McpClientManager(new McpGatewayProperties(), WebClient.builder(), mapper);
        manager.replyTimeout = Duration.ofSeconds(1);
        backend = startBackend(0);
        port = backend.getAddress().getPort();
        server = manager.registerServer(SERVER, "http://127.0.0.1:" + port, "stdio");
        awaitTrue("connected", () -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
    }

    @AfterEach
    void stop() {
        manager.shutdown();
        stopBackend();
    }

    @Test
    void aReplyThatNeverArrivesIsSentAgainOnceOnANewSession() {
        silentCalls.set(1);

        McpMessage reply = call();

        assertNull(reply.error(), "the second attempt is answered");
        assertEquals(2, toolCalls.get(), "sent once more, not more");
    }

    @Test
    void aSecondTimeoutIsReturnedAndNotRetriedAgain() {
        silentCalls.set(Integer.MAX_VALUE);

        McpMessage reply = call();

        assertNotNull(reply.error());
        assertTrue(reply.error().message().startsWith("Internal error"), reply.error().message());
        assertEquals(2, toolCalls.get(), "one retry only");
    }

    @Test
    void aResetAfterTheRequestWasSentIsNotReplayed() {
        resetCalls.set(Integer.MAX_VALUE);

        McpMessage reply = call();

        assertNotNull(reply.error());
        assertTrue(reply.error().message().startsWith("Internal error"), reply.error().message());
        assertEquals(1, toolCalls.get(), "a tool call never runs twice");
        assertEquals(ServerStatus.ERROR, manager.getServer(server.id()).status());
    }

    @Test
    void aServerThatRefusesTheConnectionAnswersServerNotReachableThenRecovers() throws IOException {
        stopBackend();
        awaitTrue("the lost stream is noticed", () -> manager.getServer(server.id()).status() != ServerStatus.CONNECTED);

        McpMessage down = call();

        assertNotNull(down.error());
        assertTrue(down.error().message().startsWith("Server not reachable"), down.error().message());
        assertEquals(0, toolCalls.get(), "nothing reached the server");

        backend = startBackend(port);
        McpMessage up = call();

        assertNull(up.error(), "the next request connects and is answered");
        assertEquals(1, toolCalls.get());
    }

    private McpMessage call() {
        McpMessage reply = manager.forwardRequest(server.id(),
                McpMessage.request(5, "tools/call", Map.of("name", "pods_list", "arguments", Map.of())))
            .block(Duration.ofSeconds(60));
        assertNotNull(reply);
        return reply;
    }

    private HttpServer startBackend(int bindPort) throws IOException {
        HttpServer http = HttpServer.create(new InetSocketAddress("127.0.0.1", bindPort), 0);
        http.createContext("/sse", this::openStream);
        http.createContext("/message", this::receive);
        http.setExecutor(Executors.newCachedThreadPool());
        http.start();
        return http;
    }

    private void stopBackend() {
        streams.values().forEach(Stream::close);
        streams.clear();
        backend.stop(0);
    }

    private void openStream(HttpExchange exchange) throws IOException {
        String session = UUID.randomUUID().toString();
        exchange.getResponseHeaders().add("Content-Type", "text/event-stream");
        exchange.sendResponseHeaders(200, 0);
        Stream stream = new Stream(exchange);
        streams.put(session, stream);
        stream.write("event: endpoint\ndata: /message?sessionId=" + session + "\n\n");
        stream.awaitClose();
    }

    private void receive(HttpExchange exchange) throws IOException {
        String query = exchange.getRequestURI().getQuery();
        Stream stream = streams.get(query == null ? "" : query.substring(query.indexOf('=') + 1));
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        if (stream == null) {
            exchange.sendResponseHeaders(503, -1);
            exchange.close();
            return;
        }
        boolean toolCall = "tools/call".equals(request.path("method").asText());
        if (toolCall) {
            toolCalls.incrementAndGet();
            if (resetCalls.getAndUpdate(n -> Math.max(0, n - 1)) > 0) {
                exchange.close();
                return;
            }
        }
        exchange.sendResponseHeaders(202, -1);
        exchange.close();
        JsonNode id = request.get("id");
        boolean silent = toolCall && silentCalls.getAndUpdate(n -> Math.max(0, n - 1)) > 0;
        if (id != null && !id.isNull() && !silent) {
            stream.write("event: message\ndata: " + reply(id, request.path("method").asText()) + "\n\n");
        }
    }

    private String reply(JsonNode id, String method) throws IOException {
        Object result = switch (method) {
            case "initialize" -> Map.of("protocolVersion", "2024-11-05", "capabilities", Map.of(),
                "serverInfo", Map.of("name", SERVER, "version", "1"));
            case "tools/list" -> Map.of("tools", List.of(
                Map.of("name", "pods_list", "description", "pods", "inputSchema", Map.of("type", "object"))));
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "ok")));
        };
        return mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id, "result", result));
    }

    private static void awaitTrue(String what, BooleanSupplier condition) {
        Boolean met = Mono.fromSupplier(condition::getAsBoolean)
            .filter(Boolean::booleanValue)
            .repeatWhenEmpty(300, attempts -> attempts.delayElements(Duration.ofMillis(50)))
            .block(Duration.ofSeconds(20));
        assertTrue(Boolean.TRUE.equals(met), "timed out waiting for: " + what);
    }

    /** One open SSE response; writes are serialized, close ends the response. */
    private static final class Stream {
        private final HttpExchange exchange;
        private final OutputStream out;
        private final CountDownLatch closed = new CountDownLatch(1);

        Stream(HttpExchange exchange) {
            this.exchange = exchange;
            this.out = exchange.getResponseBody();
        }

        synchronized void write(String text) {
            if (closed.getCount() == 0) {
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
            if (closed.getCount() == 0) {
                return;
            }
            closed.countDown();
            exchange.close();
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
