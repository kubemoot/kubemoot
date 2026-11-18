package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import io.nats.client.Connection;
import io.nats.client.Nats;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.net.ServerSocket;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.Optional;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assumptions.assumeTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * Against a real nats-server (runs only when NATS_SERVER_BIN points at a binary): the
 * server restarts and the provider keeps handing out the one connection, which then
 * publishes again.
 */
class NatsConnectionProviderRestartIT {

    private String bin;
    private int port;
    private Path store;
    private Process server;
    private Connection subscriberConn;

    @BeforeEach
    void start() throws Exception {
        bin = System.getenv("NATS_SERVER_BIN");
        assumeTrue(bin != null && new File(bin).canExecute(), "NATS_SERVER_BIN not set");
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        store = Files.createTempDirectory("nats-provider");
        startServer();
    }

    @AfterEach
    void stop() throws Exception {
        if (subscriberConn != null) {
            subscriberConn.close();
        }
        if (server != null) {
            server.destroy();
        }
    }

    private void startServer() throws Exception {
        server = new ProcessBuilder(bin, "-a", "127.0.0.1", "-p", String.valueOf(port), "-sd", store.toString())
                .redirectErrorStream(true).redirectOutput(store.resolve("server.log").toFile()).start();
        for (int i = 0; i < 100; i++) {
            try (Connection probe = Nats.connect("nats://127.0.0.1:" + port)) {
                return;
            } catch (Exception e) {
                Thread.sleep(100);
            }
        }
        throw new IllegalStateException("nats-server did not start");
    }

    private AgentProperties props() {
        var props = mock(AgentProperties.class);
        var nats = mock(AgentProperties.Nats.class);
        when(nats.url()).thenReturn(Optional.of("nats://127.0.0.1:" + port));
        when(props.nats()).thenReturn(nats);
        when(props.agentName()).thenReturn("restart-it");
        when(props.namespace()).thenReturn(Optional.of("ns"));
        when(props.crew()).thenReturn(Optional.of("crew"));
        return props;
    }

    @Test
    void oneConnectionSurvivesARestartAndPublishesAfterIt() throws Exception {
        var provider = new NatsConnectionProvider(props());
        var before = provider.getConnection();
        assertNotNull(before);

        server.destroy();
        server.waitFor(5, TimeUnit.SECONDS);
        waitUntil(() -> before.getStatus() != Connection.Status.CONNECTED);
        assertSame(before, provider.getConnection(), "the reconnecting connection is reused");

        startServer();
        waitUntil(() -> before.getStatus() == Connection.Status.CONNECTED);
        var after = provider.getConnection();
        assertSame(before, after, "no second connection was opened");

        subscriberConn = Nats.connect("nats://127.0.0.1:" + port);
        var sub = subscriberConn.subscribe("restart.it");
        subscriberConn.flush(Duration.ofSeconds(2));
        after.publish("restart.it", "hello".getBytes());
        var msg = sub.nextMessage(Duration.ofSeconds(5));
        assertNotNull(msg, "the publish after the restart arrived");
        assertEquals("hello", new String(msg.getData()));
        provider.close();
    }

    private static void waitUntil(java.util.function.BooleanSupplier condition) throws InterruptedException {
        for (int i = 0; i < 200 && !condition.getAsBoolean(); i++) {
            Thread.sleep(100);
        }
        assertEquals(true, condition.getAsBoolean(), "condition not reached");
    }
}
