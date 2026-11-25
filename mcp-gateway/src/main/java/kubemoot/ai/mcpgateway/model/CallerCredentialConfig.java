package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.annotation.JsonCreator;
import com.fasterxml.jackson.annotation.JsonProperty;

import java.net.URI;
import java.util.List;
import java.util.regex.Pattern;

/**
 * How one server gets the credential for a {@code tools/call}. In mode {@code Off} (the
 * default) the call carries the server's own registered credential, as before. In mode
 * {@code Required} the call carries the token of the person who asked, leased from the
 * credential broker for that one call; the server's registered credential is then used only
 * to list tools, never on a call.
 *
 * @param mode         {@code Off} or {@code Required}
 * @param leaseUrl     the broker's lease endpoint for this server; empty means the gateway-wide
 *                     broker address plus {@code /internal/v1/lease}
 * @param verifier     the name of the verifier that vouched for the person, sent with the lease request
 * @param accountName  how a refusal names the account to connect; the verifier name when empty
 * @param onForbidden  endpoints tried in order, with the same token, when the server's own endpoint
 *                     answers 403 to the asker
 * @param relayContext keep {@code conversationId} and {@code principalId} in the forwarded
 *                     {@code _meta}, for a tool server that calls back through the gateway
 * @param connectedAccount the name of the crew's connected account whose token the person's call
 *                     carries, in place of a verifier; in mode {@code Required} exactly one of the two is set
 */
public record CallerCredentialConfig(
    Mode mode,
    String leaseUrl,
    String verifier,
    String accountName,
    List<String> onForbidden,
    boolean relayContext,
    String connectedAccount
) {
    public enum Mode {
        @JsonProperty("Off") OFF,
        @JsonProperty("Required") REQUIRED
    }

    /** The default: the server's own credential, nothing per person. */
    public static final CallerCredentialConfig OFF =
        new CallerCredentialConfig(Mode.OFF, null, null, null, List.of(), false, null);

    private static final Pattern DNS_LABEL = Pattern.compile("[a-z0-9]([-a-z0-9]*[a-z0-9])?");
    private static final int MAX_FALLBACKS = 4;

    /** A configuration that takes its token from a verifier, or from none. */
    public CallerCredentialConfig(Mode mode, String leaseUrl, String verifier, String accountName,
                                  List<String> onForbidden, boolean relayContext) {
        this(mode, leaseUrl, verifier, accountName, onForbidden, relayContext, null);
    }

    @JsonCreator
    public CallerCredentialConfig(
            @JsonProperty("mode") Mode mode,
            @JsonProperty("leaseUrl") String leaseUrl,
            @JsonProperty("verifier") String verifier,
            @JsonProperty("accountName") String accountName,
            @JsonProperty("onForbidden") List<String> onForbidden,
            @JsonProperty("relayContext") Boolean relayContext,
            @JsonProperty("account") String connectedAccount) {
        this(mode, leaseUrl, verifier, accountName, onForbidden, Boolean.TRUE.equals(relayContext), connectedAccount);
    }

    public CallerCredentialConfig {
        mode = mode == null ? Mode.OFF : mode;
        onForbidden = onForbidden == null ? List.of() : List.copyOf(onForbidden);
        leaseUrl = blankToNull(leaseUrl);
        verifier = blankToNull(verifier);
        accountName = blankToNull(accountName);
        connectedAccount = blankToNull(connectedAccount);
        if (mode == Mode.REQUIRED) {
            requireOneSource(verifier, connectedAccount);
            if (leaseUrl != null) {
                requireHttp("callerCredential.leaseUrl", leaseUrl);
            }
            if (onForbidden.size() > MAX_FALLBACKS) {
                throw new IllegalArgumentException("callerCredential.onForbidden holds at most " + MAX_FALLBACKS);
            }
            onForbidden.forEach(CallerCredentialConfig::requireHttpsOrLoopback);
        }
    }

    public boolean required() {
        return mode == Mode.REQUIRED;
    }

    /** What a refusal calls the account to connect. */
    public String account() {
        if (accountName != null) {
            return accountName;
        }
        return verifier != null ? verifier : connectedAccount;
    }

    /** Mode Required takes the person's token from exactly one source, named by a DNS label. */
    private static void requireOneSource(String verifier, String connectedAccount) {
        if ((verifier == null) == (connectedAccount == null)) {
            throw new IllegalArgumentException(
                "callerCredential takes exactly one of verifier and account in mode Required");
        }
        boolean viaVerifier = verifier != null;
        String name = viaVerifier ? verifier : connectedAccount;
        if (!DNS_LABEL.matcher(name).matches()) {
            throw new IllegalArgumentException(
                "callerCredential." + (viaVerifier ? "verifier" : "account") + " must be a DNS label in mode Required");
        }
    }

    private static String blankToNull(String value) {
        return value == null || value.isBlank() ? null : value.trim();
    }

    /** A fallback endpoint receives the person's token: https, or plain http only to a loopback host. */
    private static void requireHttpsOrLoopback(String value) {
        requireHttp("callerCredential.onForbidden", value);
        URI uri = URI.create(value);
        if (!"https".equalsIgnoreCase(uri.getScheme()) && !isLoopback(uri.getHost())) {
            throw new IllegalArgumentException("callerCredential.onForbidden must be https (plain http is allowed"
                + " only for localhost, 127.0.0.0/8 and ::1) because the endpoint receives the person's token");
        }
    }

    private static boolean isLoopback(String host) {
        String bare = host.startsWith("[") ? host.substring(1, host.length() - 1) : host;
        return "localhost".equalsIgnoreCase(bare) || "::1".equals(bare) || bare.matches("127(\\.\\d{1,3}){3}");
    }

    private static void requireHttp(String field, String value) {
        try {
            URI uri = URI.create(value);
            boolean web = "https".equalsIgnoreCase(uri.getScheme()) || "http".equalsIgnoreCase(uri.getScheme());
            if (!web || uri.getHost() == null) {
                throw new IllegalArgumentException(field + " must be an http or https URL");
            }
        } catch (IllegalArgumentException e) {
            throw new IllegalArgumentException(field + " must be an http or https URL", e);
        }
    }
}
