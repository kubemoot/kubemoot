package ai.kubemoot.indexer.source;

import io.nats.client.Options;

import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManagerFactory;
import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.KeyStore;
import java.security.PrivateKey;
import java.security.cert.Certificate;
import java.security.cert.CertificateFactory;
import java.security.spec.PKCS8EncodedKeySpec;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Collection;
import java.util.List;
import java.util.function.Function;

/**
 * The NATS connection options every Kubemoot component reads the same way. This is a copy of
 * placement-core's {@code NatsConnectionSettings}: the indexer is a separate build with no shared
 * library, so keep the two in step. The options are the URL ({@code tls://}
 * accepted), a CA bundle, an optional client certificate and key for mutual TLS, and one kind of
 * credential (a creds file, a user and password, or a token). With none set, the component connects
 * plainly and anonymously.
 *
 * <p>The names are the contract shared with the Go and TypeScript components: {@code NATS_URL},
 * {@code NATS_CA_FILE}, {@code NATS_CLIENT_CERT_FILE}, {@code NATS_CLIENT_KEY_FILE},
 * {@code NATS_CREDS_FILE}, {@code NATS_USER}, {@code NATS_PASSWORD}, {@code NATS_TOKEN}. The CA bundle
 * and the certificate are PEM; the private key is PEM PKCS#8 ({@code BEGIN PRIVATE KEY}).
 */
public record NatsConnectionSettings(String url, String caFile, String clientCertFile, String clientKeyFile,
                                     String credsFile, String user, String password, String token) {

    public static final String ENV_URL = "NATS_URL";
    public static final String ENV_CA_FILE = "NATS_CA_FILE";
    public static final String ENV_CLIENT_CERT_FILE = "NATS_CLIENT_CERT_FILE";
    public static final String ENV_CLIENT_KEY_FILE = "NATS_CLIENT_KEY_FILE";
    public static final String ENV_CREDS_FILE = "NATS_CREDS_FILE";
    public static final String ENV_USER = "NATS_USER";
    public static final String ENV_PASSWORD = "NATS_PASSWORD";
    public static final String ENV_TOKEN = "NATS_TOKEN";

    private static final String PRIVATE_KEY_BEGIN = "-----BEGIN PRIVATE KEY-----";
    private static final String PRIVATE_KEY_END = "-----END PRIVATE KEY-----";

    public NatsConnectionSettings {
        url = blankToEmpty(url);
        caFile = blankToEmpty(caFile);
        clientCertFile = blankToEmpty(clientCertFile);
        clientKeyFile = blankToEmpty(clientKeyFile);
        credsFile = blankToEmpty(credsFile);
        user = blankToEmpty(user);
        token = blankToEmpty(token);
        password = password == null ? "" : password;
    }

    /** Reads the settings through {@code getenv} ({@code System::getenv} in a process). Blank is absent. */
    public static NatsConnectionSettings fromEnvironment(Function<String, String> getenv) {
        return new NatsConnectionSettings(getenv.apply(ENV_URL), getenv.apply(ENV_CA_FILE),
                getenv.apply(ENV_CLIENT_CERT_FILE), getenv.apply(ENV_CLIENT_KEY_FILE),
                getenv.apply(ENV_CREDS_FILE), getenv.apply(ENV_USER), getenv.apply(ENV_PASSWORD),
                getenv.apply(ENV_TOKEN));
    }

    /** The same settings with an explicit user and password, which replace any other credential. */
    public NatsConnectionSettings withUserInfo(String newUser, String newPassword) {
        return new NatsConnectionSettings(url, caFile, clientCertFile, clientKeyFile, "", newUser, newPassword, "");
    }

    /** The same settings dialing another URL. */
    public NatsConnectionSettings withUrl(String newUrl) {
        return new NatsConnectionSettings(newUrl, caFile, clientCertFile, clientKeyFile, credsFile, user,
                password, token);
    }

    /**
     * Checks for a combination that cannot work.
     *
     * @throws IllegalArgumentException naming the variables involved
     */
    public void validate() {
        if (clientCertFile.isEmpty() != clientKeyFile.isEmpty()) {
            throw new IllegalArgumentException(ENV_CLIENT_CERT_FILE + " and " + ENV_CLIENT_KEY_FILE
                    + " must be set together");
        }
        if (user.isEmpty() != password.isEmpty()) {
            throw new IllegalArgumentException(ENV_USER + " and " + ENV_PASSWORD + " must be set together");
        }
        if (credentialKinds() > 1) {
            throw new IllegalArgumentException("set only one of " + ENV_CREDS_FILE + ", " + ENV_USER + " with "
                    + ENV_PASSWORD + ", or " + ENV_TOKEN);
        }
    }

    private int credentialKinds() {
        int kinds = 0;
        for (boolean set : new boolean[] {!credsFile.isEmpty(), !user.isEmpty(), !token.isEmpty()}) {
            if (set) {
                kinds++;
            }
        }
        return kinds;
    }

    /** True when a CA, a client certificate or a credential is set. */
    public boolean hasOptions() {
        return !caFile.isEmpty() || !clientCertFile.isEmpty() || credentialKinds() > 0;
    }

    /**
     * Adds the TLS context and the credential to a connection builder. Files are read now, so a
     * missing or unusable one fails at startup naming its variable.
     *
     * @throws IllegalArgumentException on a contradiction or a file that cannot be used
     */
    public Options.Builder configure(Options.Builder builder) {
        validate();
        if (!caFile.isEmpty() || !clientCertFile.isEmpty()) {
            builder.sslContext(sslContext());
        }
        if (!credsFile.isEmpty()) {
            requireReadable(ENV_CREDS_FILE, credsFile);
            builder.credentialPath(credsFile);
        } else if (!user.isEmpty()) {
            builder.userInfo(user, password);
        } else if (!token.isEmpty()) {
            builder.token(token.toCharArray());
        }
        return builder;
    }

    /** What is configured, never a secret value. */
    public String describe() {
        List<String> parts = new ArrayList<>();
        if (!caFile.isEmpty()) {
            parts.add("ca " + caFile);
        }
        if (!clientCertFile.isEmpty()) {
            parts.add("client certificate");
        }
        if (!credsFile.isEmpty()) {
            parts.add("creds file");
        } else if (!user.isEmpty()) {
            parts.add("user and password");
        } else if (!token.isEmpty()) {
            parts.add("token");
        }
        return parts.isEmpty() ? "no TLS options, anonymous" : String.join(", ", parts);
    }

    private SSLContext sslContext() {
        try {
            TrustManagerFactory trust = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
            trust.init(caFile.isEmpty() ? null : trustStore());
            KeyManagerFactory keys = null;
            if (!clientCertFile.isEmpty()) {
                keys = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
                keys.init(clientKeyStore(), new char[0]);
            }
            SSLContext context = SSLContext.getInstance("TLS");
            context.init(keys == null ? null : keys.getKeyManagers(), trust.getTrustManagers(), null);
            return context;
        } catch (GeneralSecurityException | IOException e) {
            throw new IllegalArgumentException("NATS TLS settings (" + describe() + ") are unusable: "
                    + e.getMessage(), e);
        }
    }

    private KeyStore trustStore() throws GeneralSecurityException, IOException {
        KeyStore store = emptyStore();
        int i = 0;
        for (Certificate cert : certificates(ENV_CA_FILE, caFile)) {
            store.setCertificateEntry("ca-" + i++, cert);
        }
        return store;
    }

    private KeyStore clientKeyStore() throws GeneralSecurityException, IOException {
        PrivateKey key = privateKey();
        List<Certificate> chain = certificates(ENV_CLIENT_CERT_FILE, clientCertFile);
        KeyStore store = emptyStore();
        store.setKeyEntry("client", key, new char[0], chain.toArray(new Certificate[0]));
        return store;
    }

    private static KeyStore emptyStore() throws GeneralSecurityException, IOException {
        KeyStore store = KeyStore.getInstance(KeyStore.getDefaultType());
        store.load(null, null);
        return store;
    }

    private static List<Certificate> certificates(String env, String file) throws GeneralSecurityException {
        byte[] pem = readFile(env, file);
        Collection<? extends Certificate> certs =
                CertificateFactory.getInstance("X.509").generateCertificates(new ByteArrayInputStream(pem));
        if (certs.isEmpty()) {
            throw new IllegalArgumentException(env + ": " + file + " holds no PEM certificate");
        }
        return new ArrayList<>(certs);
    }

    private PrivateKey privateKey() throws GeneralSecurityException {
        String pem = new String(readFile(ENV_CLIENT_KEY_FILE, clientKeyFile), StandardCharsets.US_ASCII);
        int start = pem.indexOf(PRIVATE_KEY_BEGIN);
        int end = pem.indexOf(PRIVATE_KEY_END);
        if (start < 0 || end < start) {
            throw new IllegalArgumentException(ENV_CLIENT_KEY_FILE + ": " + clientKeyFile
                    + " must be a PKCS#8 PEM key (BEGIN PRIVATE KEY); convert with openssl pkcs8 -topk8 -nocrypt");
        }
        byte[] der = Base64.getMimeDecoder().decode(pem.substring(start + PRIVATE_KEY_BEGIN.length(), end));
        GeneralSecurityException last = new GeneralSecurityException("no key algorithm accepted the key");
        for (String algorithm : new String[] {"EC", "RSA", "Ed25519"}) {
            try {
                return KeyFactory.getInstance(algorithm).generatePrivate(new PKCS8EncodedKeySpec(der));
            } catch (GeneralSecurityException e) {
                last = e;
            }
        }
        throw last;
    }

    private static void requireReadable(String env, String file) {
        if (readFile(env, file).length == 0) {
            throw new IllegalArgumentException(env + ": " + file + " is empty");
        }
    }

    private static byte[] readFile(String env, String file) {
        try {
            return Files.readAllBytes(Path.of(file));
        } catch (IOException e) {
            throw new IllegalArgumentException(env + ": cannot read " + file + ": " + e.getMessage(), e);
        }
    }

    private static String blankToEmpty(String value) {
        return value == null ? "" : value.trim();
    }
}
