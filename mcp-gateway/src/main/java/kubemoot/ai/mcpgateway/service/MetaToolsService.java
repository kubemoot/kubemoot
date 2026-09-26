package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClient;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import kubemoot.ai.mcpgateway.model.ToolSearchResult;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Provides meta-tools for agent self-service tool discovery.
 * Meta-tools allow agents to search for and load MCP tools dynamically.
 */
@Service
@ConditionalOnProperty(name = "mcp.gateway.meta-tools.enabled", havingValue = "true")
public class MetaToolsService {

    private static final Logger log = LoggerFactory.getLogger(MetaToolsService.class);

    private static final String META_SERVER_ID = "_meta";

    // JSON schema field constants
    private static final String SCHEMA_TYPE = "type";
    private static final String SCHEMA_DESCRIPTION = "description";
    private static final String TYPE_STRING = "string";
    private static final String TYPE_OBJECT = "object";

    // Meta-tool name constants
    private static final String TOOL_SEARCH = "search_tools";
    private static final String TOOL_LOAD = "load_tools";

    // Response field constants
    private static final String FIELD_STATUS = "status";
    private static final String FIELD_MESSAGE = "message";
    private static final String FIELD_ERROR = "error";
    private static final String FIELD_QUERY = "query";
    private static final String FIELD_SERVERS = "servers";

    private static final Map<String, Object> SEARCH_TOOLS_SCHEMA = Map.of(
        SCHEMA_TYPE, TYPE_OBJECT,
        "properties", Map.of(
            FIELD_QUERY, Map.of(
                SCHEMA_TYPE, TYPE_STRING,
                SCHEMA_DESCRIPTION, "Natural language description of the tools you're looking for"
            ),
            "top_k", Map.of(
                SCHEMA_TYPE, "integer",
                SCHEMA_DESCRIPTION, "Maximum number of results to return (default: 10)",
                "default", 10
            )
        ),
        "required", List.of(FIELD_QUERY)
    );

    private static final Map<String, Object> LOAD_TOOLS_SCHEMA = Map.of(
        SCHEMA_TYPE, TYPE_OBJECT,
        "properties", Map.of(
            FIELD_SERVERS, Map.of(
                SCHEMA_TYPE, "array",
                "items", Map.of(SCHEMA_TYPE, TYPE_STRING),
                SCHEMA_DESCRIPTION, "List of MCP server names to load tools from"
            )
        ),
        "required", List.of(FIELD_SERVERS)
    );

    private final ToolSearchService toolSearchService;
    private final McpClientManager clientManager;
    private final McpGatewayProperties properties;
    private final ObjectMapper objectMapper;
    private final WebClient discoveryClient;

    public MetaToolsService(
            @Autowired(required = false) ToolSearchService toolSearchService,
            McpClientManager clientManager,
            McpGatewayProperties properties,
            ObjectMapper objectMapper,
            WebClient.Builder webClientBuilder) {
        this.toolSearchService = toolSearchService;
        this.clientManager = clientManager;
        this.properties = properties;
        this.objectMapper = objectMapper;

        // Initialize discovery client if configured
        var discovery = properties.getMetaTools().getDiscovery();
        if (discovery.isEnabled() && discovery.getAgentEndpoint() != null) {
            this.discoveryClient = webClientBuilder.baseUrl(discovery.getAgentEndpoint()).build();
            log.info("MetaToolsService initialized with discovery agent: {}", discovery.getAgentEndpoint());
        } else {
            this.discoveryClient = null;
            log.info("MetaToolsService initialized - discovery agent not configured");
        }
    }

    /**
     * Returns the list of meta-tools available.
     */
    public List<ToolInfo> getMetaTools() {
        return List.of(
            new ToolInfo(
                TOOL_SEARCH,
                "Search for MCP tools by natural language description. Returns matching tools with relevance scores.",
                SEARCH_TOOLS_SCHEMA,
                META_SERVER_ID,
                META_SERVER_ID
            ),
            new ToolInfo(
                TOOL_LOAD,
                "Load tools from specified MCP servers. Returns the status of loading each server.",
                LOAD_TOOLS_SCHEMA,
                META_SERVER_ID,
                META_SERVER_ID
            )
        );
    }

    /**
     * Check if a tool name is a meta-tool.
     */
    public boolean isMetaTool(String toolName) {
        return TOOL_SEARCH.equals(toolName) || TOOL_LOAD.equals(toolName);
    }

    /**
     * Execute a meta-tool and return the result as a formatted string.
     *
     * @param toolName The meta-tool name
     * @param arguments The tool arguments
     * @return JSON string with the result
     */
    @SuppressWarnings("unchecked")
    public String executeMetaTool(String toolName, Map<String, Object> arguments) {
        try {
            Object result = switch (toolName) {
                case TOOL_SEARCH -> searchTools(
                    (String) arguments.get(FIELD_QUERY),
                    arguments.get("top_k") instanceof Number n ? n.intValue() : 10
                );
                case TOOL_LOAD -> loadTools(
                    (List<String>) arguments.get(FIELD_SERVERS)
                );
                default -> throw new IllegalArgumentException("Unknown meta-tool: " + toolName);
            };
            return objectMapper.writeValueAsString(result);
        } catch (JsonProcessingException e) {
            log.error("Failed to serialize meta-tool result", e);
            return "{\"error\": \"Failed to serialize result\"}";
        }
    }

    /**
     * Search for tools using semantic search.
     * If no results found and discovery is enabled, triggers the discovery agent.
     */
    private Object searchTools(String query, int topK) {
        if (toolSearchService == null) {
            return Map.of(
                FIELD_ERROR, "Tool search not available",
                FIELD_MESSAGE, "The tool index is not configured. Enable mcp.gateway.tool-index.enabled and configure query-service-url."
            );
        }

        log.info("Meta-tool search_tools called with query: '{}', topK: {}", query, topK);

        int effectiveTopK = topK > 0 ? topK : properties.getMetaTools().getMaxResultsPerSearch();
        List<ToolSearchResult> results = toolSearchService.searchTools(query, effectiveTopK);

        // If no results and discovery is enabled, trigger the discovery agent
        if (results.isEmpty() && discoveryClient != null) {
            log.info("No tools found for '{}', triggering discovery agent", query);
            triggerDiscoveryAgent(query);
            return Map.of(
                FIELD_STATUS, "searching",
                FIELD_MESSAGE, "No tools found. Discovery agent is searching catalogs for matching MCP servers. Retry in 30-60 seconds.",
                FIELD_QUERY, query,
                "count", 0
            );
        }

        return Map.of(
            "results", results.stream().map(r -> Map.of(
                "server", r.serverName(),
                "tool", r.toolName(),
                SCHEMA_DESCRIPTION, r.description(),
                "score", r.score()
            )).toList(),
            "count", results.size()
        );
    }

    /**
     * Triggers the mcp-catalog-discovery agent to find MCP servers for a capability.
     * Fire-and-forget - the agent will create MCPServer CRs asynchronously.
     */
    private void triggerDiscoveryAgent(String query) {
        discoveryClient.post()
            .uri("/chat")
            .bodyValue(Map.of(
                FIELD_MESSAGE, "Find MCP servers that provide tools for: " + query,
                "context", Map.of(
                    "capability", query,
                    "namespace", "default"  // Discovery agent operates in default namespace; MCPServer CRs are cluster-scoped
                )
            ))
            .retrieve()
            .bodyToMono(String.class)
            .doOnSuccess(response -> log.info("Discovery agent triggered for '{}': {}", query, response))
            .doOnError(error -> log.error("Failed to trigger discovery agent for '{}': {}", query, error.getMessage()))
            .subscribe();  // Fire and forget
    }

    /**
     * Load tools from specified MCP servers.
     */
    private Object loadTools(List<String> serverNames) {
        if (serverNames == null || serverNames.isEmpty()) {
            return Map.of(FIELD_ERROR, "No servers specified");
        }

        log.info("Meta-tool load_tools called for servers: {}", serverNames);

        Map<String, Object> results = new HashMap<>();
        for (String serverName : serverNames) {
            try {
                List<ToolInfo> tools = clientManager.getToolsForServer(serverName);
                if (tools != null && !tools.isEmpty()) {
                    results.put(serverName, Map.of(
                        FIELD_STATUS, "loaded",
                        "toolCount", tools.size(),
                        "tools", tools.stream().map(ToolInfo::name).toList()
                    ));
                } else {
                    results.put(serverName, Map.of(
                        FIELD_STATUS, "empty",
                        FIELD_MESSAGE, "Server found but no tools available"
                    ));
                }
            } catch (Exception e) {
                results.put(serverName, Map.of(
                    FIELD_STATUS, FIELD_ERROR,
                    FIELD_MESSAGE, e.getMessage()
                ));
            }
        }

        return Map.of(FIELD_SERVERS, results);
    }
}
