package ai.kubemoot.agent.nats;

import io.nats.client.Connection;

/**
 * The NATS connection the placement code reads and writes through. agent-runtime backs it with the
 * agent's shared connection, the placement service with its own; the placement code needs only a
 * connection and whether one is usable now.
 */
public interface NatsLink {

    /** True when a NATS connection is configured and currently connected. */
    boolean isAvailable();

    /** The connection, or null when NATS is not configured or not reachable now. */
    Connection getConnection();
}
