package kubemoot.ai.mcpgateway.service;

import jakarta.annotation.PostConstruct;
import jakarta.annotation.PreDestroy;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientRequestException;
import org.springframework.web.reactive.function.client.ClientResponse;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.core.publisher.MonoSink;
import reactor.core.publisher.Sinks;
import reactor.core.Disposable;
import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.FeedbackEntry;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ServerRegistration.ServerStatus;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import kubemoot.ai.mcpgateway.model.ToolOverride;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.TimeoutException;
import java.util.function.Predicate;
import java.util.concurrent.atomic.AtomicLong;

@Service
public class McpClientManager {

    private static final Logger log = LoggerFactory.getLogger(McpClientManager.class);
    private static final String MCP_SESSION_HEADER = "mcp-session-id";
    private static final String ACCEPT_HEADER = "Accept";
    private static final String ACCEPT_JSON_SSE = "application/json, text/event-stream";
    private static final String TRANSPORT_STDIO = "stdio";
    private static final String SESSION_URI_PREFIX = "/message?sessionId=";
    private static final String MCP_NOTIFICATION_INITIALIZED = "notifications/initialized";
    private static final String MCP_NOTIFICATION_TOOLS_CHANGED = "notifications/tools/list_changed";
    private static final String METHOD_TOOLS_LIST = "tools/list";
    /** Safety net for a connect that never answers; the normal path ends long before. */
    private static final Duration CONNECT_DEADLINE = Duration.ofSeconds(60);

    // Tool discovery field constants
    private static final String FIELD_NAME = "name";
    private static final String FIELD_DESCRIPTION = "description";
    private static final String FIELD_INPUT_SCHEMA = "inputSchema";

    // MCP protocol constants (shared between initializeConnection and sendStdioInitializeAndWait)
    private static final String MCP_PROTOCOL_VERSION = "2024-11-05";
    private static final Map<String, Object> MCP_INIT_PARAMS = Map.of(
        "protocolVersion", MCP_PROTOCOL_VERSION,
        "capabilities", Map.of(),
        "clientInfo", Map.of(
            FIELD_NAME, "kubemoot-mcp-gateway",
            "version", "0.1.0"
        )
    );

    private final McpGatewayProperties properties;
    private final WebClient.Builder webClientBuilder;
    private final ObjectMapper objectMapper;
    private final Map<String, ServerRegistration> servers = new ConcurrentHashMap<>();
    private final Map<String, WebClient> clients = new ConcurrentHashMap<>();
    private final Map<String, List<ToolInfo>> serverTools = new ConcurrentHashMap<>();
    // Description overrides per server ID, applied to every tool list read from that server
    private final Map<String, List<ToolOverride>> serverOverrides = new ConcurrentHashMap<>();
    private final Map<String, String> serverSessions = new ConcurrentHashMap<>();
    // For supergateway: active SSE subscriptions and response sinks
    private final Map<String, Disposable> sseSubscriptions = new ConcurrentHashMap<>();
    private final Map<String, Sinks.Many<McpMessage>> responseSinks = new ConcurrentHashMap<>();
    // The current SSE stream per server; an ended stream acts only while it is still current
    private final Map<String, Object> sseStreams = new ConcurrentHashMap<>();
    // When each server's tool list was last read (or a re-read started)
    private final Map<String, Instant> toolsListedAt = new ConcurrentHashMap<>();
    // Listings started per server; only the latest one may replace the cached tools
    private final Map<String, Long> listings = new ConcurrentHashMap<>();
    /** The connect a request started for a server; requests that arrive meanwhile wait on it. */
    private final Map<String, Mono<Void>> requestReconnects = new ConcurrentHashMap<>();
    // Feedback log: server ID → list of feedback entries (consumed and cleared by operator)
    private final Map<String, List<FeedbackEntry>> feedbackLog = new ConcurrentHashMap<>();
    // Upstream JSON-RPC ids; starts above the fixed ids the handshake uses (1, 2).
    private final AtomicLong upstreamIds = new AtomicLong(1_000);

    public McpClientManager(McpGatewayProperties properties, WebClient.Builder webClientBuilder,
                           ObjectMapper objectMapper) {
        this.properties = properties;
        this.webClientBuilder = webClientBuilder;
        this.objectMapper = objectMapper;
    }

    @PostConstruct
    public void init() {
        // Register pre-configured servers from application.yaml
        for (McpGatewayProperties.ServerConfig config : properties.getServers()) {
            log.info("Registering pre-configured MCP server: {} at {}", config.getName(), config.getUrl());
            ServerRegistration registration = ServerRegistration.create(
                config.getName(),
                config.getUrl(),
                config.getTransport()
            );
            registerServerInternal(registration);
        }
    }

    @PreDestroy
    public void shutdown() {
        log.info("Shutting down MCP client connections");
        servers.values().forEach(server ->
            servers.put(server.id(), server.withStatus(ServerStatus.DISCONNECTED))
        );
        serverSessions.clear();
        sseStreams.clear();
        listings.clear();
        // Clean up SSE subscriptions
        sseSubscriptions.values().forEach(Disposable::dispose);
        sseSubscriptions.clear();
        responseSinks.values().forEach(Sinks.Many::tryEmitComplete);
        responseSinks.clear();
    }

    public ServerRegistration registerServer(String name, String url, String transport) {
        return registerServer(name, url, transport, List.of());
    }

    public ServerRegistration registerServer(String name, String url, String transport,
                                             List<ToolOverride> toolOverrides) {
        List<ToolOverride> overrides = toolOverrides == null ? List.of() : toolOverrides;
        // Check if a server with the same name already exists
        ServerRegistration existing = findServerByName(name);
        if (existing != null) {
            // Server already registered - check if URL changed
            if (existing.url().equals(url)) {
                List<ToolOverride> previous = serverOverrides.put(existing.id(), overrides);
                if (!overrides.equals(previous)) {
                    log.info("Server {} tool overrides changed", name);
                    ToolOverrides.warnUnmatched(getToolsForServer(existing.id()), overrides, name);
                }
                reconcileRegisteredServer(existing);
                return existing;
            } else {
                // URL changed, unregister old and register new
                log.info("Server {} URL changed from {} to {}, re-registering", name, existing.url(), url);
                unregisterServer(existing.id());
            }
        }

        log.info("Registering new MCP server: {} at {}", name, url);
        ServerRegistration registration = ServerRegistration.create(name, url, transport);
        serverOverrides.put(registration.id(), overrides);
        registerServerInternal(registration);
        return registration;
    }

    /**
     * The operator re-registers every server on each reconcile; the server's state decides
     * what that does. A server whose session was lost (its stream ended, as when its pod is
     * replaced), whose last connect failed, or that has no tools is connected again, which
     * re-lists its tools. A connected server whose tool list is older than the safety-net
     * age is re-listed in place. Anything else is left alone.
     */
    private void reconcileRegisteredServer(ServerRegistration existing) {
        if (claimReconnect(existing.id())) {
            log.info("Server {} lost its session or has no tools, reconnecting to re-list its tools",
                existing.name());
            connectToServer(existing.id());
        } else if (isToolListOld(existing.id())) {
            log.info("Tool list of {} is older than {}, re-listing", existing.name(),
                properties.getToolListMaxAge());
            refreshTools(existing.id());
        } else {
            log.debug("Server {} already registered with same URL and connected", existing.name());
        }
    }

    /**
     * Atomically move a server that needs a new connection to CONNECTING, so a burst of
     * re-registrations starts one connect, not one each.
     */
    private boolean claimReconnect(String serverId) {
        return claimReconnect(serverId, this::needsReconnect);
    }

    private boolean claimReconnect(String serverId, Predicate<ServerRegistration> wanted) {
        AtomicBoolean claimed = new AtomicBoolean(false);
        servers.computeIfPresent(serverId, (id, server) -> {
            if (!wanted.test(server)) {
                return server;
            }
            claimed.set(true);
            return server.withStatus(ServerStatus.CONNECTING);
        });
        return claimed.get();
    }

    private boolean needsReconnect(ServerRegistration server) {
        return switch (server.status()) {
            case DISCONNECTED, ERROR -> true;
            case CONNECTED -> getToolsForServer(server.id()).isEmpty();
            case UNKNOWN, CONNECTING -> false; // a connect is under way
        };
    }

    private boolean isToolListOld(String serverId) {
        Instant listedAt = toolsListedAt.get(serverId);
        return listedAt != null
            && listedAt.plus(properties.getToolListMaxAge()).isBefore(Instant.now());
    }

    /**
     * Re-list a server's tools and replace the cached list. Uses the current session, or
     * opens one when there is none. An empty or failed read keeps the tools already known
     * and marks the server ERROR, so the next re-registration connects again.
     */
    void refreshTools(String serverId) {
        if (!servers.containsKey(serverId)) {
            return;
        }
        toolsListedAt.put(serverId, Instant.now());
        long listing = startListing(serverId);
        discoverToolsInternal(serverId, true)
            .timeout(CONNECT_DEADLINE)
            .filter(tools -> isLatestListing(serverId, listing))
            .subscribe(
                tools -> applyRefreshedTools(serverId, tools),
                error -> {
                    log.warn("Re-listing tools of {} failed: {}", serverId, error.getMessage());
                    if (isLatestListing(serverId, listing)) {
                        setStatus(serverId, ServerStatus.ERROR);
                    }
                });
    }

    /** Start a listing; a reply to an older listing that arrives later is ignored. */
    private long startListing(String serverId) {
        return listings.merge(serverId, 1L, Long::sum);
    }

    private boolean isLatestListing(String serverId, long listing) {
        boolean latest = listings.getOrDefault(serverId, 0L) == listing;
        if (!latest) {
            log.debug("Ignoring an older tool listing of {}", serverId);
        }
        return latest;
    }

    private void applyRefreshedTools(String serverId, List<ToolInfo> tools) {
        ServerRegistration server = servers.get(serverId);
        if (server == null) {
            return;
        }
        if (tools.isEmpty()) {
            log.warn("Re-listing {} returned no tools, keeping the {} known", server.name(),
                getToolsForServer(serverId).size());
            setStatus(serverId, ServerStatus.ERROR);
            return;
        }
        List<ToolInfo> previous = serverTools.put(serverId, tools);
        ToolOverrides.warnUnmatched(tools, serverOverrides.get(serverId), server.name());
        setStatus(serverId, ServerStatus.CONNECTED);
        log.info("Re-listed {}: {} tools (was {})", server.name(), tools.size(),
            previous == null ? 0 : previous.size());
        recordFeedback(FeedbackEntry.success(server.name(), serverId, "discover", tools.size()));
    }

    /**
     * Open a new session after the old one was lost. A new session can reach a new server
     * process behind the same URL (a replaced pod) whose tool set differs, so the tools
     * are re-listed once the session is open.
     */
    private Mono<Void> reinitialize(String serverId) {
        // CONNECTING keeps a concurrent re-registration from opening a second session
        // that would replace this one under the call waiting on it.
        return Mono.defer(() -> {
                setStatus(serverId, ServerStatus.CONNECTING);
                return initializeConnection(serverId);
            })
            .timeout(CONNECT_DEADLINE)
            .doOnSuccess(ignored -> refreshTools(serverId))
            .doOnError(error -> setStatus(serverId, ServerStatus.ERROR))
            // A caller that gives up mid-way leaves no connect under way.
            .doOnCancel(() -> setStatus(serverId, ServerStatus.ERROR));
    }

    private void setStatus(String serverId, ServerStatus status) {
        servers.computeIfPresent(serverId, (id, s) -> s.withStatus(status));
    }

    /**
     * A status that means the session is gone and a new one must be opened. The mcp-bridge
     * (stdio) answers 400 for a session it does not know and 503 while its MCP server is
     * not connected; a streamable HTTP server answers 404 as the MCP transport specifies,
     * or 400/401 in older servers.
     */
    private static boolean isSessionLost(ServerRegistration server, int statusCode) {
        if (TRANSPORT_STDIO.equalsIgnoreCase(server.transport())) {
            return statusCode == 400 || statusCode == 503;
        }
        return statusCode == 400 || statusCode == 401 || statusCode == 404;
    }

    /**
     * Find a server by name.
     */
    private ServerRegistration findServerByName(String name) {
        return servers.values().stream()
            .filter(s -> s.name().equals(name))
            .findFirst()
            .orElse(null);
    }

    private void registerServerInternal(ServerRegistration registration) {
        servers.put(registration.id(), registration);

        WebClient client = webClientBuilder
            .baseUrl(registration.url())
            .build();
        clients.put(registration.id(), client);

        // Start connection attempt
        connectToServer(registration.id());
    }

    public void unregisterServer(String serverId) {
        servers.remove(serverId);
        clients.remove(serverId);
        serverTools.remove(serverId);
        serverOverrides.remove(serverId);
        toolsListedAt.remove(serverId);
        listings.remove(serverId);
        cleanupSseSession(serverId);
        log.info("Unregistered MCP server: {}", serverId);
    }

    public List<ServerRegistration> listServers() {
        return new ArrayList<>(servers.values());
    }

    public ServerRegistration getServer(String serverId) {
        return servers.get(serverId);
    }

    /** Every server's tools, with each server's description overrides applied. */
    public List<ToolInfo> getAllTools() {
        return serverTools.keySet().stream()
            .flatMap(id -> getToolsForServer(id).stream())
            .toList();
    }

    /** The server's tools as agents see them: upstream tools with description overrides applied. */
    public List<ToolInfo> getToolsForServer(String serverId) {
        return ToolOverrides.apply(serverTools.getOrDefault(serverId, Collections.emptyList()),
            serverOverrides.get(serverId));
    }

    public String findServerForTool(String toolName) {
        for (Map.Entry<String, List<ToolInfo>> entry : serverTools.entrySet()) {
            for (ToolInfo tool : entry.getValue()) {
                if (tool.name().equals(toolName)) {
                    return entry.getKey();
                }
            }
        }
        return null;
    }

    private void connectToServer(String serverId) {
        ServerRegistration server = servers.get(serverId);
        if (server == null) return;

        servers.put(serverId, server.withStatus(ServerStatus.CONNECTING));
        log.info("Connecting to MCP server: {} at {}", server.name(), server.url());

        // Initialize connection and then discover tools (using defer to ensure ordering)
        // Retry once after 2s if 0 tools are found (handles initialization race with compliant MCP servers)
        long listing = startListing(serverId);
        initializeConnection(serverId)
            .then(Mono.defer(() -> discoverTools(serverId)))
            .flatMap(tools -> {
                if (tools.isEmpty()) {
                    log.info("Got 0 tools from {}, retrying after delay", server.name());
                    return Mono.delay(java.time.Duration.ofSeconds(2))
                        .then(Mono.defer(() -> discoverTools(serverId)));
                }
                return Mono.just(tools);
            })
            .timeout(CONNECT_DEADLINE)
            .filter(tools -> isLatestListing(serverId, listing))
            .subscribe(
                tools -> onConnected(serverId, server, tools),
                error -> {
                    log.error("Failed to connect to MCP server {}: {}", server.name(), error.getMessage());
                    setStatus(serverId, ServerStatus.ERROR);
                    recordFeedback(FeedbackEntry.failure(server.name(), serverId, "connect", error.getMessage()));
                }
            );
    }

    /**
     * Store the tools a connect discovered. A reconnect that finds no tools (a replacement
     * server still starting) keeps the tools already known and marks the server ERROR, so
     * the next re-registration connects again instead of agents losing the tools meanwhile.
     */
    private void onConnected(String serverId, ServerRegistration server, List<ToolInfo> tools) {
        if (tools.isEmpty() && !getToolsForServer(serverId).isEmpty()) {
            log.warn("Reconnected to {} but it listed no tools, keeping the {} known", server.name(),
                getToolsForServer(serverId).size());
            setStatus(serverId, ServerStatus.ERROR);
            return;
        }
        serverTools.put(serverId, tools);
        ToolOverrides.warnUnmatched(tools, serverOverrides.get(serverId), server.name());
        toolsListedAt.put(serverId, Instant.now());
        setStatus(serverId, ServerStatus.CONNECTED);
        log.info("Connected to MCP server {} with {} tools", server.name(), tools.size());
        recordFeedback(FeedbackEntry.success(server.name(), serverId, "connect"));
        recordFeedback(FeedbackEntry.success(server.name(), serverId, "discover", tools.size()));
    }

    private Mono<Void> initializeConnection(String serverId) {
        WebClient client = clients.get(serverId);
        ServerRegistration server = servers.get(serverId);
        if (client == null || server == null) {
            return Mono.error(new IllegalStateException("No client for server: " + serverId));
        }

        // Stdio transport (supergateway) requires connecting to /sse first to get session
        if (TRANSPORT_STDIO.equalsIgnoreCase(server.transport())) {
            return initializeStdioConnection(serverId, client, server);
        }

        McpMessage initRequest = McpMessage.request(1, "initialize", MCP_INIT_PARAMS);

        // Use transport-specific endpoint
        String endpoint = getMcpEndpoint(server.transport());

        // Streamable-http returns SSE with session ID in header
        return client.post()
            .uri(endpoint)
            .header(ACCEPT_HEADER, ACCEPT_JSON_SSE)
            .bodyValue(initRequest)
            .exchangeToFlux(response -> {
                // Capture session ID from response headers
                String sessionId = response.headers().asHttpHeaders().getFirst(MCP_SESSION_HEADER);
                if (sessionId != null) {
                    serverSessions.put(serverId, sessionId);
                    log.debug("Captured session ID for {}: {}", serverId, sessionId);
                }
                return response.bodyToFlux(new ParameterizedTypeReference<ServerSentEvent<String>>() {});
            })
            .filter(event -> event.data() != null && !event.data().isEmpty())
            .next()
            .flatMap(event -> {
                try {
                    McpMessage response = objectMapper.readValue(event.data(), McpMessage.class);
                    log.debug("Initialize response: {}", response);
                    return Mono.just(response);
                } catch (Exception e) {
                    log.warn("Failed to parse SSE event: {}", e.getMessage());
                    return Mono.empty();
                }
            })
            .then(Mono.defer(() -> {
                // Send notifications/initialized to complete the MCP handshake
                McpMessage initialized = McpMessage.notification(MCP_NOTIFICATION_INITIALIZED);
                String ep = getMcpEndpoint(server.transport());
                return client.post()
                    .uri(ep)
                    .header(ACCEPT_HEADER, ACCEPT_JSON_SSE)
                    .bodyValue(initialized)
                    .retrieve()
                    .toBodilessEntity()
                    .doOnSuccess(r -> log.debug("Sent notifications/initialized for {}", serverId))
                    .then();
            }));
    }

    // Initialize connection for stdio/supergateway servers
    // Supergateway requires maintaining an open SSE connection while POSTing messages
    private Mono<Void> initializeStdioConnection(String serverId, WebClient client, ServerRegistration server) {
        // Clean up any existing subscription
        Disposable existing = sseSubscriptions.remove(serverId);
        if (existing != null) {
            existing.dispose();
        }
        responseSinks.remove(serverId);

        // Create a Sink to receive response messages
        // Use replay() to buffer responses for late subscribers (response may arrive before subscription)
        Sinks.Many<McpMessage> responseSink = Sinks.many().replay().limit(100);
        responseSinks.put(serverId, responseSink);

        return Mono.<Void>create(ready -> {
            // Start SSE connection and keep it open in background
            Flux<ServerSentEvent<String>> sseFlux = client.get()
                .uri("/sse")
                .accept(org.springframework.http.MediaType.TEXT_EVENT_STREAM)
                .retrieve()
                .bodyToFlux(new ParameterizedTypeReference<ServerSentEvent<String>>() {});

            Object stream = new Object();
            sseStreams.put(serverId, stream);
            Disposable subscription = sseFlux.subscribe(
                event -> handleSseEvent(serverId, server, event, ready, responseSink),
                error -> onSseStreamEnded(serverId, stream, "failed: " + error.getMessage()),
                () -> onSseStreamEnded(serverId, stream, "closed")
            );

            sseSubscriptions.put(serverId, subscription);
        })
        .timeout(java.time.Duration.ofSeconds(10))
        // Defer is critical: sendStdioInitializeAndWait reads serverSessions which is only
        // populated after Mono.create completes. Without defer, it evaluates at assembly time
        // when the session ID is null, causing initialization to be silently skipped.
        .then(Mono.defer(() -> sendStdioInitializeAndWait(serverId, client)))
        .onErrorResume(e -> {
            log.warn("Failed to initialize stdio connection for {}: {}", server.name(), e.getMessage());
            return Mono.empty();
        });
    }

    /**
     * One event on a stdio server's SSE stream: the endpoint event carries the session id;
     * a tools/list_changed notification re-lists the server's tools; any other JSON-RPC
     * message is a response for a waiting request.
     */
    private void handleSseEvent(String serverId, ServerRegistration server, ServerSentEvent<String> event,
            MonoSink<Void> ready, Sinks.Many<McpMessage> responseSink) {
        String data = event.data();
        if (data == null) {
            return;
        }
        if ("endpoint".equals(event.event()) && data.contains("sessionId=")) {
            captureSseSession(serverId, server, data);
            ready.success();
            return;
        }
        McpMessage message = data.contains("jsonrpc") ? parseSseMessage(data) : null;
        if (message == null) {
            return;
        }
        if (MCP_NOTIFICATION_TOOLS_CHANGED.equals(message.method())) {
            log.info("{} announced that its tool list changed, re-listing", server.name());
            refreshTools(serverId);
            return;
        }
        log.debug("Received SSE response with id={} for {}", message.id(), server.name());
        responseSink.tryEmitNext(message);
    }

    private void captureSseSession(String serverId, ServerRegistration server, String endpointData) {
        String sessionId = endpointData.substring(endpointData.indexOf("sessionId=") + 10);
        if (sessionId.contains("&")) {
            sessionId = sessionId.substring(0, sessionId.indexOf("&"));
        }
        serverSessions.put(serverId, sessionId);
        log.info("Captured supergateway session ID for {}: {}", server.name(), sessionId);
    }

    private McpMessage parseSseMessage(String data) {
        try {
            return objectMapper.readValue(data, McpMessage.class);
        } catch (Exception e) {
            log.debug("Failed to parse SSE message: {}", e.getMessage());
            return null;
        }
    }

    /**
     * The SSE stream to a stdio server ended: the bridge or its pod went away, so the
     * session is gone and the server behind the URL may come back with other tools. Mark
     * the server DISCONNECTED; the next tool call or re-registration opens a new session
     * and re-lists. A stream that a newer one already replaced changes nothing.
     */
    void onSseStreamEnded(String serverId, Object stream, String how) {
        if (!sseStreams.remove(serverId, stream)) {
            return;
        }
        sseSubscriptions.remove(serverId);
        serverSessions.remove(serverId);
        setStatus(serverId, ServerStatus.DISCONNECTED);
        ServerRegistration server = servers.get(serverId);
        log.warn("SSE stream to {} {}; its tools are re-listed with the next session",
            server == null ? serverId : server.name(), how);
    }

    private Mono<Void> sendStdioInitializeAndWait(String serverId, WebClient client) {
        String sessionId = serverSessions.get(serverId);
        if (sessionId == null) {
            log.warn("No session ID for stdio server {}, skipping initialize", serverId);
            return Mono.empty();
        }

        McpMessage initRequest = McpMessage.request(1, "initialize", MCP_INIT_PARAMS);

        McpMessage initialized = McpMessage.notification(MCP_NOTIFICATION_INITIALIZED);

        // Send initialize, wait for bridge to relay to stdin, then send notifications/initialized.
        // For stdio/bridge transport, responses come via SSE but we don't need to wait for them here —
        // the critical thing is that the server transitions out of initialization state before tools/list.
        return client.post()
            .uri(SESSION_URI_PREFIX + sessionId)
            .bodyValue(initRequest)
            .retrieve()
            .toBodilessEntity()
            .doOnSuccess(r -> log.info("Sent stdio initialize for {}", serverId))
            .then(Mono.delay(java.time.Duration.ofMillis(500)))
            .then(Mono.defer(() ->
                client.post()
                    .uri(SESSION_URI_PREFIX + sessionId)
                    .bodyValue(initialized)
                    .retrieve()
                    .toBodilessEntity()
                    .doOnSuccess(r -> log.info("Sent notifications/initialized for {}", serverId))
                    .then(Mono.delay(java.time.Duration.ofMillis(500)))
                    .then()
            ))
            .onErrorResume(e -> {
                log.warn("Initialize handshake error for {}: {}", serverId, e.getMessage());
                return Mono.empty();
            });
    }

    // Get MCP endpoint based on transport type
    private String getMcpEndpoint(String transport) {
        // Streamable-http (http) uses /mcp
        if ("http".equalsIgnoreCase(transport) || "streamable-http".equalsIgnoreCase(transport)) {
            return "/mcp";
        }
        // Stdio transport uses supergateway which expects /message
        if (TRANSPORT_STDIO.equalsIgnoreCase(transport)) {
            return "/message";
        }
        // SSE uses /mcp/messages (legacy)
        return "/mcp/messages";
    }

    /**
     * Dispose any active SSE subscription and drop the cached session/sink for a server.
     * Used to clear a stale session before re-initializing.
     */
    private void cleanupSseSession(String serverId) {
        serverSessions.remove(serverId);
        sseStreams.remove(serverId);
        Disposable oldSub = sseSubscriptions.remove(serverId);
        if (oldSub != null) oldSub.dispose();
        responseSinks.remove(serverId);
    }

    /**
     * Convert a single MCP tool entry (a Map) into a ToolInfo.
     */
    @SuppressWarnings("unchecked")
    private ToolInfo toToolInfo(Object toolEntry, String serverId, ServerRegistration server) {
        Map<String, Object> toolMap = (Map<String, Object>) toolEntry;
        return new ToolInfo(
            (String) toolMap.get(FIELD_NAME),
            (String) toolMap.get(FIELD_DESCRIPTION),
            toolMap.get(FIELD_INPUT_SCHEMA) instanceof Map ?
                (Map<String, Object>) toolMap.get(FIELD_INPUT_SCHEMA) : Map.of(),
            serverId,
            server.name()
        );
    }

    /**
     * Extract the tool list from an MCP tools/list result object.
     * Returns an empty list if the result does not carry a tools array.
     */
    private List<ToolInfo> parseToolList(Object resultObj, String serverId, ServerRegistration server) {
        if (resultObj instanceof Map<?, ?> result) {
            Object toolsObj = result.get("tools");
            if (toolsObj instanceof List<?> tools) {
                return tools.stream()
                    .filter(Map.class::isInstance)
                    .map(t -> toToolInfo(t, serverId, server))
                    .toList();
            }
        }
        return Collections.emptyList();
    }

    @SuppressWarnings("unchecked")
    private Mono<List<ToolInfo>> discoverTools(String serverId) {
        return discoverToolsInternal(serverId, true);
    }

    @SuppressWarnings("unchecked")
    private Mono<List<ToolInfo>> discoverToolsInternal(String serverId, boolean allowRetry) {
        WebClient client = clients.get(serverId);
        ServerRegistration server = servers.get(serverId);

        if (client == null || server == null) {
            return Mono.just(Collections.emptyList());
        }

        boolean isStdio = TRANSPORT_STDIO.equalsIgnoreCase(server.transport());

        // For stdio transport, use SSE-based communication
        if (isStdio) {
            return discoverToolsViaSSE(serverId, client, server, allowRetry);
        }

        // For other transports, use direct HTTP request
        return discoverToolsViaHttp(serverId, client, server, allowRetry);
    }

    @SuppressWarnings("unchecked")
    private Mono<List<ToolInfo>> discoverToolsViaSSE(String serverId, WebClient client,
            ServerRegistration server, boolean allowRetry) {
        String sessionId = serverSessions.get(serverId);
        Sinks.Many<McpMessage> responseSink = responseSinks.get(serverId);

        if (sessionId == null || responseSink == null) {
            if (allowRetry) {
                log.info("No active SSE session for {}, re-initializing", server.name());
                return initializeConnection(serverId)
                    .then(Mono.defer(() -> discoverToolsViaSSE(serverId, client, server, false)));
            }
            log.warn("No SSE session for stdio server {}", serverId);
            return Mono.just(Collections.emptyList());
        }

        // A fresh id per listing: the response sink replays earlier messages, and a
        // re-list on the same session must not take an older listing's reply.
        long listId = upstreamIds.incrementAndGet();
        McpMessage listToolsRequest = McpMessage.request(listId, METHOD_TOOLS_LIST, Map.of());
        log.info("Discovering tools for {} via SSE with session: {}", server.name(), sessionId);

        // POST the request, check for session errors, then wait for SSE response
        return client.post()
            .uri(SESSION_URI_PREFIX + sessionId)
            .bodyValue(listToolsRequest)
            .exchangeToMono(response -> handleSseDiscoverResponse(
                response, serverId, client, server, responseSink, allowRetry, listId))
            .doOnError(e -> log.error("Error discovering tools via SSE for {}: {}", serverId, e.getMessage()))
            .onErrorReturn(Collections.emptyList());
    }

    /**
     * Handle the POST response for SSE tool discovery: retry on session error, log non-2xx,
     * or wait for the tools/list response on the SSE stream.
     */
    private Mono<List<ToolInfo>> handleSseDiscoverResponse(ClientResponse response, String serverId,
            WebClient client, ServerRegistration server, Sinks.Many<McpMessage> responseSink,
            boolean allowRetry, long listId) {
        int statusCode = response.statusCode().value();

        // Session expired or SSE closed - re-initialize and retry
        if (isSessionLost(server, statusCode) && allowRetry) {
            log.warn("Session error ({}) during tool discovery for {}, re-initializing", statusCode, server.name());
            cleanupSseSession(serverId);
            return initializeConnection(serverId)
                .then(Mono.defer(() -> discoverToolsViaSSE(serverId, client, server, false)));
        }

        if (!response.statusCode().is2xxSuccessful()) {
            log.error("Error discovering tools for {}: HTTP {}", server.name(), statusCode);
            return Mono.just(Collections.<ToolInfo>emptyList());
        }

        // POST accepted - wait for response on SSE stream
        return awaitSseToolList(responseSink, serverId, server, listId);
    }

    /**
     * Wait for the tools/list response with the given id on the SSE response stream and parse it.
     */
    private Mono<List<ToolInfo>> awaitSseToolList(Sinks.Many<McpMessage> responseSink,
            String serverId, ServerRegistration server, long listId) {
        String expectedId = String.valueOf(listId);
        return responseSink.asFlux()
            .filter(msg -> msg.id() != null && expectedId.equals(String.valueOf(msg.id())))
            .next()
            .timeout(java.time.Duration.ofSeconds(10))
            .map(mcpResponse -> {
                if (hasToolsArray(mcpResponse.result())) {
                    List<ToolInfo> toolList = parseToolList(mcpResponse.result(), serverId, server);
                    log.info("Discovered {} tools from {} via SSE", toolList.size(), server.name());
                    return toolList;
                }
                return Collections.<ToolInfo>emptyList();
            });
    }

    /**
     * True when an MCP result object carries a "tools" array.
     */
    private boolean hasToolsArray(Object resultObj) {
        return resultObj instanceof Map<?, ?> result && result.get("tools") instanceof List<?>;
    }

    private Mono<List<ToolInfo>> discoverToolsViaHttp(String serverId, WebClient client,
            ServerRegistration server, boolean allowRetry) {
        McpMessage listToolsRequest = McpMessage.request(upstreamIds.incrementAndGet(), METHOD_TOOLS_LIST, Map.of());
        String endpoint = getMcpEndpoint(server.transport());
        String sessionId = serverSessions.get(serverId);
        log.info("Discovering tools for {} via HTTP", server.name());

        var requestSpec = client.post()
            .uri(endpoint)
            .header(ACCEPT_HEADER, ACCEPT_JSON_SSE);

        if (sessionId != null) {
            requestSpec = requestSpec.header(MCP_SESSION_HEADER, sessionId);
        }

        return requestSpec
            .bodyValue(listToolsRequest)
            .exchangeToMono(response -> {
                int statusCode = response.statusCode().value();

                if (isSessionLost(server, statusCode) && allowRetry) {
                    log.warn("Session error ({}) during tool discovery for server {}", statusCode, serverId);
                    serverSessions.remove(serverId);
                    return initializeConnection(serverId)
                        .then(Mono.defer(() -> discoverToolsViaHttp(serverId, client, server, false)));
                }

                if (!response.statusCode().is2xxSuccessful()) {
                    return response.bodyToMono(String.class)
                        .doOnNext(body -> log.error("Error discovering tools for {}: HTTP {} - {}",
                            serverId, statusCode, body))
                        .then(Mono.just(Collections.<ToolInfo>emptyList()));
                }

                return response.bodyToFlux(new ParameterizedTypeReference<ServerSentEvent<String>>() {})
                    .filter(event -> event.data() != null && !event.data().isEmpty())
                    .next()
                    .flatMap(event -> parseHttpToolListEvent(event.data(), serverId, server))
                    .defaultIfEmpty(Collections.emptyList());
            })
            .doOnError(e -> log.error("Error discovering tools for {}: {}", serverId, e.getMessage()))
            .onErrorReturn(Collections.emptyList());
    }

    /**
     * Parse a tools/list SSE event payload into a tool list, returning empty on parse failure.
     */
    private Mono<List<ToolInfo>> parseHttpToolListEvent(String data, String serverId, ServerRegistration server) {
        try {
            McpMessage mcpResponse = objectMapper.readValue(data, McpMessage.class);
            return Mono.just(parseToolList(mcpResponse.result(), serverId, server));
        } catch (Exception e) {
            log.warn("Failed to parse tools SSE event: {}", e.getMessage());
            return Mono.just(Collections.<ToolInfo>emptyList());
        }
    }

    /**
     * Forward a request to a backend server. Every upstream request gets an id unique
     * across the gateway, and the reply carries the caller's id again: callers choose
     * ids independently (a timestamp, or a client's own counter starting at 1), and a
     * backend that sees two in-flight requests with one id on a session answers only
     * one of them.
     */
    public Mono<McpMessage> forwardRequest(String serverId, McpMessage request) {
        Object callerId = request.id();
        if (callerId == null) {
            return forwardRequestInternal(serverId, request, true);
        }
        return forwardRequestInternal(serverId, request.withId(upstreamIds.incrementAndGet()), true)
            .map(reply -> reply.withId(callerId));
    }

    /**
     * Internal forward request with retry support.
     * If session is invalid (400/401), re-initializes and retries once.
     */
    private Mono<McpMessage> forwardRequestInternal(String serverId, McpMessage request, boolean allowRetry) {
        WebClient client = clients.get(serverId);
        ServerRegistration server = servers.get(serverId);
        if (client == null || server == null) {
            return Mono.just(McpMessage.error(request.id(), -32600, "Server not found: " + serverId));
        }

        if (allowRetry) {
            Mono<Void> connecting = connectForRequest(serverId);
            if (connecting != null) {
                return connecting.then(Mono.defer(() -> forwardRequestInternal(serverId, request, false)))
                    .onErrorResume(e -> unreachable(request, e));
            }
        }

        boolean isStdio = TRANSPORT_STDIO.equalsIgnoreCase(server.transport());

        // For stdio transport, use SSE-based communication
        if (isStdio) {
            return forwardRequestViaSSE(serverId, client, server, request, allowRetry);
        }

        // For other transports, use direct HTTP request
        return forwardRequestViaHttp(serverId, client, server, request, allowRetry);
    }

    /**
     * A request for a server that is not usable (its first connect failed, its session was
     * lost, or it listed no tools) opens a new session first, so a caller does not wait for
     * the next re-registration. The caller that finds the server in that state starts the
     * connect; the request is then sent once, with no further retry.
     */
    private Mono<McpMessage> reconnectThenForward(String serverId, McpMessage request) {
        return reconnectForRequest(serverId)
            .then(Mono.defer(() -> forwardRequestInternal(serverId, request, false)))
            .onErrorResume(e -> unreachable(request, e));
    }

    /**
     * The one retry both transports use when a session is lost or the connection fails: drop
     * the dead session, connect (shared with every request waiting on the same server) and
     * send the request once more over the transport's own path, with no further retry.
     */
    private Mono<McpMessage> reconnectAndResend(String serverId, McpMessage request) {
        cleanupSseSession(serverId);
        return reconnectThenForward(serverId, request);
    }

    private static Mono<McpMessage> unreachable(McpMessage request, Throwable e) {
        String reason = e.getMessage() == null ? e.getClass().getSimpleName() : e.getMessage();
        return Mono.just(McpMessage.error(request.id(), -32603, "Server not reachable: " + reason));
    }

    /**
     * The connect to wait for before forwarding: the one already started for a request, a
     * new one when the server is DISCONNECTED or ERROR, or null when the server is usable.
     * A server that is connected but lists no tools is left to re-registration, so a server
     * with no tools by design is not reconnected by every request.
     */
    private Mono<Void> connectForRequest(String serverId) {
        Mono<Void> pending = requestReconnects.get(serverId);
        if (pending != null) {
            return pending;
        }
        return claimReconnect(serverId, McpClientManager::isDown) ? reconnectForRequest(serverId) : null;
    }

    private static boolean isDown(ServerRegistration server) {
        return server.status() == ServerStatus.DISCONNECTED || server.status() == ServerStatus.ERROR;
    }

    /** One connect per server at a time; every request that needs it waits on the same one. */
    private Mono<Void> reconnectForRequest(String serverId) {
        return requestReconnects.computeIfAbsent(serverId, id -> {
            log.info("Server {} is not connected, connecting before forwarding the request", id);
            return reinitialize(id).doFinally(signal -> requestReconnects.remove(id)).share();
        });
    }

    /** A connection that was refused never reached the server, so the request is safe to send again. */
    private static boolean isConnectRefused(Throwable e) {
        for (Throwable t = e; t != null; t = t.getCause()) {
            if (t instanceof java.net.ConnectException) {
                return true;
            }
        }
        return false;
    }

    /**
     * A failed forward on either transport. A transport failure marks the server ERROR. A
     * refused connection (never reached the server) or, over SSE only, a reply timeout is retried once
     * after a reconnect when a retry is allowed; any other error, such as a reset after the
     * request was sent, is returned to the caller so a tool call never runs twice.
     */
    private Mono<McpMessage> recoverForwardError(Throwable e, String serverId, McpMessage request,
            boolean allowRetry, boolean replayOnTimeout) {
        log.warn("Error forwarding request to {}: {}", serverId, e.getMessage());
        if (e instanceof WebClientRequestException) {
            setStatus(serverId, ServerStatus.ERROR);
        }
        boolean retryable = (replayOnTimeout && e instanceof TimeoutException) || isConnectRefused(e);
        if (allowRetry && retryable) {
            return reconnectAndResend(serverId, request);
        }
        return Mono.just(McpMessage.error(request.id(), -32603, "Internal error: " + e.getMessage()));
    }

    private Mono<McpMessage> forwardRequestViaSSE(String serverId, WebClient client,
            ServerRegistration server, McpMessage request, boolean allowRetry) {
        String sessionId = serverSessions.get(serverId);
        Sinks.Many<McpMessage> responseSink = responseSinks.get(serverId);

        if (sessionId == null || responseSink == null) {
            if (allowRetry) {
                log.info("No active SSE session for {}, re-initializing", server.name());
                return reconnectAndResend(serverId, request);
            }
            return Mono.just(McpMessage.error(request.id(), -32600, "No SSE session for server"));
        }

        log.debug("Forwarding request to {} via SSE", server.name());

        // POST the request, check for session errors, then wait for SSE response
        return client.post()
            .uri(SESSION_URI_PREFIX + sessionId)
            .bodyValue(request)
            .exchangeToMono(response -> handleSseForwardResponse(
                response, serverId, client, server, request, responseSink, allowRetry))
            .onErrorResume(e -> recoverForwardError(e, serverId, request, allowRetry, true));
    }

    /**
     * Handle the POST response for an SSE forward: retry on session error, map non-2xx to an
     * error message, or wait for the matching response on the SSE stream.
     */
    private Mono<McpMessage> handleSseForwardResponse(ClientResponse response, String serverId,
            WebClient client, ServerRegistration server, McpMessage request,
            Sinks.Many<McpMessage> responseSink, boolean allowRetry) {
        int statusCode = response.statusCode().value();

        // Session expired or SSE closed - re-initialize and retry
        if (isSessionLost(server, statusCode) && allowRetry) {
            log.warn("Session error ({}) for stdio server {}, re-initializing", statusCode, server.name());
            return reconnectAndResend(serverId, request);
        }

        if (!response.statusCode().is2xxSuccessful()) {
            return response.bodyToMono(String.class)
                .defaultIfEmpty("Unknown error")
                .map(body -> McpMessage.error(request.id(), -32603,
                    "HTTP " + statusCode + ": " + body));
        }

        // POST accepted - wait for response on SSE stream
        // Use String comparison for IDs to avoid numeric type mismatch (Long vs Integer)
        String expectedId = String.valueOf(request.id());
        return responseSink.asFlux()
            .filter(msg -> msg.id() != null && String.valueOf(msg.id()).equals(expectedId))
            .next()
            .timeout(java.time.Duration.ofSeconds(30));
    }

    private Mono<McpMessage> forwardRequestViaHttp(String serverId, WebClient client,
            ServerRegistration server, McpMessage request, boolean allowRetry) {
        String endpoint = getMcpEndpoint(server.transport());
        String sessionId = serverSessions.get(serverId);
        log.debug("Forwarding request to {} via HTTP", server.name());

        var requestSpec = client.post()
            .uri(endpoint)
            .header(ACCEPT_HEADER, ACCEPT_JSON_SSE);

        if (sessionId != null) {
            requestSpec = requestSpec.header(MCP_SESSION_HEADER, sessionId);
        }

        return requestSpec
            .bodyValue(request)
            .exchangeToMono(response -> {
                int statusCode = response.statusCode().value();

                if (isSessionLost(server, statusCode) && allowRetry) {
                    log.warn("Session error ({}) for server {}", statusCode, serverId);
                    return reconnectAndResend(serverId, request);
                }

                if (!response.statusCode().is2xxSuccessful()) {
                    return response.bodyToMono(String.class)
                        .defaultIfEmpty("Unknown error")
                        .map(body -> McpMessage.error(request.id(), -32603,
                            "HTTP " + statusCode + ": " + body));
                }

                return response.bodyToFlux(new ParameterizedTypeReference<ServerSentEvent<String>>() {})
                    .filter(event -> event.data() != null && !event.data().isEmpty())
                    .next()
                    .flatMap(event -> {
                        try {
                            return Mono.just(objectMapper.readValue(event.data(), McpMessage.class));
                        } catch (Exception e) {
                            return Mono.just(McpMessage.error(request.id(), -32603,
                                "Failed to parse response: " + e.getMessage()));
                        }
                    })
                    .defaultIfEmpty(McpMessage.error(request.id(), -32603, "Empty response from server"));
            })
            .onErrorResume(e -> recoverForwardError(e, serverId, request, allowRetry, false));
    }

    /**
     * Get and clear feedback entries for a server (consumed by operator).
     */
    public List<FeedbackEntry> consumeFeedback(String serverId) {
        List<FeedbackEntry> entries = feedbackLog.remove(serverId);
        return entries != null ? entries : Collections.emptyList();
    }

    /**
     * Get all feedback entries across all servers (consumed by operator).
     */
    public Map<String, List<FeedbackEntry>> consumeAllFeedback() {
        Map<String, List<FeedbackEntry>> all = new ConcurrentHashMap<>(feedbackLog);
        feedbackLog.clear();
        return all;
    }

    private void recordFeedback(FeedbackEntry entry) {
        feedbackLog.computeIfAbsent(entry.serverId(), k -> Collections.synchronizedList(new ArrayList<>()))
            .add(entry);
    }

    public Flux<String> connectSse(String serverId) {
        WebClient client = clients.get(serverId);
        if (client == null) {
            return Flux.error(new IllegalStateException("Server not found: " + serverId));
        }

        return client.get()
            .uri("/mcp")
            .retrieve()
            .bodyToFlux(String.class);
    }
}
