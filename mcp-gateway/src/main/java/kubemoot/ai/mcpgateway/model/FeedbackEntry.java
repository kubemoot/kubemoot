package kubemoot.ai.mcpgateway.model;

import java.time.Instant;

/**
 * Records a runtime feedback event from MCP server interactions.
 * Collected automatically by McpClientManager and consumed by the operator
 * to populate MCPServerReport trial records.
 */
public record FeedbackEntry(
    String serverName,
    String serverId,
    String phase,       // connect, initialize, discover, call
    boolean success,
    String errorMessage,
    String toolName,    // for "call" phase
    int toolsFound,     // for "discover" phase
    Instant timestamp
) {
    public static FeedbackEntry success(String serverName, String serverId, String phase) {
        return new FeedbackEntry(serverName, serverId, phase, true, null, null, 0, Instant.now());
    }

    public static FeedbackEntry success(String serverName, String serverId, String phase, int toolsFound) {
        return new FeedbackEntry(serverName, serverId, phase, true, null, null, toolsFound, Instant.now());
    }

    public static FeedbackEntry failure(String serverName, String serverId, String phase, String error) {
        return new FeedbackEntry(serverName, serverId, phase, false, error, null, 0, Instant.now());
    }

    public static FeedbackEntry callFailure(String serverName, String serverId, String toolName, String error) {
        return new FeedbackEntry(serverName, serverId, "call", false, error, toolName, 0, Instant.now());
    }
}
