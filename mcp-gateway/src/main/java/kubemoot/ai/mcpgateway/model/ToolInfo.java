package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.annotation.JsonInclude;

import java.util.Map;

/**
 * A tool a backend MCP server offers. {@code annotations} are the server's MCP tool annotations
 * (readOnlyHint, idempotentHint, ...), passed on so a caller can decide whether an identical repeat
 * call may be answered from an earlier result; null when the server declares none.
 */
public record ToolInfo(
    String name,
    String description,
    Map<String, Object> inputSchema,
    String serverId,
    String serverName,
    @JsonInclude(JsonInclude.Include.NON_NULL) Map<String, Object> annotations
) {
    public ToolInfo(String name, String description, Map<String, Object> inputSchema,
                    String serverId, String serverName) {
        this(name, description, inputSchema, serverId, serverName, null);
    }
}
