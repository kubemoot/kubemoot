package kubemoot.ai.mcpgateway.model;

import java.util.List;

public record RegisterServerRequest(
    String name,
    String url,
    String transport,
    List<ToolOverride> toolOverrides,
    ServerCredential auth,
    CallerCredentialConfig callerCredential
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
        callerCredential = callerCredential == null ? CallerCredentialConfig.OFF : callerCredential;
    }

    public RegisterServerRequest(String name, String url, String transport, List<ToolOverride> toolOverrides,
                                 ServerCredential auth) {
        this(name, url, transport, toolOverrides, auth, null);
    }

    public RegisterServerRequest(String name, String url, String transport, List<ToolOverride> toolOverrides) {
        this(name, url, transport, toolOverrides, null);
    }

    public RegisterServerRequest(String name, String url, String transport) {
        this(name, url, transport, List.of(), null);
    }
}
