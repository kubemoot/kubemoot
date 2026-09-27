package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.NUID;
import io.nats.client.Nats;
import io.nats.client.api.KeyValueConfiguration;
import io.nats.client.api.KeyValueEntry;
import io.nats.client.api.KeyValueWatchOption;
import io.nats.client.api.KeyValueWatcher;
import io.nats.client.api.StorageType;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.lang.reflect.Field;
import java.net.ServerSocket;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.*;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

/**
 * Against a real nats-server (runs only when NATS_SERVER_BIN points at a binary):
 * KV key listing and capacity watches coexist, and the consumer-name collision
 * between two processes with identical NUID state is reproduced and avoided.
 */
class NatsKvConsumerNamesIT {

    private static Process server;
    private static String url;
    private static Path storeDir;

    @BeforeAll
    static void start() throws Exception {
        String bin = System.getenv("NATS_SERVER_BIN");
        assumeTrue(bin != null && new File(bin).canExecute(), "NATS_SERVER_BIN not set");
        int port;
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        storeDir = Files.createTempDirectory("nats-js");
        server = new ProcessBuilder(bin, "-js", "-p", String.valueOf(port), "-sd", storeDir.toString())
                .redirectErrorStream(true).redirectOutput(storeDir.resolve("server.log").toFile()).start();
        url = "nats://127.0.0.1:" + port;
        for (int i = 0; i < 50; i++) {
            try (Connection c = Nats.connect(url)) {
                return;
            } catch (Exception e) {
                Thread.sleep(100);
            }
        }
        fail("nats-server did not start");
    }

    @AfterAll
    static void stop() throws Exception {
        if (server != null) {
            server.destroy();
            server.waitFor(5, TimeUnit.SECONDS);
        }
    }

    private static KeyValue bucket(Connection c, String name) throws Exception {
        try {
            c.keyValueManagement().create(KeyValueConfiguration.builder().name(name).storageType(StorageType.Memory).build());
        } catch (Exception e) {
            // exists
        }
        KeyValue kv = c.keyValue(name);
        kv.put("p1", "v".getBytes());
        return kv;
    }

    private static final KeyValueWatcher NOOP = new KeyValueWatcher() {
        @Override public void watch(KeyValueEntry e) { }
        @Override public void endOfData() { }
    };

    private static ai.kubemoot.agent.provider.NatsCapacityWatch capacityWatch(Connection c) {
        var provider = org.mockito.Mockito.mock(NatsConnectionProvider.class);
        org.mockito.Mockito.when(provider.getConnection()).thenReturn(c);
        return new ai.kubemoot.agent.provider.NatsCapacityWatch(provider);
    }

    @Test
    void keysAndWatchInOneProcess_bothSucceedRepeatedly() throws Exception {
        try (Connection c = Nats.connect(url)) {
            KeyValue state = bucket(c, "kubemoot_provider_state");
            bucket(c, "kubemoot_provider_tickets");
            for (int i = 0; i < 25; i++) {
                assertEquals(java.util.List.of("p1"), KvKeys.list(c, "kubemoot_provider_state"));
                assertFalse(state.keys().isEmpty());
                assertTrue(capacityWatch(c).ensureWatching());
            }
        }
    }

    @Test
    void identicalNuidStateInTwoProcesses_breaksKeysButNotOurReadsOrWatches() throws Exception {
        // Two pods started from one native image with NUID initialized at build time
        // share its state, so their generated consumer names repeat.
        Field f = NUID.class.getDeclaredField("globalNUID");
        f.setAccessible(true);
        NUID global = (NUID) f.get(null);
        Field seq = NUID.class.getDeclaredField("seq");
        seq.setAccessible(true);
        try (Connection podA = Nats.connect(url); Connection podB = Nats.connect(url)) {
            KeyValue a = bucket(podA, "kubemoot_provider_state");
            bucket(podA, "kubemoot_provider_tickets");
            KeyValue b = podB.keyValue("kubemoot_provider_state");

            long snapshot = seq.getLong(global);
            a.watchAll(NOOP, KeyValueWatchOption.UPDATES_ONLY, KeyValueWatchOption.META_ONLY);
            seq.setLong(global, snapshot);
            var collision = assertThrows(io.nats.client.JetStreamApiException.class, b::keys);
            assertEquals(10012, collision.getApiErrorCode(), "the failure seen in the cluster");

            seq.setLong(global, snapshot);
            assertEquals(java.util.List.of("p1"), KvKeys.list(podB, "kubemoot_provider_state"),
                    "the subject index needs no consumer");
            seq.setLong(global, snapshot);
            assertTrue(capacityWatch(podB).ensureWatching(), "a per-process prefix keeps watch names distinct");
        }
    }

    @Test
    void deletedKeyIsListedButReadsBackAbsent() throws Exception {
        try (Connection c = Nats.connect(url)) {
            KeyValue kv = bucket(c, "deletes");
            kv.put("gone", "x".getBytes());
            kv.delete("gone");
            assertTrue(KvKeys.list(c, "deletes").contains("gone"));
            assertNull(kv.get("gone"));
        }
    }
}
