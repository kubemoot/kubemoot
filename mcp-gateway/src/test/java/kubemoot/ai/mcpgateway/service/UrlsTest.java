package kubemoot.ai.mcpgateway.service;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTimeoutPreemptively;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.Test;

class UrlsTest {

    @Test
    void trailingSlashesAreRemovedAndOthersKept() {
        assertEquals("http://a/b", Urls.trimTrailingSlashes("http://a/b///"));
        assertEquals("http://a/b", Urls.trimTrailingSlashes("http://a/b"));
        assertEquals("", Urls.trimTrailingSlashes("///"));
        assertEquals("", Urls.trimTrailingSlashes(""));
        assertEquals("a//b", Urls.trimTrailingSlashes("a//b/"));
    }

    @Test
    void aLongRunOfSlashesIsTrimmedInOnePass() {
        String url = "http://a" + "/".repeat(200_000);
        assertTimeoutPreemptively(java.time.Duration.ofSeconds(2), () -> assertEquals("http://a", Urls.trimTrailingSlashes(url)));
    }

    @Test
    void anMcpEndingIsMcpOrMcpWithASlash() {
        assertTrue(Urls.endsWithMcp("http://s/mcp"));
        assertTrue(Urls.endsWithMcp("http://s/mcp/"));
        assertTrue(Urls.endsWithMcp("/mcp"));
        assertFalse(Urls.endsWithMcp("http://s/mcp2"));
        assertFalse(Urls.endsWithMcp("http://s/xmcp"));
        assertFalse(Urls.endsWithMcp("http://s"));
    }
}
