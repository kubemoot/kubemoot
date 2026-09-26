package ai.kubemoot.indexer.service;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * Tests for ChecksumService pure logic and the document-checksum branch that
 * does not require network access. Branches that issue real HTTP/Git requests
 * are not exercised here (they need live endpoints).
 */
class ChecksumServiceTest {

    private ChecksumService newService(IndexerConfig config) {
        WebClient.Builder builder = WebClient.builder();
        return new ChecksumService(config, builder);
    }

    @Test
    void isForceReindex_trueWhenConfigured() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.forceReindex()).thenReturn("true");

        assertTrue(newService(config).isForceReindex());
    }

    @Test
    void isForceReindex_caseInsensitive() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.forceReindex()).thenReturn("TrUe");

        assertTrue(newService(config).isForceReindex());
    }

    @Test
    void isForceReindex_falseForOtherValues() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.forceReindex()).thenReturn("false");

        assertFalse(newService(config).isForceReindex());
    }

    @Test
    void getLastChecksum_returnsConfiguredValue() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.lastChecksum()).thenReturn("sha256:abc");

        org.junit.jupiter.api.Assertions.assertEquals(
            "sha256:abc", newService(config).getLastChecksum());
    }

    @Test
    void hasSourceChanged_trueWhenForceReindex() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.forceReindex()).thenReturn("true");

        assertTrue(newService(config).hasSourceChanged());
    }

    @Test
    void hasSourceChanged_trueWhenNoPreviousChecksum() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.forceReindex()).thenReturn("false");
        when(config.lastChecksum()).thenReturn(null);

        assertTrue(newService(config).hasSourceChanged());
    }

    @Test
    void hasSourceChanged_trueWhenPreviousChecksumBlank() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.forceReindex()).thenReturn("false");
        when(config.lastChecksum()).thenReturn("   ");

        assertTrue(newService(config).hasSourceChanged());
    }

    @Test
    void calculateChecksum_documentWithNoUrlsReturnsNull() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.sourceType()).thenReturn("document");
        when(config.documentUrls()).thenReturn(List.of());

        assertNull(newService(config).calculateChecksum());
    }

    @Test
    void calculateChecksum_unknownSourceTypeReturnsNull() {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.sourceType()).thenReturn("does-not-exist");

        assertNull(newService(config).calculateChecksum());
    }
}
