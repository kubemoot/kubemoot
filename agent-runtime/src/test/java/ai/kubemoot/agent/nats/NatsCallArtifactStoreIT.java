package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallHeaders;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.Nats;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.net.ServerSocket;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;
import static org.junit.jupiter.api.Assumptions.assumeTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * Against a real nats-server (runs only when NATS_SERVER_BIN points at a binary): a per-call
 * object is written under the thread's prefix with its headers, reads back byte for byte, is
 * announced on the artifact subject, gets its own number on each reservation, and is deleted with
 * the thread's other objects by the thread-end clean-up.
 */
class NatsCallArtifactStoreIT {

    private static final CrewScope SCOPE = CrewScope.of("ns-a", "homelab-pilot");

    private static Process server;
    private static Path storeDir;
    private static String url;

    @BeforeAll
    static void start() throws Exception {
        String bin = System.getenv("NATS_SERVER_BIN");
        assumeTrue(bin != null && new File(bin).canExecute(), "NATS_SERVER_BIN not set");
        int port;
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        storeDir = Files.createTempDirectory("nats-calls");
        server = new ProcessBuilder(bin, "-js", "-p", String.valueOf(port), "-sd", storeDir.toString())
                .redirectErrorStream(true).redirectOutput(storeDir.resolve("server.log").toFile()).start();
        url = "nats://127.0.0.1:" + port;
        for (int i = 0; i < 50; i++) {
            try (Connection c = Nats.connect(url)) {
                return;
            } catch (Exception e) {
                if (server.waitFor(100, TimeUnit.MILLISECONDS)) {
                    fail("nats-server exited with " + server.exitValue());
                }
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
        if (storeDir != null) {
            try (var files = Files.walk(storeDir)) {
                files.sorted(java.util.Comparator.reverseOrder()).forEach(p -> p.toFile().delete());
            }
        }
    }

    private static byte[] read(Connection conn, String key) throws Exception {
        var out = new ByteArrayOutputStream();
        conn.objectStore(DiscussionArtifacts.BUCKET).get(key, out);
        return out.toByteArray();
    }

    @Test
    void aCallIsWrittenWithItsHeadersReadsBackAndIsDeletedWithTheThread() throws Exception {
        try (Connection conn = Nats.connect(url)) {
            var provider = mock(NatsConnectionProvider.class);
            when(provider.scope()).thenReturn(SCOPE);
            when(provider.getConnection()).thenReturn(conn);
            var store = new NatsCallArtifactStore(provider, new ObjectMapper());
            var references = new java.util.concurrent.LinkedBlockingQueue<String>();
            conn.createDispatcher(m -> references.add(new String(m.getData(), StandardCharsets.UTF_8)))
                    .subscribe(SCOPE.artifactSubject("t1"));

            String key = store.reserveKey("t1", "k8s-workloads", "pods_list");
            String second = store.reserveKey("t1", "k8s-workloads", "pods_list");
            assertEquals("ns-a/homelab-pilot/t1/k8s-workloads/001-pods_list", key);
            assertEquals("ns-a/homelab-pilot/t1/k8s-workloads/002-pods_list", second);
            assertEquals("ns-a/homelab-pilot/t1/", store.threadPrefix("t1"),
                    "the prefix every key of the thread starts with, so short keys can be made from long ones");
            assertTrue(key.startsWith(store.threadPrefix("t1")));

            var headers = new LinkedHashMap<String, String>();
            headers.put(CallHeaders.TOOL_NAME, "pods_list");
            headers.put(CallHeaders.TOOL_CALL_ARGUMENTS, "{\"a\":\n\"b\"}");
            headers.put(CallHeaders.ROWS, "2");
            headers.put(CallHeaders.FORMAT, CallHeaders.FORMAT_JSON);
            headers.put(CallHeaders.ERROR, "false");
            byte[] body = "[{\"a\":1},{\"a\":2}]".getBytes(StandardCharsets.UTF_8);

            assertTrue(store.put("t1", "k8s-workloads", "pods_list", key, body, headers));

            assertEquals(new String(body, StandardCharsets.UTF_8), new String(read(conn, key), StandardCharsets.UTF_8));
            var info = conn.objectStore(DiscussionArtifacts.BUCKET).getInfo(key);
            assertEquals("pods_list", info.getHeaders().getFirst(CallHeaders.TOOL_NAME));
            assertEquals("2", info.getHeaders().getFirst(CallHeaders.ROWS));
            assertEquals("json", info.getHeaders().getFirst(CallHeaders.FORMAT));
            assertEquals("{\"a\": \"b\"}", info.getHeaders().getFirst(CallHeaders.TOOL_CALL_ARGUMENTS),
                    "a line break in a value becomes a space");
            // The wire form other clients (artifact-access, in Go) read: headers as name -> list of values.
            String metaSubject = "$O." + DiscussionArtifacts.BUCKET + ".M."
                    + java.util.Base64.getUrlEncoder().encodeToString(key.getBytes(StandardCharsets.UTF_8));
            String rawMeta = new String(conn.jetStreamManagement()
                    .getLastMessage("OBJ_" + DiscussionArtifacts.BUCKET, metaSubject).getData(), StandardCharsets.UTF_8);
            assertTrue(rawMeta.contains("\"gen_ai.tool.name\":[\"pods_list\"]"), rawMeta);
            String reference = references.poll(5, TimeUnit.SECONDS);
            assertTrue(reference != null && reference.contains("\"key\":\"" + key + "\"")
                    && reference.contains("\"rows\":2") && reference.contains("application/json"), reference);

            assertEquals(1, DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t1")));
            assertThrows(Exception.class, () -> read(conn, key));
        }
    }

    @Test
    void numberingCarriesOnAfterARestartInsteadOfOverwritingEarlierCalls() throws Exception {
        try (Connection conn = Nats.connect(url)) {
            var provider = mock(NatsConnectionProvider.class);
            when(provider.scope()).thenReturn(SCOPE);
            when(provider.getConnection()).thenReturn(conn);
            var before = new NatsCallArtifactStore(provider, new ObjectMapper());
            for (int i = 0; i < 3; i++) {
                String key = before.reserveKey("t-restart", "a", "tool");
                assertTrue(before.put("t-restart", "a", "tool", key, new byte[] {'x'}, new LinkedHashMap<>()));
            }

            var after = new NatsCallArtifactStore(provider, new ObjectMapper());

            assertEquals("ns-a/homelab-pilot/t-restart/a/004-tool", after.reserveKey("t-restart", "a", "tool"));
            assertEquals("ns-a/homelab-pilot/t-restart/a/005-tool", after.reserveKey("t-restart", "a", "tool"));
            assertEquals("ns-a/homelab-pilot/t-restart/b/001-tool", after.reserveKey("t-restart", "b", "tool"),
                    "another agent of the thread has its own numbers");
        }
    }

    @Test
    void anotherThreadsCallsAreNotDeleted() throws Exception {
        try (Connection conn = Nats.connect(url)) {
            var provider = mock(NatsConnectionProvider.class);
            when(provider.scope()).thenReturn(SCOPE);
            when(provider.getConnection()).thenReturn(conn);
            var store = new NatsCallArtifactStore(provider, new ObjectMapper());
            String mine = store.reserveKey("t-keep", "a", "tool");
            assertTrue(store.put("t-keep", "a", "tool", mine, new byte[] {'x'}, new LinkedHashMap<>()));
            String gone = store.reserveKey("t-gone", "a", "tool");
            assertTrue(store.put("t-gone", "a", "tool", gone, new byte[] {'y'}, new LinkedHashMap<>()));

            DiscussionArtifacts.deleteThreadArtifacts(conn, SCOPE, List.of("t-gone"));

            assertEquals(1, read(conn, mine).length);
            assertNotEquals(mine, gone);
        }
    }

    @Test
    void aWriteWithNoConnectionReturnsFalse() {
        var provider = mock(NatsConnectionProvider.class);
        when(provider.scope()).thenReturn(SCOPE);
        var store = new NatsCallArtifactStore(provider, new ObjectMapper());

        assertEquals(false, store.put("t", "a", "tool", "k", new byte[0], new LinkedHashMap<>()));
    }
}
