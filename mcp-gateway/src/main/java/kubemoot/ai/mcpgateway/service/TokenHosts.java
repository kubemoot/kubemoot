package kubemoot.ai.mcpgateway.service;

import java.net.URI;
import java.util.List;
import java.util.Locale;

/**
 * Where a person's connected account token may be sent: an https address whose host is exactly
 * one of the hosts the account declares, on the port the declaration allows. A declared host is
 * a bare host name, which allows port 443 only (the https default when the address names none);
 * a declared {@code host:port} allows that port only. A token leased for a connected account goes
 * nowhere else.
 */
final class TokenHosts {

    private TokenHosts() { }

    private static final int HTTPS_PORT = 443;

    /**
     * True when {@code url} is https, its host equals the host of one of {@code hosts} ignoring
     * case, and its port (443 when it names none) is the port that entry allows (443 when it
     * names none).
     */
    static boolean permit(String url, List<String> hosts) {
        if (url == null || hosts == null || hosts.isEmpty()) {
            return false;
        }
        try {
            URI uri = URI.create(url);
            String host = uri.getHost();
            if (!"https".equalsIgnoreCase(uri.getScheme()) || host == null) {
                return false;
            }
            String bare = host.toLowerCase(Locale.ROOT);
            int port = uri.getPort() == -1 ? HTTPS_PORT : uri.getPort();
            return hosts.stream().anyMatch(allowed -> allowed != null && matches(allowed.trim().toLowerCase(Locale.ROOT), bare, port));
        } catch (IllegalArgumentException e) {
            return false;
        }
    }

    /** One declared entry, a bare host or {@code host:port}, against the address's host and port. */
    private static boolean matches(String declared, String host, int port) {
        int colon = declared.lastIndexOf(':');
        if (colon < 0) {
            return declared.equals(host) && port == HTTPS_PORT;
        }
        try {
            return declared.substring(0, colon).equals(host) && Integer.parseInt(declared.substring(colon + 1)) == port;
        } catch (NumberFormatException e) {
            return false;
        }
    }
}
