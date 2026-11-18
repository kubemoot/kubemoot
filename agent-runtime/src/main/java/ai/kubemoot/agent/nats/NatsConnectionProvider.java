package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import io.nats.client.Nats;
import io.nats.client.Options;
import jakarta.annotation.PreDestroy;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.time.Instant;
import java.util.concurrent.locks.ReentrantLock;

/**
 * Shared NATS connection server used by ChatEventPublisher, DiscussionSubscriber,
 * and DiscussionOrchestrator. Lazy connection with retry — no-op when NATS_URL is not set.
 */
@ApplicationScoped
public class NatsConnectionProvider implements NatsLink {

    private static final Logger log = LoggerFactory.getLogger(NatsConnectionProvider.class);
    private static final int MAX_INITIAL_RETRIES = 5;
    private static final Duration RETRY_INTERVAL = Duration.ofSeconds(3);
    private static final Duration RECONNECT_BACKOFF = Duration.ofSeconds(30);

    private final String natsUrl;
    private final String agentName;
    private final CrewScope scope;
    private final ReentrantLock connectLock = new ReentrantLock();
    private final Connector connector;
    private final Duration retryInterval;
    private volatile Connection connection;
    private volatile Instant lastFailedAttempt;

    /** Opens a connection; replaced in tests so no server is needed. */
    @FunctionalInterface
    interface Connector {
        Connection connect(Options options) throws Exception;
    }

    @Inject
    public NatsConnectionProvider(AgentProperties properties) {
        this(properties, Nats::connect, RETRY_INTERVAL);
    }

    NatsConnectionProvider(AgentProperties properties, Connector connector, Duration retryInterval) {
        this.connector = connector;
        this.retryInterval = retryInterval;
        this.natsUrl = properties.nats().url().orElse("");
        this.agentName = properties.agentName();
        if (natsUrl.isEmpty()) {
            this.scope = null;
            log.info("NATS URL not configured — NATS features disabled");
        } else {
            // Fails startup when the namespace is unknown: an unscoped subject or key
            // would cross into every other namespace running the same crew name.
            this.scope = CrewScope.fromProperties(properties);
            log.info("NATS connection server initialized: {} (scope {})", natsUrl, scope);
        }
    }

    /**
     * The namespace and crew scoping every subject and key this agent uses.
     *
     * @throws IllegalStateException when NATS is not configured (there is no scope to use)
     */
    public CrewScope scope() {
        if (scope == null) {
            throw new IllegalStateException("NATS not configured; no crew scope");
        }
        return scope;
    }

    public boolean isConfigured() {
        return !natsUrl.isEmpty();
    }

    @Override
    public boolean isAvailable() {
        if (!isConfigured()) return false;
        var conn = getConnection();
        return conn != null && conn.getStatus() == Connection.Status.CONNECTED;
    }

    /**
     * The shared connection. An existing connection that is CONNECTED, RECONNECTING or
     * DISCONNECTED is returned as is: the client reconnects it by itself and keeps its
     * buffered publishes. A new connection is made only when there is none or the old
     * one is CLOSED, and a replaced connection is closed.
     */
    @Override
    public Connection getConnection() {
        if (!isConfigured()) return null;

        var current = connection;
        if (isReusable(current)) {
            return current;
        }
        connectLock.lock();
        try {
            // Another caller may have connected while this one waited for the lock.
            current = connection;
            if (isReusable(current)) {
                return current;
            }
            if (inBackoff()) {
                return null;
            }
            return replaceWith(attemptConnect(), current);
        } finally {
            connectLock.unlock();
        }
    }

    private static boolean isReusable(Connection conn) {
        return conn != null && conn.getStatus() != Connection.Status.CLOSED;
    }

    private boolean inBackoff() {
        var failed = lastFailedAttempt;
        return failed != null && Instant.now().isBefore(failed.plus(RECONNECT_BACKOFF));
    }

    private Connection replaceWith(Connection fresh, Connection old) {
        if (fresh == null) {
            return null;
        }
        connection = fresh;
        lastFailedAttempt = null;
        closeQuietly(old);
        return fresh;
    }

    private void closeQuietly(Connection conn) {
        if (conn == null) {
            return;
        }
        try {
            conn.close();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } catch (RuntimeException e) {
            log.warn("Failed to close replaced NATS connection for agent {}: {}", agentName, e.toString());
        }
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
                var fresh = connector.connect(options);
                log.info("Connected to NATS: {}", natsUrl);
                return fresh;
            } catch (Exception e) {
                if (!afterFailedAttempt(attempt, e)) {
                    break;
                }
            }
        }
        return null;
    }

    /** Logs a failed attempt and waits before the next one; false when no further attempt should be made. */
    private boolean afterFailedAttempt(int attempt, Exception e) {
        if (attempt >= MAX_INITIAL_RETRIES) {
            log.warn("NATS connection failed after {} attempts: {} - will retry in {}s",
                    MAX_INITIAL_RETRIES, e.getMessage(), RECONNECT_BACKOFF.toSeconds());
            lastFailedAttempt = Instant.now();
            return false;
        }
        log.warn("NATS connection attempt {}/{} failed: {} - retrying in {}s",
                attempt, MAX_INITIAL_RETRIES, e.getMessage(), RETRY_INTERVAL.toSeconds());
        try {
            Thread.sleep(retryInterval.toMillis());
            return true;
        } catch (InterruptedException ie) {
            Thread.currentThread().interrupt();
            return false;
        }
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
