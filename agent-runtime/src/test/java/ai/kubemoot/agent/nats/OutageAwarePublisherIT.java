package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.JetStreamManagement;
import io.nats.client.Nats;
import io.nats.client.Options;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.net.ServerSocket;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assumptions.assumeTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * Against a real nats-server (runs only when NATS_SERVER_BIN points at a binary): the
 * server is stopped while an answer is being published and started again on the same
 * port and store; the answer ends up in the stream exactly once.
 */
class OutageAwarePublisherIT {

    private static final String SUBJECT = "kubemoot.discuss.ns.crew.broadcast.t1";
    private static final String STREAM = "KUBEMOOT_DISCUSS";

    private String bin;
    private int port;
    private Path store;
    private Process server;
    private Connection conn;

    @BeforeEach
    void start() throws Exception {
        bin = System.getenv("NATS_SERVER_BIN");
        assumeTrue(bin != null && new File(bin).canExecute(), "NATS_SERVER_BIN not set");
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        store = Files.createTempDirectory("nats-outage");
        startServer();
        conn = Nats.connect(new Options.Builder().server("nats://127.0.0.1:" + port).maxReconnects(-1)
                .reconnectWait(Duration.ofMillis(100)).build());
        conn.jetStreamManagement().addStream(StreamConfiguration.builder().name(STREAM)
                .subjects("kubemoot.discuss.>").storageType(StorageType.File).build());
    }

    @AfterEach
    void stop() throws Exception {
        if (conn != null) {
            conn.close();
        }
        if (server != null) {
            server.destroy();
        }
    }

    private void startServer() throws Exception {
        server = new ProcessBuilder(bin, "-js", "-a", "127.0.0.1", "-p", String.valueOf(port), "-sd", store.toString())
                .redirectErrorStream(true).redirectOutput(store.resolve("server.log").toFile()).start();
        org.awaitility.Awaitility.await().atMost(Duration.ofSeconds(30)).pollInterval(Duration.ofMillis(50))
                .ignoreExceptions().until(() -> {
                    try (Connection probe = Nats.connect("nats://127.0.0.1:" + port)) {
                        return probe.jetStreamManagement().getAccountStatistics() != null;
                    }
                });
    }

    @Test
    void anAnswerPublishedDuringARestartIsStoredExactlyOnce() throws Exception {
        var provider = mock(NatsConnectionProvider.class);
        when(provider.isConfigured()).thenReturn(true);
        when(provider.getConnection()).thenAnswer(call -> conn);
        var publisher = new OutageAwarePublisher(provider, new OutageBackoff(Duration.ofSeconds(30), Duration.ofMillis(50)));
        byte[] body = "{\"messageId\":\"m1\",\"messageType\":\"synthesis\",\"content\":\"the answer\"}"
                .getBytes(StandardCharsets.UTF_8);

        server.destroy();
        server.waitFor(5, TimeUnit.SECONDS);
        var sent = CompletableFuture.supplyAsync(() -> publisher.run("synthesis", js ->
                DerivedIds.publishAcked(js, SUBJECT, body, "t1/0/synthesis/done/1/msg/synthesis"), publisher.startWait()));
        // the publisher is retrying once the client has seen the server go away
        org.awaitility.Awaitility.await().atMost(Duration.ofSeconds(10))
                .until(() -> conn.getStatus() != Connection.Status.CONNECTED);
        startServer();

        assertTrue(sent.get(30, TimeUnit.SECONDS), "the answer was acknowledged once NATS came back");
        JetStreamManagement jsm = conn.jetStreamManagement();
        assertEquals(1, jsm.getStreamInfo(STREAM).getStreamState().getMsgCount());
    }
}
