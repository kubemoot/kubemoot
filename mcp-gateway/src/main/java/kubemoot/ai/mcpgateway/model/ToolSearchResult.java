package kubemoot.ai.mcpgateway.model;

import java.util.Map;

/**
 * Represents a search result from the semantic tool index.
 * Contains information about a tool found via semantic search.
 */
public record ToolSearchResult(
    String serverName,
    String toolName,
    String description,
    double score,
    Map<String, Object> metadata
) {
}
