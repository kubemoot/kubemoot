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
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.Executors;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A server registered with caller-credential mode Required is called with the token of the person
 * who asked, leased for that one call, and never with the credential it was registered with. The
 * lease broker and the upstream servers are fakes.
 */
class CallerCredentialRequiredTest {

    private static final String LISTING_SECRET = "LISTING-SECRET-7c1d";
    private static final String SA_TOKEN = "sa-token-projected-42";
    private static final String CANARY = "CANARY-TOKEN-1";
    private static final String HTTP = "http";

    /** One request an upstream received. */
    private record Seen(String httpMethod, String rpcMethod, String authorization, String session, JsonNode body) { }

    /** One lease request the broker received. */
    private record Leased(String authorization, JsonNode body) { }

    private final ObjectMapper mapper = new ObjectMapper().findAndRegisterModules();
    private final List<Seen> expert = new CopyOnWriteArrayList<>();
    private final List<Seen> search = new CopyOnWriteArrayList<>();
    private final List<Leased> leases = new CopyOnWriteArrayList<>();
    /** Bearer values the expert upstream refuses with 403, and the body it refuses with. */
    private final Set<String> expertForbids = ConcurrentHashMap.newKeySet();
    private final Set<String> searchForbids = ConcurrentHashMap.newKeySet();
    private volatile boolean searchKnowsTool = true;
    private volatile int expertStatusOnCall = 200;
    /** conversationId to the broker's status and token. */
    private final Map<String, Integer> leaseStatus = new ConcurrentHashMap<>();
    private final Map<String, String> leaseToken = new ConcurrentHashMap<>();
    private final ListAppender<ILoggingEvent> logs = new ListAppender<>();
    private final Logger root = (Logger) LoggerFactory.getLogger(Logger.ROOT_LOGGER_NAME);
    private Level rootLevel;
    private McpClientManager manager;
    private ToolController controller;
    private HttpServer expertServer;
    private HttpServer searchServer;
    private HttpServer broker;
    private McpGatewayProperties properties;

    @BeforeEach
    void start(@TempDir Path dir) throws IOException {
        Path tokenFile = dir.resolve("token");
        Files.writeString(tokenFile, SA_TOKEN + "\n");
        properties = new McpGatewayProperties();
        properties.getCredentialBroker().setTokenFile(tokenFile.toString());
        expertServer = upstream(expert, expertForbids, true);
        searchServer = upstream(search, searchForbids, false);
        broker = broker();
        properties.getCredentialBroker().setUrl(base(broker));
        manager = new McpClientManager(properties, WebClient.builder(), mapper);
        controller = new ToolController(manager, null);
        rootLevel = root.getLevel();
        root.setLevel(Level.TRACE);
        logs.start();
        root.addAppender(logs);
        grant("conv-1", "ASKER-TOKEN-ONE");
        grant("conv-2", "ASKER-TOKEN-TWO");
    }

    @AfterEach
    void stop() {
        root.detachAppender(logs);
        root.setLevel(rootLevel);
        manager.shutdown();
        expertServer.stop(0);
        searchServer.stop(0);
        broker.stop(0);
    }

    // ---- the contract -------------------------------------------------------------------------

    @Test
    void aGrantMakesTheCallWithTheAskersTokenAndNeverTheRegisteredSecret() {
        register(required(List.of()));

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of("q", "x"));

        assertFalse(reply.isError(), reply.content());
        assertEquals("answer-from-expert", reply.content());
        List<Seen> calls = rpc(expert, "tools/call");
        assertEquals(1, calls.size());
        assertEquals("Bearer ASKER-TOKEN-ONE", calls.get(0).authorization());
        assertTrue(expert.stream().noneMatch(s -> String.valueOf(s.authorization()).contains(LISTING_SECRET)),
            "the registered secret is never sent on a call");
        assertTrue(rpc(listing, "tools/list").stream().anyMatch(s -> s.authorization().contains(LISTING_SECRET)),
            "the registered secret still lists the tools");
        assertEquals(1, leases.size());
        assertEquals("Bearer " + SA_TOKEN, leases.get(0).authorization());
        assertEquals("conv-1", leases.get(0).body().path("conversationId").asText());
        assertEquals("principal-a", leases.get(0).body().path("principalId").asText());
        assertEquals("oreilly", leases.get(0).body().path("verifier").asText());
    }

    @Test
    void aServerThatTakesAConnectedAccountSendsTheAccountAndThePrincipalButNoVerifier() {
        register(new CallerCredentialConfig(Mode.REQUIRED, base(broker) + "/internal/v1/lease", null, "Example Library",
            List.of(), false, "library"));

        call("conv-1", "principal-a", Map.of());

        assertEquals(1, leases.size());
        JsonNode body = leases.get(0).body();
        assertEquals("library", body.path("account").asText());
        assertTrue(body.path("verifier").isMissingNode());
        assertEquals("principal-a", body.path("principalId").asText());
        assertTrue(rpc(expert, "tools/call").isEmpty(), "a lease without token hosts is not used");
    }

    @Test
    void theLeaseIsAskedForOnEveryCallAndNotCached() {
        register(required(List.of()));

        call("conv-1", "principal-a", Map.of());
        call("conv-1", "principal-a", Map.of());

        assertEquals(2, leases.size());
    }

    @Test
    void noGrantRefusesWithoutCallingTheUpstream() {
        register(required(List.of()));
        leaseStatus.put("conv-1", 404);

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("Connect your O'Reilly account"), reply.content());
        assertTrue(rpc(expert, "tools/call").isEmpty());
        assertTrue(expert.stream().noneMatch(s -> s.authorization() != null && s.authorization().contains("ASKER")));
    }

    @Test
    void aPrincipalThatDoesNotOwnTheGrantIsRefusedLikeNoGrant() {
        register(required(List.of()));
        leaseStatus.put("conv-1", 403);

        ToolCallResponse reply = call("conv-1", "someone-else", Map.of());

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("Connect your O'Reilly account"), reply.content());
        assertTrue(rpc(expert, "tools/call").isEmpty());
    }

    @Test
    void anExpiredGrantAsksForANewSignIn() {
        register(required(List.of()));
        leaseStatus.put("conv-1", 410);

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("Sign in again"), reply.content());
        assertTrue(rpc(expert, "tools/call").isEmpty());
    }

    @Test
    void aTokenWhoseExpiryHasPassedIsNotUsed() {
        register(required(List.of()));
        leaseExpiry = Instant.now().minusSeconds(5);

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.content().contains("Sign in again"), reply.content());
        assertTrue(rpc(expert, "tools/call").isEmpty());
    }

    @Test
    void anUnreachableLeaseServiceIsAToolResultAndNeverFallsBackToTheSecret() throws IOException {
        register(required(List.of()));
        try (ServerSocket closed = new ServerSocket(0)) {
            properties.getCredentialBroker().setUrl("http://127.0.0.1:" + closed.getLocalPort());
        }

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("unavailable"), reply.content());
        assertTrue(reply.content().contains("Ask again"), reply.content());
        assertTrue(rpc(expert, "tools/call").isEmpty());
    }

    @Test
    void aMissingServiceAccountTokenFileIsTheSameToolResult() {
        register(required(List.of()));
        properties.getCredentialBroker().setTokenFile("/nonexistent/token");

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.content().contains("unavailable"), reply.content());
        assertTrue(leases.isEmpty());
    }

    @Test
    void aCallWithNoIdentityInMetaMakesNeitherALeaseNorAnUpstreamCall() {
        register(required(List.of()));

        ToolCallResponse reply = controller.callTool("ping", new ToolCallRequest(Map.of())).block(Duration.ofSeconds(30));

        assertNotNull(reply);
        assertTrue(reply.isError());
        assertTrue(reply.content().contains("Connect your O'Reilly account"), reply.content());
        assertTrue(leases.isEmpty());
        assertTrue(rpc(expert, "tools/call").isEmpty());
    }

    @Test
    void identityLikeKeysInsideArgumentsAreIgnored() {
        register(required(List.of()));
        Map<String, Object> arguments = Map.of("conversationId", "conv-2", "principalId", "evil");

        ToolCallResponse withoutMeta = controller.callTool("ping", new ToolCallRequest(arguments))
            .block(Duration.ofSeconds(30));
        assertNotNull(withoutMeta);
        assertTrue(withoutMeta.isError());
        assertTrue(leases.isEmpty(), "arguments never name a conversation");

        call("conv-1", "principal-a", arguments);
        assertEquals("conv-1", leases.get(0).body().path("conversationId").asText());
        assertEquals("Bearer ASKER-TOKEN-ONE", rpc(expert, "tools/call").get(0).authorization());
    }

    @Test
    void aMalformedBrokerAddressIsAToolResultNotAnException() {
        register(required(List.of()));
        properties.getCredentialBroker().setUrl("http://bad host:99999");

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("unavailable"), reply.content());
        assertTrue(rpc(expert, "tools/call").isEmpty());
    }

    @Test
    void aRegisteredLeaseAddressOnAnotherHostNeverGetsTheGatewaysOwnToken() {
        register(new CallerCredentialConfig(Mode.REQUIRED, "http://127.0.0.1:1/internal/v1/lease", "oreilly",
            "O'Reilly", List.of(), false));

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.content().contains("unavailable"), reply.content());
        assertTrue(leases.isEmpty());
    }

    @Test
    void modeRequiredNeedsAnHttpTransport() {
        org.junit.jupiter.api.Assertions.assertThrows(IllegalArgumentException.class,
            () -> manager.registerServer("sse-one", url(expertServer, ""), "sse", List.of(), null, required(List.of())));
    }

    @Test
    void aServerRegisteredWithoutTheMcpSuffixIsCalledAtMcpLikeEveryOtherCall() {
        ServerRegistration server = manager.registerServer("bare", url(expertServer, "/api/expert"), HTTP,
            List.of(), new ServerCredential("Authorization", "Bearer " + LISTING_SECRET), required(List.of()));
        connected(server);
        expert.clear();

        call("conv-1", "principal-a", Map.of());

        assertFalse(rpc(expert, "tools/call").isEmpty());
    }

    // ---- the _meta handling -------------------------------------------------------------------

    @Test
    void theIdentityKeysAreRemovedFromMetaBeforeForwardingAndOtherKeysStillPass() {
        register(required(List.of()));
        Map<String, Object> meta = new HashMap<>(identity("conv-1", "principal-a"));
        meta.put("timeZone", "America/New_York");

        controller.callTool("ping", new ToolCallRequest(Map.of(), meta)).block(Duration.ofSeconds(30));

        JsonNode forwarded = rpc(expert, "tools/call").get(0).body().path("params").path("_meta");
        assertTrue(forwarded.path("conversationId").isMissingNode());
        assertTrue(forwarded.path("principalId").isMissingNode());
        assertEquals("America/New_York", forwarded.path("timeZone").asText());
    }

    @Test
    void relayContextKeepsTheTwoIdsInMetaForAToolServerThatCallsBack() {
        register(new CallerCredentialConfig(Mode.REQUIRED, base(broker) + "/internal/v1/lease", "oreilly", "O'Reilly",
            List.of(), true));

        call("conv-1", "principal-a", Map.of());

        JsonNode forwarded = rpc(expert, "tools/call").get(0).body().path("params").path("_meta");
        assertEquals("conv-1", forwarded.path("conversationId").asText());
        assertEquals("principal-a", forwarded.path("principalId").asText());
    }

    @Test
    void modeOffWithRelayContextForwardsTheTwoIdsAndKeepsTheServersOwnCredential() {
        register(new CallerCredentialConfig(Mode.OFF, null, null, null, List.of(), true));

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertFalse(reply.isError(), reply.content());
        List<Seen> calls = rpc(expert, "tools/call");
        assertEquals(1, calls.size());
        JsonNode forwarded = calls.get(0).body().path("params").path("_meta");
        assertEquals("conv-1", forwarded.path("conversationId").asText());
        assertEquals("principal-a", forwarded.path("principalId").asText());
        assertEquals("Bearer " + LISTING_SECRET, calls.get(0).authorization());
        assertTrue(leases.isEmpty(), "no token is leased in mode Off");
        assertTrue(calls.get(0).body().toString().indexOf("ASKER-TOKEN") < 0, "no person's token is forwarded");
    }

    @Test
    void modeOffWithoutRelayContextForwardsNoIds() {
        register(new CallerCredentialConfig(Mode.OFF, null, null, null, List.of(), false));

        call("conv-1", "principal-a", Map.of());

        JsonNode forwarded = rpc(expert, "tools/call").get(0).body().path("params").path("_meta");
        assertTrue(forwarded.path("conversationId").isMissingNode());
        assertTrue(forwarded.path("principalId").isMissingNode());
        assertTrue(leases.isEmpty());
    }

    @Test
    void modeOffWithRelayContextAndNoIdsForwardsNoIds() {
        register(new CallerCredentialConfig(Mode.OFF, null, null, null, List.of(), true));

        controller.callTool("ping", new ToolCallRequest(Map.of(), Map.of("timeZone", "America/New_York")))
            .block(Duration.ofSeconds(30));

        JsonNode forwarded = rpc(expert, "tools/call").get(0).body().path("params").path("_meta");
        assertTrue(forwarded.path("conversationId").isMissingNode());
        assertTrue(forwarded.path("principalId").isMissingNode());
        assertEquals("America/New_York", forwarded.path("timeZone").asText());
    }

    // ---- the fallback -------------------------------------------------------------------------

    @Test
    void aForbiddenOnTheExpertEndpointRetriesOnSearchWithTheSameToken() {
        register(required(List.of(url(searchServer, "/api/search/v1/mcp"))));
        expertForbids.add("Bearer ASKER-TOKEN-ONE");

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertFalse(reply.isError(), reply.content());
        assertEquals("answer-from-search", reply.content());
        assertEquals("Bearer ASKER-TOKEN-ONE", rpc(search, "tools/call").get(0).authorization());
        assertTrue(rpc(search, "tools/call").size() == 1 && rpc(expert, "tools/call").isEmpty(),
            "the refused endpoint never ran the call");
    }

    @Test
    void whenBothEndpointsRefuseTheUpstreamsRefusalTextIsTheToolResult() {
        register(required(List.of(url(searchServer, "/api/search/v1/mcp"))));
        expertForbids.add("Bearer ASKER-TOKEN-ONE");
        searchForbids.add("Bearer ASKER-TOKEN-ONE");

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("Expert is not in this plan"), reply.content());
        assertFalse(reply.content().contains("ASKER-TOKEN-ONE"));
    }

    @Test
    void aToolOnlyTheExpertEndpointHasReturnsTheExpertRefusalWhenSearchDoesNotKnowIt() {
        register(required(List.of(url(searchServer, "/api/search/v1/mcp"))));
        expertForbids.add("Bearer ASKER-TOKEN-ONE");
        searchKnowsTool = false;

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("Expert is not in this plan"), reply.content());
    }

    @Test
    void aRejectedSignInIsReportedAndNotRetriedOnAFallback() {
        register(required(List.of(url(searchServer, "/api/search/v1/mcp"))));
        expertStatusOnCall = 401;

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertTrue(reply.isError());
        assertTrue(reply.content().contains("rejected your sign-in"), reply.content());
        assertTrue(rpc(search, "tools/call").isEmpty());
    }

    @Test
    void aRedirectIsNotFollowedSoTheTokenNeverGoesToAnotherHost() throws IOException {
        HttpServer elsewhere = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        List<String> hits = new CopyOnWriteArrayList<>();
        elsewhere.createContext("/", exchange -> {
            hits.add(String.valueOf(exchange.getRequestHeaders().getFirst("Authorization")));
            exchange.sendResponseHeaders(200, -1);
            exchange.close();
        });
        elsewhere.start();
        try {
            redirectTo = url(elsewhere, "/steal");
            register(required(List.of()));

            ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

            assertTrue(reply.isError());
            assertTrue(hits.isEmpty(), "the redirect target was called: " + hits);
        } finally {
            redirectTo = null;
            elsewhere.stop(0);
        }
    }

    // ---- isolation, redaction, regression -----------------------------------------------------

    @Test
    void concurrentCallsForTwoConversationsEachUseTheirOwnToken() {
        register(required(List.of()));

        List<ToolCallResponse> replies = Flux.range(0, 20)
            .flatMap(i -> Mono.fromCallable(() -> call(i % 2 == 0 ? "conv-1" : "conv-2", "p" + (i % 2), Map.of()))
                .subscribeOn(reactor.core.scheduler.Schedulers.boundedElastic()), 8)
            .collectList().block(Duration.ofSeconds(60));

        assertNotNull(replies);
        assertTrue(replies.stream().noneMatch(ToolCallResponse::isError));
        for (Seen s : rpc(expert, "tools/call")) {
            String conversation = s.body().path("params").path("arguments").path("who").asText();
            assertEquals("conv-1".equals(conversation) ? "Bearer ASKER-TOKEN-ONE" : "Bearer ASKER-TOKEN-TWO",
                s.authorization(), "conversation " + conversation);
        }
        assertEquals(20, rpc(expert, "tools/call").size());
    }

    @Test
    void aCanaryTokenNeverAppearsInLogsResultsOrPrintedValues() {
        grant("conv-1", CANARY);
        register(required(List.of(url(searchServer, "/api/search/v1/mcp"))));
        expertForbids.add("Bearer " + CANARY);
        searchForbids.add("Bearer " + CANARY);
        echoTokenInRefusal = true;

        ToolCallResponse refused = call("conv-1", "principal-a", Map.of());
        searchForbids.clear();
        ToolCallResponse answered = call("conv-1", "principal-a", Map.of());
        leaseStatus.put("conv-1", 410);
        call("conv-1", "principal-a", Map.of());

        root.detachAppender(logs);
        List<ILoggingEvent> captured = new ArrayList<>(logs.list);
        List<String> everything = new ArrayList<>(List.of(refused.content(), answered.content(),
            new kubemoot.ai.mcpgateway.model.CallerToken(CANARY, Instant.now()).toString()));
        captured.forEach(e -> {
            everything.add(e.getFormattedMessage());
            everything.add(String.valueOf(e.getThrowableProxy()));
            everything.add(String.valueOf(e.getArgumentArray() == null ? "" : List.of(e.getArgumentArray())));
        });
        assertFalse(captured.isEmpty());
        assertTrue(refused.isError());
        everything.forEach(text -> assertFalse(text.contains(CANARY), "found the canary in: " + text));
        everything.forEach(text -> assertFalse(text.contains(SA_TOKEN), "found the service account token in: " + text));
    }

    @Test
    void modeOffIsUnchangedAndStillUsesTheRegisteredCredentialWithNoLease() {
        ServerRegistration server = manager.registerServer("oreilly-off", url(expertServer, "/api/expert/v1/mcp"), HTTP,
            List.of(), new ServerCredential("Authorization", "Bearer " + LISTING_SECRET));
        connected(server);
        expert.clear();

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertFalse(reply.isError(), reply.content());
        assertTrue(leases.isEmpty());
        assertTrue(expert.stream().allMatch(s -> ("Bearer " + LISTING_SECRET).equals(s.authorization())), expert.toString());
        assertFalse(rpc(expert, "tools/call").isEmpty());
    }

    @Test
    void anExplicitOffRegistrationIsTheSameAsNone() {
        ServerRegistration server = manager.registerServer("oreilly-off", url(expertServer, "/api/expert/v1/mcp"), HTTP,
            List.of(), new ServerCredential("Authorization", "Bearer " + LISTING_SECRET), CallerCredentialConfig.OFF);
        connected(server);

        ToolCallResponse reply = call("conv-1", "principal-a", Map.of());

        assertFalse(reply.isError(), reply.content());
        assertTrue(leases.isEmpty());
    }

    @Test
    void reRegisteringWithModeOffReturnsTheServerToItsOwnCredential() {
        ServerRegistration server = register(required(List.of()));
        manager.registerServer("oreilly", url(expertServer, "/api/expert/v1/mcp"), HTTP, List.of(),
            new ServerCredential("Authorization", "Bearer " + LISTING_SECRET), CallerCredentialConfig.OFF);
        expert.clear();

        call("conv-1", "principal-a", Map.of());

        assertTrue(leases.isEmpty());
        assertEquals(server.id(), manager.findServerForTool("ping"));
    }

    // ---- helpers ------------------------------------------------------------------------------

    private volatile List<Seen> listing = List.of();
    private volatile Instant leaseExpiry = Instant.now().plusSeconds(600);
    private volatile String redirectTo;
    private volatile boolean echoTokenInRefusal;

    private CallerCredentialConfig required(List<String> fallbacks) {
        return new CallerCredentialConfig(Mode.REQUIRED, null, "oreilly", "O'Reilly", fallbacks, false);
    }

    private ServerRegistration register(CallerCredentialConfig config) {
        ServerRegistration server = manager.registerServer("oreilly", url(expertServer, "/api/expert/v1/mcp"), HTTP,
            List.of(), new ServerCredential("Authorization", "Bearer " + LISTING_SECRET), config);
        connected(server);
        listing = new ArrayList<>(expert);
        expert.clear();
        search.clear();
        return server;
    }

    private void grant(String conversation, String token) {
        leaseStatus.put(conversation, 200);
        leaseToken.put(conversation, token);
    }

    private static Map<String, Object> identity(String conversation, String principal) {
        return Map.of("conversationId", conversation, "principalId", principal);
    }

    private ToolCallResponse call(String conversation, String principal, Map<String, Object> arguments) {
        var args = new HashMap<>(arguments);
        args.put("who", conversation);
        ToolCallResponse reply = controller.callTool("ping",
            new ToolCallRequest(args, identity(conversation, principal))).block(Duration.ofSeconds(30));
        assertNotNull(reply);
        return reply;
    }

    private static List<Seen> rpc(List<Seen> seen, String method) {
        return seen.stream().filter(s -> method.equals(s.rpcMethod())).toList();
    }

    private static String base(HttpServer server) {
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    private static String url(HttpServer server, String path) {
        return base(server) + path;
    }

    private void connected(ServerRegistration server) {
        awaitTrue(() -> manager.getServer(server.id()).status() == ServerStatus.CONNECTED
            && !manager.getToolsForServer(server.id()).isEmpty());
    }

    private HttpServer broker() throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/internal/v1/lease", exchange -> {
            JsonNode body = mapper.readTree(exchange.getRequestBody());
            leases.add(new Leased(exchange.getRequestHeaders().getFirst("Authorization"), body));
            String conversation = body.path("conversationId").asText();
            int status = leaseStatus.getOrDefault(conversation, 404);
            String json = status == 200 ? mapper.writeValueAsString(Map.of(
                "token", leaseToken.get(conversation), "expiresAt", leaseExpiry.toString())) : "{}";
            reply(exchange, status, "application/json", json);
        });
        server.setExecutor(Executors.newCachedThreadPool());
        server.start();
        return server;
    }

    private HttpServer upstream(List<Seen> into, Set<String> forbids, boolean isExpert) throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> serve(exchange, into, forbids, isExpert));
        server.setExecutor(Executors.newCachedThreadPool());
        server.start();
        return server;
    }

    private void serve(HttpExchange exchange, List<Seen> into, Set<String> forbids, boolean isExpert)
            throws IOException {
        String auth = exchange.getRequestHeaders().getFirst("Authorization");
        String session = exchange.getRequestHeaders().getFirst("Mcp-Session-Id");
        if ("DELETE".equals(exchange.getRequestMethod())) {
            into.add(new Seen("DELETE", "", auth, session, null));
            reply(exchange, 200, "application/json", "{}");
            return;
        }
        JsonNode request = mapper.readTree(exchange.getRequestBody());
        String method = request.path("method").asText();
        into.add(new Seen("POST", method, auth, session, request));
        if (refuseOrRedirect(exchange, auth, method, forbids, isExpert)) {
            return;
        }
        JsonNode id = request.get("id");
        if (id == null || id.isNull()) {
            reply(exchange, 202, "application/json", "");
        } else if (!isExpert && "tools/call".equals(method) && !searchKnowsTool) {
            reply(exchange, 200, "application/json", mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id,
                "error", Map.of("code", -32602, "message", "Unknown tool"))));
        } else {
            answer(exchange, id, method, isExpert);
        }
    }

    /** The scripted refusals: a redirect, a 403 for a forbidden bearer, a forced status on the call. */
    private boolean refuseOrRedirect(HttpExchange exchange, String auth, String method, Set<String> forbids,
                                     boolean isExpert) throws IOException {
        boolean asker = isExpert && isAsker(auth);
        if (asker && redirectTo != null) {
            exchange.getResponseHeaders().add("Location", redirectTo);
            exchange.sendResponseHeaders(302, -1);
            exchange.close();
        } else if (auth != null && forbids.contains(auth)) {
            forbid(exchange, auth);
        } else if (asker && "tools/call".equals(method) && expertStatusOnCall != 200) {
            reply(exchange, expertStatusOnCall, "application/json", "{}");
        } else {
            return false;
        }
        return true;
    }

    private static boolean isAsker(String auth) {
        return auth != null && (auth.contains("ASKER") || auth.contains(CANARY));
    }

    private void forbid(HttpExchange exchange, String auth) throws IOException {
        String message = "Expert is not in this plan" + (echoTokenInRefusal ? " for " + auth : "");
        reply(exchange, 403, "application/json", mapper.writeValueAsString(Map.of("message", message)));
    }

    private void answer(HttpExchange exchange, JsonNode id, String method, boolean isExpert) throws IOException {
        String json = mapper.writeValueAsString(Map.of("jsonrpc", "2.0", "id", id,
            "result", resultFor(method, isExpert)));
        exchange.getResponseHeaders().add("Mcp-Session-Id", "s-1");
        if ("tools/call".equals(method)) {
            reply(exchange, 200, "text/event-stream", "event: message\ndata: " + json + "\n\n");
        } else {
            reply(exchange, 200, "application/json", json);
        }
    }

    private static Object resultFor(String method, boolean isExpert) {
        return switch (method) {
            case "initialize" -> Map.of("protocolVersion", "2024-11-05",
                "capabilities", Map.of("tools", Map.of()), "serverInfo", Map.of("name", "fake", "version", "1"));
            case "tools/list" -> Map.of("tools", List.of(
                Map.of("name", "ping", "description", "ping", "inputSchema", Map.of("type", "object"))));
            default -> Map.of("content", List.of(Map.of("type", "text",
                "text", isExpert ? "answer-from-expert" : "answer-from-search")));
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
