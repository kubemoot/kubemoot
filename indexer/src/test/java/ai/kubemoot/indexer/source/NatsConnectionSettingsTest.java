package ai.kubemoot.indexer.source;

import io.nats.client.Options;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class NatsConnectionSettingsTest {

    private static final String URL = "nats://nats:4222";

    private static NatsConnectionSettings of(Map<String, String> env) {
        return NatsConnectionSettings.fromEnvironment(env::get);
    }

    private static Options.Builder builder() {
        return new Options.Builder().server(URL);
    }

    @Test
    void readsEveryNameAndTreatsBlankAsAbsent() {
        var s = of(Map.of("NATS_URL", " tls://nats:4222 ", "NATS_CA_FILE", "/ca.crt",
                "NATS_CLIENT_CERT_FILE", "/c.crt", "NATS_CLIENT_KEY_FILE", "/c.key",
                "NATS_CREDS_FILE", "  ", "NATS_USER", "", "NATS_TOKEN", " "));
        assertEquals("tls://nats:4222", s.url());
        assertEquals("/ca.crt", s.caFile());
        assertEquals("/c.crt", s.clientCertFile());
        assertEquals("/c.key", s.clientKeyFile());
        assertEquals("", s.credsFile());
        assertEquals("", s.user());
        assertEquals("", s.token());
        assertTrue(s.hasOptions());
    }

    @Test
    void nothingSetIsAPlainAnonymousConnection() {
        var s = of(Map.of());
        assertFalse(s.hasOptions());
        assertEquals("no TLS options, anonymous", s.describe());
        Options options = s.configure(builder()).build();
        assertEquals(null, options.getSslContext());
        assertEquals(null, options.getUsername());
    }

    @Test
    void contradictionsAreRefusedNamingTheVariables() {
        for (Map<String, String> env : new Map[] {
                Map.of("NATS_CLIENT_CERT_FILE", "/c"),
                Map.of("NATS_CLIENT_KEY_FILE", "/k"),
                Map.of("NATS_USER", "u"),
                Map.of("NATS_PASSWORD", "p"),
                Map.of("NATS_CREDS_FILE", "/c", "NATS_TOKEN", "t"),
                Map.of("NATS_USER", "u", "NATS_PASSWORD", "p", "NATS_TOKEN", "t")}) {
            var e = assertThrows(IllegalArgumentException.class, () -> of(env).validate(), env.toString());
            assertTrue(e.getMessage().contains("NATS_"), e.getMessage());
        }
    }

    @Test
    void userAndPasswordAndTokenReachTheOptions() {
        Options up = of(Map.of("NATS_USER", "alice", "NATS_PASSWORD", "s3cret")).configure(builder()).build();
        assertEquals("alice", up.getUsername());
        assertEquals("s3cret", new String(up.getPasswordChars()));
        Options tok = of(Map.of("NATS_TOKEN", "tok")).configure(builder()).build();
        assertEquals("tok", new String(tok.getAuthHandler() == null ? tok.getTokenChars() : new char[0]));
    }

    @Test
    void describeNeverShowsASecret() {
        String d = of(Map.of("NATS_USER", "alice", "NATS_PASSWORD", "hunter2")).describe();
        assertFalse(d.contains("alice") || d.contains("hunter2"), d);
    }

    @Test
    void explicitUserInfoReplacesAnyOtherCredential() {
        var s = of(Map.of("NATS_TOKEN", "t")).withUserInfo("u", "p");
        s.validate();
        assertEquals("u", s.user());
        assertEquals("", s.token());
    }

    @Test
    void aMissingOrWrongFileFailsNamingItsVariable(@TempDir Path dir) throws Exception {
        var missing = of(Map.of("NATS_CA_FILE", dir.resolve("absent").toString()));
        assertTrue(assertThrows(IllegalArgumentException.class, () -> missing.configure(builder()))
                .getMessage().contains("NATS_CA_FILE"));

        Path notPem = Files.writeString(dir.resolve("ca.crt"), "not a certificate");
        var wrongCa = of(Map.of("NATS_CA_FILE", notPem.toString()));
        assertThrows(IllegalArgumentException.class, () -> wrongCa.configure(builder()));

        Path empty = Files.writeString(dir.resolve("empty.creds"), "");
        var emptyCreds = of(Map.of("NATS_CREDS_FILE", empty.toString()));
        assertTrue(assertThrows(IllegalArgumentException.class, () -> emptyCreds.configure(builder()))
                .getMessage().contains("empty"));
    }

    @Test
    void aKeyThatIsNotPkcs8IsRefusedWithTheConversionToDo(@TempDir Path dir) throws Exception {
        Path key = Files.writeString(dir.resolve("k.pem"), "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n");
        Path cert = Files.writeString(dir.resolve("c.pem"), "x");
        var s = of(Map.of("NATS_CLIENT_CERT_FILE", cert.toString(), "NATS_CLIENT_KEY_FILE", key.toString()));
        var e = assertThrows(IllegalArgumentException.class, () -> s.configure(builder()));
        assertTrue(e.getMessage().contains("PKCS#8") || e.getMessage().contains("PEM"), e.getMessage());
    }

    @Test
    void aPkcs8KeyNoAlgorithmAcceptsIsRefusedWithAnExplainedFailureNotANullPointer(@TempDir Path dir) throws Exception {
        Path key = Files.writeString(dir.resolve("k.pem"), "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n");
        Path cert = Files.writeString(dir.resolve("c.pem"), "x");
        var s = of(Map.of("NATS_CLIENT_CERT_FILE", cert.toString(), "NATS_CLIENT_KEY_FILE", key.toString()));
        var e = assertThrows(IllegalArgumentException.class, () -> s.configure(builder()));
        assertTrue(e.getMessage().contains("unusable"), e.getMessage());
    }
}
