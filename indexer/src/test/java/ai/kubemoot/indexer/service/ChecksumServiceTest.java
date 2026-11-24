package ai.kubemoot.indexer.service;

import ai.kubemoot.indexer.config.IndexerConfig;
import ai.kubemoot.indexer.testsupport.StubHttpServer;
import ai.kubemoot.indexer.testsupport.StubHttpServer.Reply;
import org.eclipse.jgit.api.Git;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.springframework.web.reactive.function.client.WebClient;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * Tests for ChecksumService: its pure logic, and each source type's checksum against
 * a local HTTP stub or a local git repository. The S3 source has no checksum yet and
 * the nats-kv source reads the environment, so neither is exercised against a remote.
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

    private static final String SHA256_PREFIX = "sha256:";

    private IndexerConfig typed(String type) {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.sourceType()).thenReturn(type);
        return config;
    }

    @Test
    void urlChecksumFollowsTheEtagAndLastModifiedHeaders() {
        try (StubHttpServer server = new StubHttpServer()
            .route("HEAD", "/a", Reply.withHeaders(Map.of("ETag", "\"v1\"")))) {
            IndexerConfig config = typed("url");
            when(config.url()).thenReturn(server.baseUrl() + "/a");
            ChecksumService service = newService(config);

            String first = service.calculateChecksum();

            assertTrue(first.startsWith(SHA256_PREFIX));
            assertEquals(first, service.calculateChecksum(), "same headers, same checksum");
        }
    }

    @Test
    void urlChecksumChangesWhenTheEtagChanges() {
        String[] etag = {"\"v1\""};
        try (StubHttpServer server = new StubHttpServer()
            .route("HEAD", "/a", body -> Reply.withHeaders(Map.of("ETag", etag[0])))) {
            IndexerConfig config = typed("url");
            when(config.url()).thenReturn(server.baseUrl() + "/a");
            ChecksumService service = newService(config);

            String before = service.calculateChecksum();
            etag[0] = "\"v2\"";

            assertNotEquals(before, service.calculateChecksum());
        }
    }

    @Test
    void urlChecksumIsNullWithoutUsableHeadersOrUrl() {
        try (StubHttpServer server = new StubHttpServer().route("HEAD", "/bare", Reply.ok(""))) {
            IndexerConfig config = typed("url");
            when(config.url()).thenReturn(server.baseUrl() + "/bare");
            assertNull(newService(config).calculateChecksum());
        }
        IndexerConfig unset = typed("url");
        assertNull(newService(unset).calculateChecksum());
    }

    @Test
    void mcpRegistryChecksumFollowsTheServerList() {
        String[] catalog = {"[{\"name\":\"a\"}]"};
        try (StubHttpServer server = new StubHttpServer()
            .route("GET", "/servers", body -> Reply.ok(catalog[0]))) {
            IndexerConfig config = typed("mcp-registry");
            when(config.mcpRegistryUrl()).thenReturn(server.baseUrl());
            ChecksumService service = newService(config);

            String before = service.calculateChecksum();
            assertEquals(before, service.calculateChecksum());
            catalog[0] = "[{\"name\":\"a\"},{\"name\":\"b\"}]";

            assertNotEquals(before, service.calculateChecksum());
        }
    }

    @Test
    void mcpRegistryChecksumIsNullWhenUnsetOrUnreachable() {
        assertNull(newService(typed("mcp-registry")).calculateChecksum());
        try (StubHttpServer server = new StubHttpServer()) {
            IndexerConfig config = typed("mcp-registry");
            when(config.mcpRegistryUrl()).thenReturn(server.baseUrl());
            assertNull(newService(config).calculateChecksum());
        }
    }

    @Test
    void documentChecksumCoversEveryUrlAndSurvivesAFailingOne() {
        try (StubHttpServer server = new StubHttpServer()
            .route("HEAD", "/a.pdf", Reply.withHeaders(Map.of("ETag", "\"a1\"")))) {
            IndexerConfig config = typed("document");
            when(config.documentUrls()).thenReturn(List.of(server.baseUrl() + "/a.pdf", "http://127.0.0.1:1/down"));

            String checksum = newService(config).calculateChecksum();

            assertTrue(checksum.startsWith(SHA256_PREFIX));
            IndexerConfig onlyFirst = typed("document");
            when(onlyFirst.documentUrls()).thenReturn(List.of(server.baseUrl() + "/a.pdf"));
            assertNotEquals(checksum, newService(onlyFirst).calculateChecksum());
        }
    }

    @Test
    void s3ChecksumIsNullSoTheSourceAlwaysReindexes() {
        assertNull(newService(typed("s3")).calculateChecksum());
    }

    @Test
    void gitChecksumIsTheBranchCommitAndMissingBranchIsNull(@TempDir Path dir) throws Exception {
        try (Git git = Git.init().setDirectory(dir.toFile()).setInitialBranch("main").call()) {
            Files.writeString(dir.resolve("f.txt"), "one");
            git.add().addFilepattern("f.txt").call();
            git.commit().setMessage("one").setSign(false).call();

            IndexerConfig config = typed("git");
            when(config.gitUrl()).thenReturn(dir.toUri().toString());
            when(config.gitBranch()).thenReturn("main");
            ChecksumService service = newService(config);
            String first = service.calculateChecksum();
            assertTrue(first.startsWith(SHA256_PREFIX));
            assertEquals(first, service.calculateChecksum());

            Files.writeString(dir.resolve("f.txt"), "two");
            git.add().addFilepattern("f.txt").call();
            git.commit().setMessage("two").setSign(false).call();
            assertNotEquals(first, service.calculateChecksum());

            IndexerConfig missing = typed("git");
            when(missing.gitUrl()).thenReturn(dir.toUri().toString());
            when(missing.gitBranch()).thenReturn("nope");
            assertNull(newService(missing).calculateChecksum());
        }
    }

    @Test
    void gitChecksumIsNullWithoutAUrlOrWhenTheRemoteFails() {
        assertNull(newService(typed("git")).calculateChecksum());
        IndexerConfig config = typed("git");
        when(config.gitUrl()).thenReturn("/no/such/repository");
        when(config.gitBranch()).thenReturn("main");
        assertNull(newService(config).calculateChecksum());
    }

    @Test
    void hasSourceChangedComparesTheStoredChecksumWithTheCurrentOne() {
        try (StubHttpServer server = new StubHttpServer()
            .route("HEAD", "/a", Reply.withHeaders(Map.of("ETag", "\"v1\"")))) {
            IndexerConfig config = typed("url");
            when(config.url()).thenReturn(server.baseUrl() + "/a");
            when(config.forceReindex()).thenReturn("false");
            String current = newService(config).calculateChecksum();

            when(config.lastChecksum()).thenReturn(current);
            assertFalse(newService(config).hasSourceChanged());

            when(config.lastChecksum()).thenReturn(SHA256_PREFIX + "stale");
            assertTrue(newService(config).hasSourceChanged());
        }
    }

    @Test
    void hasSourceChangedIsTrueWhenTheCurrentChecksumCannotBeComputed() {
        IndexerConfig config = typed("s3");
        when(config.forceReindex()).thenReturn("false");
        when(config.lastChecksum()).thenReturn(SHA256_PREFIX + "old");
        assertTrue(newService(config).hasSourceChanged());
    }
}
