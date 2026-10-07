package kubemoot.ai.mcpgateway.model;

import java.util.List;

public record RegisterServerRequest(
    String name,
    String url,
    String transport,
    List<ToolOverride> toolOverrides
) {
    public RegisterServerRequest {
        if (name == null || name.isBlank()) {
            throw new IllegalArgumentException("Server name is required");
        }
        if (url == null || url.isBlank()) {
            throw new IllegalArgumentException("Server URL is required");
        }
        if (transport == null) {
            transport = "sse";
        }
        toolOverrides = toolOverrides == null ? List.of() : List.copyOf(toolOverrides);
    }

    public RegisterServerRequest(String name, String url, String transport) {
        this(name, url, transport, List.of());
    }
}
