package kubemoot.ai.mcpgateway.service;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClient;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.ToolSearchResult;

import java.util.Collections;
import java.util.List;
import java.util.Map;

/**
 * Service for semantic tool search.
 * Queries the RAGSource query service via HTTP to find tools matching user intent.
 * This approach delegates all pgvector interactions to the query service.
 */
@Service
@ConditionalOnProperty(name = "mcp.gateway.tool-index.enabled", havingValue = "true")
public class ToolSearchService {

    private static final Logger log = LoggerFactory.getLogger(ToolSearchService.class);

    private final WebClient webClient;
    private final McpGatewayProperties properties;

    public ToolSearchService(McpGatewayProperties properties) {
        this.properties = properties;
        String queryServiceUrl = properties.getToolIndex().getQueryServiceUrl();
        if (queryServiceUrl == null || queryServiceUrl.isBlank()) {
            log.warn("Tool index enabled but queryServiceUrl not configured");
            this.webClient = null;
        } else {
            log.info("Tool search service initialized with query service: {}", queryServiceUrl);
            this.webClient = WebClient.builder()
                .baseUrl(queryServiceUrl)
                .build();
        }
    }

    /**
     * Search for tools by natural language query.
     *
     * @param query Natural language description of desired tools
     * @param topK Maximum number of results to return
     * @return List of matching tools with scores
     */
    public List<ToolSearchResult> searchTools(String query, int topK) {
        if (webClient == null) {
            log.warn("Cannot search tools - query service not configured");
            return Collections.emptyList();
        }

        int effectiveTopK = topK > 0 ? topK : properties.getMetaTools().getMaxResultsPerSearch();

        log.debug("Searching tools with query: '{}', topK: {}", query, effectiveTopK);

        try {
            QueryResponse response = webClient.post()
                .uri("/query")
                .bodyValue(Map.of(
                    "query", query,
                    "top_k", effectiveTopK
                ))
                .retrieve()
                .bodyToMono(QueryResponse.class)
                .block();

            if (response == null || response.results() == null) {
                return Collections.emptyList();
            }

            List<ToolSearchResult> results = response.results().stream()
                .map(r -> new ToolSearchResult(
                    getStringFromMetadata(r.metadata(), "server_name"),
                    getStringFromMetadata(r.metadata(), "tool_name"),
                    r.content(),
                    r.score(),
                    r.metadata()
                ))
                .toList();

            log.debug("Found {} matching tools", results.size());
            return results;

        } catch (Exception e) {
            log.error("Failed to search tools: {}", e.getMessage(), e);
            return Collections.emptyList();
        }
    }

    private String getStringFromMetadata(Map<String, Object> metadata, String key) {
        if (metadata == null) return "";
        Object value = metadata.get(key);
        return value != null ? value.toString() : "";
    }

    /**
     * Response from the RAGSource query service.
     */
    record QueryResponse(List<QueryResult> results) {}

    /**
     * Individual result from the query service.
     */
    record QueryResult(String content, double score, Map<String, Object> metadata) {}
}
