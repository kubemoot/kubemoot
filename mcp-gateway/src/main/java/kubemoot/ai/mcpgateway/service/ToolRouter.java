package kubemoot.ai.mcpgateway.service;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Mono;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.model.ToolInfo;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

@Service
public class ToolRouter {

    private static final Logger log = LoggerFactory.getLogger(ToolRouter.class);

    // JSON field constants
    private static final String FIELD_NAME = "name";
    private static final String FIELD_LIST_CHANGED = "listChanged";
    private static final String FIELD_ARGUMENTS = "arguments";

    private final McpClientManager clientManager;
    private final MetaToolsService metaToolsService;

    public ToolRouter(
            McpClientManager clientManager,
            @Autowired(required = false) MetaToolsService metaToolsService) {
        this.clientManager = clientManager;
        this.metaToolsService = metaToolsService;
    }

    public Mono<McpMessage> handleRequest(McpMessage request) {
        String method = request.method();

        if (method == null) {
            return Mono.just(McpMessage.error(request.id(), -32600, "Invalid request: missing method"));
        }

        return switch (method) {
            case "initialize" -> handleInitialize(request);
            case "initialized" -> handleInitialized(request);
            case "tools/list" -> handleToolsList(request);
            case "tools/call" -> handleToolsCall(request);
            case "resources/list" -> handleResourcesList(request);
            case "resources/read" -> handleResourcesRead(request);
            case "prompts/list" -> handlePromptsList(request);
            case "prompts/get" -> handlePromptsGet(request);
            default -> Mono.just(McpMessage.error(request.id(), -32601, "Method not found: " + method));
        };
    }

    private Mono<McpMessage> handleInitialize(McpMessage request) {
        log.info("Handling initialize request");

        Map<String, Object> result = Map.of(
            "protocolVersion", "2024-11-05",
            "capabilities", Map.of(
                "tools", Map.of(FIELD_LIST_CHANGED, true),
                "resources", Map.of("subscribe", false, FIELD_LIST_CHANGED, true),
                "prompts", Map.of(FIELD_LIST_CHANGED, true)
            ),
            "serverInfo", Map.of(
                FIELD_NAME, "kubemoot-mcp-gateway",
                "version", "0.1.0"
            )
        );

        return Mono.just(McpMessage.response(request.id(), result));
    }

    private Mono<McpMessage> handleInitialized(McpMessage request) {
        log.info("Client initialized");
        return Mono.just(McpMessage.response(request.id(), Map.of()));
    }

    private Mono<McpMessage> handleToolsList(McpMessage request) {
        List<ToolInfo> tools = new ArrayList<>(clientManager.getAllTools());

        // Add meta-tools if enabled
        if (metaToolsService != null) {
            tools.addAll(metaToolsService.getMetaTools());
        }

        List<Map<String, Object>> toolsList = tools.stream()
            .map(tool -> Map.<String, Object>of(
                FIELD_NAME, tool.name(),
                "description", tool.description() != null ? tool.description() : "",
                "inputSchema", tool.inputSchema() != null ? tool.inputSchema() : Map.of()
            ))
            .toList();

        return Mono.just(McpMessage.response(request.id(), Map.of("tools", toolsList)));
    }

    @SuppressWarnings("unchecked")
    private Mono<McpMessage> handleToolsCall(McpMessage request) {
        Map<String, Object> params = request.params();
        if (params == null || !params.containsKey(FIELD_NAME)) {
            return Mono.just(McpMessage.error(request.id(), -32602, "Invalid params: missing tool name"));
        }

        String toolName = (String) params.get(FIELD_NAME);
        Map<String, Object> arguments = params.get(FIELD_ARGUMENTS) instanceof Map ?
            (Map<String, Object>) params.get(FIELD_ARGUMENTS) : Map.of();

        log.info("Routing tool call: {} with arguments: {}", toolName, arguments);

        // Check if it's a meta-tool
        if (metaToolsService != null && metaToolsService.isMetaTool(toolName)) {
            log.info("Executing meta-tool: {}", toolName);
            String result = metaToolsService.executeMetaTool(toolName, arguments);
            return Mono.just(McpMessage.response(request.id(), Map.of(
                "content", List.of(Map.of(
                    "type", "text",
                    "text", result
                ))
            )));
        }

        String serverId = clientManager.findServerForTool(toolName);
        if (serverId == null) {
            return Mono.just(McpMessage.error(request.id(), -32602, "Tool not found: " + toolName));
        }

        // Forward the request to the appropriate backend server
        McpMessage forwardRequest = McpMessage.request(
            request.id(),
            "tools/call",
            Map.of(FIELD_NAME, toolName, FIELD_ARGUMENTS, arguments)
        );

        return clientManager.forwardRequest(serverId, forwardRequest);
    }

    private Mono<McpMessage> handleResourcesList(McpMessage request) {
        // For MVP, return empty list - resources aggregation can be added later
        return Mono.just(McpMessage.response(request.id(), Map.of("resources", List.of())));
    }

    private Mono<McpMessage> handleResourcesRead(McpMessage request) {
        return Mono.just(McpMessage.error(request.id(), -32601, "Resource reading not implemented"));
    }

    private Mono<McpMessage> handlePromptsList(McpMessage request) {
        // For MVP, return empty list - prompts aggregation can be added later
        return Mono.just(McpMessage.response(request.id(), Map.of("prompts", List.of())));
    }

    private Mono<McpMessage> handlePromptsGet(McpMessage request) {
        return Mono.just(McpMessage.error(request.id(), -32601, "Prompts not implemented"));
    }
}
