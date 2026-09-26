package kubemoot.ai.mcpgateway.service;

import jakarta.annotation.PostConstruct;
import jakarta.annotation.PreDestroy;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.ClientResponse;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.core.publisher.Sinks;
import reactor.core.Disposable;
import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.FeedbackEntry;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ServerRegistration.ServerStatus;
import kubemoot.ai.mcpgateway.model.ToolInfo;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

@Service
public class McpClientManager {

    private static final Logger log = LoggerFactory.getLogger(McpClientManager.class);
    private static final String MCP_SESSION_HEADER = "mcp-session-id";
    private static final String ACCEPT_HEADER = "Accept";
    private static final String ACCEPT_JSON_SSE = "application/json, text/event-stream";
    private static final String TRANSPORT_STDIO = "stdio";
    private static final String SESSION_URI_PREFIX = "/message?sessionId=";
    private static final String MCP_NOTIFICATION_INITIALIZED = "notifications/initialized";

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
    private final Map<String, String> serverSessions = new ConcurrentHashMap<>();
    // For supergateway: active SSE subscriptions and response sinks
    private final Map<String, Disposable> sseSubscriptions = new ConcurrentHashMap<>();
    private final Map<String, Sinks.Many<McpMessage>> responseSinks = new ConcurrentHashMap<>();
    // Feedback log: server ID → list of feedback entries (consumed and cleared by operator)
    private final Map<String, List<FeedbackEntry>> feedbackLog = new ConcurrentHashMap<>();

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
        // Clean up SSE subscriptions
        sseSubscriptions.values().forEach(Disposable::dispose);
        sseSubscriptions.clear();
        responseSinks.values().forEach(Sinks.Many::tryEmitComplete);
        responseSinks.clear();
    }

    public ServerRegistration registerServer(String name, String url, String transport) {
        // Check if a server with the same name already exists
        ServerRegistration existing = findServerByName(name);
        if (existing != null) {
            // Server already registered - check if URL changed
            if (existing.url().equals(url)) {
                // Self-heal a previously FAILED discovery: if the server is
                // registered but has 0 tools cached, its initial connect/discover
                // didn't succeed (e.g. a transient split-brain before Service
                // session-affinity, or a cold-start race). Don't cache that empty
                // result forever — re-run connect+discover. The operator
                // re-registers periodically, so this recovers without a gateway
                // restart (the old workaround). Observed 2026-05-27.
                if (getToolsForServer(existing.id()).isEmpty()) {
                    log.info("Server {} registered but has 0 tools — re-discovering", name);
                    connectToServer(existing.id());
                } else {
                    log.debug("Server {} already registered with same URL, skipping re-registration", name);
                }
                return existing;
            } else {
                // URL changed, unregister old and register new
                log.info("Server {} URL changed from {} to {}, re-registering", name, existing.url(), url);
                unregisterServer(existing.id());
            }
        }

        ServerRegistration registration = ServerRegistration.create(name, url, transport);
        registerServerInternal(registration);
        return registration;
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
        serverSessions.remove(serverId);
        log.info("Unregistered MCP server: {}", serverId);
    }

    public List<ServerRegistration> listServers() {
        return new ArrayList<>(servers.values());
    }

    public ServerRegistration getServer(String serverId) {
        return servers.get(serverId);
    }

    public List<ToolInfo> getAllTools() {
        return serverTools.values().stream()
            .flatMap(List::stream)
            .toList();
    }

    public List<ToolInfo> getToolsForServer(String serverId) {
        return serverTools.getOrDefault(serverId, Collections.emptyList());
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
            .subscribe(
                tools -> {
                    serverTools.put(serverId, tools);
                    servers.put(serverId, servers.get(serverId).withStatus(ServerStatus.CONNECTED));
                    log.info("Connected to MCP server {} with {} tools", server.name(), tools.size());
                    recordFeedback(FeedbackEntry.success(server.name(), serverId, "connect"));
                    recordFeedback(FeedbackEntry.success(server.name(), serverId, "discover", tools.size()));
                },
                error -> {
                    log.error("Failed to connect to MCP server {}: {}", server.name(), error.getMessage());
                    servers.put(serverId, servers.get(serverId).withStatus(ServerStatus.ERROR));
                    recordFeedback(FeedbackEntry.failure(server.name(), serverId, "connect", error.getMessage()));
                }
            );
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

        return Mono.create(monoSink -> {
            // Start SSE connection and keep it open in background
            Flux<ServerSentEvent<String>> sseFlux = client.get()
                .uri("/sse")
                .accept(org.springframework.http.MediaType.TEXT_EVENT_STREAM)
                .retrieve()
                .bodyToFlux(new ParameterizedTypeReference<ServerSentEvent<String>>() {});

            Disposable subscription = sseFlux.subscribe(
                event -> {
                    String eventType = event.event();
                    String data = event.data();

                    if ("endpoint".equals(eventType) && data != null && data.contains("sessionId=")) {
                        // Extract and store session ID
                        String sessionId = data.substring(data.indexOf("sessionId=") + 10);
                        if (sessionId.contains("&")) {
                            sessionId = sessionId.substring(0, sessionId.indexOf("&"));
                        }
                        serverSessions.put(serverId, sessionId);
                        log.info("Captured supergateway session ID for {}: {}", server.name(), sessionId);
                        // Signal that we're ready to proceed
                        monoSink.success();
                    } else if (data != null && data.contains("jsonrpc")) {
                        // Parse and emit response message (handles both "message" event type and no event type)
                        try {
                            McpMessage response = objectMapper.readValue(data, McpMessage.class);
                            log.debug("Received SSE response with id={} for {}", response.id(), server.name());
                            responseSink.tryEmitNext(response);
                        } catch (Exception e) {
                            log.debug("Failed to parse SSE message: {}", e.getMessage());
                        }
                    }
                },
                error -> {
                    log.warn("SSE connection error for {}: {}", server.name(), error.getMessage());
                    sseSubscriptions.remove(serverId);
                    serverSessions.remove(serverId);
                },
                () -> {
                    log.info("SSE connection closed for {}", server.name());
                    sseSubscriptions.remove(serverId);
                    serverSessions.remove(serverId);
                }
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

        McpMessage listToolsRequest = McpMessage.request(2, "tools/list", Map.of());
        log.info("Discovering tools for {} via SSE with session: {}", server.name(), sessionId);

        // POST the request, check for session errors, then wait for SSE response
        return client.post()
            .uri(SESSION_URI_PREFIX + sessionId)
            .bodyValue(listToolsRequest)
            .exchangeToMono(response -> handleSseDiscoverResponse(
                response, serverId, client, server, responseSink, allowRetry))
            .doOnError(e -> log.error("Error discovering tools via SSE for {}: {}", serverId, e.getMessage()))
            .onErrorReturn(Collections.emptyList());
    }

    /**
     * Handle the POST response for SSE tool discovery: retry on session error, log non-2xx,
     * or wait for the tools/list response on the SSE stream.
     */
    private Mono<List<ToolInfo>> handleSseDiscoverResponse(ClientResponse response, String serverId,
            WebClient client, ServerRegistration server, Sinks.Many<McpMessage> responseSink,
            boolean allowRetry) {
        int statusCode = response.statusCode().value();

        // Session expired or SSE closed - re-initialize and retry
        if ((statusCode == 400 || statusCode == 503) && allowRetry) {
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
        return awaitSseToolList(responseSink, serverId, server);
    }

    /**
     * Wait for the tools/list response (id=2) on the SSE response stream and parse it.
     */
    private Mono<List<ToolInfo>> awaitSseToolList(Sinks.Many<McpMessage> responseSink,
            String serverId, ServerRegistration server) {
        return responseSink.asFlux()
            .filter(msg -> msg.id() != null && "2".equals(String.valueOf(msg.id())))
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
        McpMessage listToolsRequest = McpMessage.request(2, "tools/list", Map.of());
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

                if ((statusCode == 400 || statusCode == 401) && allowRetry) {
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

    public Mono<McpMessage> forwardRequest(String serverId, McpMessage request) {
        return forwardRequestInternal(serverId, request, true);
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

        boolean isStdio = TRANSPORT_STDIO.equalsIgnoreCase(server.transport());

        // For stdio transport, use SSE-based communication
        if (isStdio) {
            return forwardRequestViaSSE(serverId, client, server, request, allowRetry);
        }

        // For other transports, use direct HTTP request
        return forwardRequestViaHttp(serverId, client, server, request, allowRetry);
    }

    private Mono<McpMessage> forwardRequestViaSSE(String serverId, WebClient client,
            ServerRegistration server, McpMessage request, boolean allowRetry) {
        String sessionId = serverSessions.get(serverId);
        Sinks.Many<McpMessage> responseSink = responseSinks.get(serverId);

        if (sessionId == null || responseSink == null) {
            if (allowRetry) {
                log.info("No active SSE session for {}, re-initializing", server.name());
                return initializeConnection(serverId)
                    .then(Mono.defer(() -> forwardRequestViaSSE(serverId, client, server, request, false)));
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
            .onErrorResume(e -> handleSseForwardError(e, serverId, client, server, request, allowRetry));
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
        if ((statusCode == 400 || statusCode == 503) && allowRetry) {
            log.warn("Session error ({}) for stdio server {}, re-initializing", statusCode, server.name());
            cleanupSseSession(serverId);
            return initializeConnection(serverId)
                .then(Mono.defer(() -> forwardRequestViaSSE(serverId, client, server, request, false)));
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

    /**
     * Resume an SSE forward error: re-initialize and retry on timeout when allowed, else
     * surface an internal-error message.
     */
    private Mono<McpMessage> handleSseForwardError(Throwable e, String serverId, WebClient client,
            ServerRegistration server, McpMessage request, boolean allowRetry) {
        log.error("Error forwarding request via SSE to {}: {}", serverId, e.getMessage());
        // On timeout or other error, try to re-initialize if allowed
        if (allowRetry && e instanceof java.util.concurrent.TimeoutException) {
            log.info("SSE response timeout for {}, re-initializing", server.name());
            cleanupSseSession(serverId);
            return initializeConnection(serverId)
                .then(Mono.defer(() -> forwardRequestViaSSE(serverId, client, server, request, false)));
        }
        return Mono.just(McpMessage.error(request.id(), -32603, "Internal error: " + e.getMessage()));
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

                if ((statusCode == 400 || statusCode == 401) && allowRetry) {
                    log.warn("Session error ({}) for server {}", statusCode, serverId);
                    serverSessions.remove(serverId);
                    return initializeConnection(serverId)
                        .then(Mono.defer(() -> forwardRequestViaHttp(serverId, client, server, request, false)));
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
            .onErrorResume(e -> {
                log.error("Error forwarding request to {}: {}", serverId, e.getMessage());
                return Mono.just(McpMessage.error(request.id(), -32603, "Internal error: " + e.getMessage()));
            });
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
