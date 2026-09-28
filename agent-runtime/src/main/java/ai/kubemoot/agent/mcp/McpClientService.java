package ai.kubemoot.agent.mcp;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.gateway.GatewayClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.agent.tool.ToolExecutionRequest;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.service.tool.ToolExecutor;
import io.modelcontextprotocol.client.McpClient;
import io.modelcontextprotocol.client.McpSyncClient;
import io.modelcontextprotocol.client.transport.HttpClientSseClientTransport;
import io.modelcontextprotocol.spec.McpSchema;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import jakarta.annotation.PostConstruct;
import jakarta.annotation.PreDestroy;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import java.time.Duration;
import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.stream.Collectors;

/**
 * MCP client service for tool integration using LangChain4j's ToolSpecification/ToolExecutor.
 *
 * Supports two modes:
 * 1. Gateway mode (preferred): Connects to MCPGateway which aggregates all MCP servers
 * 2. Direct mode: Connects directly to individual MCP servers
 *
 * Gateway mode is enabled when kubemoot.gateway.enabled=true and a gateway endpoint is configured.
 */
@ApplicationScoped
public class McpClientService {

    private static final Logger log = LoggerFactory.getLogger(McpClientService.class);
    private static final ObjectMapper objectMapper = new ObjectMapper();

    private final AgentProperties properties;
    private final Map<String, McpSyncClient> clients = new ConcurrentHashMap<>();
    private final Map<String, McpSchema.Tool> tools = new ConcurrentHashMap<>();
    // Gateway tools stored separately (from REST API, not MCP protocol)
    private final Map<String, GatewayClient.ToolInfo> gatewayTools = new ConcurrentHashMap<>();
    private boolean gatewayMode = false;

    /** How long a loaded gateway tool list is trusted before an evaluation reads it again. */
    static final Duration TOOL_LIST_TTL = Duration.ofSeconds(60);
    /** How soon an empty tool list is read again: a server may be registering right now. */
    static final Duration EMPTY_TOOL_LIST_RETRY = Duration.ofSeconds(10);
    private volatile Instant gatewayToolsLoadedAt = Instant.EPOCH;

    @Inject
    GatewayClient gatewayClient;

    public McpClientService(AgentProperties properties) {
        this.properties = properties;
    }

    @PostConstruct
    public void initialize() {
        String gatewayEndpoint = getGatewayEndpoint();
        boolean gatewayEnabled = isGatewayEnabled();

        log.info("Gateway config: enabled={}, endpoint={}, gatewayClient={}",
                gatewayEnabled, gatewayEndpoint, gatewayClient != null);

        if (gatewayEnabled && gatewayEndpoint != null && !gatewayEndpoint.isEmpty()) {
            log.info("Gateway mode enabled, using REST API to gateway at: {}", gatewayEndpoint);
            gatewayMode = true;
            loadToolsFromGateway();
        } else {
            var serverConfigs = properties.mcpServers().orElse(List.of());
            log.info("Direct mode: initializing MCP client with {} servers", serverConfigs.size());
            for (var config : serverConfigs) {
                try {
                    connectToServer(config);
                } catch (Exception e) {
                    log.warn("Failed to connect to MCP server {}: {}", config.name(), e.getMessage());
                }
            }
        }
        int totalTools = gatewayMode ? gatewayTools.size() : tools.size();
        log.info("MCP client initialized: {} servers, {} tools, gateway={}",
                clients.size(), totalTools, gatewayMode);
    }

    private void loadToolsFromGateway() {
        if (gatewayClient == null || !gatewayClient.isConfigured()) {
            log.error("Gateway mode enabled but GatewayClient is not available. " +
                    "Check that kubemoot.gateway.enabled=true is set correctly.");
            return;
        }

        try {
            var toolsList = gatewayClient.listTools();
            gatewayToolsLoadedAt = Instant.now();
            if (toolsList != null) {
                for (var tool : toolsList) {
                    gatewayTools.put(tool.name(), tool);
                    log.debug("Loaded gateway tool: {}", tool.name());
                }
                log.info("Loaded {} tools from gateway via REST API", gatewayTools.size());
            } else {
                log.warn("Gateway returned null tool list");
            }
        } catch (Exception e) {
            log.error("Failed to load tools from gateway: {}", e.getMessage(), e);
        }
    }

    /**
     * Re-read the gateway's tool list when it is older than {@link #TOOL_LIST_TTL}, or than
     * {@link #EMPTY_TOOL_LIST_RETRY} while it is empty,
     * so a tool server registered after this agent started is used without a restart.
     * Returns true when the set of tool names changed. An empty or failed read keeps the
     * tools already known: the gateway client answers an error with an empty list.
     */
    public boolean refreshGatewayToolsIfStale() {
        return refreshGatewayToolsIfStale(Instant.now());
    }

    boolean refreshGatewayToolsIfStale(Instant now) {
        if (!gatewayMode || gatewayClient == null || !gatewayClient.isConfigured()) {
            return false;
        }
        var trustedFor = gatewayTools.isEmpty() ? EMPTY_TOOL_LIST_RETRY : TOOL_LIST_TTL;
        if (now.isBefore(gatewayToolsLoadedAt.plus(trustedFor))) {
            return false;
        }
        List<GatewayClient.ToolInfo> fresh;
        try {
            fresh = gatewayClient.listTools();
        } catch (RuntimeException e) {
            log.warn("Gateway tool list refresh failed: {}", e.getMessage());
            return false;
        }
        gatewayToolsLoadedAt = now;
        if (fresh == null || fresh.isEmpty()) {
            return false;
        }
        var names = fresh.stream().map(GatewayClient.ToolInfo::name).collect(Collectors.toSet());
        if (names.equals(gatewayTools.keySet())) {
            return false;
        }
        gatewayTools.keySet().retainAll(names);
        fresh.forEach(tool -> gatewayTools.put(tool.name(), tool));
        log.info("Gateway tool list changed: now {} tools", gatewayTools.size());
        return true;
    }

    private String getGatewayEndpoint() {
        return properties.gateway().endpoint().orElse("");
    }

    private boolean isGatewayEnabled() {
        return properties.gateway().enabled();
    }

    private void connectToServer(AgentProperties.McpServer config) {
        if (config.endpoint() == null || config.endpoint().isEmpty()) {
            log.warn("Skipping MCP server {} - no endpoint configured", config.name());
            return;
        }

        log.info("Connecting to MCP server: {} at {}", config.name(), config.endpoint());

        var transport = HttpClientSseClientTransport.builder(config.endpoint())
                .customizeClient(builder -> builder.connectTimeout(Duration.ofSeconds(30)))
                .build();
        var client = McpClient.sync(transport).requestTimeout(Duration.ofSeconds(60)).build();

        client.initialize();
        log.debug("MCP server {} initialized", config.name());

        var toolsResult = client.listTools();
        var serverEnabledTools = config.enabledTools().orElse(List.of());
        var serverDisabledTools = config.disabledTools().orElse(List.of());

        for (var tool : toolsResult.tools()) {
            if (isServerToolAllowed(tool.name(), serverEnabledTools, serverDisabledTools)) {
                tools.put(config.name() + "." + tool.name(), tool);
            }
        }

        clients.put(config.name(), client);
        log.info("Connected to MCP server {}: {} tools", config.name(), toolsResult.tools().size());
    }

    @PreDestroy
    public void shutdown() {
        clients.values().forEach(client -> {
            try { client.close(); } catch (Exception e) { /* ignore */ }
        });
    }

    public ToolResult callTool(String toolName, Map<String, Object> arguments) {
        if (gatewayMode) {
            return callToolViaGateway(toolName, arguments);
        }

        String serverName;
        String actualToolName;

        int dotIndex = toolName.indexOf('.');
        if (dotIndex > 0) {
            serverName = toolName.substring(0, dotIndex);
            actualToolName = toolName.substring(dotIndex + 1);
        } else {
            serverName = findServerForTool(toolName);
            actualToolName = toolName;
        }

        var client = clients.get(serverName);
        if (client == null) {
            return new ToolResult(toolName, false, null, "Server not connected: " + serverName);
        }

        log.debug("Calling tool {} on server {}", actualToolName, serverName);
        var result = client.callTool(new McpSchema.CallToolRequest(actualToolName, arguments));
        String content = null;
        if (result.content() != null && !result.content().isEmpty()) {
            var first = result.content().getFirst();
            if (first instanceof McpSchema.TextContent tc) content = tc.text();
        }
        return new ToolResult(toolName, !result.isError(), content, result.isError() ? content : null);
    }

    private ToolResult callToolViaGateway(String toolName, Map<String, Object> arguments) {
        if (gatewayClient == null || !gatewayClient.isConfigured()) {
            return new ToolResult(toolName, false, null, "Gateway client not available");
        }

        log.debug("Calling tool {} via gateway REST API", toolName);

        try {
            var result = gatewayClient.callTool(toolName, arguments);
            if (result == null) {
                return new ToolResult(toolName, false, null, "Gateway returned null response");
            }
            return new ToolResult(toolName, result.success(), result.result(), result.error());
        } catch (Exception e) {
            log.error("Gateway tool call failed: {}", e.getMessage());
            return new ToolResult(toolName, false, null, "Gateway error: " + e.getMessage());
        }
    }

    private String findServerForTool(String toolName) {
        for (var key : tools.keySet()) {
            if (key.endsWith("." + toolName)) return key.substring(0, key.indexOf('.'));
        }
        throw new IllegalArgumentException("Tool not found: " + toolName);
    }

    public List<ToolInfo> listTools() {
        if (gatewayMode) {
            return gatewayTools.entrySet().stream()
                    .map(e -> new ToolInfo(e.getKey(), e.getValue().description(), e.getValue().inputSchema()))
                    .toList();
        }
        return tools.entrySet().stream()
                .map(e -> new ToolInfo(e.getKey(), e.getValue().description(), e.getValue().inputSchema()))
                .toList();
    }

    public Map<String, Boolean> healthCheck() {
        if (gatewayMode) {
            return Map.of("gateway", gatewayClient != null && gatewayClient.isConfigured() && !gatewayTools.isEmpty());
        }
        return clients.keySet().stream().collect(java.util.stream.Collectors.toMap(k -> k, k -> true));
    }

    public boolean isGatewayMode() {
        return gatewayMode;
    }

    /**
     * Build LangChain4j ToolSpecification/ToolExecutor pairs for all registered tools.
     * Applies enabledTools/disabledTools filtering.
     */
    public Map<ToolSpecification, ToolExecutor> getToolSpecifications() {
        var enabled = properties.enabledTools().orElse(List.of());
        var disabled = properties.disabledTools().orElse(List.of());
        var result = new LinkedHashMap<ToolSpecification, ToolExecutor>();

        if (gatewayMode) {
            for (var entry : gatewayTools.entrySet()) {
                if (!isToolAllowed(entry.getKey(), enabled, disabled)) continue;
                var spec = buildToolSpec(entry.getKey(), entry.getValue().description(), entry.getValue().inputSchema());
                result.put(spec, createToolExecutor(entry.getKey()));
            }
        } else {
            for (var entry : tools.entrySet()) {
                if (!isToolAllowed(entry.getKey(), enabled, disabled)) continue;
                var spec = buildToolSpec(entry.getKey(), entry.getValue().description(), entry.getValue().inputSchema());
                result.put(spec, createToolExecutor(entry.getKey()));
            }
        }

        if (gatewayMode && result.size() != gatewayTools.size()) {
            log.info("Tool filtering: {} of {} gateway tools registered", result.size(), gatewayTools.size());
        }

        return result;
    }

    private ToolSpecification buildToolSpec(String name, String description, Object inputSchema) {
        var builder = ToolSpecification.builder()
                .name(name)
                .description(description != null ? description : "");

        applyInputSchema(builder, name, inputSchema);

        return builder.build();
    }

    private void applyInputSchema(ToolSpecification.Builder builder, String name, Object inputSchema) {
        if (inputSchema == null) {
            return;
        }
        try {
            String schemaJson = objectMapper.writeValueAsString(inputSchema);
            var schemaNode = objectMapper.readTree(schemaJson);

            // Parse JSON Schema properties into LangChain4j JsonObjectSchema
            if (schemaNode.has("properties")) {
                builder.parameters(buildObjectSchema(schemaNode));
            }
        } catch (Exception e) {
            log.debug("Failed to parse input schema for tool {}: {}", name, e.getMessage());
        }
    }

    private dev.langchain4j.model.chat.request.json.JsonObjectSchema buildObjectSchema(
            com.fasterxml.jackson.databind.JsonNode schemaNode) {
        var schemaBuilder = dev.langchain4j.model.chat.request.json.JsonObjectSchema.builder();
        addStringProperties(schemaBuilder, schemaNode.get("properties"));
        var requiredList = parseRequired(schemaNode);
        if (!requiredList.isEmpty()) {
            schemaBuilder.required(requiredList);
        }
        return schemaBuilder.build();
    }

    private void addStringProperties(
            dev.langchain4j.model.chat.request.json.JsonObjectSchema.Builder schemaBuilder,
            com.fasterxml.jackson.databind.JsonNode propsNode) {
        var propIterator = propsNode.fields();
        while (propIterator.hasNext()) {
            var prop = propIterator.next();
            String paramName = prop.getKey();
            var paramNode = prop.getValue();
            String paramDesc = paramNode.has("description") ? paramNode.get("description").asText() : "";
            schemaBuilder.addStringProperty(paramName, paramDesc);
        }
    }

    private List<String> parseRequired(com.fasterxml.jackson.databind.JsonNode schemaNode) {
        var requiredList = new java.util.ArrayList<String>();
        var requiredNode = schemaNode.has("required") ? schemaNode.get("required") : null;
        if (requiredNode != null && requiredNode.isArray()) {
            requiredNode.forEach(n -> requiredList.add(n.asText()));
        }
        return requiredList;
    }

    private ToolExecutor createToolExecutor(String toolName) {
        return (request, memoryId) -> {
            try {
                Map<String, Object> args = objectMapper.readValue(
                        request.arguments(),
                        new com.fasterxml.jackson.core.type.TypeReference<>() {});
                var result = callTool(toolName, args);
                if (!result.success()) {
                    return "{\"error\": \"" + escape(result.error()) + "\"}";
                }
                return result.result() != null ? result.result() : "{\"success\": true}";
            } catch (Exception e) {
                return "{\"error\": \"" + escape(e.getMessage()) + "\"}";
            }
        };
    }

    static boolean isServerToolAllowed(String toolName, List<String> enabled, List<String> disabled) {
        if (!enabled.isEmpty() && !enabled.contains(toolName)) {
            return false;
        }
        return !disabled.contains(toolName);
    }

    private boolean isToolAllowed(String toolName, List<String> enabled, List<String> disabled) {
        // Single source of truth for the enable/disable filter (see isServerToolAllowed).
        return isServerToolAllowed(toolName, enabled, disabled);
    }

    private static String escape(String text) {
        if (text == null) return "";
        return text.replace("\\", "\\\\").replace("\"", "\\\"")
                .replace("\n", "\\n").replace("\r", "\\r").replace("\t", "\\t");
    }

    public record ToolResult(String toolName, boolean success, String result, String error) {}
    public record ToolInfo(String name, String description, Object inputSchema) {}
}
