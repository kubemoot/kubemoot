package kubemoot.ai.mcpgateway.controller;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;
import kubemoot.ai.mcpgateway.model.FeedbackEntry;
import kubemoot.ai.mcpgateway.model.RegisterServerRequest;
import kubemoot.ai.mcpgateway.model.ServerRegistration;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import kubemoot.ai.mcpgateway.model.ToolSearchResult;
import kubemoot.ai.mcpgateway.service.McpClientManager;
import kubemoot.ai.mcpgateway.service.ToolSearchService;

import java.util.List;
import java.util.Map;

@RestController
@RequestMapping("/admin")
public class AdminController {

    private static final Logger log = LoggerFactory.getLogger(AdminController.class);

    private final McpClientManager clientManager;
    private final ToolSearchService toolSearchService;

    public AdminController(
            McpClientManager clientManager,
            @Autowired(required = false) ToolSearchService toolSearchService) {
        this.clientManager = clientManager;
        this.toolSearchService = toolSearchService;
    }

    @GetMapping("/servers")
    public List<ServerRegistration> listServers() {
        return clientManager.listServers();
    }

    @PostMapping("/servers")
    public ResponseEntity<ServerRegistration> registerServer(@RequestBody RegisterServerRequest request) {
        log.info("Registering new MCP server: {} at {}", request.name(), request.url());

        ServerRegistration registration = clientManager.registerServer(
            request.name(),
            request.url(),
            request.transport()
        );

        return ResponseEntity.status(HttpStatus.CREATED).body(registration);
    }

    @GetMapping("/servers/{id}")
    public ResponseEntity<ServerRegistration> getServer(@PathVariable String id) {
        ServerRegistration server = clientManager.getServer(id);
        if (server == null) {
            return ResponseEntity.notFound().build();
        }
        return ResponseEntity.ok(server);
    }

    @DeleteMapping("/servers/{id}")
    public ResponseEntity<Void> unregisterServer(@PathVariable String id) {
        ServerRegistration server = clientManager.getServer(id);
        if (server == null) {
            return ResponseEntity.notFound().build();
        }

        log.info("Unregistering MCP server: {}", id);
        clientManager.unregisterServer(id);
        return ResponseEntity.noContent().build();
    }

    @GetMapping("/tools")
    public Map<String, Object> listTools() {
        List<ToolInfo> tools = clientManager.getAllTools();
        return Map.of(
            "count", tools.size(),
            "tools", tools
        );
    }

    @GetMapping("/servers/{id}/tools")
    public ResponseEntity<List<ToolInfo>> listServerTools(@PathVariable String id) {
        ServerRegistration server = clientManager.getServer(id);
        if (server == null) {
            return ResponseEntity.notFound().build();
        }

        return ResponseEntity.ok(clientManager.getToolsForServer(id));
    }

    /**
     * Get and clear feedback entries for a specific server.
     * The operator scrapes this endpoint to populate MCPServerReport trial records.
     * Entries are cleared on read.
     */
    @GetMapping("/servers/{id}/feedback")
    public ResponseEntity<List<FeedbackEntry>> getServerFeedback(@PathVariable String id) {
        ServerRegistration server = clientManager.getServer(id);
        if (server == null) {
            return ResponseEntity.notFound().build();
        }
        return ResponseEntity.ok(clientManager.consumeFeedback(id));
    }

    /**
     * Get and clear all feedback entries across all servers.
     * Convenience endpoint for bulk scraping by the operator.
     */
    @GetMapping("/feedback")
    public Map<String, List<FeedbackEntry>> getAllFeedback() {
        return clientManager.consumeAllFeedback();
    }

    /**
     * Search for tools by natural language description.
     * Requires tool index to be configured.
     *
     * @param query Natural language description of desired tools
     * @param topK Maximum number of results (default: 10)
     * @return List of matching tools with scores
     */
    @GetMapping("/search/tools")
    public List<ToolSearchResult> searchTools(
            @RequestParam String query,
            @RequestParam(defaultValue = "10") int topK) {
        if (toolSearchService == null) {
            throw new ResponseStatusException(HttpStatus.SERVICE_UNAVAILABLE,
                "Tool search not configured. Enable mcp.gateway.tool-index.enabled and set query-service-url");
        }
        log.info("Searching tools with query: '{}', topK: {}", query, topK);
        return toolSearchService.searchTools(query, topK);
    }
}
