package ai.kubemoot.indexer.source;

import ai.kubemoot.indexer.config.IndexerConfig;
import ai.kubemoot.indexer.testsupport.StubHttpServer;
import ai.kubemoot.indexer.testsupport.StubHttpServer.Reply;
import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientResponseException;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

class UrlSourceLoaderTest {

    private UrlSourceLoader loader(String url) {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.url()).thenReturn(url);
        return new UrlSourceLoader(config, WebClient.builder());
    }

    @Test
    void supportsOnlyTheUrlType() {
        UrlSourceLoader loader = loader(null);
        assertTrue(loader.supports("url"));
        assertTrue(loader.supports("URL"));
        assertFalse(loader.supports("git"));
    }

    @Test
    void loadReturnsTheBodyWithItsSourceMetadata() {
        try (StubHttpServer server = new StubHttpServer().route("GET", "/doc", Reply.ok("hello page"))) {
            String url = server.baseUrl() + "/doc";

            List<Document> documents = loader(url).load();

            assertEquals(1, documents.size());
            assertEquals("hello page", documents.get(0).getText());
            assertEquals("url", documents.get(0).getMetadata().get("source"));
            assertEquals(url, documents.get(0).getMetadata().get("file_path"));
        }
    }

    @Test
    void loadRequiresAUrl() {
        assertThrows(IllegalArgumentException.class, () -> loader(null).load());
        assertThrows(IllegalArgumentException.class, () -> loader("  ").load());
    }

    @Test
    void loadFailsWhenTheServerAnswersAnError() {
        try (StubHttpServer server = new StubHttpServer()) {
            UrlSourceLoader loader = loader(server.baseUrl() + "/missing");
            assertThrows(WebClientResponseException.NotFound.class, loader::load);
        }
    }
}
