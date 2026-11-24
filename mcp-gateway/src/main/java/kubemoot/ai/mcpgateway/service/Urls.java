package kubemoot.ai.mcpgateway.service;

/** Small URL text helpers. */
final class Urls {

    private Urls() {
    }

    /** {@code url} without the slashes at its end; a loop, so a long run of slashes costs one pass. */
    static String trimTrailingSlashes(String url) {
        int end = url.length();
        while (end > 0 && url.charAt(end - 1) == '/') {
            end--;
        }
        return url.substring(0, end);
    }

    /** True when {@code url} ends in {@code /mcp} or {@code /mcp/}. */
    static boolean endsWithMcp(String url) {
        return url.endsWith("/mcp") || url.endsWith("/mcp/");
    }
}
