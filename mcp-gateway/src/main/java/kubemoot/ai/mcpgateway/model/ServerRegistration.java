package kubemoot.ai.mcpgateway.model;

import java.time.Duration;
import java.time.Instant;
import java.util.UUID;

public record ServerRegistration(
    String id,
    String name,
    String url,
    String transport,
    ServerStatus status,
    Instant registeredAt,
    Instant lastHealthCheck,
    /** Why the last connect failed; kept while the server reconnects, cleared once it is CONNECTED. */
    String statusReason,
    /** Consecutive failed connects; 0 once the server is CONNECTED. */
    int failedConnects,
    /** Earliest time the next request may start a new connect after a failure; null when none is held back. */
    Instant nextConnectAt
) {
    public static ServerRegistration create(String name, String url, String transport) {
        return new ServerRegistration(
            UUID.randomUUID().toString(),
            name,
            url,
            transport != null ? transport : "sse",
            ServerStatus.UNKNOWN,
            Instant.now(),
            null,
            null,
            0,
            null
        );
    }

    public ServerRegistration withStatus(ServerStatus newStatus) {
        boolean connected = newStatus == ServerStatus.CONNECTED;
        return new ServerRegistration(id, name, url, transport, newStatus, registeredAt, Instant.now(),
            connected ? null : statusReason, connected ? 0 : failedConnects, connected ? null : nextConnectAt);
    }

    /**
     * The server is ERROR after a failed connect. Each consecutive failure holds the next
     * connect back by a longer backoff, so a server that stays down is not tried by every request.
     */
    public ServerRegistration withFailure(String reason, Duration backoff) {
        Instant now = Instant.now();
        return new ServerRegistration(id, name, url, transport, ServerStatus.ERROR, registeredAt, now,
            reason, failedConnects + 1, now.plus(backoff));
    }

    public enum ServerStatus {
        UNKNOWN,
        CONNECTING,
        CONNECTED,
        DISCONNECTED,
        ERROR
    }
}
