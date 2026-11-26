package kubemoot.ai.mcpgateway.service;

import java.net.URI;
import java.util.List;
import java.util.Locale;

/**
 * Where a person's connected account token may be sent: an https address whose host is exactly
 * one of the hosts the account declares. A token leased for a connected account goes nowhere else.
 */
final class TokenHosts {

    private TokenHosts() { }

    /** True when {@code url} is https and its host equals one of {@code hosts}, ignoring case. */
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
            return hosts.stream().anyMatch(allowed -> allowed != null && bare.equals(allowed.trim().toLowerCase(Locale.ROOT)));
        } catch (IllegalArgumentException e) {
            return false;
        }
    }
}
