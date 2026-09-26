package kubemoot.ai.mcpgateway.model;

public record RegisterServerRequest(
    String name,
    String url,
    String transport
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
    }
}
