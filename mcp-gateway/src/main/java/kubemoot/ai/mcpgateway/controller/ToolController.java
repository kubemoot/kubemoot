package kubemoot.ai.mcpgateway.controller;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.web.bind.annotation.*;
import reactor.core.publisher.Mono;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import kubemoot.ai.mcpgateway.service.McpClientManager;
import kubemoot.ai.mcpgateway.service.MetaToolsService;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

/**
 * REST API for tool operations.
 * Provides a simple HTTP interface for agents to call MCP tools
 * without needing full MCP protocol support (SSE/streamable-http).
 */
@RestController
@RequestMapping("/tools")
public class ToolController {

    private static final Logger log = LoggerFactory.getLogger(ToolController.class);

    private final McpClientManager clientManager;
    private final MetaToolsService metaToolsService;

    public ToolController(
            McpClientManager clientManager,
            @org.springframework.beans.factory.annotation.Autowired(required = false) MetaToolsService metaToolsService) {
        this.clientManager = clientManager;
        this.metaToolsService = metaToolsService;
    }

    /**
     * List all available tools from all connected MCP servers.
     */
    @GetMapping
    public ToolListResponse listTools() {
        List<ToolInfo> tools = new ArrayList<>(clientManager.getAllTools());

        // Add meta-tools if enabled
        if (metaToolsService != null) {
            tools.addAll(metaToolsService.getMetaTools());
        }

        return new ToolListResponse(tools.size(), tools);
    }

    /**
     * Call a tool by name with the provided arguments.
     * Routes the call to the appropriate backend MCP server.
     *
     * @param toolName The name of the tool to call
     * @param request The request containing tool arguments
     * @return The tool call result
     */
    @PostMapping("/{toolName}/call")
    public Mono<ToolCallResponse> callTool(
            @PathVariable String toolName,
            @RequestBody ToolCallRequest request) {

        Map<String, Object> arguments = request.arguments() != null ? request.arguments() : Map.of();
        log.info("Tool call request: {} with arguments: {}", toolName, arguments);
        arguments = sanitizeArguments(toolName, arguments);

        // Check if it's a meta-tool
        if (metaToolsService != null && metaToolsService.isMetaTool(toolName)) {
            return callMetaTool(toolName, arguments);
        }

        // Find which server has the tool
        String serverId = clientManager.findServerForTool(toolName);
        if (serverId == null) {
            log.warn("Tool not found: {}", toolName);
            return Mono.just(new ToolCallResponse(true, "Tool not found: " + toolName));
        }

        return forwardToolCall(serverId, toolName, arguments);
    }

    // Prometheus instant-query tool whose optional `time` arg the models frequently
    // fill with an invalid PromQL-style "now"/"now()" token (Prometheus wants RFC3339
    // or a unix timestamp, and defaults an OMITTED time to now). Left as-is it 400s an
    // otherwise well-formed query. See [[Metrics Tooler Drills Before Declaring
    // Unavailable]] - the drill helps the agent find the right metric name, but the
    // corrected query still failed on time="now()".
    private static final String PROM_INSTANT_QUERY_TOOL = "execute_query";
    private static final java.util.Set<String> NOWISH_TIME = java.util.Set.of("now", "now()", "");

    /**
     * Drop an invalid now-ish `time` on a Prometheus instant query so it runs at now
     * (an omitted time is exactly "now") instead of 400ing. Narrowly scoped to the
     * verified failure; returns the input unchanged for every other tool/arg. Never
     * touches execute_range_query (its start/end are required, so dropping them would
     * break the call). Package-private for unit testing.
     */
    static Map<String, Object> sanitizeArguments(String toolName, Map<String, Object> arguments) {
        if (!PROM_INSTANT_QUERY_TOOL.equals(toolName) || arguments == null) {
            return arguments;
        }
        Object time = arguments.get("time");
        if (time instanceof String s && NOWISH_TIME.contains(s.trim().toLowerCase(java.util.Locale.ROOT))) {
            var copy = new java.util.HashMap<String, Object>(arguments);
            copy.remove("time");
            log.info("Sanitized invalid time='{}' on {} -> instant query at now", s, toolName);
            return copy;
        }
        return arguments;
    }

    /**
     * Execute a meta-tool and wrap the result (or failure) in a ToolCallResponse.
     */
    private Mono<ToolCallResponse> callMetaTool(String toolName, Map<String, Object> arguments) {
        log.info("Executing meta-tool: {}", toolName);
        try {
            String result = metaToolsService.executeMetaTool(toolName, arguments);
            return Mono.just(new ToolCallResponse(false, result));
        } catch (Exception e) {
            log.error("Meta-tool execution failed: {}", e.getMessage());
            return Mono.just(new ToolCallResponse(true, "Meta-tool error: " + e.getMessage()));
        }
    }

    /**
     * Build an MCP request, forward it to the backend server, and map the
     * response (or error) into a ToolCallResponse.
     */
    private Mono<ToolCallResponse> forwardToolCall(String serverId, String toolName, Map<String, Object> arguments) {
        McpMessage mcpRequest = McpMessage.request(
            System.currentTimeMillis(),
            "tools/call",
            Map.of("name", toolName, "arguments", arguments)
        );

        return clientManager.forwardRequest(serverId, mcpRequest)
            .map(this::toToolCallResponse)
            .onErrorResume(e -> {
                log.error("Tool call failed: {}", e.getMessage());
                return Mono.just(new ToolCallResponse(true, "Error: " + e.getMessage()));
            });
    }

    /**
     * Map a backend MCP response into a ToolCallResponse, surfacing any error.
     */
    private ToolCallResponse toToolCallResponse(McpMessage response) {
        if (response.error() != null) {
            return new ToolCallResponse(true, response.error().message());
        }

        // Extract content from MCP response
        String content = extractContent(response.result());
        return new ToolCallResponse(false, content);
    }

    /**
     * Extract text content from MCP result.
     * MCP tools return content as: { "content": [{ "type": "text", "text": "..." }] }
     */
    static String extractContent(Object result) {
        if (result == null) {
            return "";
        }

        if (result instanceof Map<?, ?> resultMap
                && resultMap.get("content") instanceof List<?> contentList) {
            return joinContentText(contentList);
        }

        return result.toString();
    }

    /**
     * Join the "text" fields of MCP content items, separated by newlines.
     * Non-map items and items without a "text" field are skipped.
     */
    private static String joinContentText(List<?> contentList) {
        StringBuilder sb = new StringBuilder();
        for (Object item : contentList) {
            appendItemText(sb, item);
        }
        return sb.toString();
    }

    private static void appendItemText(StringBuilder sb, Object item) {
        if (item instanceof Map<?, ?> contentItem) {
            Object text = contentItem.get("text");
            if (text != null) {
                if (sb.length() > 0) {
                    sb.append("\n");
                }
                sb.append(text);
            }
        }
    }

    // Request/Response DTOs
    public record ToolCallRequest(Map<String, Object> arguments) {}
    public record ToolCallResponse(boolean isError, String content) {}
    public record ToolListResponse(int count, List<ToolInfo> tools) {}
}
