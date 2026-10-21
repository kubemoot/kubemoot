package ai.kubemoot.placement;

import ai.kubemoot.agent.nats.NatsLink;
import io.nats.client.Connection;
import io.nats.client.Nats;
import io.nats.client.Options;
import jakarta.annotation.PreDestroy;
import jakarta.inject.Inject;
import jakarta.inject.Singleton;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.time.Instant;

/**
 * The service's NATS connection. It connects on first use and, after a failed attempt, does not try
 * again for {@link #RETRY_BACKOFF}, so a NATS outage costs a request nothing but the answer "not
 * available". The client reconnects by itself once connected.
 */
@Singleton
public class NatsConnection implements NatsLink {

    private static final Logger log = LoggerFactory.getLogger(NatsConnection.class);
    private static final Duration RETRY_BACKOFF = Duration.ofSeconds(5);

    private final ServiceSettings settings;
    private Connection connection;
    private Instant lastFailure;

    @Inject
    public NatsConnection(ServiceSettings settings) {
        this.settings = settings;
    }

    @Override
    public boolean isAvailable() {
        Connection conn = getConnection();
        return conn != null && conn.getStatus() == Connection.Status.CONNECTED;
    }

    @Override
    public synchronized Connection getConnection() {
        if (settings.natsUrl().isEmpty()) {
            return null;
        }
        if (connection != null && connection.getStatus() == Connection.Status.CONNECTED) {
            return connection;
        }
        if (lastFailure != null && Instant.now().isBefore(lastFailure.plus(RETRY_BACKOFF))) {
            return connection != null && connection.getStatus() == Connection.Status.RECONNECTING ? connection : null;
        }
        return connect();
    }

    /**
     * The connection.
     *
     * @throws IllegalStateException when NATS is not reachable now
     */
    public Connection require() {
        Connection conn = getConnection();
        if (conn == null) {
            throw new IllegalStateException("NATS is not reachable");
        }
        return conn;
    }

    private Connection connect() {
        Options.Builder options = new Options.Builder()
                .server(settings.natsUrl().orElseThrow())
                .maxReconnects(-1)
                .reconnectWait(Duration.ofSeconds(2))
                .connectionName("placement-" + settings.holderId());
        if (settings.natsUser().isPresent()) {
            options.userInfo(settings.natsUser().get(), settings.natsPassword().orElse(""));
        }
        try {
            connection = Nats.connect(options.build());
            lastFailure = null;
            log.info("Connected to NATS");
            return connection;
        } catch (Exception e) {
            if (e instanceof InterruptedException) {
                Thread.currentThread().interrupt();
            }
            lastFailure = Instant.now();
            log.warn("NATS connection failed: {} (retrying in {}s)", e.getMessage(), RETRY_BACKOFF.toSeconds());
            return null;
        }
    }

    @PreDestroy
    void close() {
        Connection conn;
        synchronized (this) {
            conn = connection;
            connection = null;
        }
        if (conn == null) {
            return;
        }
        try {
            conn.close();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } catch (Exception e) {
            log.warn("Closing the NATS connection failed: {}", e.getMessage());
        }
    }
}
