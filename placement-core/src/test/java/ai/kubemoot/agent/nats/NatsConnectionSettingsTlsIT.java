package ai.kubemoot.agent.nats;

import io.nats.client.Connection;
import io.nats.client.Nats;
import io.nats.client.Options;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.net.ServerSocket;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

/**
 * Against a real nats-server that demands TLS with a client certificate and a user (runs only when
 * NATS_SERVER_BIN points at a binary and openssl is installed): a client with the CA, the client
 * certificate and the credential connects, and one without each of them is refused.
 */
class NatsConnectionSettingsTlsIT {

    private static Process server;
    private static Path dir;
    private static String url;

    @BeforeAll
    static void start() throws Exception {
        String bin = System.getenv("NATS_SERVER_BIN");
        assumeTrue(bin != null && new File(bin).canExecute(), "NATS_SERVER_BIN not set");
        dir = Files.createTempDirectory("nats-tls");
        assumeTrue(openssl("version") == 0, "openssl not installed");
        makeCertificates();
        int port;
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        Files.writeString(dir.resolve("nats.conf"), """
                host: 127.0.0.1
                port: %d
                authorization { user: kubemoot, password: "s3cret-pass" }
                tls { cert_file: "%s", key_file: "%s", ca_file: "%s", verify: true, timeout: 5 }
                """.formatted(port, dir.resolve("server.crt"), dir.resolve("server.key"), dir.resolve("ca.crt")));
        server = new ProcessBuilder(bin, "-c", dir.resolve("nats.conf").toString())
                .redirectErrorStream(true).redirectOutput(dir.resolve("server.log").toFile()).start();
        url = "tls://127.0.0.1:" + port;
        for (int i = 0; i < 50; i++) {
            try (var c = new java.net.Socket("127.0.0.1", port)) {
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
    static void stop() {
        if (server != null) {
            server.destroy();
        }
    }

    private static int openssl(String... args) throws Exception {
        var command = new java.util.ArrayList<String>(List.of("openssl"));
        command.addAll(List.of(args));
        return new ProcessBuilder(command).directory(dir.toFile()).redirectErrorStream(true)
                .redirectOutput(ProcessBuilder.Redirect.DISCARD).start().waitFor();
    }

    private static void makeCertificates() throws Exception {
        assertEquals(0, openssl("genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-out", "ca.key"));
        assertEquals(0, openssl("req", "-x509", "-new", "-key", "ca.key", "-subj", "/CN=test CA", "-days", "2", "-out", "ca.crt"));
        for (String leaf : new String[] {"server", "client"}) {
            assertEquals(0, openssl("genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-out", leaf + ".key"));
            assertEquals(0, openssl("req", "-new", "-key", leaf + ".key", "-subj", "/CN=" + leaf, "-out", leaf + ".csr"));
            Files.writeString(dir.resolve(leaf + ".ext"), "subjectAltName=IP:127.0.0.1,DNS:localhost\n");
            assertEquals(0, openssl("x509", "-req", "-in", leaf + ".csr", "-CA", "ca.crt", "-CAkey", "ca.key",
                    "-CAcreateserial", "-days", "2", "-extfile", leaf + ".ext", "-out", leaf + ".crt"));
        }
    }

    private static Map<String, String> full() {
        return Map.of("NATS_CA_FILE", dir.resolve("ca.crt").toString(),
                "NATS_CLIENT_CERT_FILE", dir.resolve("client.crt").toString(),
                "NATS_CLIENT_KEY_FILE", dir.resolve("client.key").toString(),
                "NATS_USER", "kubemoot", "NATS_PASSWORD", "s3cret-pass");
    }

    private static Connection connect(Map<String, String> env) throws Exception {
        var settings = NatsConnectionSettings.fromEnvironment(env::get);
        Options options = settings.configure(new Options.Builder().server(url).maxReconnects(0)
                .connectionTimeout(java.time.Duration.ofSeconds(3))).build();
        return Nats.connect(options);
    }

    @Test
    void connectsWithTheCaTheClientCertificateAndTheCredential() throws Exception {
        try (Connection c = connect(full())) {
            assertEquals(Connection.Status.CONNECTED, c.getStatus());
        }
    }

    @Test
    void isRefusedWithoutTheCa() {
        var env = new java.util.HashMap<>(full());
        env.remove("NATS_CA_FILE");
        assertThrows(Exception.class, () -> connect(env).close());
    }

    @Test
    void isRefusedWithoutTheClientCertificate() {
        var env = new java.util.HashMap<>(full());
        env.remove("NATS_CLIENT_CERT_FILE");
        env.remove("NATS_CLIENT_KEY_FILE");
        assertThrows(Exception.class, () -> connect(env).close());
    }

    @Test
    void isRefusedWithoutTheCredential() {
        var env = new java.util.HashMap<>(full());
        env.remove("NATS_USER");
        env.remove("NATS_PASSWORD");
        var e = assertThrows(Exception.class, () -> connect(env).close());
        assertTrue(e.getMessage().toLowerCase().contains("authorization"), e.getMessage());
    }
}
