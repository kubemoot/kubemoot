package ai.kubemoot.indexer.source;

import ai.kubemoot.indexer.config.IndexerConfig;
import ai.kubemoot.indexer.testsupport.StubHttpServer;
import ai.kubemoot.indexer.testsupport.StubHttpServer.Reply;
import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;
import org.springframework.web.reactive.function.client.WebClient;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** Document conversion against a stand-in for the docling-serve sidecar. */
class DocumentSourceLoaderConversionTest {

    private DocumentSourceLoader loader(StubHttpServer docling, List<String> urls) {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.documentUrls()).thenReturn(urls);
        when(config.doclingEndpoint()).thenReturn(docling.baseUrl());
        return new DocumentSourceLoader(config, WebClient.builder());
    }

    private static Reply json(String body) {
        return new Reply(200, Map.of("Content-Type", "application/json"), body);
    }

    private StubHttpServer docling(String convertedBody) {
        return new StubHttpServer()
            .route("GET", "/health", Reply.ok("ok"))
            .route("POST", "/v1/convert/source", json(convertedBody));
    }

    @Test
    void readsMarkdownFromTheV2Response() {
        try (StubHttpServer docling = docling("{\"document\": {\"md_content\": \"# Title\"}}")) {
            List<Document> documents = loader(docling, List.of("https://x/a.pdf")).load();

            assertEquals(1, documents.size());
            assertEquals("# Title", documents.get(0).getText());
            assertEquals("document", documents.get(0).getMetadata().get("source"));
            assertEquals("https://x/a.pdf", documents.get(0).getMetadata().get("file_path"));
        }
    }

    @Test
    void fallsBackToTheV1ExportKey() {
        try (StubHttpServer docling = docling("{\"document\": {\"export_to_markdown\": \"old style\"}}")) {
            assertEquals("old style", loader(docling, List.of("u")).load().get(0).getText());
        }
    }

    @Test
    void fallsBackToADirectMarkdownKey() {
        try (StubHttpServer docling = docling("{\"markdown\": \"direct\"}")) {
            assertEquals("direct", loader(docling, List.of("u")).load().get(0).getText());
        }
    }

    @Test
    void skipsADocumentWhoseConversionHasNoMarkdown() {
        try (StubHttpServer docling = docling("{\"document\": {\"other\": 1}}")) {
            assertTrue(loader(docling, List.of("u")).load().isEmpty());
        }
    }

    @Test
    void skipsABlankConversion() {
        try (StubHttpServer docling = docling("{\"markdown\": \"  \"}")) {
            assertTrue(loader(docling, List.of("u")).load().isEmpty());
        }
    }

    @Test
    void aFailedConversionDoesNotStopTheOtherDocuments() {
        try (StubHttpServer docling = new StubHttpServer()
            .route("GET", "/health", Reply.ok("ok"))
            .route("POST", "/v1/convert/source", body ->
                body.contains("bad") ? Reply.status(500) : json("{\"markdown\": \"fine\"}"))) {
            List<Document> documents = loader(docling, List.of("https://x/bad.pdf", "https://x/good.pdf")).load();

            assertEquals(1, documents.size());
            assertEquals("https://x/good.pdf", documents.get(0).getMetadata().get("file_path"));
        }
    }

    @Test
    void loadRequiresDocumentUrls() {
        try (StubHttpServer docling = docling("{}")) {
            assertThrows(IllegalArgumentException.class, () -> loader(docling, null).load());
            assertThrows(IllegalArgumentException.class, () -> loader(docling, List.of()).load());
        }
    }
}
