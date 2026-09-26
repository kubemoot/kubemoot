package kubemoot.ai.mcpgateway.model;

import java.util.Map;

public record ToolInfo(
    String name,
    String description,
    Map<String, Object> inputSchema,
    String serverId,
    String serverName
) {
}
