package kubemoot.ai.mcpgateway.config;

import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import org.springframework.web.filter.OncePerRequestFilter;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.regex.Pattern;

/**
 * Splits the gateway's HTTP surface across two ports.
 *
 * <p>The tool port serves tool calls, the MCP endpoints and the read-only tool lists
 * ({@code GET /admin/tools}, {@code GET /admin/servers/{id}/tools}, {@code GET /admin/search/tools})
 * that agents read. Every other {@code /admin} route (registration, removal, server list,
 * feedback) is answered 404 there.
 *
 * <p>The admin port serves only {@code /admin} routes, and only to a caller that presents the
 * bearer token. With no token configured, or no admin port, the mutating routes are not
 * reachable at all.
 */
public class AdminAccessFilter extends OncePerRequestFilter {

    private static final String ADMIN_PREFIX = "/admin";
    private static final String BEARER = "bearer ";
    private static final Pattern SERVER_TOOLS = Pattern.compile("^/admin/servers/[^/]+/tools$");

    private final int adminPort;
    private final byte[] tokenDigest;

    public AdminAccessFilter(int adminPort, String token) {
        this.adminPort = adminPort;
        this.tokenDigest = (token == null || token.isBlank()) ? null : digest(token);
    }

    @Override
    protected void doFilterInternal(HttpServletRequest request, HttpServletResponse response, FilterChain chain)
            throws ServletException, IOException {
        String path = request.getServletPath();
        boolean adminPath = path.equals(ADMIN_PREFIX) || path.startsWith(ADMIN_PREFIX + "/");
        boolean onAdminPort = adminPort > 0 && request.getLocalPort() == adminPort;

        if (onAdminPort) {
            if (!adminPath) {
                response.sendError(HttpServletResponse.SC_NOT_FOUND);
            } else if (!authorized(request)) {
                response.setHeader("WWW-Authenticate", "Bearer");
                response.sendError(HttpServletResponse.SC_UNAUTHORIZED);
            } else {
                chain.doFilter(request, response);
            }
            return;
        }
        if (adminPath && !isOpenRead(request.getMethod(), path)) {
            response.sendError(HttpServletResponse.SC_NOT_FOUND);
            return;
        }
        chain.doFilter(request, response);
    }

    /** The tool lists agents read; nothing that changes or consumes state. */
    static boolean isOpenRead(String method, String path) {
        if (!"GET".equals(method)) {
            return false;
        }
        return path.equals("/admin/tools") || path.equals("/admin/search/tools") || SERVER_TOOLS.matcher(path).matches();
    }

    private boolean authorized(HttpServletRequest request) {
        if (tokenDigest == null) {
            return false;
        }
        String header = request.getHeader("Authorization");
        if (header == null || header.length() <= BEARER.length()
                || !header.regionMatches(true, 0, BEARER, 0, BEARER.length())) {
            return false;
        }
        String presented = header.substring(BEARER.length()).trim();
        return MessageDigest.isEqual(tokenDigest, digest(presented));
    }

    /** Equal-length digests make the comparison independent of the token's length and content. */
    private static byte[] digest(String value) {
        try {
            return MessageDigest.getInstance("SHA-256").digest(value.getBytes(StandardCharsets.UTF_8));
        } catch (java.security.NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 is unavailable", e);
        }
    }
}
