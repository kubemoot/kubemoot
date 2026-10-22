package ai.kubemoot.agent.gateway;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import jakarta.annotation.PostConstruct;
import jakarta.enterprise.context.ApplicationScoped;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.LinkedHashMap;

/**
 * Client for interacting with the MCP Gateway.
 *
 * The gateway provides a unified endpoint for accessing multiple MCP servers.
 * Tools are accessed via the gateway's routing mechanism rather than direct
 * connections to individual MCP servers.
 */
@ApplicationScoped
public class GatewayClient {

    private static final Logger log = LoggerFactory.getLogger(GatewayClient.class);
    private static final Duration DEFAULT_TIMEOUT = Duration.ofSeconds(30);

    private final AgentProperties.Gateway gatewayConfig;
    private final HttpClient httpClient;
    private final ObjectMapper mapper;
    private final String endpoint;
    /** Why the last tool-list read failed; null when it succeeded. */
    private volatile String lastListError;

    public GatewayClient(AgentProperties properties, ObjectMapper mapper) {
        this.gatewayConfig = properties.gateway();
        this.mapper = mapper;
        this.endpoint = getEndpointFromConfig();
        this.httpClient = HttpClient.newBuilder()
                .connectTimeout(Duration.ofSeconds(10))
                .build();
    }

    private String getEndpointFromConfig() {
        return gatewayConfig.endpoint().orElse("");
    }

    @PostConstruct
    public void initialize() {
        if (endpoint != null && !endpoint.isEmpty()) {
            log.info("Gateway client initialized for endpoint: {}", endpoint);
        }
    }

    public boolean isConfigured() {
        return endpoint != null && !endpoint.isEmpty();
    }

    /** The gateway's address, empty when none is configured. */
    public String endpoint() {
        return endpoint == null ? "" : endpoint;
    }

    /**
     * Why the most recent {@link #listTools()} could not read the gateway (it answers an
     * error with an empty list), or empty when that read worked.
     */
    public Optional<String> lastListError() {
        return Optional.ofNullable(lastListError);
    }

    /**
     * List all tools available through the gateway.
     *
     * Body shape: {"count": N, "tools": [{"name": ..., "description": ..., "inputSchema": ...}, ...]}
     * Parsed via readTree to avoid the GraalVM-native Jackson record reflection
     * trap (NPE with null message when ObjectMapper has no reflection metadata
     * for a record type).
     */
    public List<ToolInfo> listTools() {
        try {
            var request = HttpRequest.newBuilder()
                    .uri(URI.create(endpoint + "/admin/tools"))
                    .timeout(DEFAULT_TIMEOUT)
                    .GET()
                    .build();

            var response = httpClient.send(request, HttpResponse.BodyHandlers.ofString());
            if (response.statusCode() < 200 || response.statusCode() >= 300) {
                return listFailed("gateway answered HTTP " + response.statusCode(), null);
            }
            var out = parseTools(mapper.readTree(response.body()));
            lastListError = null;
            return out;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return listFailed("gateway unreachable: interrupted", e);
        } catch (Exception e) {
            return listFailed("gateway unreachable: " + e.getClass().getSimpleName()
                    + (e.getMessage() == null ? "" : " " + e.getMessage()), e);
        }
    }

    /** Remember why the read failed and answer with the empty list callers already expect. */
    private List<ToolInfo> listFailed(String reason, Exception cause) {
        lastListError = reason;
        log.error("Failed to list gateway tools: {}", reason, cause);
        return List.of();
    }

    private static List<ToolInfo> parseTools(JsonNode root) {
        var toolsNode = root.path("tools");
        if (toolsNode.isMissingNode() || !toolsNode.isArray()) {
            return List.of();
        }
        var out = new java.util.ArrayList<ToolInfo>(toolsNode.size());
        for (var t : toolsNode) {
            out.add(new ToolInfo(
                    t.path("name").asText(""),
                    t.path("description").asText(""),
                    t.path("inputSchema").isMissingNode() ? null : t.get("inputSchema")));
        }
        return out;
    }

    /**
     * Call a tool through the gateway.
     *
     * Request body shape: {"arguments": {...}}
     * Response body shape: {"isError": bool, "content": "..."}
     *
     * Both sides use manual Jackson serialization/deserialization
     * (writeValueAsString on a Map, readTree on the response) to avoid
     * the GraalVM-native record reflection trap. The previous version
     * used `mapper.writeValueAsString(new ToolCallRequest(...))` +
     * `mapper.readValue(body, ToolCallResponse.class)` on Java records,
     * which fails silently with a null-message NPE in native builds.
     */
    public ToolCallResult callTool(String toolName, Map<String, Object> arguments) {
        return callTool(toolName, arguments, Map.of());
    }

    /**
     * Calls the tool with the MCP per-request metadata ({@code _meta}) the gateway forwards
     * to the tool's server; an empty map sends none.
     */
    public ToolCallResult callTool(String toolName, Map<String, Object> arguments, Map<String, Object> meta) {
        log.debug("Calling tool {} via gateway with arguments: {}", toolName, arguments);

        try {
            var body = mapper.writeValueAsString(callBody(arguments, meta));
            var request = HttpRequest.newBuilder()
                    .uri(URI.create(endpoint + "/tools/" + toolName + "/call"))
                    .timeout(DEFAULT_TIMEOUT)
                    .header("Content-Type", "application/json")
                    .POST(HttpRequest.BodyPublishers.ofString(body))
                    .build();

            var response = httpClient.send(request, HttpResponse.BodyHandlers.ofString());
            if (response.statusCode() < 200 || response.statusCode() >= 300) {
                var msg = "Gateway returned HTTP " + response.statusCode() + ": " + response.body();
                log.error("Tool call {} failed: {}", toolName, msg);
                return new ToolCallResult(toolName, false, null, msg);
            }
            var root = mapper.readTree(response.body());
            boolean isError = root.path("isError").asBoolean(false);
            String content = root.path("content").asText("");

            return new ToolCallResult(
                    toolName,
                    !isError,
                    content,
                    isError ? content : null
            );
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            log.error("Tool call {} failed", toolName, e);
            return new ToolCallResult(toolName, false, null, "Gateway error: " + e.getClass().getSimpleName());
        } catch (Exception e) {
            log.error("Tool call {} failed", toolName, e);
            String detail = e.getMessage() != null ? e.getMessage() : e.getClass().getSimpleName();
            return new ToolCallResult(toolName, false, null, "Gateway error: " + detail);
        }
    }

    /** The JSON body of a tool call: the arguments, and {@code _meta} when there is any. */
    static Map<String, Object> callBody(Map<String, Object> arguments, Map<String, Object> meta) {
        var body = new LinkedHashMap<String, Object>();
        body.put("arguments", arguments);
        if (meta != null && !meta.isEmpty()) {
            body.put("_meta", meta);
        }
        return body;
    }

    public String getEndpoint() {
        return endpoint;
    }

    // Public DTOs — caller-side records, never deserialized by Jackson, so
    // safe in native mode. The wire-format DTOs that used to live here
    // (ToolListResponse, ToolInfoDto, ToolCallRequest, ToolCallResponse)
    // were Jackson-deserialized records that hit the GraalVM reflection
    // trap; readTree + manual extraction replaces them.
    public record McpServerInfo(String id, String name, String url, String transport, String status) {}
    public record ToolInfo(String name, String description, Object inputSchema) {}
    public record ToolCallResult(String toolName, boolean success, String result, String error) {}
}
