package kubemoot.ai.mcpgateway.service;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.core.read.ListAppender;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import com.sun.net.httpserver.HttpsConfigurator;
import com.sun.net.httpserver.HttpsServer;
import io.netty.handler.ssl.SslContext;
import io.netty.handler.ssl.SslContextBuilder;
import io.netty.handler.ssl.util.InsecureTrustManagerFactory;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.controller.ToolController;
import kubemoot.ai.mcpgateway.controller.ToolController.ToolCallRequest;
import kubemoot.ai.mcpgateway.controller.ToolController.ToolCallResponse;
import kubemoot.ai.mcpgateway.model.CallerCredentialConfig;
import kubemoot.ai.mcpgateway.model.CallerCredentialConfig.Mode;
import kubemoot.ai.mcpgateway.model.ServerCredential;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ServerRegistration.ServerStatus;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.slf4j.LoggerFactory;
import org.springframework.http.client.reactive.ReactorClientHttpConnector;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;
import reactor.netty.http.client.HttpClient;

import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.KeyStore;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.Executors;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A server that takes a connected account is called with the token the crew's gateway leases for
 * the conversation's owner, only at an https address on a host the account declares, once, and
 * never with the credential it was registered with. The discussion gateway and the upstream are
 * fakes; the upstream speaks TLS with a certificate made for the test.
 */
class CallerAccountCredentialTest {

    private static final String LISTING_SECRET = "LISTING-SECRET-9f";
    private static final String SA_TOKEN = "sa-token-projected-77";
    private static final String CANARY = "ACCOUNT-CANARY-TOKEN-5";
    private static final String STORE_PASSWORD = "changeit";
    private static final String ACCOUNT = "library";
    private static final String CONVERSATION = "conv-1";
    private static final String LEASE_PATH = "/internal/v1/lease";
    private static final String JSON = "application/json";

    /** One request the upstream received. */
    private record Seen(String rpcMethod, String authorization, JsonNode body) { }

    /** One lease request the discussion gateway fake received. */
    private record Leased(String authorization, JsonNode body) { }

    private final ObjectMapper mapper = new ObjectMapper().findAndRegisterModules();
    private final List<Seen> seen = new CopyOnWriteArrayList<>();
    private final List<Leased> leases = new CopyOnWriteArrayList<>();
    private final Map<String, Integer> leaseStatus = new ConcurrentHashMap<>();
    private volatile List<String> leasedHosts = List.of("localhost");
    private final ListAppender<ILoggingEvent> logs = new ListAppender<>();
    private final Logger root = (Logger) LoggerFactory.getLogger(Logger.ROOT_LOGGER_NAME);
    private Level rootLevel;
    private McpClientManager manager;
    private ToolController controller;
    private HttpServer upstream;
    private HttpServer gateway;
    private Path directory;

    @BeforeEach
    void start(@TempDir Path dir) throws Exception {
        directory = dir;
        Path tokenFile = dir.resolve("token");
        Files.writeString(tokenFile, SA_TOKEN + "\n");
        McpGatewayProperties properties = new McpGatewayProperties();
        properties.getCredentialBroker().setTokenFile(tokenFile.toString());
        gateway = discussionGateway();
        properties.getCredentialBroker().setUrl("http://127.0.0.1:" + gateway.getAddress().getPort());
        SslContext trustAll = SslContextBuilder.forClient().trustManager(InsecureTrustManagerFactory.INSTANCE).build();
        HttpClient trusting = HttpClient.create().secure(spec -> spec.sslContext(trustAll));
        manager = new McpClientManager(properties,
            WebClient.builder().clientConnector(new ReactorClientHttpConnector(trusting)), mapper);
        controller = new ToolController(manager, null);
        rootLevel = root.getLevel();
        root.setLevel(Level.TRACE);
        logs.start();
        root.addAppender(logs);
        leaseStatus.put(CONVERSATION, 200);
    }

    @AfterEach
    void stop() {
        root.detachAppender(logs);
        root.setLevel(rootLevel);
        manager.shutdown();
        gateway.stop(0);
        if (upstream != null) {
            upstream.stop(0);
        }
    }

    @Test
    void aLeaseNamesTheAccountAndTheConversationAndNoPerson() throws Exception {
        registerOverHttps(config());

        ToolCallResponse reply = call();

        assertFalse(reply.isError(), reply.content());
        assertEquals(1, leases.size());
        assertEquals("Bearer " + SA_TOKEN, leases.get(0).authorization());
        JsonNode body = leases.get(0).body();
        assertEquals(ACCOUNT, body.path("account").asText());
        assertEquals(CONVERSATION, body.path("conversationId").asText());
        assertTrue(body.path("principalId").isMissingNode(), "the caller's claim of who asked is not sent");
        assertTrue(body.path("verifier").isMissingNode());
    }

    @Test
    void theTokenGoesOnTheCallToADeclaredHostAndNeverTheRegisteredSecret() throws Exception {
        registerOverHttps(config());

        ToolCallResponse reply = call();

        assertEquals("answer", reply.content());
        List<Seen> calls = rpc("tools/call");
        assertEquals(1, calls.size());
        assertEquals("Bearer " + CANARY, calls.get(0).authorization());
        assertTrue(seen.stream().filter(s -> !"tools/list".equals(s.rpcMethod()) && !"initialize".equals(s.rpcMethod()))
            .noneMatch(s -> String.valueOf(s.authorization()).contains(LISTING_SECRET)),
            "the registered secret is never sent on a call");
    }

    @Test
    void theTokenIsLeasedForEveryCallAndNotKept() throws Exception {
        registerOverHttps(config());

        call();
        call();

        assertEquals(2, leases.size());
    }

    @Test
    void aHostThatIsNotDeclaredNeverSeesTheToken() throws Exception {
        leasedHosts = List.of("somewhere-else.example.org");
        registerOverHttps(config());

        ToolCallResponse reply = call();

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("Example Library"), reply.content());
        assertTrue(rpc("tools/call").isEmpty());
        assertTrue(seen.stream().noneMatch(s -> String.valueOf(s.authorization()).contains(CANARY)));
    }

    @Test
    void anAddressThatIsNotHttpsNeverSeesTheTokenEvenOnADeclaredHost() throws Exception {
        leasedHosts = List.of("127.0.0.1");
        registerOverHttp(config());

        ToolCallResponse reply = call();

        assertTrue(reply.isError());
        assertTrue(rpc("tools/call").isEmpty());
        assertTrue(seen.stream().noneMatch(s -> String.valueOf(s.authorization()).contains(CANARY)));
    }

    @Test
    void aFallbackOffTheDeclaredHostsIsNotCalledWithTheToken() throws Exception {
        CallerCredentialConfig withFallback = new CallerCredentialConfig(Mode.REQUIRED, null, null, "Example Library",
            List.of("https://elsewhere.example.org/mcp"), false, ACCOUNT);
        registerOverHttps(withFallback);
        forbidCalls = true;

        ToolCallResponse reply = call();

        assertTrue(reply.isError());
        assertTrue(seen.stream().filter(s -> "tools/call".equals(s.rpcMethod()))
            .allMatch(s -> String.valueOf(s.authorization()).contains(CANARY)), "only the declared host was called");
    }

    @Test
    void aLeaseWithNoHostsIsRefusedBeforeAnyCall() throws Exception {
        leasedHosts = List.of();
        registerOverHttps(config());

        ToolCallResponse reply = call();

        assertTrue(reply.isError());
        assertTrue(rpc("tools/call").isEmpty());
    }

    @Test
    void eachReasonSaysWhichAccountAndNothingIsCalled() throws Exception {
        registerOverHttps(config());
        Map<Integer, String> expected = Map.of(
            404, "Connect your Example Library account and ask again",
            409, "Connect your Example Library account again",
            410, "Example Library sign-in expired",
            503, "Sign-in is unavailable");
        for (Map.Entry<Integer, String> e : expected.entrySet()) {
            leaseStatus.put(CONVERSATION, e.getKey());

            ToolCallResponse reply = call();

            assertTrue(reply.isError(), "status " + e.getKey());
            assertTrue(reply.content().contains(e.getValue()), e.getKey() + ": " + reply.content());
        }
        assertTrue(rpc("tools/call").isEmpty(), "a refused lease never reaches the server");
    }

    @Test
    void theTokenNeverAppearsInLogsResultsOrExceptionText() throws Exception {
        registerOverHttps(config());
        List<String> everything = new ArrayList<>();
        everything.add(call().content());
        forbidCalls = true;
        everything.add(call().content());
        leaseStatus.put(CONVERSATION, 410);
        everything.add(call().content());

        root.detachAppender(logs);
        logs.list.forEach(e -> {
            everything.add(e.getFormattedMessage());
            everything.add(String.valueOf(e.getThrowableProxy()));
            everything.add(String.valueOf(e.getArgumentArray() == null ? "" : List.of(e.getArgumentArray())));
        });
        assertFalse(logs.list.isEmpty());
        everything.forEach(text -> assertFalse(text.contains(CANARY), "found the token in: " + text));
        everything.forEach(text -> assertFalse(text.contains(SA_TOKEN), "found the service account token in: " + text));
    }

    @Test
    void tokenHostsPermitOnlyAnExactHostOverHttps() {
        List<String> hosts = List.of("api.example.org");
        assertTrue(TokenHosts.permit("https://api.example.org/mcp", hosts));
        assertTrue(TokenHosts.permit("https://API.Example.org:8443/mcp", hosts));
        assertFalse(TokenHosts.permit("http://api.example.org/mcp", hosts));
        assertFalse(TokenHosts.permit("https://evil.api.example.org/mcp", hosts));
        assertFalse(TokenHosts.permit("https://api.example.org.evil.net/mcp", hosts));
        assertFalse(TokenHosts.permit("https://example.org/mcp", hosts));
        assertFalse(TokenHosts.permit("https://user@evil.net@api.example.org/mcp", hosts));
        assertFalse(TokenHosts.permit("ftp://api.example.org/mcp", hosts));
        assertFalse(TokenHosts.permit("not a url", hosts));
        assertFalse(TokenHosts.permit(null, hosts));
        assertFalse(TokenHosts.permit("https://api.example.org/mcp", List.of()));
        assertFalse(TokenHosts.permit("https://api.example.org/mcp", null));
    }

    // ---- helpers ------------------------------------------------------------------------------

    private volatile boolean forbidCalls;

    private CallerCredentialConfig config() {
        return new CallerCredentialConfig(Mode.REQUIRED, null, null, "Example Library", List.of(), false, ACCOUNT);
    }

    private ToolCallResponse call() {
        ToolCallResponse reply = controller.callTool("ping", new ToolCallRequest(Map.of(),
            Map.of("conversationId", CONVERSATION, "principalId", "claimed-by-the-caller"))).block(Duration.ofSeconds(30));
        assertNotNull(reply);
        return reply;
    }

    private List<Seen> rpc(String method) {
        return seen.stream().filter(s -> method.equals(s.rpcMethod())).toList();
    }

    private void registerOverHttps(CallerCredentialConfig config) throws Exception {
        HttpsServer server = HttpsServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.setHttpsConfigurator(new HttpsConfigurator(serverContext()));
        serve(server);
        register("https://localhost:" + server.getAddress().getPort() + "/mcp", config);
    }

    private void registerOverHttp(CallerCredentialConfig config) throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        serve(server);
        register("http://127.0.0.1:" + server.getAddress().getPort() + "/mcp", config);
    }

    private void serve(HttpServer server) {
        server.createContext("/", this::answer);
        server.setExecutor(Executors.newCachedThreadPool());
        server.start();
        upstream = server;
    }

    private void register(String url, CallerCredentialConfig config) {
        ServerRegistration server = manager.registerServer("library", url, "http", List.of(),
            new ServerCredential("Authorization", "Bearer " + LISTING_SECRET), config);
        awaitTrue(() -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
        seen.clear();
    }

    private SSLContext serverContext() throws Exception {
        Path store = directory.resolve("server.p12");
        Process keytool = new ProcessBuilder(
            Path.of(System.getProperty("java.home"), "bin", "keytool").toString(), "-genkeypair", "-alias", "t",
            "-keyalg", "RSA", "-keysize", "2048", "-dname", "CN=localhost", "-ext", "san=dns:localhost,ip:127.0.0.1",
            "-validity", "2", "-storetype", "PKCS12", "-keystore", store.toString(),
            "-storepass", STORE_PASSWORD, "-keypass", STORE_PASSWORD)
            .redirectErrorStream(true).start();
        keytool.getInputStream().readAllBytes();
        assertEquals(0, keytool.waitFor(), "the test certificate could not be made");
        KeyStore keys = KeyStore.getInstance("PKCS12");
        try (var in = Files.newInputStream(store)) {
            keys.load(in, STORE_PASSWORD.toCharArray());
        }
        KeyManagerFactory factory = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
        factory.init(keys, STORE_PASSWORD.toCharArray());
        SSLContext context = SSLContext.getInstance("TLS");
        context.init(factory.getKeyManagers(), null, null);
        return context;
    }

    private HttpServer discussionGateway() throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext(LEASE_PATH, exchange -> {
            JsonNode body = mapper.readTree(exchange.getRequestBody());
            leases.add(new Leased(exchange.getRequestHeaders().getFirst("Authorization"), body));
            int status = leaseStatus.getOrDefault(body.path("conversationId").asText(), 404);
            String json = status == 200 ? mapper.writeValueAsString(Map.of("token", CANARY,
                "expiresAt", Instant.now().plusSeconds(600).toString(), "tokenHosts", leasedHosts)) : "{}";
            reply(exchange, status, JSON, json);
        });
        server.setExecutor(Executors.newCachedThreadPool());
        server.start();
        return server;
    }

    private void answer(HttpExchange exchange) throws IOException {
        String auth = exchange.getRequestHeaders().getFirst("Authorization");
        if ("DELETE".equals(exchange.getRequestMethod())) {
            reply(exchange, 200, JSON, "{}");
            return;
        }
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        String method = request.path("method").asText();
        seen.add(new Seen(method, auth, request));
        JsonNode id = request.get("id");
        if (id == null || id.isNull()) {
            reply(exchange, 202, JSON, "");
        } else if (forbidCalls && "tools/call".equals(method)) {
            reply(exchange, 403, JSON, mapper.writeValueAsString(Map.of("message", "No access for " + auth)));
        } else {
            exchange.getResponseHeaders().add("Mcp-Session-Id", "s-1");
            reply(exchange, 200, JSON, mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id,
                "result", resultFor(method))));
        }
    }

    private static Object resultFor(String method) {
        return switch (method) {
            case "initialize" -> Map.of("protocolVersion", "2024-11-05",
                "capabilities", Map.of("tools", Map.of()), "serverInfo", Map.of("name", "fake", "version", "1"));
            case "tools/list" -> Map.of("tools", List.of(
                Map.of("name", "ping", "description", "ping", "inputSchema", Map.of("type", "object"))));
            default -> Map.of("content", List.of(Map.of("type", "text", "text", "answer")));
        };
    }

    private static void reply(HttpExchange exchange, int status, String type, String body) throws IOException {
        byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
        exchange.getResponseHeaders().set("Content-Type", type);
        if (bytes.length == 0) {
            exchange.sendResponseHeaders(status, -1);
            exchange.close();
            return;
        }
        exchange.sendResponseHeaders(status, bytes.length);
        try (OutputStream out = exchange.getResponseBody()) {
            out.write(bytes);
        }
    }

    private static void awaitTrue(BooleanSupplier condition) {
        Boolean met = Mono.fromSupplier(condition::getAsBoolean)
            .filter(Boolean::booleanValue)
            .repeatWhenEmpty(300, attempts -> attempts.delayElements(Duration.ofMillis(50)))
            .block(Duration.ofSeconds(20));
        assertTrue(Boolean.TRUE.equals(met), "timed out waiting for the server to connect");
    }
}
