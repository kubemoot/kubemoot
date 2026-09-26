package kubemoot.ai.mcpgateway.model;

import java.time.Instant;
import java.util.UUID;

public record ServerRegistration(
    String id,
    String name,
    String url,
    String transport,
    ServerStatus status,
    Instant registeredAt,
    Instant lastHealthCheck
) {
    public static ServerRegistration create(String name, String url, String transport) {
        return new ServerRegistration(
            UUID.randomUUID().toString(),
            name,
            url,
            transport != null ? transport : "sse",
            ServerStatus.UNKNOWN,
            Instant.now(),
            null
        );
    }

    public ServerRegistration withStatus(ServerStatus newStatus) {
        return new ServerRegistration(id, name, url, transport, newStatus, registeredAt, Instant.now());
    }

    public enum ServerStatus {
        UNKNOWN,
        CONNECTING,
        CONNECTED,
        DISCONNECTED,
        ERROR
    }
}
