package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import io.nats.client.Nats;
import io.nats.client.Options;
import jakarta.annotation.PreDestroy;
import jakarta.enterprise.context.ApplicationScoped;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.time.Instant;

/**
 * Shared NATS connection provider used by ChatEventPublisher, DiscussionSubscriber,
 * and DiscussionOrchestrator. Lazy connection with retry — no-op when NATS_URL is not set.
 */
@ApplicationScoped
public class NatsConnectionProvider {

    private static final Logger log = LoggerFactory.getLogger(NatsConnectionProvider.class);
    private static final int MAX_INITIAL_RETRIES = 5;
    private static final Duration RETRY_INTERVAL = Duration.ofSeconds(3);
    private static final Duration RECONNECT_BACKOFF = Duration.ofSeconds(30);

    private final String natsUrl;
    private final String agentName;
    private Connection connection;
    private Instant lastFailedAttempt;

    public NatsConnectionProvider(AgentProperties properties) {
        this.natsUrl = properties.nats().url().orElse("");
        this.agentName = properties.agentName();
        if (natsUrl.isEmpty()) {
            log.info("NATS URL not configured — NATS features disabled");
        } else {
            log.info("NATS connection provider initialized: {}", natsUrl);
        }
    }

    public boolean isConfigured() {
        return !natsUrl.isEmpty();
    }

    public boolean isAvailable() {
        if (!isConfigured()) return false;
        var conn = getConnection();
        return conn != null && conn.getStatus() == Connection.Status.CONNECTED;
    }

    public synchronized Connection getConnection() {
        if (!isConfigured()) return null;

        if (connection != null && connection.getStatus() == Connection.Status.CONNECTED) {
            return connection;
        }

        // Back off between reconnection attempts
        if (lastFailedAttempt != null
                && Instant.now().isBefore(lastFailedAttempt.plus(RECONNECT_BACKOFF))) {
            return null;
        }

        return attemptConnect();
    }

    private Connection attemptConnect() {
        var options = new Options.Builder()
                .server(natsUrl)
                .maxReconnects(-1)
                .reconnectWait(Duration.ofSeconds(2))
                .connectionName("agent-" + agentName)
                .build();

        for (int attempt = 1; attempt <= MAX_INITIAL_RETRIES; attempt++) {
            try {
                connection = Nats.connect(options);
                log.info("Connected to NATS: {}", natsUrl);
                lastFailedAttempt = null;
                return connection;
            } catch (Exception e) {
                if (attempt < MAX_INITIAL_RETRIES) {
                    log.warn("NATS connection attempt {}/{} failed: {} — retrying in {}s",
                            attempt, MAX_INITIAL_RETRIES, e.getMessage(), RETRY_INTERVAL.toSeconds());
                    try {
                        Thread.sleep(RETRY_INTERVAL.toMillis());
                    } catch (InterruptedException ie) {
                        Thread.currentThread().interrupt();
                        break;
                    }
                } else {
                    log.warn("NATS connection failed after {} attempts: {} — will retry in {}s",
                            MAX_INITIAL_RETRIES, e.getMessage(), RECONNECT_BACKOFF.toSeconds());
                    lastFailedAttempt = Instant.now();
                }
            }
        }
        return null;
    }

    public String getAgentName() {
        return agentName;
    }

    @PreDestroy
    public void close() {
        if (connection == null) {
            return;
        }
        try {
            connection.close();
            log.info("Closed shared NATS connection for agent {}", agentName);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            log.warn("Interrupted while closing NATS connection for agent {}", agentName);
        } catch (RuntimeException e) {
            // Log rather than swallow: a failed close during shutdown is still worth a record.
            log.warn("Failed to close NATS connection for agent {}: {}", agentName, e.toString());
        }
    }
}
