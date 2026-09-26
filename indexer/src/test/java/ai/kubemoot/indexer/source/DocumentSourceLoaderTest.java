package ai.kubemoot.indexer.source;

import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Tests for DocumentSourceLoader source-type matching.
 * Document conversion and docling-serve connectivity are not tested here
 * (they require a running docling-serve sidecar).
 */
class DocumentSourceLoaderTest {

    // supports() reads no fields, so a default WebClient builder (never used for I/O) suffices.
    private final DocumentSourceLoader loader =
            new DocumentSourceLoader(null, WebClient.builder());

    @Test
    void supports_document() {
        assertTrue(loader.supports("document"), "exact lowercase type should match");
        assertTrue(loader.supports("DOCUMENT"), "uppercase type should match (case-insensitive)");
        assertTrue(loader.supports("Document"), "mixed-case type should match (case-insensitive)");
    }

    @Test
    void supports_rejectsOtherTypes() {
        assertFalse(loader.supports("git"), "git source type should not match");
        assertFalse(loader.supports("nats-kv"), "nats-kv source type should not match");
        assertFalse(loader.supports(""), "empty string should not match");
    }
}
