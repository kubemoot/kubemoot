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
        String path = normalize(request);
        if (path == null) {
            response.sendError(HttpServletResponse.SC_BAD_REQUEST);
            return;
        }
        boolean adminPath = path.equals(ADMIN_PREFIX) || path.startsWith(ADMIN_PREFIX + "/");
        boolean onAdminPort = adminPort > 0 && request.getLocalPort() == adminPort;
        if (onAdminPort) {
            serveAdminPort(request, response, chain, adminPath);
        } else if (adminPath && !isOpenRead(request.getMethod(), path)) {
            response.sendError(HttpServletResponse.SC_NOT_FOUND);
        } else {
            chain.doFilter(request, response);
        }
    }

    private void serveAdminPort(HttpServletRequest request, HttpServletResponse response, FilterChain chain,
            boolean adminPath) throws ServletException, IOException {
        if (!adminPath) {
            response.sendError(HttpServletResponse.SC_NOT_FOUND);
        } else if (!authorized(request)) {
            response.setHeader("WWW-Authenticate", "Bearer");
            response.sendError(HttpServletResponse.SC_UNAUTHORIZED);
        } else {
            chain.doFilter(request, response);
        }
    }

    /**
     * The path the request will be routed by: servlet path plus path info, decoded once,
     * with path parameters removed, slashes collapsed and the trailing slash dropped.
     * Returns null for an ambiguous path (encoded slash, backslash, dot segments, control
     * characters, or a second layer of encoding), which is refused.
     */
    static String normalize(HttpServletRequest request) {
        String raw = request.getRequestURI();
        if (raw == null) {
            return null;
        }
        String context = request.getContextPath();
        if (context != null && !context.isEmpty() && raw.startsWith(context)) {
            raw = raw.substring(context.length());
        }
        if (hasAmbiguousEncoding(raw)) {
            return null;
        }
        String decoded;
        try {
            decoded = java.net.URLDecoder.decode(raw.replace("+", "%2B"), StandardCharsets.UTF_8);
        } catch (IllegalArgumentException e) {
            return null;
        }
        return collapse(decoded);
    }

    private static boolean hasAmbiguousEncoding(String raw) {
        String lower = raw.toLowerCase(java.util.Locale.ROOT);
        return lower.contains("%2f") || lower.contains("%5c") || lower.contains("%25") || raw.indexOf('\\') >= 0;
    }

    /** Drops path parameters, empty segments and the trailing slash; null on dot or control segments. */
    private static String collapse(String decoded) {
        StringBuilder out = new StringBuilder();
        for (String segment : decoded.split("/", -1)) {
            int param = segment.indexOf(';');
            String clean = param >= 0 ? segment.substring(0, param) : segment;
            if (isForbiddenSegment(clean)) {
                return null;
            }
            if (!clean.isEmpty()) {
                out.append('/').append(clean);
            }
        }
        return out.length() == 0 ? "/" : out.toString();
    }

    private static boolean isForbiddenSegment(String segment) {
        return segment.equals(".") || segment.equals("..")
                || segment.chars().anyMatch(c -> c < 0x20 || c == 0x7f);
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
